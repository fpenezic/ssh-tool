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
	"crypto/rand"
	"encoding/hex"
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

	"ssh-tool/internal/creds"
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
	// KeepOnLock leaves the runner running when the vault locks. Off by
	// default: a running runner holds its tokens and cache keys in its
	// container, which a locked vault should not leave out.
	KeepOnLock bool `json:"keep_on_lock,omitempty"`
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

// A runner's secrets are keyed in the vault by an id kept in the stored
// config.toml (ciIDField), so reordering or removing [[runners]] cannot
// hand one runner another's token or cache keys. The id is taken out of
// the config the runner gets. A runner without one (form mode, or a config
// stored before ids) uses its index, and its secrets move to an id at the
// next save.
const ciIDField = "ssh_tool_key"

// The vault keys carry this machine's id: the vault is part of the synced
// profile, and another machine's runner (its own token, maybe the same
// form-mode id "0") must neither run here nor overwrite this one.
func ciTokenKey(id string) string { return ciVaultPrefix() + "token:" + id }

// ciMachineKey holds this machine's id in local state.
const ciMachineKey = "ci_runner_machine"

var ciMachine struct {
	sync.Mutex
	id string
}

// ciVaultPrefix is "ci_runner:<machine id>:", the id made on first use.
func ciVaultPrefix() string {
	ciMachine.Lock()
	defer ciMachine.Unlock()
	if ciMachine.id == "" {
		if v, ok := localStateStore.get(ciMachineKey); ok && v != "" {
			ciMachine.id = v
		} else if id, err := ciNewID(); err == nil {
			if localStateStore.set(ciMachineKey, id) == nil {
				ciMachine.id = id
			}
		}
	}
	if ciMachine.id == "" {
		return "ci_runner:local:"
	}
	return "ci_runner:" + ciMachine.id + ":"
}

// ciAdoptLegacyVaultKeys moves the runner secrets stored before the keys
// carried a machine id ("ci_runner:token:<id>", "ci_runner:secret:...")
// to this machine's keys, for the runners in this machine's config.
func (a *App) ciAdoptLegacyVaultKeys() {
	c := a.ciRunnerConfig()
	ids := []string{"0"}
	if c.ConfigTOML != "" {
		if m, err := ciParseTOML(c.ConfigTOML); err == nil {
			ids = nil
			for i, r := range ciRunners(m) {
				ids = append(ids, ciRunnerID(r, i))
			}
		}
	}
	moved := 0
	for _, id := range ids {
		pairs := [][2]string{{"ci_runner:token:" + id, ciTokenKey(id)}}
		for section, names := range ciCacheSecrets {
			for _, name := range names {
				pairs = append(pairs, [2]string{"ci_runner:secret:" + id + ":" + section + "." + name, ciSecretKey(id, section, name)})
			}
		}
		for _, p := range pairs {
			v, ok, err := a.vault.Get(p[0])
			if err != nil || !ok {
				continue
			}
			if has, err := a.ciVaultHas(p[1]); err != nil || has {
				continue
			}
			if a.vault.Put(p[1], v) == nil {
				_ = a.vault.Delete(p[0])
				moved++
			}
		}
	}
	if moved > 0 {
		log.Printf("ci runner: %d secrets moved to this machine's vault keys", moved)
	}
}

func ciRunnerID(r map[string]any, i int) string {
	if id, _ := r[ciIDField].(string); id != "" {
		return id
	}
	return strconv.Itoa(i)
}

func ciNewID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// ciCopySecrets copies runner from's token and cache secrets to runner to
// and returns the keys copied from, for the caller to delete once its
// save went through.
func (a *App) ciCopySecrets(from, to string) ([]string, error) {
	keys := [][2]string{{ciTokenKey(from), ciTokenKey(to)}}
	for section, names := range ciCacheSecrets {
		for _, name := range names {
			keys = append(keys, [2]string{ciSecretKey(from, section, name), ciSecretKey(to, section, name)})
		}
	}
	var copied []string
	for _, k := range keys {
		v, ok, err := a.vault.Get(k[0])
		if err != nil {
			return nil, fmt.Errorf("runner secrets: %w", err)
		}
		if !ok {
			continue
		}
		if err := a.vault.Put(k[1], v); err != nil {
			return nil, fmt.Errorf("runner secrets: %w", err)
		}
		copied = append(copied, k[0])
	}
	return copied, nil
}

