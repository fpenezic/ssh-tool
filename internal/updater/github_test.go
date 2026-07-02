package updater

import "testing"

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
