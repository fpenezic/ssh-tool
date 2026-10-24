package main

import (
	"context"
	"net/netip"
	"strings"
	"testing"
	"time"
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
	if tok, ok, _ := a.vault.Get(ciTokenKey(0)); !ok || tok != "glrt-secret-one" {
		t.Fatalf("vault token = %q, %v", tok, ok)
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
	if tok, _, _ := a.vault.Get(ciTokenKey(0)); tok != "glrt-secret-one" {
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
// in flight before it cleans up.
func TestCIQuitWaitsAndBlocksStarts(t *testing.T) {
	a := &App{}
	if !ciBegin() {
		t.Fatal("begin refused")
	}
	cancelled := make(chan struct{})
	ciState.Lock()
	ciState.cancel = func() { close(cancelled) }
	ciState.Unlock()
	go func() {
		<-cancelled
		ciEnd(nil, false)
	}()
	ciState.Lock()
	ciState.started = false
	ciState.Unlock()
	finished := make(chan struct{})
	go func() {
		a.ciRunnerStopOnQuit()
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(15 * time.Second):
		t.Fatal("quit did not return")
	}
	if ciBegin() {
		t.Fatal("a start began after quit")
	}
	ciState.Lock()
	ciState.closing, ciState.started = false, false
	ciState.Unlock()
}
