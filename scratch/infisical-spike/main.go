// Infisical spike: prove the read chain against a live Infisical server.
//
// Universal Auth (client_credentials-style) -> access token -> list projects
// (browse tree source) -> list secrets raw for a project+environment -> read a
// single secret's value. Server-side decryption means NO client crypto: the
// values come back as plaintext over TLS. This is the whole model.
//
// Run (in a separate terminal, creds never touch the repo):
//
//	export INFISICAL_HOST=https://your.infisical.host   # no trailing slash
//	export INFISICAL_CLIENT_ID=...
//	export INFISICAL_CLIENT_SECRET=...
//	# optional, to test the secrets read leg:
//	export INFISICAL_PROJECT_ID=...        # workspaceId from a project
//	export INFISICAL_ENV=dev               # environment slug
//	export INFISICAL_SECRET_PATH=/         # defaults to /
//	export INFISICAL_SECRET_KEY=SOME_KEY   # optional single-secret read
//	go run ./scratch/infisical-spike
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

func main() {
	host := strings.TrimRight(os.Getenv("INFISICAL_HOST"), "/")
	clientID := os.Getenv("INFISICAL_CLIENT_ID")
	clientSecret := os.Getenv("INFISICAL_CLIENT_SECRET")
	if host == "" || clientID == "" || clientSecret == "" {
		fmt.Println("set INFISICAL_HOST, INFISICAL_CLIENT_ID, INFISICAL_CLIENT_SECRET")
		os.Exit(2)
	}

	hc := &http.Client{Timeout: 20 * time.Second}

	// 1) Universal Auth login.
	token, ttl, err := login(hc, host, clientID, clientSecret)
	if err != nil {
		fmt.Println("LOGIN FAILED:", err)
		os.Exit(1)
	}
	fmt.Printf("LOGIN OK  token=%s...  expiresIn=%ds\n", firstN(token, 12), ttl)

	// 2) List projects (browse tree: Project -> Environment -> Secret).
	projects, err := listProjects(hc, host, token)
	if err != nil {
		fmt.Println("LIST PROJECTS FAILED:", err)
	} else {
		fmt.Printf("PROJECTS (%d):\n", len(projects))
		for _, p := range projects {
			envs := make([]string, 0, len(p.Environments))
			for _, e := range p.Environments {
				envs = append(envs, e.Slug)
			}
			fmt.Printf("  - %-30s id=%s  envs=[%s]\n", p.Name, p.ID, strings.Join(envs, ","))
		}
	}

	// 3) List secrets raw for a chosen project+environment.
	projectID := os.Getenv("INFISICAL_PROJECT_ID")
	env := os.Getenv("INFISICAL_ENV")
	secretPath := os.Getenv("INFISICAL_SECRET_PATH")
	if secretPath == "" {
		secretPath = "/"
	}
	if projectID != "" && env != "" {
		secrets, err := listSecretsRaw(hc, host, token, projectID, env, secretPath)
		if err != nil {
			fmt.Println("LIST SECRETS FAILED:", err)
		} else {
			fmt.Printf("SECRETS in %s/%s path=%s (%d):\n", projectID, env, secretPath, len(secrets))
			for _, s := range secrets {
				fmt.Printf("  - %-30s len(value)=%d  comment=%q\n", s.SecretKey, len(s.SecretValue), firstN(s.SecretComment, 40))
			}
		}

		// 4) Single-secret read (the Manager.Resolve leg).
		// A key like "cloudflare/password" is folder path "/cloudflare" + key
		// "password". Split trailing segment off as the key, the rest as path.
		if rawKey := os.Getenv("INFISICAL_SECRET_KEY"); rawKey != "" {
			path, key := splitSecretRef(secretPath, rawKey)
			fmt.Printf("(resolved ref: path=%s key=%s)\n", path, key)
			val, err := readSecretRaw(hc, host, token, projectID, env, path, key)
			if err != nil {
				fmt.Println("READ SECRET FAILED:", err)
			} else {
				fmt.Printf("READ %s OK  value=%s...\n", key, firstN(val, 4))
			}
		}
	} else {
		fmt.Println("(skip secrets read: set INFISICAL_PROJECT_ID + INFISICAL_ENV)")
	}
}

// --- login ---