// ciInVault stands in a stored config.toml for a secret kept in the vault:
// left as is it keeps the vault's value, replaced it saves the new one,
// emptied it deletes it.
const ciInVault = "(saved in the vault)"

// ciCacheSecrets are the secret fields of [runners.cache.<section>]; they
// go to the vault like the token.
var ciCacheSecrets = map[string][]string{
	"s3":    {"AccessKey", "SecretKey", "SessionToken"},
	"gcs":   {"PrivateKey"},
	"azure": {"AccountKey"},
}

func ciSecretKey(id, section, name string) string {
	return ciVaultPrefix() + "secret:" + id + ":" + section + "." + name
}

// ciVaultHas reports whether the vault holds a value under key; a failed
// read is an error, not "absent".
func (a *App) ciVaultHas(key string) (bool, error) {
	v, ok, err := a.vault.Get(key)
	if err != nil {
		return false, fmt.Errorf("vault: %w", err)
	}
	return ok && v != "", nil
}

// ciStashCacheSecrets moves a runner's cache secrets to the vault and
// leaves the marker in their place. It returns the keys of emptied
// secrets, which the caller deletes once its save went through.
func (a *App) ciStashCacheSecrets(id string, r map[string]any) ([]string, error) {
	var removed []string
	cache, _ := r["cache"].(map[string]any)
	for section, names := range ciCacheSecrets {
		sec, _ := cache[section].(map[string]any)
		for _, name := range names {
			key := ciSecretKey(id, section, name)
			v, _ := sec[name].(string)
			switch v = strings.TrimSpace(v); v {
			case ciInVault:
				has, err := a.ciVaultHas(key)
				if err != nil {
					return nil, err
				}
				if !has {
					sec[name] = ""
				}
			case "":
				has, err := a.ciVaultHas(key)
				if err != nil {
					return nil, err
				}
				if has {
					removed = append(removed, key)
				}
			default:
				if err := a.vault.Put(key, v); err != nil {
					return nil, fmt.Errorf("runner cache secret: %w", err)
				}
				sec[name] = ciInVault
			}
		}
	}
	return removed, nil
}

// ciClearMarkers empties a runner's vault markers (its token and cache
// keys), for a block that is not the runner they were stored for.
func ciClearMarkers(r map[string]any) {
	if t, _ := r["token"].(string); t == ciInVault {
		delete(r, "token")
	}
	cache, _ := r["cache"].(map[string]any)
	for section, names := range ciCacheSecrets {
		sec, _ := cache[section].(map[string]any)
		for _, name := range names {
			if v, _ := sec[name].(string); v == ciInVault {
				sec[name] = ""
			}
		}
	}
}

// ciFillCacheSecrets puts runner i's cache secrets back from the vault.
func (a *App) ciFillCacheSecrets(id string, r map[string]any) error {
	cache, _ := r["cache"].(map[string]any)
	for section, names := range ciCacheSecrets {
		sec, _ := cache[section].(map[string]any)
		for _, name := range names {
			if v, _ := sec[name].(string); v != ciInVault {
				continue
			}
			v, ok, err := a.vault.Get(ciSecretKey(id, section, name))
			if err != nil {
				return fmt.Errorf("runner cache secret: %w", err)
			}
			if !ok {
				v = ""
			}
			sec[name] = v
		}
	}
	return nil
}

var ciState struct {
	sync.Mutex
	busy      bool
	lastErr   string
	startedAt time.Time
	started   bool // started by this process: stopped on quit
	// closing: quit has begun, nothing new starts. done closes when the
	// operation in flight ends; cancel aborts a start.
	closing bool
	done    chan struct{}
	cancel  context.CancelFunc
	// gen counts successful starts: a route sync of an earlier run sees a
	// newer gen and stops instead of touching the new gateway.
	gen int
}

