package main

import (
	"context"
	"encoding/json"
	"net"
	"net/netip"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"

	sshlayer "ssh-tool/internal/ssh"
)

const ciTestTOML = `concurrent = 1
check_interval = 0

[[runners]]
  name = "dev"
  url = "http://192.0.2.10"
  token = "glrt-secret-one"
  executor = "docker"
  [runners.cache]
    Type = "s3"
    [runners.cache.s3]
      ServerAddress = "192.0.2.20:9000"
      AccessKey = "cache-access-one"
      SecretKey = "cache-secret-one"
  [runners.docker]
    image = "alpine:3.22"
    volumes = ["/cache"]
    extra_hosts = ["gitlab.example.com:192.0.2.10"]
`

// An imported config.toml keeps its token out of the stored setting, and
// the built one has it back, the jobs on the gateway's network and the
// extra hosts moved to the gateway.
func TestCIRunnerImportAndBuild(t *testing.T) {
	a := newDbTestApp(t)
	c, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: ciTestTOML}, "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(c.ConfigTOML, "glrt-secret-one") {
		t.Fatalf("token left in the stored config:\n%s", c.ConfigTOML)
	}
	if stored := a.ciRunnerConfig(); strings.Contains(stored.ConfigTOML, "glrt-") {
		t.Fatal("token in the settings row")
	}
	if tok, ok, _ := a.vault.Get(ciTokenKey(ciStoredID(t, a, 0))); !ok || tok != "glrt-secret-one" {
		t.Fatalf("vault token = %q, %v", tok, ok)
	}
	for _, secret := range []string{"cache-access-one", "cache-secret-one"} {
		if strings.Contains(c.ConfigTOML, secret) {
			t.Fatalf("cache secret %q left in the stored config", secret)
		}
	}
	if !strings.Contains(c.ConfigTOML, ciInVault) {
		t.Fatalf("no vault marker in the stored config:\n%s", c.ConfigTOML)
	}

	text, direct, err := a.ciBuildTOML(a.ciRunnerConfig())
	if err != nil {
		t.Fatal(err)
	}
	m, err := ciParseTOML(text)
	if err != nil {
		t.Fatal(err)
	}
	r := ciRunners(m)[0]
	if r["token"] != "glrt-secret-one" {
		t.Fatalf("built token = %v", r["token"])
	}
	s3 := r["cache"].(map[string]any)["s3"].(map[string]any)
	if s3["AccessKey"] != "cache-access-one" || s3["SecretKey"] != "cache-secret-one" {
		t.Fatalf("built cache secrets = %v / %v", s3["AccessKey"], s3["SecretKey"])
	}
	d := r["docker"].(map[string]any)
	if d["network_mode"] != ciNetworkName {
		t.Fatalf("network_mode = %v", d["network_mode"])
	}
	if eh, _ := d["extra_hosts"].([]any); len(eh) != 1 || eh[0] != "gitlab.example.com:192.0.2.10" {
		t.Fatalf("extra_hosts = %v", d["extra_hosts"])
	}
	if strings.Join(direct, ",") != "192.0.2.10,192.0.2.20" {
		t.Fatalf("auto direct = %v", direct)
	}

	// The form's extra hosts join the runner's own.
	c = a.ciRunnerConfig()
	c.ExtraHosts = []string{"app01.customer.example:10.1.2.3"}
	if _, err := a.CIRunnerSet(c, ""); err != nil {
		t.Fatal(err)
	}
	text, _, err = a.ciBuildTOML(a.ciRunnerConfig())
	if err != nil {
		t.Fatal(err)
	}
	m, _ = ciParseTOML(text)
	if eh, _ := ciRunners(m)[0]["docker"].(map[string]any)["extra_hosts"].([]any); len(eh) != 2 {
		t.Fatalf("merged extra_hosts = %v", eh)
	}

	// Saving the stored (token-free) config again keeps the vault token.
	if _, err := a.CIRunnerSet(a.ciRunnerConfig(), ""); err != nil {
		t.Fatal(err)
	}
	if tok, _, _ := a.vault.Get(ciTokenKey(ciStoredID(t, a, 0))); tok != "glrt-secret-one" {
		t.Fatalf("token lost on re-save: %q", tok)
	}
}

