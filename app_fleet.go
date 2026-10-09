package main

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"math"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	sshlayer "ssh-tool/internal/ssh"
	"ssh-tool/internal/store"
)

// FactsInput is what the Gather facts dialog sends.
type FactsInput struct {
	ConnectionIDs  []string `json:"connection_ids"`
	Facts          []string `json:"facts"`
	Custom         string   `json:"custom"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// FactsHostResult is one row of the report: the host plus what it
// answered, or why it did not.
type FactsHostResult struct {
	ConnectionID string             `json:"connection_id"`
	Name         string             `json:"name"`
	Hostname     string             `json:"hostname"`
	State        string             `json:"state"` // ok | error | skipped
	Error        string             `json:"error,omitempty"`
	Facts        sshlayer.HostFacts `json:"facts"`
}

// GatherFacts runs the chosen read-only fact snippets on every host in
// parallel (the Run command batch runner, 8 at a time) and parses each
// answer. The custom column is the only user text that reaches a host, so
// it has to pass the same read-only check MCP commands do.
func (a *App) GatherFacts(in FactsInput) ([]FactsHostResult, error) {
	if len(in.ConnectionIDs) == 0 {
		return nil, fmt.Errorf("no connections selected")
	}
	custom := strings.TrimSpace(in.Custom)
	if custom != "" && !sshlayer.IsReadOnly(custom, a.mcpReadOnlyExtra()) {
		return nil, fmt.Errorf("the custom column must be a read-only command; %q could change the host", custom)
	}
	cmd, err := sshlayer.BuildFactsCommand(in.Facts, custom)
	if err != nil {
		return nil, err
	}
	timeout := in.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}
	// An inventory host that left the cache since the caller listed it
	// (pinned into a saved connection, removed by a refresh) is not a host
	// that failed to answer: leave it out.
	hosts := a.batchHosts(in.ConnectionIDs)
	live := hosts[:0]
	for _, h := range hosts {
		if strings.HasPrefix(h.ConnectionID, "dyn:") && h.Settings == nil {
			if e, _ := a.db.GetDynamicEntry(strings.TrimPrefix(h.ConnectionID, "dyn:")); e == nil {
				continue
			}
		}
		live = append(live, h)
	}
	raw := a.runBatch(live, cmd, timeout)
	out := make([]FactsHostResult, 0, len(raw))
	for _, r := range raw {
		row := FactsHostResult{ConnectionID: r.ConnectionID, Name: r.Name, Hostname: r.Hostname, State: r.State, Error: r.Error}
		switch {
		case r.Error == sshlayer.ErrBatchTimeout.Error():
			// A partial report would read as "this host has no updates /
			// no failed units"; say which fact was still running instead.
			row.Error = fmt.Sprintf("timed out after %ds", timeout)
			if k := sshlayer.RunningFact(r.Stdout); k != "" {
				row.Error += " while collecting " + k
			}
		case r.State == "ok" || r.Stdout != "":
			row.Facts = sshlayer.ParseFacts(r.Stdout)
			row.State = "ok"
			row.Error = ""
		}
		out = append(out, row)
	}
	return out, nil
}

// ----- TLS certificates -----

// TLSInput asks for the certificate on each host's ports.
type TLSInput struct {
	ConnectionIDs []string `json:"connection_ids"`
	Ports         []int    `json:"ports"`
}

// TLSCertResult is one host:port. DaysLeft is negative once expired.
type TLSCertResult struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	Port         int    `json:"port"`
	State        string `json:"state"` // ok | error | skipped
	Error        string `json:"error,omitempty"`
	Subject      string `json:"subject"`
	Issuer       string `json:"issuer"`
	NotAfter     int64  `json:"not_after"` // unix seconds
	DaysLeft     int    `json:"days_left"`
	Trusted      bool   `json:"trusted"`
	TrustError   string `json:"trust_error,omitempty"`
}

// CheckTLSCerts reads the certificate each host serves on each port. Only
// hosts reached by a name are checked: a certificate is issued for a name,
// so a connection that dials an IP has nothing meaningful to compare. The
// port is reached the way a connect reaches the host: from its last jump
// host when it has a chain (a bastion-only host would otherwise always fail
// here), else over its network profile, else directly.
func (a *App) CheckTLSCerts(in TLSInput) ([]TLSCertResult, error) {
	if len(in.ConnectionIDs) == 0 {
		return nil, fmt.Errorf("no connections selected")
	}
	ports := in.Ports
	if len(ports) == 0 {
		ports = []int{443}
	}
	hosts := a.batchHosts(in.ConnectionIDs)
	var jobs []TLSCertResult
	var settings []*store.ResolvedSettings
	for _, h := range hosts {
		for _, p := range ports {
			jobs = append(jobs, TLSCertResult{ConnectionID: h.ConnectionID, Name: h.Name, Hostname: h.Hostname, Port: p})
			settings = append(settings, h.Settings)
		}
	}
	sem := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i := range jobs {
		wg.Add(1)
		go func(j *TLSCertResult, i int) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			a.checkOneTLS(j, settings[i])
		}(&jobs[i], i)
	}
	wg.Wait()
	return jobs, nil
}

func (a *App) checkOneTLS(j *TLSCertResult, settings *store.ResolvedSettings) {
	host := strings.TrimSpace(j.Hostname)
	if host == "" {
		j.State, j.Error = "skipped", "no hostname"
		return
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		j.State, j.Error = "skipped", "connected by IP address - no name to check a certificate against"
		return
	}
	addr := net.JoinHostPort(host, strconv.Itoa(j.Port))
	var raw net.Conn
	if settings != nil {
		c, cleanup, err := sshlayer.DialVia(context.Background(), a.db, a.vault, settings, addr,
			a.makeHostKeyCallback(), a.makeAlgoLookup(), 8*time.Second)
		if err != nil {
			j.State, j.Error = "error", err.Error()
			return
		}
		defer cleanup()
		raw = c
	} else {
		c, err := net.DialTimeout("tcp", addr, 6*time.Second)
		if err != nil {
			j.State, j.Error = "error", err.Error()
			return
		}
		defer c.Close()
		raw = c
	}
	_ = raw.SetDeadline(time.Now().Add(10 * time.Second))
	conn := tls.Client(raw, &tls.Config{
		ServerName:         host,
		InsecureSkipVerify: true, // read the cert whatever it is; trust is judged below
	})
	if err := conn.Handshake(); err != nil {
		j.State, j.Error = "error", err.Error()
		return
	}
	certs := conn.ConnectionState().PeerCertificates
	if len(certs) == 0 {
		j.State, j.Error = "error", "no certificate presented"
		return
	}
	leaf := certs[0]
	j.State = "ok"
	j.Subject = leaf.Subject.CommonName
	if j.Subject == "" && len(leaf.DNSNames) > 0 {
		j.Subject = leaf.DNSNames[0]
	}
	j.Issuer = leaf.Issuer.CommonName
	j.NotAfter = leaf.NotAfter.Unix()
	j.DaysLeft = int(math.Floor(time.Until(leaf.NotAfter).Hours() / 24))
	inter := x509.NewCertPool()
	for _, c := range certs[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: host, Intermediates: inter}); err != nil {
		j.TrustError = err.Error()
	} else {
		j.Trusted = true
	}
}

// ----- Compare file -----

// FileReadResult is one host's copy of the compared file.
type FileReadResult struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	State        string `json:"state"` // ok | error | skipped
	Error        string `json:"error,omitempty"`
	Content      string `json:"content"`
	SHA256       string `json:"sha256"`
	Truncated    bool   `json:"truncated"`
}

// compareMaxBytes caps each copy: this compares configuration files, and a
// multi-megabyte log would only freeze the diff view.
const compareMaxBytes = 1 << 20

// ReadFileAcross reads one absolute path on every host as the login user.
// Read-only: cat through head, nothing else.
func (a *App) ReadFileAcross(ids []string, path string) ([]FileReadResult, error) {
	if !strings.HasPrefix(path, "/") && !strings.HasPrefix(path, "~/") {
		return nil, fmt.Errorf("give an absolute path (or one starting with ~/)")
	}
	if strings.ContainsAny(path, "\n\x00") {
		return nil, fmt.Errorf("invalid path")
	}
	arg := "'" + strings.ReplaceAll(path, "'", `'\''`) + "'"
	if rest, ok := strings.CutPrefix(path, "~/"); ok {
		arg = `"$HOME"/'` + strings.ReplaceAll(rest, "'", `'\''`) + "'"
	}
	cmd := fmt.Sprintf(`if [ -r %[1]s ]; then head -c %[2]d -- %[1]s; else echo "__SSHTOOL_NOREAD__"; ls -ld -- %[1]s 2>&1; fi`, arg, compareMaxBytes+1)
	raw := a.runBatch(a.batchHosts(ids), cmd, 30)
	out := make([]FileReadResult, 0, len(raw))
	for _, r := range raw {
		row := FileReadResult{ConnectionID: r.ConnectionID, Name: r.Name, Hostname: r.Hostname, State: r.State, Error: r.Error}
		if r.State == "ok" {
			if rest, ok := strings.CutPrefix(r.Stdout, "__SSHTOOL_NOREAD__\n"); ok {
				row.State, row.Error = "error", "not readable as the login user: "+strings.TrimSpace(rest)
			} else {
				c := r.Stdout
				if len(c) > compareMaxBytes {
					c, row.Truncated = c[:compareMaxBytes], true
				}
				row.Content = c
				sum := sha256.Sum256([]byte(c))
				row.SHA256 = hex.EncodeToString(sum[:])
			}
		}
		out = append(out, row)
	}
	return out, nil
}

