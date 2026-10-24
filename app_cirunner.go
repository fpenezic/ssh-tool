package main

// CI runner: a GitLab runner in Docker whose jobs reach customers through
// the app proxy, managed from Settings instead of a hand-made gateway and
// config.toml. A docker network for the jobs (sshtool-ci), the gateway
// (tun2socks) and gitlab-runner, whose docker executor starts jobs on
// that network.
//
// The jobs cannot share the gateway's network (network_mode =
// "container:..."): the runner always sets a hostname, and Docker refuses
// a hostname on a container that joins another's network. So the
// gateway is the jobs' router instead: the network is created with
// inhibit_ipv4 (the host bridge takes no address), and the gateway takes
// the network's gateway address itself. A job's default route then leads
// to it; it sends the VPN networks into the proxy and masquerades the rest
// out through its own bridge. The runner itself talks to GitLab from the
// default bridge.
//
// The runner tokens live in the vault. config.toml is built at start and
// streamed into the runner container with docker cp, never written to
// the host disk.

import (
	"archive/tar"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
)

// CIRunnerConfig is the app setting ciRunnerKey.
type CIRunnerConfig struct {
	// URL and Image build a one-runner config.toml when ConfigTOML is
	// empty (URL + the token from the form).
	URL   string `json:"url,omitempty"`
	Name  string `json:"name,omitempty"`
	Image string `json:"image,omitempty"`
	// ConfigTOML is an imported config.toml, its tokens moved to the
	// vault. The app sets network_mode at start; everything else is used
	// as written.
	ConfigTOML string `json:"config_toml,omitempty"`
	// ExtraHosts are "name:ip" entries the jobs resolve (customer names
	// the VPN's DNS would answer), added to every runner's extra_hosts.
	ExtraHosts []string `json:"extra_hosts,omitempty"`
	// Direct are destinations that never go through a VPN, even when a
	// customer's networks contain them (a cache or GitLab on the LAN).
	// The runner URL and the S3 cache address are added automatically.
	Direct    []string `json:"direct,omitempty"`
	AutoStart bool     `json:"auto_start,omitempty"`
}

// CIRunnerStatus is what the Settings card shows.
type CIRunnerStatus struct {
	Docker    string `json:"docker"` // server version, "" = not reachable
	DockerErr string `json:"docker_err,omitempty"`
	Desktop   bool   `json:"desktop"` // Docker Desktop (host.docker.internal reaches loopback)
	Gateway   string `json:"gateway"` // container state, "" = none
	Runner    string `json:"runner"`
	Running   bool   `json:"running"`
	Tokens    int    `json:"tokens"`  // runners with a token in the vault
	Runners   int    `json:"runners"` // runners in the config
	Routes    int    `json:"routes"`  // networks the gateway sends into the VPNs
	LastErr   string `json:"last_err,omitempty"`
	StartedAt int64  `json:"started_at,omitempty"`
	Busy      bool   `json:"busy"`
}

const (
	ciRunnerKey       = "ci_runner"
	ciGatewayName     = "sshtool-ci-gw"
	ciNetworkName     = "sshtool-ci"
	ciRunnerName      = "sshtool-ci-runner"
	ciLabel           = "app.sshtool.ci"
	ciRunnerImage     = "gitlab/gitlab-runner:alpine-v18.5.0"
	ciDefaultJobImage = "alpine:3.22"
	ciMaxRunners      = 16
)

func ciTokenKey(i int) string { return "ci_runner:token:" + strconv.Itoa(i) }

var ciState struct {
	sync.Mutex
	busy      bool
	lastErr   string
	startedAt time.Time
	started   bool // started by this process: stopped on quit
}

func (a *App) ciRunnerConfig() CIRunnerConfig {
	var c CIRunnerConfig
	if v, ok, _ := a.db.GetSetting(ciRunnerKey); ok {
		_ = json.Unmarshal([]byte(v), &c)
	}
	return c
}

// CIRunnerGet returns the setting and the containers' state.
func (a *App) CIRunnerGet() (CIRunnerConfig, CIRunnerStatus) {
	c := a.ciRunnerConfig()
	return c, a.ciStatus(c)
}

