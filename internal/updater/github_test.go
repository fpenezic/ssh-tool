package updater

import (
	"strings"
	"testing"
)

func TestParseGitHubRelease(t *testing.T) {
	body := []byte(`{
		"tag_name": "v0.45.0",
		"published_at": "2026-07-02T12:00:00Z",
		"html_url": "https://github.com/fpenezic/ssh-tool/releases/tag/v0.45.0",
		"body": "### Fixed\n- something",
		"assets": [
			{"name": "ssh-tool-linux-amd64", "size": 100,
			 "browser_download_url": "https://github.com/dl/linux-amd64",
			 "digest": "sha256:aabbcc"},
			{"name": "ssh-tool-windows-arm64.exe", "size": 200,
			 "browser_download_url": "https://github.com/dl/win-arm64",
			 "digest": ""},
			{"name": "ssh-tool-android-arm64.apk", "size": 300,
			 "browser_download_url": "https://github.com/dl/apk",
			 "digest": "sha256:ddeeff"},
			{"name": "checksums.txt", "size": 1,
			 "browser_download_url": "https://github.com/dl/sums", "digest": ""}
		]
	}`)
	info, err := parseGitHubRelease(body)
	if err != nil {
		t.Fatal(err)
	}
	if info.Version != "v0.45.0" || info.ChangelogURL == "" || info.NotesMD == "" {
		t.Errorf("metadata not mapped: %+v", info)
	}
	if len(info.Assets) != 3 {
		t.Fatalf("want 3 platform assets, got %d: %+v", len(info.Assets), info.Assets)
	}
	lin := info.Assets["linux-amd64"]
	if lin.URL != "https://github.com/dl/linux-amd64" || lin.SHA256 != "aabbcc" || lin.Size != 100 {
		t.Errorf("linux-amd64 = %+v", lin)
	}
	if win := info.Assets["windows-arm64"]; win.SHA256 != "" || win.Size != 200 {
		t.Errorf("windows-arm64 = %+v", win)
	}
	if _, ok := info.Assets["android-arm64"]; !ok {
		t.Error("apk asset not mapped")
	}
}

func TestParseGitHubRelease_Malformed(t *testing.T) {
	if _, err := parseGitHubRelease([]byte(`{"assets": []}`)); err == nil {
		t.Error("expected error for missing tag_name")
	}
	if _, err := parseGitHubRelease([]byte(`not json`)); err == nil {
		t.Error("expected error for non-JSON")
	}
}

func TestAssetPlatformKey(t *testing.T) {
	cases := map[string]struct {
		key string
		ok  bool
	}{
		"ssh-tool-linux-amd64":       {"linux-amd64", true},
		"ssh-tool-linux-arm64":       {"linux-arm64", true},
		"ssh-tool-windows-amd64.exe": {"windows-amd64", true},
		"ssh-tool-android-arm64.apk": {"android-arm64", true},
		"ssh-tool-0.45.0.tar.gz":     {"", false},
		"checksums.txt":              {"", false},
		"ssh-tool-":                  {"", false},
	}
	for in, want := range cases {
		key, ok := assetPlatformKey(in)
		if key != want.key || ok != want.ok {
			t.Errorf("assetPlatformKey(%q) = %q,%v want %q,%v", in, key, ok, want.key, want.ok)
		}
	}
}