// ----- Copy SSH key -----

// CopyKeyResult is one host's answer: present, added, would_add (check
// only), skipped_root, or error.
type CopyKeyResult struct {
	ConnectionID string `json:"connection_id"`
	Name         string `json:"name"`
	Hostname     string `json:"hostname"`
	User         string `json:"user"`
	Result       string `json:"result"`
	Error        string `json:"error,omitempty"`
}

// keyCommentRe is what may follow the key on its authorized_keys line: the
// label someone reading the file later sees ("jane@laptop", a credential
// name). Nothing that the shell or sshd option parsing could read as more.
var keyCommentRe = regexp.MustCompile(`^[A-Za-z0-9@._+:,= -]{0,80}$`)

var authorizedKeyRe = regexp.MustCompile(`^(ssh-(ed25519|rsa|dss)|ecdsa-sha2-nistp(256|384|521)|sk-(ssh-ed25519|ecdsa-sha2-nistp256)@openssh\.com) [A-Za-z0-9+/=]+( [^'\n\r]*)?$`)

// CopySSHKey appends a credential's public key to authorized_keys of each
// host's login user. It never removes or rewrites a line: the key is added
// only when that exact line is not there yet. apply=false only checks.
// Root logins are skipped unless allowRoot, so a slip cannot quietly widen
// root access across a folder.
func (a *App) CopySSHKey(ids []string, credentialID, comment string, apply, allowRoot bool) ([]CopyKeyResult, error) {
	cred, err := a.db.GetCredential(credentialID)
	if err != nil {
		return nil, err
	}
	if cred.PublicKey == nil || strings.TrimSpace(*cred.PublicKey) == "" {
		return nil, fmt.Errorf("credential %q has no public key", cred.Name)
	}
	key := strings.TrimSpace(*cred.PublicKey)
	if !authorizedKeyRe.MatchString(key) {
		return nil, fmt.Errorf("credential %q does not hold a usable OpenSSH public key line", cred.Name)
	}
	// Presence is decided by the key itself (type + base64), not the whole
	// line: the same key under another comment is already there, and adding
	// it again would only leave a duplicate to confuse the next audit.
	f := strings.Fields(key)
	body := f[0] + " " + f[1]
	comment = strings.TrimSpace(comment)
	if !keyCommentRe.MatchString(comment) {
		return nil, fmt.Errorf("the comment may only hold letters, digits, spaces and @ . _ + - : , =")
	}
	line := body
	if comment != "" {
		line += " " + comment
	}
	qb, ql := "'"+body+"'", "'"+line+"'" // the regexps rule out quotes and newlines
	check := fmt.Sprintf(`grep -qF -- %s ~/.ssh/authorized_keys 2>/dev/null && echo __PRESENT__ || echo __ABSENT__`, qb)
	add := fmt.Sprintf(`if grep -qF -- %s ~/.ssh/authorized_keys 2>/dev/null; then echo __PRESENT__; else umask 077; mkdir -p ~/.ssh && printf '%%s\n' %s >> ~/.ssh/authorized_keys && echo __ADDED__; fi`, qb, ql)

	all := a.batchHosts(ids)
	var run []sshlayer.BatchHostInput
	results := make(map[string]*CopyKeyResult, len(all))
	order := make([]string, 0, len(all))
	for _, h := range all {
		user := ""
		if h.Settings != nil && h.Settings.Username != nil {
			user = *h.Settings.Username
		}
		r := &CopyKeyResult{ConnectionID: h.ConnectionID, Name: h.Name, Hostname: h.Hostname, User: user}
		results[h.ConnectionID] = r
		order = append(order, h.ConnectionID)
		if user == "root" && !allowRoot {
			r.Result = "skipped_root"
			continue
		}
		run = append(run, h)
	}
	cmd := check
	if apply {
		cmd = add
	}
	for _, br := range a.runBatch(run, cmd, 20) {
		r := results[br.ConnectionID]
		switch {
		case br.State != "ok":
			r.Result, r.Error = "error", firstNonEmptyStr(br.Error, strings.TrimSpace(br.Stderr), br.State)
		case strings.Contains(br.Stdout, "__PRESENT__"):
			r.Result = "present"
		case strings.Contains(br.Stdout, "__ADDED__"):
			r.Result = "added"
		case strings.Contains(br.Stdout, "__ABSENT__"):
			r.Result = "would_add"
		default:
			r.Result, r.Error = "error", firstNonEmptyStr(strings.TrimSpace(br.Stderr), "unexpected answer")
		}
	}
	out := make([]CopyKeyResult, 0, len(order))
	for _, id := range order {
		out = append(out, *results[id])
	}
	return out, nil
}

func firstNonEmptyStr(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