type tokenResp struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int    `json:"expiresIn"`
	TokenType   string `json:"tokenType"`
}

func login(hc *http.Client, host, clientID, clientSecret string) (string, int, error) {
	body, _ := json.Marshal(map[string]string{
		"clientId":     clientID,
		"clientSecret": clientSecret,
	})
	req, _ := http.NewRequest(http.MethodPost, host+"/api/v1/auth/universal-auth/login", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	var tr tokenResp
	if err := do(hc, req, &tr); err != nil {
		return "", 0, err
	}
	if tr.AccessToken == "" {
		return "", 0, fmt.Errorf("empty access token")
	}
	return tr.AccessToken, tr.ExpiresIn, nil
}

// --- projects (browse) ---

type projectEnv struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	ID   string `json:"id"`
}

type project struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Environments []projectEnv `json:"environments"`
}

type projectsResp struct {
	Projects []project `json:"projects"`
}

func listProjects(hc *http.Client, host, token string) ([]project, error) {
	// Try several known endpoints; log each so we learn which one this
	// deployment actually serves. Both "projects" and "workspaces" wrappers.
	candidates := []string{
		"/api/v2/workspaces",
		"/api/v1/workspace",
		"/api/v2/organizations/{org}/workspaces", // needs org id, tried below if we learn it
		"/api/v1/projects",
	}
	for _, path := range candidates {
		if strings.Contains(path, "{org}") {
			continue // handled separately once we know the org id
		}
		req, _ := http.NewRequest(http.MethodGet, host+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		var wrap struct {
			Projects   []project `json:"projects"`
			Workspaces []project `json:"workspaces"`
		}
		err := do(hc, req, &wrap)
		list := wrap.Projects
		if len(list) == 0 {
			list = wrap.Workspaces
		}
		fmt.Printf("  [try %s] err=%v count=%d\n", path, err, len(list))
		if err == nil && len(list) > 0 {
			return list, nil
		}
	}
	return nil, fmt.Errorf("no projects endpoint returned a list")
}

// --- secrets raw ---

type rawSecret struct {
	SecretKey     string `json:"secretKey"`
	SecretValue   string `json:"secretValue"`
	SecretComment string `json:"secretComment"`
}

type secretsResp struct {
	Secrets []rawSecret `json:"secrets"`
}

func listSecretsRaw(hc *http.Client, host, token, projectID, env, secretPath string) ([]rawSecret, error) {
	q := url.Values{}
	q.Set("workspaceId", projectID)
	q.Set("environment", env)
	q.Set("secretPath", secretPath)
	u := host + "/api/v3/secrets/raw?" + q.Encode()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	var sr secretsResp
	if err := do(hc, req, &sr); err != nil {
		return nil, err
	}
	return sr.Secrets, nil
}

type singleSecretResp struct {
	Secret rawSecret `json:"secret"`
}

func readSecretRaw(hc *http.Client, host, token, projectID, env, secretPath, key string) (string, error) {
	q := url.Values{}
	q.Set("workspaceId", projectID)
	q.Set("environment", env)
	q.Set("secretPath", secretPath)
	u := host + "/api/v3/secrets/raw/" + url.PathEscape(key) + "?" + q.Encode()
	req, _ := http.NewRequest(http.MethodGet, u, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	var sr singleSecretResp
	if err := do(hc, req, &sr); err != nil {
		return "", err
	}
	return sr.Secret.SecretValue, nil
}

// --- helpers ---

func do(hc *http.Client, req *http.Request, out any) error {
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, firstN(string(b), 200))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(b, out)
}

// splitSecretRef turns a base path + a key that may itself contain folder
// segments ("cloudflare/password") into an absolute Infisical folder path and a
// bare key. base "/" + "cloudflare/password" -> ("/cloudflare", "password").
func splitSecretRef(base, rawKey string) (path, key string) {
	base = "/" + strings.Trim(base, "/")
	rawKey = strings.Trim(rawKey, "/")
	if i := strings.LastIndex(rawKey, "/"); i >= 0 {
		folder := rawKey[:i]
		key = rawKey[i+1:]
		if base == "/" {
			path = "/" + folder
		} else {
			path = base + "/" + folder
		}
		return path, key
	}
	return base, rawKey
}

func firstN(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