func TestCIRunnerFormConfig(t *testing.T) {
	a := newDbTestApp(t)
	if _, err := a.CIRunnerSet(CIRunnerConfig{URL: "gitlab.example.com"}, "t"); err == nil {
		t.Fatal("URL without a scheme accepted")
	}
	if _, err := a.CIRunnerSet(CIRunnerConfig{URL: "https://gitlab.example.com"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.ciBuildTOML(a.ciRunnerConfig()); err == nil || !strings.Contains(err.Error(), "no token") {
		t.Fatalf("build without a token: %v", err)
	}
	if _, err := a.CIRunnerSet(CIRunnerConfig{URL: "https://gitlab.example.com"}, "glrt-x"); err != nil {
		t.Fatal(err)
	}
	text, direct, err := a.ciBuildTOML(a.ciRunnerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, `network_mode = '`+ciNetworkName+`'`) && !strings.Contains(text, `network_mode = "`+ciNetworkName+`"`) {
		t.Fatalf("no network_mode:\n%s", text)
	}
	if len(direct) != 1 || direct[0] != "gitlab.example.com" {
		t.Fatalf("auto direct = %v", direct)
	}
}

func TestCIRunnerRefusesOtherExecutors(t *testing.T) {
	a := newDbTestApp(t)
	src := strings.Replace(ciTestTOML, `executor = "docker"`, `executor = "shell"`, 1)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: src}, ""); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.ciBuildTOML(a.ciRunnerConfig()); err == nil || !strings.Contains(err.Error(), "docker executor") {
		t.Fatalf("shell executor: %v", err)
	}
}