func (a *App) ciStatus(c CIRunnerConfig) CIRunnerStatus {
	var st CIRunnerStatus
	ciState.Lock()
	st.Busy, st.LastErr = ciState.busy, ciState.lastErr
	if !ciState.startedAt.IsZero() {
		st.StartedAt = ciState.startedAt.Unix()
	}
	ciState.Unlock()
	st.Runners = 1
	if c.ConfigTOML != "" {
		if m, err := ciParseTOML(c.ConfigTOML); err == nil {
			st.Runners = len(ciRunners(m))
		}
	}
	for i := 0; i < st.Runners; i++ {
		if t, ok, _ := a.vault.Get(ciTokenKey(i)); ok && t != "" {
			st.Tokens++
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	ver, err := docker(ctx, nil, "version", "--format", "{{.Server.Version}}")
	if err != nil {
		st.DockerErr = err.Error()
		return st
	}
	st.Docker = ver
	if osName, err := docker(ctx, nil, "info", "--format", "{{.OperatingSystem}}"); err == nil {
		st.Desktop = strings.Contains(osName, "Docker Desktop")
	}
	st.Gateway, _, _ = ciContainer(ctx, ciGatewayName)
	st.Runner, _, _ = ciContainer(ctx, ciRunnerName)
	st.Running = st.Gateway == "running" && st.Runner == "running"
	return st
}

// CIRunnerSet saves the setting. token, when not empty, is the runner
// token for a config built from the form; an imported config.toml
// brings its own, which move to the vault here.
func (a *App) CIRunnerSet(c CIRunnerConfig, token string) (CIRunnerConfig, error) {
	c.URL = strings.TrimSpace(c.URL)
	c.Name = strings.TrimSpace(c.Name)
	c.Image = strings.TrimSpace(c.Image)
	c.ConfigTOML = strings.TrimSpace(c.ConfigTOML)
	hosts, err := ciNormHosts(c.ExtraHosts)
	if err != nil {
		return c, err
	}
	c.ExtraHosts = hosts
	var direct []string
	for _, d := range c.Direct {
		if d = strings.TrimSpace(d); d != "" {
			direct = append(direct, d)
		}
	}
	c.Direct = direct
	var tokens map[int]string
	if c.ConfigTOML != "" {
		m, err := ciParseTOML(c.ConfigTOML)
		if err != nil {
			return c, err
		}
		rs := ciRunners(m)
		if len(rs) == 0 {
			return c, errors.New("config.toml has no [[runners]]")
		}
		if len(rs) > ciMaxRunners {
			return c, fmt.Errorf("config.toml has %d runners; at most %d", len(rs), ciMaxRunners)
		}
		tokens = map[int]string{}
		for i, r := range rs {
			if t, _ := r["token"].(string); strings.TrimSpace(t) != "" {
				tokens[i] = strings.TrimSpace(t)
			}
			delete(r, "token")
		}
		b, err := toml.Marshal(m)
		if err != nil {
			return c, fmt.Errorf("config.toml: %w", err)
		}
		c.ConfigTOML = string(b)
	} else {
		if c.URL != "" {
			u, err := url.Parse(c.URL)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				return c, fmt.Errorf("GitLab URL %q: want http(s)://host", c.URL)
			}
		}
		if t := strings.TrimSpace(token); t != "" {
			tokens = map[int]string{0: t}
		}
	}
	for i, t := range tokens {
		if err := a.vault.Put(ciTokenKey(i), t); err != nil {
			return c, fmt.Errorf("runner token: %w", err)
		}
	}
	b, _ := json.Marshal(c)
	if err := a.db.SetSetting(ciRunnerKey, string(b)); err != nil {
		return c, err
	}
	a.recordAudit("ci_runner.set", "", map[string]string{
		"imported": strconv.FormatBool(c.ConfigTOML != ""), "tokens": strconv.Itoa(len(tokens)),
	})
	return c, nil
}

// CIRunnerStart (re)creates the gateway and the runner.
func (a *App) CIRunnerStart() (CIRunnerStatus, error) {
	c := a.ciRunnerConfig()
	if err := a.ciStart(c); err != nil {
		return a.ciStatus(c), err
	}
	return a.ciStatus(c), nil
}

// CIRunnerStop removes both containers.
func (a *App) CIRunnerStop() (CIRunnerStatus, error) {
	c := a.ciRunnerConfig()
	if !ciBegin() {
		return a.ciStatus(c), errors.New("the CI runner is starting or stopping")
	}
	err := ciRemoveAll()
	ciEnd(err, false)
	a.recordAudit("ci_runner.stop", "", nil)
	EventsEmit("ci_runner_changed", nil)
	return a.ciStatus(c), err
}

// CIRunnerLogs is the runner container's recent log.
func (a *App) CIRunnerLogs() (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := dockerCombined(ctx, "logs", "--tail", "200", ciRunnerName)
	if err != nil {
		return "", err
	}
	// The runner colours its log; the page shows plain text.
	return ansiEscape.ReplaceAllString(out, ""), nil
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*[A-Za-z]`)

func ciBegin() bool {
	ciState.Lock()
	defer ciState.Unlock()
	if ciState.busy {
		return false
	}
	ciState.busy = true
	return true
}

func ciEnd(err error, started bool) {
	ciState.Lock()
	ciState.busy = false
	ciState.lastErr = ""
	if err != nil {
		ciState.lastErr = err.Error()
	}
	ciState.started = started
	if started {
		ciState.startedAt = time.Now()
	} else {
		ciState.startedAt = time.Time{}
	}
	ciState.Unlock()
}

func (a *App) ciStart(c CIRunnerConfig) (err error) {
	if runtime.GOOS == "android" || runtime.GOOS == "ios" {
		return errors.New("the CI runner needs Docker on a desktop")
	}
	if !ciBegin() {
		return errors.New("the CI runner is starting or stopping")
	}
	// A start that fails before the old containers are gone leaves them
	// running: keep "started by us" so quit still removes them.
	ciState.Lock()
	wasStarted := ciState.started
	ciState.Unlock()
	started, tornDown := false, false
	defer func() {
		ciEnd(err, started || (wasStarted && !tornDown))
		EventsEmit("ci_runner_changed", nil)
	}()

	sp, listening := a.NetworkSharedProxyGet()
	if !sp.Enabled || !listening {
		return errors.New("the app proxy is off")
	}
	details, err := a.NetworkSharedProxyDetails()
	if err != nil {
		return err
	}
	if len(details.Routes) == 0 {
		return errors.New("no VPN is marked Reachable through the app proxy")
	}
	pw, err := a.sharedProxyPassword()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	if _, err := docker(ctx, nil, "version", "--format", "{{.Server.Version}}"); err != nil {
		return fmt.Errorf("docker: %w", err)
	}
	osName, _ := docker(ctx, nil, "info", "--format", "{{.OperatingSystem}}")
	if !strings.Contains(osName, "Docker Desktop") {
		if ip, err := netip.ParseAddr(sp.Bind); err == nil && ip.IsLoopback() {
			bridge, _ := docker(ctx, nil, "network", "inspect", "bridge", "--format", "{{(index .IPAM.Config 0).Gateway}}")
			return fmt.Errorf("this Docker runs on Linux directly: its containers cannot reach the app proxy on %s (docker bridge address: %s)", sp.Bind, orDash(bridge))
		}
	}

	cfgText, autoDirect, err := a.ciBuildTOML(c)
	if err != nil {
		return err
	}
	direct, err := ciDirectRoutes(ctx, c.Direct, autoDirect)
	if err != nil {
		return err
	}

	if err := ciRemoveAll(); err != nil {
		return err
	}
	tornDown = true
	subnet, gwAddr, err := ciNetwork(ctx)
	if err != nil {
		return err
	}
	own, err := ciLastAddr(subnet)
	if err != nil {
		return err
	}
	// The gateway's own address is the network's last one (jobs get theirs
	// from the bottom); it then claims the gateway address on the same
	// interface, masquerades what is not for the VPNs, and tun2socks's
	// catch-all rule goes (gotcha 108).
	extra := strings.Join([]string{
		"ip rule del not fwmark 0x22b table 0x22b",
		fmt.Sprintf("IF=$(ip -o -4 addr show | awk '$4 ~ /^%s\\//{print $2}')", strings.ReplaceAll(own.String(), ".", "\\.")),
		fmt.Sprintf("ip addr add %s/32 dev $IF", gwAddr),
		fmt.Sprintf("iptables -t nat -A POSTROUTING -s %s ! -o $IF -j MASQUERADE", subnet),
	}, "; ")
	gw := []string{"create", "--name", ciGatewayName, "--label", ciLabel + "=gateway",
		"--cap-add", "NET_ADMIN", "--device", "/dev/net/tun",
		"--sysctl", "net.ipv4.ip_forward=1",
		"--add-host", "host.docker.internal:host-gateway",
		// The value comes from the CLI's environment, not argv: argv is
		// readable by every local user (ps, /proc) while docker runs.
		"-e", "PROXY",
		"-e", "TUN_INCLUDED_ROUTES=" + strings.Join(details.Routes, ","),
		"-e", "EXTRA_COMMANDS=" + extra}
	if len(direct) > 0 {
		gw = append(gw, "-e", "TUN_EXCLUDED_ROUTES="+strings.Join(direct, ","))
	}
	gw = append(gw, tun2socksImage)
	proxyEnv := fmt.Sprintf("PROXY=socks5://%s:%s@host.docker.internal:%d", localProxyUser, pw, sp.Port)
	if _, err := dockerEnv(ctx, nil, []string{proxyEnv}, gw...); err != nil {
		_ = ciRemoveAll()
		return fmt.Errorf("gateway: %w", err)
	}
	if _, err := docker(ctx, nil, "network", "connect", "--ip", own.String(), ciNetworkName, ciGatewayName); err != nil {
		_ = ciRemoveAll()
		return fmt.Errorf("gateway: %w", err)
	}
	if _, err := docker(ctx, nil, "start", ciGatewayName); err != nil {
		_ = ciRemoveAll()
		return fmt.Errorf("gateway: %w", err)
	}
	if _, err := docker(ctx, nil, "create", "--name", ciRunnerName, "--label", ciLabel+"=runner",
		"-v", "/var/run/docker.sock:/var/run/docker.sock", ciRunnerImage); err != nil {
		_ = ciRemoveAll()
		return fmt.Errorf("runner: %w", err)
	}
	tarball, err := ciTar("config.toml", []byte(cfgText))
	if err != nil {
		_ = ciRemoveAll()
		return err
	}
	if _, err := docker(ctx, tarball, "cp", "-", ciRunnerName+":/etc/gitlab-runner"); err != nil {
		_ = ciRemoveAll()
		return fmt.Errorf("runner config: %w", err)
	}
	if _, err := docker(ctx, nil, "start", ciRunnerName); err != nil {
		_ = ciRemoveAll()
		return fmt.Errorf("runner: %w", err)
	}
	started = true
	log.Printf("ci runner: started, %d routes, %d direct, jobs on %s", len(details.Routes), len(direct), subnet)
	a.recordAudit("ci_runner.start", "", map[string]string{
		"routes": strconv.Itoa(len(details.Routes)), "direct": strconv.Itoa(len(direct)),
	})
	return nil
}

// ciRunnerAutoStart starts the runner when it is set to start with the
// app and is not up yet. Called once the vault opens: the tokens and
// the proxy password live there.
func (a *App) ciRunnerAutoStart() {
	c := a.ciRunnerConfig()
	if !c.AutoStart {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		gwState, _, _ := ciContainer(ctx, ciGatewayName)
		rState, _, _ := ciContainer(ctx, ciRunnerName)
		cancel()
		if gwState == "running" && rState == "running" {
			return
		}
		if err := a.ciStart(c); err != nil {
			log.Printf("ci runner: auto start: %v", err)
		}
	}()
}

// ciRunnerStopOnQuit removes the containers this process started: with
// the app gone the proxy is gone, and a runner left behind would take
// jobs it cannot finish.
func (a *App) ciRunnerStopOnQuit() {
	ciState.Lock()
	started := ciState.started
	ciState.Unlock()
	if !started {
		return
	}
	// This process created them, so no label checks: two docker calls
	// instead of seven keep quit short.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := docker(ctx, nil, "rm", "-f", "-v", ciRunnerName, ciGatewayName); err != nil {
		log.Printf("ci runner: stop on quit: %v", err)
	}
	if _, err := docker(ctx, nil, "network", "rm", ciNetworkName); err != nil {
		log.Printf("ci runner: stop on quit: %v", err)
	}
}

// ciBuildTOML is the config.toml the runner gets: the imported one or
// one built from the form, with the tokens back in, every runner's jobs
// on the jobs' network and the form's extra hosts added. autoDirect are
// the hosts that must not go through a VPN: each runner's GitLab and its
// S3 cache.
func (a *App) ciBuildTOML(c CIRunnerConfig) (text string, autoDirect []string, err error) {
	var m map[string]any
	if c.ConfigTOML != "" {
		if m, err = ciParseTOML(c.ConfigTOML); err != nil {
			return "", nil, err
		}
	} else {
		if c.URL == "" {
			return "", nil, errors.New("no GitLab URL and no imported config.toml")
		}
		image := c.Image
		if image == "" {
			image = ciDefaultJobImage
		}
		name := c.Name
		if name == "" {
			name = "ssh-tool"
		}
		m = map[string]any{
			"concurrent":     int64(1),
			"check_interval": int64(0),
			"runners": []any{map[string]any{
				"name":     name,
				"url":      c.URL,
				"executor": "docker",
				"docker": map[string]any{
					"image":   image,
					"volumes": []any{"/cache"},
				},
			}},
		}
	}
	rs := ciRunners(m)
	if len(rs) == 0 {
		return "", nil, errors.New("config.toml has no [[runners]]")
	}
	for i, r := range rs {
		label, _ := r["name"].(string)
		if label == "" {
			label = "#" + strconv.Itoa(i+1)
		}
		if ex, _ := r["executor"].(string); ex != "docker" {
			return "", nil, fmt.Errorf("runner %s: executor %q; only the docker executor runs its jobs through the gateway", label, ex)
		}
		t, ok, verr := a.vault.Get(ciTokenKey(i))
		if verr != nil {
			return "", nil, fmt.Errorf("runner token: %w", verr)
		}
		if !ok || t == "" {
			return "", nil, fmt.Errorf("runner %s has no token", label)
		}
		r["token"] = t
		if u, _ := r["url"].(string); u != "" {
			if pu, err := url.Parse(u); err == nil && pu.Hostname() != "" {
				autoDirect = append(autoDirect, pu.Hostname())
			}
		}
		if cache, ok := r["cache"].(map[string]any); ok {
			if s3, ok := cache["s3"].(map[string]any); ok {
				if addr, _ := s3["ServerAddress"].(string); addr != "" {
					h := addr
					if hh, _, err := net.SplitHostPort(addr); err == nil {
						h = hh
					}
					autoDirect = append(autoDirect, h)
				}
			}
		}
		d, ok := r["docker"].(map[string]any)
		if !ok {
			d = map[string]any{}
			r["docker"] = d
		}
		if len(c.ExtraHosts) > 0 {
			var hosts []string
			if eh, ok := d["extra_hosts"].([]any); ok {
				for _, h := range eh {
					if s, ok := h.(string); ok {
						hosts = append(hosts, s)
					}
				}
			}
			merged, err := ciNormHosts(append(hosts, c.ExtraHosts...))
			if err != nil {
				return "", nil, err
			}
			list := make([]any, len(merged))
			for i, h := range merged {
				list[i] = h
			}
			d["extra_hosts"] = list
		}
		d["network_mode"] = ciNetworkName
	}
	b, err := toml.Marshal(m)
	if err != nil {
		return "", nil, fmt.Errorf("config.toml: %w", err)
	}
	return string(b), autoDirect, nil
}

func ciParseTOML(s string) (map[string]any, error) {
	var m map[string]any
	if err := toml.Unmarshal([]byte(s), &m); err != nil {
		return nil, fmt.Errorf("config.toml: %w", err)
	}
	return m, nil
}

func ciRunners(m map[string]any) []map[string]any {
	list, _ := m["runners"].([]any)
	out := make([]map[string]any, 0, len(list))
	for _, r := range list {
		if rm, ok := r.(map[string]any); ok {
			out = append(out, rm)
		}
	}
	return out
}

// ciNormHosts checks "name:ip" entries (Docker's --add-host form; the
// address may be IPv6, so the name ends at the first colon), dropping
// duplicates.
func ciNormHosts(in []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, h := range in {
		h = strings.TrimSpace(h)
		if h == "" {
			continue
		}
		name, ip, ok := strings.Cut(h, ":")
		name, ip = strings.TrimSpace(name), strings.TrimSpace(ip)
		if !ok || name == "" || strings.ContainsAny(name, " \t") {
			return nil, fmt.Errorf("extra host %q: want name:ip", h)
		}
		if _, err := netip.ParseAddr(strings.Trim(ip, "[]")); err != nil {
			return nil, fmt.Errorf("extra host %q: %q is not an IP address", h, ip)
		}
		e := name + ":" + ip
		if !seen[e] {
			seen[e] = true
			out = append(out, e)
		}
	}
	return out, nil
}

// ciDirectRoutes turns destinations that must stay off the VPNs into
// prefixes for TUN_EXCLUDED_ROUTES. Names resolve here, on the host
// resolver, as the jobs would see them. A name the user typed that does
// not resolve is an error; an automatic one (a GitLab only reachable
// some other way) is skipped.
func ciDirectRoutes(ctx context.Context, user, auto []string) ([]string, error) {
	var out []string
	add := func(s string, strict bool) error {
		if p, err := parseRoute(s); err == nil {
			out = append(out, p.String())
			return nil
		}
		lctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		addrs, err := net.DefaultResolver.LookupNetIP(lctx, "ip4", s)
		cancel()
		if err != nil || len(addrs) == 0 {
			if strict {
				return fmt.Errorf("direct destination %q: not an address or CIDR, and it does not resolve", s)
			}
			log.Printf("ci runner: %s does not resolve, not kept off the VPNs", s)
			return nil
		}
		for _, a := range addrs {
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()).String())
		}
		return nil
	}
	for _, s := range user {
		if err := add(s, true); err != nil {
			return nil, err
		}
	}
	for _, s := range auto {
		_ = add(s, false)
	}
	out = collapseRoutes(out)
	sort.Strings(out)
	return out, nil
}

func ciTar(name string, data []byte) (*bytes.Reader, error) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(data)), ModTime: time.Now()}); err != nil {
		return nil, err
	}
	if _, err := tw.Write(data); err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	return bytes.NewReader(buf.Bytes()), nil
}

// ciContainer is a container's state and our label on it ("" state = no
// such container).
func ciContainer(ctx context.Context, name string) (state, label string, err error) {
	out, err := docker(ctx, nil, "inspect", "--type", "container", "--format",
		"{{.State.Status}}|{{index .Config.Labels \""+ciLabel+"\"}}", name)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "no such") {
			return "", "", nil
		}
		return "", "", err
	}
	state, label, _ = strings.Cut(out, "|")
	return state, label, nil
}

// ciNetwork creates the jobs' network and returns its subnet and gateway
// address. inhibit_ipv4 keeps the address off the host bridge, so the
// gateway container can take it.
func ciNetwork(ctx context.Context) (netip.Prefix, netip.Addr, error) {
	if _, err := docker(ctx, nil, "network", "create", "-d", "bridge", "--label", ciLabel+"=network",
		"-o", "com.docker.network.bridge.inhibit_ipv4=true", ciNetworkName); err != nil {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("jobs network: %w", err)
	}
	out, err := docker(ctx, nil, "network", "inspect", ciNetworkName, "--format", "{{(index .IPAM.Config 0).Subnet}}|{{(index .IPAM.Config 0).Gateway}}")
	if err != nil {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("jobs network: %w", err)
	}
	sn, gw, _ := strings.Cut(out, "|")
	subnet, err1 := netip.ParsePrefix(sn)
	addr, err2 := netip.ParseAddr(gw)
	if err1 != nil || err2 != nil || !addr.Is4() {
		return netip.Prefix{}, netip.Addr{}, fmt.Errorf("jobs network: unexpected subnet %q / gateway %q", sn, gw)
	}
	return subnet.Masked(), addr, nil
}

// ciLastAddr is the last usable IPv4 address of a subnet (before the
// broadcast address).
func ciLastAddr(p netip.Prefix) (netip.Addr, error) {
	if !p.Addr().Is4() || p.Bits() > 30 {
		return netip.Addr{}, fmt.Errorf("jobs network %s: want an IPv4 subnet of /30 or larger", p)
	}
	b := p.Masked().Addr().As4()
	n := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
	n |= (1 << (32 - p.Bits())) - 1
	n--
	return netip.AddrFrom4([4]byte{byte(n >> 24), byte(n >> 16), byte(n >> 8), byte(n)}), nil
}

// ciRemoveAll removes the runner, the gateway and the jobs' network,
// refusing a container or network of the same name that ssh-tool did not
// create.
func ciRemoveAll() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	for _, name := range []string{ciRunnerName, ciGatewayName} {
		state, label, err := ciContainer(ctx, name)
		if err != nil {
			return fmt.Errorf("docker: %w", err)
		}
		if state == "" {
			continue
		}
		if label == "" || label == "<no value>" {
			return fmt.Errorf("a container named %s exists that ssh-tool did not create", name)
		}
		if _, err := docker(ctx, nil, "rm", "-f", "-v", name); err != nil {
			return fmt.Errorf("remove %s: %w", name, err)
		}
	}
	label, err := docker(ctx, nil, "network", "inspect", ciNetworkName, "--format", "{{index .Labels \""+ciLabel+"\"}}")
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "no such") {
			return nil
		}
		return fmt.Errorf("docker: %w", err)
	}
	if label == "" || label == "<no value>" {
		return fmt.Errorf("a docker network named %s exists that ssh-tool did not create", ciNetworkName)
	}
	// A job still running holds the network: cut it off (its runner is
	// gone, it cannot finish anyway).
	if out, err := docker(ctx, nil, "network", "inspect", ciNetworkName, "--format", "{{range .Containers}}{{.Name}} {{end}}"); err == nil {
		for _, name := range strings.Fields(out) {
			_, _ = docker(ctx, nil, "network", "disconnect", "-f", ciNetworkName, name)
		}
	}
	if _, err := docker(ctx, nil, "network", "rm", ciNetworkName); err != nil {
		return fmt.Errorf("remove %s: %w", ciNetworkName, err)
	}
	return nil
}

// dockerBin finds the docker CLI. An app started from the macOS Finder or
// a Linux desktop menu gets a short PATH without /usr/local/bin or
// Homebrew, so the usual install places are tried after PATH.
var dockerBin = sync.OnceValue(func() string {
	if p, err := exec.LookPath("docker"); err == nil {
		return p
	}
	home, _ := os.UserHomeDir()
	for _, p := range []string{
		"/usr/local/bin/docker",
		"/opt/homebrew/bin/docker",
		filepath.Join(home, ".docker", "bin", "docker"),
		"/Applications/Docker.app/Contents/Resources/bin/docker",
		filepath.Join(home, ".orbstack", "bin", "docker"),
		"/usr/bin/docker",
		"/snap/bin/docker",
	} {
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p
		}
	}
	return "docker"
})

// dockerCommand is a docker CLI call. Found outside PATH, its directory
// goes on the child's PATH: Docker Desktop's CLI runs its credential
// helper (docker-credential-desktop) from there when it pulls an image.
func dockerCommand(ctx context.Context, args ...string) *exec.Cmd {
	bin := dockerBin()
	cmd := exec.CommandContext(ctx, bin, args...)
	hideConsole(cmd)
	if filepath.IsAbs(bin) {
		dir := filepath.Dir(bin)
		path := os.Getenv("PATH")
		if !slices.Contains(filepath.SplitList(path), dir) {
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+path)
		}
	}
	return cmd
}

// docker runs the docker CLI and returns its trimmed stdout; on failure
// the error carries stderr.
func docker(ctx context.Context, stdin *bytes.Reader, args ...string) (string, error) {
	return dockerEnv(ctx, stdin, nil, args...)
}

// dockerEnv is docker with extra environment for the CLI (values passed
// with a bare "-e NAME").
func dockerEnv(ctx context.Context, stdin *bytes.Reader, env []string, args ...string) (string, error) {
	cmd := dockerCommand(ctx, args...)
	if len(env) > 0 {
		if cmd.Env == nil {
			cmd.Env = os.Environ()
		}
		cmd.Env = append(cmd.Env, env...)
	}
	if stdin != nil {
		cmd.Stdin = stdin
	}
	var out, errb bytes.Buffer
	cmd.Stdout, cmd.Stderr = &out, &errb
	if err := cmd.Run(); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return "", errors.New("docker is not installed (or not on PATH)")
		}
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return "", errors.New(msg)
	}
	return strings.TrimSpace(out.String()), nil
}

func dockerCombined(ctx context.Context, args ...string) (string, error) {
	cmd := dockerCommand(ctx, args...)
	out, err := cmd.CombinedOutput()
	if err != nil && len(out) == 0 {
		return "", err
	}
	return string(out), nil
}

func orDash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}