func (a *App) ciRunnerConfig() CIRunnerConfig {
	var c CIRunnerConfig
	if v, ok := a.localSettingGet(ciRunnerKey); ok {
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
	ids := []string{"0"}
	if c.ConfigTOML != "" {
		if m, err := ciParseTOML(c.ConfigTOML); err == nil {
			ids = nil
			for i, r := range ciRunners(m) {
				ids = append(ids, ciRunnerID(r, i))
			}
		}
	}
	st.Runners = len(ids)
	for _, id := range ids {
		if t, ok, _ := a.vault.Get(ciTokenKey(id)); ok && t != "" {
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
	var tokens map[string]string
	// legacy: index keys copied to runner ids; removed: tokens emptied in
	// the form. Both are deleted from the vault after the save.
	var legacy, removed []string
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
		tokens = map[string]string{}
		seen := map[string]bool{}
		for i, r := range rs {
			id, _ := r[ciIDField].(string)
			if seen[id] {
				// A block copied from another runner, id and all: it is a
				// runner of its own, and the copied markers point at nothing.
				id = ""
				ciClearMarkers(r)
			}
			if id == "" {
				if id, err = ciNewID(); err != nil {
					return c, err
				}
				if _, had := r[ciIDField]; !had {
					// First save with ids: take over what the index held,
					// and let the old keys go once the save is through.
					copied, err := a.ciCopySecrets(strconv.Itoa(i), id)
					if err != nil {
						return c, err
					}
					legacy = append(legacy, copied...)
				}
				r[ciIDField] = id
			}
			seen[id] = true
			raw, present := r["token"]
			t, _ := raw.(string)
			has, err := a.ciVaultHas(ciTokenKey(id))
			if err != nil {
				return c, err
			}
			switch t = strings.TrimSpace(t); {
			case t != "" && t != ciInVault:
				tokens[id] = t
				r["token"] = ciInVault
			case present && t == "":
				// Emptied: the token goes.
				removed = append(removed, ciTokenKey(id))
				delete(r, "token")
			case has:
				// The marker, or no token line in an older stored config:
				// the vault keeps it.
				r["token"] = ciInVault
			default:
				delete(r, "token")
			}
			gone, err := a.ciStashCacheSecrets(id, r)
			if err != nil {
				return c, err
			}
			removed = append(removed, gone...)
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
			tokens = map[string]string{"0": t}
		}
	}
	for id, t := range tokens {
		if err := a.vault.Put(ciTokenKey(id), t); err != nil {
			return c, fmt.Errorf("runner token: %w", err)
		}
	}
	b, _ := json.Marshal(c)
	if err := a.localSettingSet(ciRunnerKey, string(b)); err != nil {
		return c, err
	}
	for _, k := range append(legacy, removed...) {
		if err := a.vault.Delete(k); err != nil {
			log.Printf("ci runner: vault cleanup %s: %v", k, err)
		}
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
	if ciState.busy || ciState.closing {
		return false
	}
	ciState.busy = true
	ciState.done = make(chan struct{})
	return true
}

func ciEnd(err error, started bool) {
	ciState.Lock()
	ciState.busy = false
	ciState.cancel = nil
	if ciState.done != nil {
		close(ciState.done)
		ciState.done = nil
	}
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
	// Cancellable from the first moment: a quit while this runs aborts
	// every docker call it has left.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	ciState.Lock()
	ciState.cancel = cancel
	if ciState.closing {
		// Quit came between ciBegin and here and saw no cancel to call.
		cancel()
	}
	ciState.Unlock()
	defer func() {
		cancel()
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
		return errors.New("no VPN or bastion goes through the app proxy")
	}
	pw, err := a.sharedProxyPassword()
	if err != nil {
		return err
	}
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
	subnet, gwAddr, err := ciNetwork(ctx, append(append([]string{}, details.Routes...), direct...))
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
	ciState.Lock()
	ciState.gen++
	gen := ciState.gen
	ciState.Unlock()
	go a.ciSyncRoutes(gen, subnet, details.Routes)
	a.recordAudit("ci_runner.start", "", map[string]string{
		"routes": strconv.Itoa(len(details.Routes)), "direct": strconv.Itoa(len(direct)),
	})
	return nil
}

// ciRouteSyncEvery is how often a running gateway is brought up to date
// with the app proxy's routes.
const ciRouteSyncEvery = 20 * time.Second

// ciSyncRoutes keeps the running gateway's routes in step with the app
// proxy: a VPN or a jump-host connection added, or a network changed,
// after Start would otherwise go past the gateway (straight out, where
// another VPN on the machine may even accept it and never answer). The
// difference is applied to the live gateway with ip rule, so running jobs
// are not cut. It stops when the runner is no longer ours.
func (a *App) ciSyncRoutes(gen int, jobs netip.Prefix, applied []string) {
	have := map[string]bool{}
	for _, r := range applied {
		have[r] = true
	}
	warned := map[string]bool{}
	lastErr := ""
	t := time.NewTicker(ciRouteSyncEvery)
	defer t.Stop()
	for range t.C {
		ciState.Lock()
		current := ciState.gen == gen && ciState.started && !ciState.closing
		busy := ciState.busy
		ciState.Unlock()
		if !current {
			return
		}
		if busy {
			continue
		}
		d, err := a.NetworkSharedProxyDetails()
		if err != nil {
			// The proxy off, the vault locked: the gateway keeps what it
			// has. Said once per new reason, not every 20 s.
			if err.Error() != lastErr {
				lastErr = err.Error()
				log.Printf("ci runner: gateway routes not updated: %v", err)
			}
			continue
		}
		lastErr = ""
		want := map[string]bool{}
		for _, r := range d.Routes {
			// A route over the jobs' own subnet cannot work: a job takes
			// those addresses for its own network. The subnet was chosen
			// clear of the routes at Start; a later one needs a restart.
			p, err := netip.ParsePrefix(r)
			if err != nil {
				continue
			}
			// The jobs' network and the gateway's tunnel are IPv4 only.
			if !p.Addr().Is4() {
				if !warned[r] {
					warned[r] = true
					log.Printf("ci runner: route %s is IPv6; the jobs' network is IPv4 only, left out", r)
				}
				continue
			}
			if p.Overlaps(jobs) {
				if !warned[r] {
					warned[r] = true
					log.Printf("ci runner: route %s overlaps the jobs' network %s; Stop and Start the runner to move it", r, jobs)
				}
				continue
			}
			want[r] = true
		}
		add, del := ciRouteDiff(have, want)
		if len(add) == 0 && len(del) == 0 {
			continue
		}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		failed, err := ciApplyRoutes(ctx, add, del)
		cancel()
		if err != nil {
			log.Printf("ci runner: gateway routes: %v", err)
			continue
		}
		// A route whose rule could not be added stays out of have, so the
		// next sync tries it again.
		have = want
		for _, r := range failed {
			delete(have, r)
		}
		log.Printf("ci runner: gateway routes updated, %d added, %d removed, %d failed", len(add)-len(failed), len(del), len(failed))
	}
}

// ciRouteDiff is what to add to and remove from have to get want, sorted.
func ciRouteDiff(have, want map[string]bool) (add, del []string) {
	for r := range want {
		if !have[r] {
			add = append(add, r)
		}
	}
	for r := range have {
		if !want[r] {
			del = append(del, r)
		}
	}
	sort.Strings(add)
	sort.Strings(del)
	return add, del
}

// ciApplyRoutes changes the gateway's policy rules in one docker exec and
// returns the added routes whose rule failed. Added routes get priority
// 32765: after the direct (excluded) rules the gateway started with, which
// have smaller numbers and so win, and before the main table's catch-all
// at 32766. A rule that is already gone counts as removed.
func ciApplyRoutes(ctx context.Context, add, del []string) ([]string, error) {
	var cmds []string
	for _, r := range del {
		if _, err := netip.ParsePrefix(r); err != nil {
			continue
		}
		cmds = append(cmds, "ip rule del to "+r+" table 0x22b 2>/dev/null")
	}
	for _, r := range add {
		if _, err := netip.ParsePrefix(r); err != nil {
			continue
		}
		cmds = append(cmds, "ip rule add to "+r+" table 0x22b pref 32765 || echo FAILED "+r)
	}
	if len(cmds) == 0 {
		return nil, nil
	}
	// One shell, every command attempted; each failed add is reported.
	out, err := docker(ctx, nil, "exec", ciGatewayName, "sh", "-c", strings.Join(cmds, "; ")+"; true")
	if err != nil {
		return nil, err
	}
	var failed []string
	for _, line := range strings.Split(out, "\n") {
		if r, ok := strings.CutPrefix(strings.TrimSpace(line), "FAILED "); ok {
			failed = append(failed, r)
		}
	}
	return failed, nil
}

// ciRunnerAutoStart starts the runner when it is set to start with the
// app and is not up yet. Called once the vault opens: the tokens and
// the proxy password live there.
func (a *App) ciRunnerAutoStart() {
	a.ciAdoptLegacyVaultKeys()
	a.ciStashStoredSecrets()
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

// ciStashStoredSecrets moves cache secrets an earlier version left in the
// stored config.toml into the vault (once the vault is open), the way a
// save does now.
func (a *App) ciStashStoredSecrets() {
	c := a.ciRunnerConfig()
	if c.ConfigTOML == "" {
		return
	}
	m, err := ciParseTOML(c.ConfigTOML)
	if err != nil {
		return
	}
	plain := false
	for _, r := range ciRunners(m) {
		cache, _ := r["cache"].(map[string]any)
		for section, names := range ciCacheSecrets {
			sec, _ := cache[section].(map[string]any)
			for _, name := range names {
				if v, _ := sec[name].(string); strings.TrimSpace(v) != "" && v != ciInVault {
					plain = true
				}
			}
		}
	}
	if !plain {
		return
	}
	if _, err := a.CIRunnerSet(c, ""); err != nil {
		log.Printf("ci runner: moving cache secrets to the vault: %v", err)
		return
	}
	log.Printf("ci runner: cache secrets moved from the settings to the vault")
}

// ciRunnerStopOnLock stops the runner when the vault locks, unless it is
// set to keep running: its container holds the tokens and cache keys.
// "Start with the app" brings it back at the next unlock.
func (a *App) ciRunnerStopOnLock() {
	if a.ciRunnerConfig().KeepOnLock {
		return
	}
	go func() {
		// A start in flight (an auto start, say) is aborted and waited for,
		// as at quit, so it cannot bring the runner up after the lock.
		ciState.Lock()
		started, busy, done, cancel := ciState.started, ciState.busy, ciState.done, ciState.cancel
		ciState.Unlock()
		if busy {
			if cancel != nil {
				cancel()
			}
			if done != nil {
				select {
				case <-done:
				case <-time.After(15 * time.Second):
				}
			}
			started = true
		}
		if !started {
			// Not started by this process: only what carries our label,
			// and an unknown state (docker not answering) counts as there.
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			ids, err := docker(ctx, nil, "ps", "-aq", "--filter", "label="+ciLabel)
			cancel()
			if (err == nil && ids == "") || errors.Is(err, errDockerMissing) {
				return
			}
		}
		var err error
		for attempt := 0; attempt < 3; attempt++ {
			if attempt > 0 {
				time.Sleep(5 * time.Second)
			}
			// Unlocked again meanwhile (while a start or docker was waited
			// for, or between attempts): the runner, maybe started anew by
			// the unlock, stays.
			if a.vault.Status().Kind == creds.StatusUnlocked {
				return
			}
			if _, err = a.CIRunnerStop(); err == nil {
				log.Printf("ci runner: stopped, the vault locked")
				return
			}
		}
		log.Printf("ci runner: stop on vault lock: %v", err)
	}()
}

// ciRunnerStopOnQuit removes the containers this process started: with
// the app gone the proxy is gone, and a runner left behind would take
// jobs it cannot finish.
func (a *App) ciRunnerStopOnQuit() {
	ciState.Lock()
	ciState.closing = true
	started, busy, done, cancel := ciState.started, ciState.busy, ciState.done, ciState.cancel
	ciState.Unlock()
	if busy {
		// A start (an auto start after unlock, say) may be half way:
		// abort it, give it a moment to return, and clean up whatever
		// it created.
		if cancel != nil {
			cancel()
		}
		if done != nil {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
			}
		}
		started = true
	}
	if !started {
		return
	}
	// Only what carries our label, found by the label rather than the
	// name (a same-named container is not ours): four docker calls keep
	// quit short.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if ids, err := docker(ctx, nil, "ps", "-aq", "--filter", "label="+ciLabel); err == nil && ids != "" {
		if _, err := docker(ctx, nil, append([]string{"rm", "-f", "-v"}, strings.Fields(ids)...)...); err != nil {
			log.Printf("ci runner: stop on quit: %v", err)
		}
	}
	if ids, err := docker(ctx, nil, "network", "ls", "-q", "--filter", "label="+ciLabel); err == nil && ids != "" {
		rm := append([]string{"network", "rm"}, strings.Fields(ids)...)
		if _, err := docker(ctx, nil, rm...); err != nil {
			// A job still running holds the network (job containers carry
			// the runner's labels, not ours): cut it off and try again.
			ciDisconnectJobs(ctx)
			if _, err := docker(ctx, nil, rm...); err != nil {
				log.Printf("ci runner: stop on quit: %v", err)
			}
		}
	}
}

// ciDisconnectJobs cuts every container off the jobs' network (its runner
// is gone, a job cannot finish anyway).
func ciDisconnectJobs(ctx context.Context) {
	out, err := docker(ctx, nil, "network", "inspect", ciNetworkName, "--format", "{{range .Containers}}{{.Name}} {{end}}")
	if err != nil {
		return
	}
	for _, name := range strings.Fields(out) {
		_, _ = docker(ctx, nil, "network", "disconnect", "-f", ciNetworkName, name)
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
		id := ciRunnerID(r, i)
		delete(r, ciIDField)
		t, ok, verr := a.vault.Get(ciTokenKey(id))
		if verr != nil {
			return "", nil, fmt.Errorf("runner token: %w", verr)
		}
		if !ok || t == "" {
			return "", nil, fmt.Errorf("runner %s has no token", label)
		}
		r["token"] = t
		if err := a.ciFillCacheSecrets(id, r); err != nil {
			return "", nil, err
		}
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

// ciExtraHosts are the runner's "name:ip" entries: the form's and those
// of an imported config.toml.
func (a *App) ciExtraHosts() []string {
	c := a.ciRunnerConfig()
	out := append([]string{}, c.ExtraHosts...)
	if c.ConfigTOML != "" {
		if m, err := ciParseTOML(c.ConfigTOML); err == nil {
			for _, r := range ciRunners(m) {
				if d, ok := r["docker"].(map[string]any); ok {
					if eh, ok := d["extra_hosts"].([]any); ok {
						for _, h := range eh {
							if s, ok := h.(string); ok {
								out = append(out, s)
							}
						}
					}
				}
			}
		}
	}
	return out
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
// gateway container can take it. The subnet is chosen here, not left to
// Docker: Docker Desktop allows a fixed container address (the gateway's)
// only on a network with a user-configured subnet. The first /24 of
// ciSubnetPool that no VPN route or direct destination overlaps and that
// Docker accepts (it refuses one another network uses) is taken.
func ciNetwork(ctx context.Context, avoid []string) (netip.Prefix, netip.Addr, error) {
	var avoidP []netip.Prefix
	for _, a := range avoid {
		if p, err := parseRoute(a); err == nil {
			avoidP = append(avoidP, p)
		}
	}
	var lastErr error
	for _, subnet := range ciSubnets() {
		if slices.ContainsFunc(avoidP, subnet.Overlaps) {
			continue
		}
		gw := subnet.Addr().Next()
		_, err := docker(ctx, nil, "network", "create", "-d", "bridge", "--label", ciLabel+"=network",
			"--subnet", subnet.String(), "--gateway", gw.String(),
			"-o", "com.docker.network.bridge.inhibit_ipv4=true", ciNetworkName)
		if err == nil {
			return subnet, gw, nil
		}
		lastErr = err
		if !strings.Contains(strings.ToLower(err.Error()), "overlap") {
			break
		}
		if ctx.Err() != nil {
			break
		}
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("every subnet in %s overlaps a VPN network", ciSubnetPool)
	}
	return netip.Prefix{}, netip.Addr{}, fmt.Errorf("jobs network: %w", lastErr)
}

// ciSubnetPool is where the jobs' network takes its /24 from: the top of
// the private 172.16.0.0/12, away from Docker's own defaults (172.17-
// 172.29 and 192.168.x for user networks).
var ciSubnetPool = netip.MustParsePrefix("172.30.0.0/15")

// ciSubnets lists the /24s of ciSubnetPool, from the top down.
func ciSubnets() []netip.Prefix {
	b := ciSubnetPool.Addr().As4()
	base := uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8
	n := 1 << (24 - ciSubnetPool.Bits())
	out := make([]netip.Prefix, 0, n)
	for i := n - 1; i >= 0; i-- {
		v := base + uint32(i)<<8
		out = append(out, netip.PrefixFrom(netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), 0}), 24))
	}
	return out
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
	// A job still running holds the network.
	ciDisconnectJobs(ctx)
	if _, err := docker(ctx, nil, "network", "rm", ciNetworkName); err != nil {
		return fmt.Errorf("remove %s: %w", ciNetworkName, err)
	}
	return nil
}

var errDockerMissing = errors.New("docker is not installed (or not on PATH)")

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
			return "", errDockerMissing
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