func TestCINormHosts(t *testing.T) {
	got, err := ciNormHosts([]string{" git:192.0.2.1 ", "git:192.0.2.1", "v6:2001:db8::1", ""})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "git:192.0.2.1,v6:2001:db8::1" {
		t.Fatalf("got %v", got)
	}
	for _, bad := range []string{"git", "git:192.1680.31", ":192.0.2.1", "a b:192.0.2.1"} {
		if _, err := ciNormHosts([]string{bad}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestCIDirectRoutes(t *testing.T) {
	got, err := ciDirectRoutes(context.Background(), []string{"192.0.2.0/24", "192.0.2.5", "198.51.100.7"}, []string{"name.invalid"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got, ",") != "192.0.2.0/24,198.51.100.7/32" {
		t.Fatalf("got %v", got)
	}
	if _, err := ciDirectRoutes(context.Background(), []string{"name.invalid"}, nil); err == nil {
		t.Fatal("unresolvable typed name accepted")
	}
}

func TestCILastAddr(t *testing.T) {
	for in, want := range map[string]string{"172.18.0.0/16": "172.18.255.254", "192.0.2.0/24": "192.0.2.254", "10.0.0.8/30": "10.0.0.10"} {
		got, err := ciLastAddr(netip.MustParsePrefix(in))
		if err != nil || got.String() != want {
			t.Errorf("%s -> %v, %v (want %s)", in, got, err, want)
		}
	}
	if _, err := ciLastAddr(netip.MustParsePrefix("192.0.2.1/32")); err == nil {
		t.Error("/32 accepted")
	}
}

// Once quit has begun no start begins, and quit waits for the operation
// in flight (cancelling it) before it cleans up. The docker CLI is
// swapped for a missing one: the cleanup must not touch a real daemon.
func TestCIQuitWaitsAndBlocksStarts(t *testing.T) {
	realBin := dockerBin
	dockerBin = func() string { return "/nonexistent/docker-for-test" }
	defer func() { dockerBin = realBin }()
	a := &App{}
	if !ciBegin() {
		t.Fatal("begin refused")
	}
	cancelled := make(chan struct{})
	ended := make(chan struct{})
	ciState.Lock()
	ciState.cancel = func() { close(cancelled) }
	ciState.Unlock()
	go func() {
		<-cancelled
		ciEnd(nil, false)
		close(ended)
	}()
	finished := make(chan struct{})
	go func() {
		a.ciRunnerStopOnQuit()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("quit did not return before its 10 s fallback")
	}
	ciState.Lock()
	stillBusy := ciState.busy
	ciState.Unlock()
	if stillBusy {
		t.Fatal("quit returned before the start in flight ended")
	}
	<-ended
	if ciBegin() {
		t.Fatal("a start began after quit")
	}
	ciState.Lock()
	ciState.closing, ciState.started = false, false
	ciState.Unlock()
}

func TestCISubnets(t *testing.T) {
	sn := ciSubnets()
	if len(sn) != 512 || sn[0].String() != "172.31.255.0/24" || sn[511].String() != "172.30.0.0/24" {
		t.Fatalf("got %d, first %v, last %v", len(sn), sn[0], sn[len(sn)-1])
	}
	for _, p := range sn {
		if !ciSubnetPool.Contains(p.Addr()) || p.Bits() != 24 {
			t.Fatalf("%v outside the pool", p)
		}
	}
}

func TestCIRouteDiff(t *testing.T) {
	have := map[string]bool{"10.0.0.0/8": true, "192.0.2.0/24": true}
	want := map[string]bool{"10.0.0.0/8": true, "10.239.248.54/32": true}
	add, del := ciRouteDiff(have, want)
	if strings.Join(add, ",") != "10.239.248.54/32" || strings.Join(del, ",") != "192.0.2.0/24" {
		t.Fatalf("add %v del %v", add, del)
	}
}

// Emptying a cache secret in the stored config deletes it from the vault;
// the marker keeps it.
func TestCICacheSecretEmptied(t *testing.T) {
	a := newDbTestApp(t)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: ciTestTOML}, ""); err != nil {
		t.Fatal(err)
	}
	stored := a.ciRunnerConfig().ConfigTOML
	cleared := strings.Replace(stored, "AccessKey = '"+ciInVault+"'", "AccessKey = ''", 1)
	if cleared == stored {
		t.Fatalf("marker not found as expected:\n%s", stored)
	}
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: cleared}, ""); err != nil {
		t.Fatal(err)
	}
	id := ciStoredID(t, a, 0)
	if has, _ := a.ciVaultHas(ciSecretKey(id, "s3", "AccessKey")); has {
		t.Error("emptied AccessKey still in the vault")
	}
	if has, _ := a.ciVaultHas(ciSecretKey(id, "s3", "SecretKey")); !has {
		t.Error("SecretKey (marker kept) lost")
	}
}

// A config stored by an earlier version, cache secrets in plain text, is
// moved to the vault once the vault opens.
func TestCIStashStoredSecrets(t *testing.T) {
	a := newDbTestApp(t)
	b, _ := json.Marshal(CIRunnerConfig{ConfigTOML: strings.Replace(ciTestTOML, `token = "glrt-secret-one"`, "", 1)})
	if err := a.db.SetSetting(ciRunnerKey, string(b)); err != nil {
		t.Fatal(err)
	}
	a.ciStashStoredSecrets()
	if strings.Contains(a.ciRunnerConfig().ConfigTOML, "cache-secret-one") {
		t.Fatal("cache secret still in the settings")
	}
	if has, _ := a.ciVaultHas(ciSecretKey(ciStoredID(t, a, 0), "s3", "SecretKey")); !has {
		t.Fatal("cache secret not in the vault")
	}
}

// ciStoredID is runner i's id in the stored config.
func ciStoredID(t *testing.T, a *App, i int) string {
	t.Helper()
	m, err := ciParseTOML(a.ciRunnerConfig().ConfigTOML)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := ciRunners(m)[i][ciIDField].(string)
	if id == "" {
		t.Fatalf("runner %d has no id", i)
	}
	return id
}