func TestHelperTagMajor(t *testing.T) {
	cases := []struct {
		tag    string
		want   int
		wantOK bool
	}{
		{"helper-v1", 1, true},
		{"helper-v2", 2, true},
		{"helper-v10", 10, true},
		{"helper-v", 0, false},   // no number
		{"helper-vx", 0, false},  // non-numeric
		{"v0.49.0", 0, false},    // app tag
		{"helper-v1.2", 1, true}, // a patched helper keeps its major
		{"helper-v1.", 0, false},
		{"helper-v1.2.3", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		got, ok := helperTagMajor(c.tag)
		if ok != c.wantOK || (ok && got != c.want) {
			t.Errorf("helperTagMajor(%q) = (%d,%v), want (%d,%v)", c.tag, got, ok, c.want, c.wantOK)
		}
	}
}

func TestFilterReleaseList(t *testing.T) {
	// Newest-first, with a prerelease and a draft mixed in.
	list := []ghReleasePayload{
		{TagName: "v0.66.0", Body: "notes 66"},
		{TagName: "v0.66.0-rc1", Prerelease: true, Body: "rc"},
		{TagName: "v0.65.0", Body: "notes 65"},
		{TagName: "v0.64.0", Body: "notes 64"},
		{TagName: "v0.63.0-draft", Draft: true, Body: "draft"},
		{TagName: "v0.63.0", Body: "notes 63"},
	}
	// Simple numeric-ish tag compare good enough for the test range
	// (0.63 < x <= 0.66); the app supplies the real semver comparator.
	rank := map[string]int{"v0.63.0": 63, "v0.64.0": 64, "v0.65.0": 65, "v0.66.0": 66}
	inRange := func(tag string) bool {
		r, ok := rank[tag]
		return ok && r > 63 && r <= 66
	}
	got := filterReleaseList(list, inRange)
	want := []string{"v0.66.0", "v0.65.0", "v0.64.0"} // not 0.63 (=from), not rc/draft
	if len(got) != len(want) {
		t.Fatalf("got %d releases, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i].Version != want[i] {
			t.Fatalf("at %d: got %q, want %q", i, got[i].Version, want[i])
		}
		if got[i].NotesMD == "" {
			t.Fatalf("%s has no notes", got[i].Version)
		}
	}
}

func TestPickLatestApp(t *testing.T) {
	// Model the exact regression: a re-published helper-v1 lands FIRST in
	// GitHub's newest-first list (most recent published_at), but the app
	// update check must still pick the newest v* app release, never the
	// helper.
	list := []ghReleasePayload{
		{TagName: "helper-v1"}, // freshly re-published, newest by date
		{TagName: "v0.68.0"},   // the real latest app release
		{TagName: "v0.67.2"},
		{TagName: "v0.68.1-rc1", Prerelease: true},
		{TagName: "v0.60.0-draft", Draft: true},
	}
	isAppTag := func(tag string) bool {
		return strings.HasPrefix(tag, "v") && !strings.HasPrefix(tag, "helper-")
	}
	// Numeric-ish rank good enough for this range.
	rank := map[string]int{"v0.67.2": 6702, "v0.68.0": 6800}
	newer := func(a, b string) bool { return rank[a] > rank[b] }

	best := pickLatestApp(list, isAppTag, newer, nil)
	if best == nil {
		t.Fatal("pickLatestApp returned nil")
	}
	if best.TagName != "v0.68.0" {
		t.Fatalf("picked %q, want v0.68.0 (helper/rc/draft must not win)", best.TagName)
	}
}

func TestPickLatestApp_NoAppReleases(t *testing.T) {
	list := []ghReleasePayload{{TagName: "helper-v1"}, {TagName: "helper-v2"}}
	isAppTag := func(tag string) bool {
		return strings.HasPrefix(tag, "v") && !strings.HasPrefix(tag, "helper-")
	}
	if best := pickLatestApp(list, isAppTag, func(a, b string) bool { return false }, nil); best != nil {
		t.Fatalf("expected nil when no app releases, got %q", best.TagName)
	}
}

// The rc channel takes release candidates; stable and -test prereleases
// stay out either way.
func TestPickLatestAppPrereleaseChannel(t *testing.T) {
	list := []ghReleasePayload{
		{TagName: "v0.110.0"},
		{TagName: "v0.111.0-rc1", Prerelease: true},
		{TagName: "v0.111.0-test", Prerelease: true},
	}
	isApp := func(string) bool { return true }
	newer := func(a, b string) bool { return a > b } // lexical is enough for these tags
	if b := pickLatestApp(list, isApp, newer, nil); b == nil || b.TagName != "v0.110.0" {
		t.Fatalf("stable picked %+v", b)
	}
	rcOnly := func(tag string) bool { return strings.HasSuffix(tag, "-rc1") }
	if b := pickLatestApp(list, isApp, newer, rcOnly); b == nil || b.TagName != "v0.111.0-rc1" {
		t.Fatalf("rc channel picked %+v", b)
	}
}

// A patched helper (helper-v1.1) is picked over the original helper-v1, and
// a higher major still wins when the app speaks it.
func TestHelperPatchTagOrdering(t *testing.T) {
	for _, c := range []struct {
		tag          string
		major, patch int
	}{{"helper-v1", 1, 0}, {"helper-v1.1", 1, 1}, {"helper-v1.10", 1, 10}, {"helper-v2", 2, 0}} {
		m, p, ok := helperTagVersion(c.tag)
		if !ok || m != c.major || p != c.patch {
			t.Errorf("helperTagVersion(%q) = %d.%d,%v", c.tag, m, p, ok)
		}
	}
}

func TestPickHelperReleasePrefersPatch(t *testing.T) {
	list := []ghReleasePayload{ // newest-first, as GitHub returns them
		{TagName: "v0.111.0"},
		{TagName: "helper-v1.1"},
		{TagName: "helper-v2"},
		{TagName: "helper-v1"},
		{TagName: "helper-v1.2", Draft: true},
	}
	if b := pickHelperRelease(list, 1); b == nil || b.TagName != "helper-v1.1" {
		t.Fatalf("max 1 picked %+v, want helper-v1.1", b)
	}
	if b := pickHelperRelease(list, 2); b == nil || b.TagName != "helper-v2" {
		t.Fatalf("max 2 picked %+v, want helper-v2", b)
	}
}
