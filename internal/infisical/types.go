package infisical

import (
	"context"
	"net"
)

// DialContext dials a host:port, optionally through a tunnel. Matches
// http.Transport.DialContext and ssh.ContextDialer so the app can route a
// server behind a WireGuard profile (mirrors internal/bitwarden.DialContext).
type DialContext func(ctx context.Context, network, addr string) (net.Conn, error)

// Credentials are the Universal Auth (machine identity) login inputs: the
// client id + client secret of a machine identity. Same shape as the Bitwarden
// API key.
type Credentials struct {
	ClientID     string
	ClientSecret string
}

// tokenResp is the /api/v1/auth/universal-auth/login response.
type tokenResp struct {
	AccessToken string `json:"accessToken"`
	ExpiresIn   int    `json:"expiresIn"` // seconds; TTL of the returned token
	TokenType   string `json:"tokenType"` // "Bearer"
}

// projectEnv is one environment inside a project (dev / staging / prod ...).
type projectEnv struct {
	Name string `json:"name"`
	Slug string `json:"slug"`
	ID   string `json:"id"`
}

// project is one workspace the machine identity can see, with its environments.
type project struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Environments []projectEnv `json:"environments"`
}

// workspacesResp wraps GET /api/v1/workspace. The list key is "workspaces"; a
// "projects" alias is tolerated for deployments that rename it.
type workspacesResp struct {
	Workspaces []project `json:"workspaces"`
	Projects   []project `json:"projects"`
}

// rawSecret is one decrypted secret returned by /api/v3/secrets/raw. The value
// is plaintext: Infisical decrypts server-side, so this layer does no crypto.
// SecretPath is only populated in recursive listings (it tells the browse tree
// which folder a key lives in); a non-recursive list at a single path leaves it
// empty and the caller supplies the path it queried.
type rawSecret struct {
	SecretKey     string `json:"secretKey"`
	SecretValue   string `json:"secretValue"`
	SecretComment string `json:"secretComment"`
	SecretPath    string `json:"secretPath"`
}

// secretsResp wraps the list form of /api/v3/secrets/raw.
type secretsResp struct {
	Secrets []rawSecret `json:"secrets"`
}

// singleSecretResp wraps the single-key form /api/v3/secrets/raw/<key>.
type singleSecretResp struct {
	Secret rawSecret `json:"secret"`
}