// Swapping two runners in the stored config keeps each one's token: the
// vault keys follow the runner's id, not its position; the id never
// reaches the runner's config.
func TestCIRunnerReorderKeepsSecrets(t *testing.T) {
	a := newDbTestApp(t)
	two := ciTestTOML + `
[[runners]]
  name = "second"
  url = "https://gitlab.example.com"
  token = "glrt-secret-two"
  executor = "docker"
  [runners.docker]
    image = "alpine:3.22"
`
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: two}, ""); err != nil {
		t.Fatal(err)
	}
	m, _ := ciParseTOML(a.ciRunnerConfig().ConfigTOML)
	rs := m["runners"].([]any)
	m["runners"] = []any{rs[1], rs[0]}
	swapped, _ := toml.Marshal(m)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: string(swapped)}, ""); err != nil {
		t.Fatal(err)
	}
	text, _, err := a.ciBuildTOML(a.ciRunnerConfig())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(text, ciIDField) {
		t.Fatal("the id reached the runner's config")
	}
	built, _ := ciParseTOML(text)
	got := ciRunners(built)
	if got[0]["name"] != "second" || got[0]["token"] != "glrt-secret-two" || got[1]["token"] != "glrt-secret-one" {
		t.Fatalf("tokens after reorder: %v=%v, %v=%v", got[0]["name"], got[0]["token"], got[1]["name"], got[1]["token"])
	}
}

// A config stored before ids (secrets under index keys) keeps working and
// moves to ids at the next save.
func TestCIRunnerLegacyIndexKeys(t *testing.T) {
	a := newDbTestApp(t)
	if err := a.vault.Put(ciTokenKey("0"), "glrt-legacy"); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(CIRunnerConfig{ConfigTOML: strings.Replace(ciTestTOML, `token = "glrt-secret-one"`, "", 1)})
	if err := a.db.SetSetting(ciRunnerKey, string(b)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := a.ciBuildTOML(a.ciRunnerConfig()); err != nil {
		t.Fatalf("legacy build: %v", err)
	}
	if _, err := a.CIRunnerSet(a.ciRunnerConfig(), ""); err != nil {
		t.Fatal(err)
	}
	if tok, _, _ := a.vault.Get(ciTokenKey(ciStoredID(t, a, 0))); tok != "glrt-legacy" {
		t.Fatalf("moved token = %q", tok)
	}
	if has, _ := a.ciVaultHas(ciTokenKey("0")); has {
		t.Error("legacy index key left behind")
	}
}

// A [[runners]] block copied from another, id included, becomes a runner
// of its own; emptying a token deletes it.
func TestCIRunnerCopiedBlockAndEmptiedToken(t *testing.T) {
	a := newDbTestApp(t)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: ciTestTOML}, ""); err != nil {
		t.Fatal(err)
	}
	m, _ := ciParseTOML(a.ciRunnerConfig().ConfigTOML)
	rs := m["runners"].([]any)
	dup, _ := ciParseTOML(a.ciRunnerConfig().ConfigTOML)
	copyBlock := ciRunners(dup)[0]
	copyBlock["name"] = "copy"
	copyBlock["token"] = "glrt-copy"
	m["runners"] = append(rs, copyBlock)
	text, _ := toml.Marshal(m)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: string(text)}, ""); err != nil {
		t.Fatal(err)
	}
	first, second := ciStoredID(t, a, 0), ciStoredID(t, a, 1)
	if first == second {
		t.Fatal("copied block kept the same id")
	}
	if tok, _, _ := a.vault.Get(ciTokenKey(first)); tok != "glrt-secret-one" {
		t.Fatalf("first token = %q", tok)
	}
	if tok, _, _ := a.vault.Get(ciTokenKey(second)); tok != "glrt-copy" {
		t.Fatalf("copy token = %q", tok)
	}
	if has, _ := a.ciVaultHas(ciSecretKey(second, "s3", "SecretKey")); has {
		t.Error("copy got the first runner's cache secret")
	}

	// Empty the first runner's token.
	m, _ = ciParseTOML(a.ciRunnerConfig().ConfigTOML)
	ciRunners(m)[0]["token"] = ""
	text, _ = toml.Marshal(m)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: string(text)}, ""); err != nil {
		t.Fatal(err)
	}
	if has, _ := a.ciVaultHas(ciTokenKey(first)); has {
		t.Error("emptied token still in the vault")
	}
}

