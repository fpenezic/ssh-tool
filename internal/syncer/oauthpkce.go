package syncer

// The PKCE flow lives in internal/oauthpkce, shared with other clients
// that sign in through a browser. These names keep the cloud-storage
// backends and their callers unchanged.

import (
	"context"

	"ssh-tool/internal/oauthpkce"
)

// OAuthEndpoints is the per-provider configuration (oauthpkce.Endpoints).
type OAuthEndpoints = oauthpkce.Endpoints

// Token is the result of an authorization or refresh.
type Token = oauthpkce.Token

// RedirectURIForPort is the exact redirect URI the flow listens on.
func RedirectURIForPort(port int) string { return oauthpkce.RedirectURIForPort(port) }

// DefaultRedirectURI is the redirect URI providers should register.
func DefaultRedirectURI() string { return oauthpkce.DefaultRedirectURI() }

// Authorize runs the interactive flow (oauthpkce.Authorize).
func Authorize(ctx context.Context, ep OAuthEndpoints, clientID, clientSecret string, openBrowser func(string)) (Token, error) {
	return oauthpkce.Authorize(ctx, ep, clientID, clientSecret, openBrowser)
}

// Refresh trades a refresh token for a new access token (oauthpkce.Refresh).
func Refresh(ctx context.Context, ep OAuthEndpoints, clientID, clientSecret, refreshToken string) (Token, error) {
	return oauthpkce.Refresh(ctx, ep, clientID, clientSecret, refreshToken)
}
