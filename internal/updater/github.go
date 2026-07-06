package updater

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// GitHub Releases as an update source. The release workflow uploads
// binaries named ssh-tool-<os>-<arch>[.exe] as release assets, and the
// release body is the tag's CHANGELOG.md block, so a GitHub release
// carries everything the sshtool.app manifest used to: version,
// per-platform download URLs, sizes, sha256 digests (GitHub's asset
// `digest` field) and markdown notes.
//
// /releases/latest excludes prereleases and drafts, so -test / -rcN
// tags never get offered as updates. Unauthenticated API quota is 60
// requests/hour/IP - plenty for an update check; callers fall back to
// the legacy release server on any error.

// ReleaseAsset is one downloadable binary attached to a release.
type ReleaseAsset struct {
	URL    string
	SHA256 string // hex, empty when GitHub did not report a digest
	Size   int64
}

// ReleaseInfo is the update-relevant subset of a GitHub release,
// with assets keyed by platform ("linux-amd64", "windows-arm64", ...).
type ReleaseInfo struct {
	Version      string // tag name, e.g. "v0.45.0"
	ReleasedAt   string // RFC3339 publish timestamp
	ChangelogURL string // html_url of the release page
	NotesMD      string // release body (markdown)
	Assets       map[string]ReleaseAsset
	// AssetsByName carries EVERY asset under its verbatim filename -
	// the plugin downloader looks up helper binaries
	// (ssh-tool-netbird-linux-amd64, ...) that the platform-keyed
	// Assets map deliberately filters out.
	AssetsByName map[string]ReleaseAsset
}

type ghReleasePayload struct {
	TagName     string `json:"tag_name"`
	PublishedAt string `json:"published_at"`
	HTMLURL     string `json:"html_url"`
	Body        string `json:"body"`
	Assets      []struct {
		Name               string `json:"name"`
		Size               int64  `json:"size"`
		BrowserDownloadURL string `json:"browser_download_url"`
		Digest             string `json:"digest"` // "sha256:<hex>"
	} `json:"assets"`
}

// FetchGitHubLatest returns the newest non-prerelease release of
// owner/repo.
func FetchGitHubLatest(repo, userAgent string) (*ReleaseInfo, error) {
	return fetchGitHubRelease(
		fmt.Sprintf("https://api.github.com/repos/%s/releases/latest", repo),
		userAgent)
}

// FetchGitHubByTag returns the release for one specific tag (used for
// release notes of a version that is not necessarily the latest).
func FetchGitHubByTag(repo, tag, userAgent string) (*ReleaseInfo, error) {
	return fetchGitHubRelease(
		fmt.Sprintf("https://api.github.com/repos/%s/releases/tags/%s", repo, tag),
		userAgent)
}

// FetchGitHubHelperRelease returns the newest published release whose
// tag is "helper-v<major>" with major <= maxMajor - i.e. the newest
// helper release this app can still speak to. The helpers ship on their
// own tag namespace, decoupled from the app version (see
// docs/helper-release-plan.md); the app picks the highest compatible
// major, and within it the most recent release. Returns an error if the
// list can't be fetched or no compatible helper release exists yet.
func FetchGitHubHelperRelease(repo string, maxMajor int, userAgent string) (*ReleaseInfo, error) {
	url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=100", repo)
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github releases API returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var list []ghReleasePayload
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("malformed github releases list: %w", err)
	}
	// GitHub returns releases newest-first, so the first tag matching the
	// highest compatible major wins. Track the best major seen so a
	// helper-v1 release isn't picked over a newer helper-v2 the app also
	// supports (both <= maxMajor).
	bestMajor := -1
	var best *ghReleasePayload
	for i := range list {
		major, ok := helperTagMajor(list[i].TagName)
		if !ok || major > maxMajor {
			continue
		}
		if major > bestMajor {
			bestMajor = major
			best = &list[i]
		}
	}
	if best == nil {
		return nil, fmt.Errorf("no helper release found (tag helper-v<=%d) in %s", maxMajor, repo)
	}
	raw, err := json.Marshal(best)
	if err != nil {
		return nil, err
	}
	return parseGitHubRelease(raw)
}

// helperTagMajor parses "helper-v<N>" -> N. Reports ok=false for any
// other tag shape (app tags, malformed).
func helperTagMajor(tag string) (int, bool) {
	rest, ok := strings.CutPrefix(tag, "helper-v")
	if !ok || rest == "" {
		return 0, false
	}
	n := 0
	for _, c := range rest {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, true
}

func fetchGitHubRelease(url, userAgent string) (*ReleaseInfo, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github+json")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("github releases API returned %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return parseGitHubRelease(body)
}

func parseGitHubRelease(body []byte) (*ReleaseInfo, error) {
	var p ghReleasePayload
	if err := json.Unmarshal(body, &p); err != nil {
		return nil, fmt.Errorf("malformed github release response: %w", err)
	}
	if p.TagName == "" {
		return nil, fmt.Errorf("github release response has no tag_name")
	}
	info := &ReleaseInfo{
		Version:      p.TagName,
		ReleasedAt:   p.PublishedAt,
		ChangelogURL: p.HTMLURL,
		NotesMD:      p.Body,
		Assets:       map[string]ReleaseAsset{},
		AssetsByName: map[string]ReleaseAsset{},
	}
	for _, a := range p.Assets {
		sha := ""
		if rest, found := strings.CutPrefix(a.Digest, "sha256:"); found {
			sha = rest
		}
		asset := ReleaseAsset{
			URL:    a.BrowserDownloadURL,
			SHA256: sha,
			Size:   a.Size,
		}
		info.AssetsByName[a.Name] = asset
		if key, ok := assetPlatformKey(a.Name); ok {
			info.Assets[key] = asset
		}
	}
	return info, nil
}

// assetPlatformKey maps a release asset filename to the platform key
// used by the updater ("ssh-tool-linux-amd64" -> "linux-amd64",
// "ssh-tool-windows-arm64.exe" -> "windows-arm64",
// "ssh-tool-android-arm64.apk" -> "android-arm64"). Non-binary assets
// (checksums, source archives) report ok=false.
func assetPlatformKey(name string) (string, bool) {
	base, found := strings.CutPrefix(name, "ssh-tool-")
	if !found || base == "" {
		return "", false
	}
	base = strings.TrimSuffix(base, ".exe")
	base = strings.TrimSuffix(base, ".apk")
	// Expect exactly <os>-<arch> with no further dots (filters out
	// stray assets like ssh-tool-0.45.0.tar.gz).
	if strings.Contains(base, ".") || strings.Count(base, "-") != 1 {
		return "", false
	}
	return base, true
}