// The CI runner is this machine's: its setting stays out of the synced
// store, its vault keys carry this machine's id, and secrets stored under
// the old keys are taken over.
func TestCIRunnerIsMachineLocal(t *testing.T) {
	a := newDbTestApp(t)
	if err := a.vault.Put("ci_runner:token:0", "glrt-legacy"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CIRunnerSet(CIRunnerConfig{URL: "https://gitlab.example.com"}, ""); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := a.db.GetSetting(ciRunnerKey); ok {
		t.Fatal("the CI runner setting is in the synced store")
	}
	if _, ok := localStateStore.get(ciRunnerKey); !ok {
		t.Fatal("the CI runner setting is not in local state")
	}
	if !strings.HasPrefix(ciTokenKey("0"), "ci_runner:") || ciTokenKey("0") == "ci_runner:token:0" {
		t.Fatalf("vault key without a machine id: %s", ciTokenKey("0"))
	}
	a.ciAdoptLegacyVaultKeys()
	if tok, _, _ := a.vault.Get(ciTokenKey("0")); tok != "glrt-legacy" {
		t.Fatalf("adopted token = %q", tok)
	}
	// The old key stays for a machine that still reads it.
	if _, ok, _ := a.vault.Get("ci_runner:token:0"); !ok {
		t.Error("legacy key deleted")
	}
}

// Whether the app proxy listens, and where, is this machine's; the order
// of the profiles syncs, with listening off for a machine that pulls it.
func TestSharedProxyListenIsMachineLocal(t *testing.T) {
	a := newDbTestApp(t)
	if err := a.NetworkSharedProxySet(SharedProxyConfig{Enabled: true, Bind: "127.0.0.1", Port: freePort(t), Priority: []string{"p1"}}); err != nil {
		t.Fatal(err)
	}
	defer stopSharedProxy()
	v, ok, _ := a.db.GetSetting(sharedProxyKey)
	if !ok {
		t.Fatal("no synced app proxy setting")
	}
	var synced SharedProxyConfig
	_ = json.Unmarshal([]byte(v), &synced)
	if synced.Enabled || synced.Bind != "" || synced.Port != 0 || len(synced.Priority) != 1 {
		t.Fatalf("synced part = %+v", synced)
	}
	if c := a.sharedProxyConfig(); !c.Enabled || c.Bind != "127.0.0.1" || c.Port == 0 {
		t.Fatalf("this machine's config = %+v", c)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

// A runner setting still in the synced store moves to this machine at
// unlock, and the store row goes.
func TestCIRunnerSettingMovesOutOfTheStore(t *testing.T) {
	a := newDbTestApp(t)
	if err := a.db.SetSetting(ciRunnerKey, `{"url":"https://gitlab.example.com"}`); err != nil {
		t.Fatal(err)
	}
	a.ciRunnerAutoStart()
	if _, ok, _ := a.db.GetSetting(ciRunnerKey); ok {
		t.Error("store row still there")
	}
	if c := a.ciRunnerConfig(); c.URL != "https://gitlab.example.com" {
		t.Errorf("config = %+v", c)
	}
}

// A dial through a just-started tunnel gets at least the floor, even when
// the connect deadline is almost spent; an explicit cancel still ends it.
func TestWithDialFloor(t *testing.T) {
	parent, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	c, done := withDialFloor(parent, time.Second)
	defer done()
	time.Sleep(30 * time.Millisecond)
	if c.Err() != nil {
		t.Fatal("floor context ended with the parent's deadline")
	}
	parent2, cancel2 := context.WithTimeout(context.Background(), time.Minute)
	c2, done2 := withDialFloor(parent2, time.Second)
	defer done2()
	cancel2()
	select {
	case <-c2.Done():
	case <-time.After(time.Second):
		t.Fatal("cancel of the parent did not reach the dial")
	}
	parent3, cancel3 := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel3()
	c3, done3 := withDialFloor(parent3, time.Second)
	defer done3()
	time.Sleep(20 * time.Millisecond) // the deadline passes first
	cancel3()
	time.Sleep(20 * time.Millisecond)
	if c3.Err() != nil {
		t.Error("a parent cancelled after its deadline passed ended the floor")
	}
}

// The connect's own context, handed down by firstHopDial, reaches the
// floored dial even after the hop deadline has passed.
func TestWithDialFloorWatchesTheConnect(t *testing.T) {
	outer, cancelOuter := context.WithCancel(context.Background())
	hop, cancelHop := context.WithTimeout(context.WithValue(outer, sshlayer.ConnectCtxKey{}, outer), 10*time.Millisecond)
	defer cancelHop()
	c, done := withDialFloor(hop, time.Second)
	defer done()
	time.Sleep(20 * time.Millisecond) // the hop deadline passes
	if c.Err() != nil {
		t.Fatal("the floor ended with the hop deadline")
	}
	cancelOuter()
	select {
	case <-c.Done():
	case <-time.After(time.Second):
		t.Fatal("a cancel of the connect after the hop deadline did not reach the dial")
	}
}

// A save whose settings write fails leaves the vault as it was: the old
// config keeps its old token and cache keys.
func TestCIRunnerFailedSaveRollsBackSecrets(t *testing.T) {
	a := newDbTestApp(t)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: ciTestTOML}, ""); err != nil {
		t.Fatal(err)
	}
	id := ciStoredID(t, a, 0)
	m, _ := ciParseTOML(a.ciRunnerConfig().ConfigTOML)
	r := ciRunners(m)[0]
	r["token"] = "glrt-new"
	r["cache"].(map[string]any)["s3"].(map[string]any)["SecretKey"] = "cache-secret-new"
	text, _ := toml.Marshal(m)

	// The settings write fails: local state points into a missing directory.
	good := localStateStore.path
	localStateStore.path = filepath.Join(t.TempDir(), "missing", "local-state.json")
	_, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: string(text)}, "")
	localStateStore.path = good
	if err == nil {
		t.Fatal("save succeeded with an unwritable local state")
	}
	if tok, _, _ := a.vault.Get(ciTokenKey(id)); tok != "glrt-secret-one" {
		t.Errorf("token after the failed save = %q", tok)
	}
	if sk, _, _ := a.vault.Get(ciSecretKey(id, "s3", "SecretKey")); sk != "cache-secret-one" {
		t.Errorf("cache secret after the failed save = %q", sk)
	}
}

// Index-keyed secrets go only to the same runner at the same place: a
// reordered pre-id config does not hand one runner another's token.
func TestCIRunnerLegacyReorderDoesNotInherit(t *testing.T) {
	a := newDbTestApp(t)
	two := strings.Replace(ciTestTOML, `token = "glrt-secret-one"`, "", 1) + `
[[runners]]
  name = "second"
  url = "https://gitlab.example.com"
  executor = "docker"
`
	if err := a.vault.Put(ciTokenKey("0"), "glrt-first"); err != nil {
		t.Fatal(err)
	}
	if err := localStateStore.set(ciRunnerKey, mustJSON(CIRunnerConfig{ConfigTOML: two})); err != nil {
		t.Fatal(err)
	}
	m, _ := ciParseTOML(two)
	rs := m["runners"].([]any)
	m["runners"] = []any{rs[1], rs[0]}
	swapped, _ := toml.Marshal(m)
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: string(swapped)}, ""); err != nil {
		t.Fatal(err)
	}
	if has, _ := a.ciVaultHas(ciTokenKey(ciStoredID(t, a, 0))); has {
		t.Error("the moved second runner inherited the first runner's token")
	}
}

func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

// An array-valued url or name in both the saved and the submitted config
// is no match, not a panic.
func TestCIRunnerLegacyArrayFieldsNoPanic(t *testing.T) {
	a := newDbTestApp(t)
	odd := `
[[runners]]
  name = ["a", "b"]
  url = ["https://gitlab.example.com"]
  executor = "docker"
`
	if err := localStateStore.set(ciRunnerKey, mustJSON(CIRunnerConfig{ConfigTOML: odd})); err != nil {
		t.Fatal(err)
	}
	if _, err := a.CIRunnerSet(CIRunnerConfig{ConfigTOML: odd}, ""); err != nil {
		t.Fatalf("CIRunnerSet: %v", err)
	}
}
