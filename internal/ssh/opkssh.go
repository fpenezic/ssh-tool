// Package ssh implements the SSH client layer: connect chains, auth
// material resolution (including opkssh cert lifecycle), and PTY-backed
// session management with output events streamed to the frontend.
package ssh

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"log"
	"strconv"
	"time"

	opkclient "github.com/openpubkey/openpubkey/client"
	"github.com/openpubkey/openpubkey/jose"
	"github.com/openpubkey/openpubkey/pktoken"
	"github.com/openpubkey/openpubkey/providers"
	"github.com/openpubkey/openpubkey/util"
	opkconfig "github.com/openpubkey/opkssh/commands/config"
	"github.com/openpubkey/opkssh/sshcert"
	"golang.org/x/crypto/ed25519"
	"golang.org/x/crypto/ssh"

	"ssh-tool/internal/creds"
	"ssh-tool/internal/store"
)

// BrowserOpenHook, when non-nil, overrides how the opkssh OIDC login
// flow opens the system browser. The default (nil) lets openpubkey shell
// out via util.OpenUrl (cmd/start, open, xdg-open) - correct on desktop.
// On android there is no such command; main wires this to fire an
// Android Intent.ACTION_VIEW through the JNI bridge. Keeping it a hook
// (rather than importing the Wails application package here) keeps the
// portable core free of the desktop shell and CGO-on-android coupling.
var BrowserOpenHook func(url string) error

// OpksshConfig is extracted from a credential's config_json.
type OpksshConfig struct {
	CredentialID                     string
	KeyBasename                      string
	ConfigYAML                       string
	ProviderHint                     string
	MaxCertAgeHours                  uint32
	MinRemainingBeforeRefreshMinutes uint32
}

// ParseOpksshConfig reads the credential's config map into typed fields.
func ParseOpksshConfig(cred *store.CredentialRef) (*OpksshConfig, error) {
	basename, _ := cred.Config["key_basename"].(string)
	if basename == "" {
		basename = "id_ecdsa"
	}
	cfg := &OpksshConfig{
		CredentialID: cred.ID,
		KeyBasename:  basename,
	}
	if v, ok := cred.Config["opkssh_config_yaml"].(string); ok {
		cfg.ConfigYAML = v
	}
	if v, ok := cred.Config["provider_hint"].(string); ok {
		cfg.ProviderHint = v
	}
	if v, ok := cred.Config["max_cert_age_hours"].(float64); ok {
		cfg.MaxCertAgeHours = uint32(v)
	} else {
		cfg.MaxCertAgeHours = 168
	}
	if v, ok := cred.Config["min_remaining_before_refresh_minutes"].(float64); ok {
		cfg.MinRemainingBeforeRefreshMinutes = uint32(v)
	} else {
		cfg.MinRemainingBeforeRefreshMinutes = 60
	}
	return cfg, nil
}

// vault account key helpers
func opksshKeyAccount(credID string) string      { return "cred:" + credID + ":opkssh:key" }
func opksshCertAccount(credID string) string     { return "cred:" + credID + ":opkssh:cert" }
func opksshIssuedAtAccount(credID string) string { return "cred:" + credID + ":opkssh:issued_at" }

// OpksshAuth holds the result of EnsureFreshCert.
type OpksshAuth struct {
	Signer ssh.Signer
}

// CertStatus reports what currently sits in the vault for an opkssh
// credential and when the connect path will force a browser re-login.
// Display-only - EnsureFreshCert stays the single source of truth for
// the actual refresh decision; this mirrors its rules.
type CertStatus struct {
	HasCert     bool  `json:"has_cert"`
	IssuedAt    int64 `json:"issued_at"`    // unix seconds; 0 = unknown
	ValidBefore int64 `json:"valid_before"` // unix seconds; 0 = forever cert
	RenewAt     int64 `json:"renew_at"`     // unix seconds; 0 = on next connect
}

// GetCertStatus inspects the vault without refreshing anything.
func GetCertStatus(cfg *OpksshConfig, vault *creds.Vault) *CertStatus {
	st := &CertStatus{}
	certPEM, ok, _ := vault.Get(opksshCertAccount(cfg.CredentialID))
	if !ok {
		return st
	}
	cert, err := parseCert([]byte(certPEM))
	if err != nil {
		return st
	}
	st.HasCert = true
	if s, ok, _ := vault.Get(opksshIssuedAtAccount(cfg.CredentialID)); ok {
		if sec, err := strconv.ParseInt(s, 10, 64); err == nil {
			st.IssuedAt = sec
		}
	}
	if cert.ValidBefore != ssh.CertTimeInfinity {
		// Finite cert: proactive refresh when remaining < threshold.
		st.ValidBefore = int64(cert.ValidBefore)
		st.RenewAt = st.ValidBefore - int64(cfg.MinRemainingBeforeRefreshMinutes)*60
	} else if st.IssuedAt > 0 {
		// Forever cert: age-based forced re-login.
		st.RenewAt = st.IssuedAt + int64(cfg.MaxCertAgeHours)*3600
	}
	return st
}

// EnsureFreshCert reads (and refreshes if needed) the opkssh cert + key pair.
// Key and cert material live entirely in the vault - no filesystem access.
// ctx cancels an in-flight OIDC login (the browser flow) so a connect that
// hangs on auth - wrong config, closed browser - can be aborted from the UI
// instead of waiting out the full timeout.
func EnsureFreshCert(ctx context.Context, cfg *OpksshConfig, vault *creds.Vault) (*OpksshAuth, error) {
	needsRefresh := false

	keyPEM, keyOK, _ := vault.Get(opksshKeyAccount(cfg.CredentialID))
	certPEM, certOK, _ := vault.Get(opksshCertAccount(cfg.CredentialID))
	issuedAtStr, tsOK, _ := vault.Get(opksshIssuedAtAccount(cfg.CredentialID))

	if !keyOK || !certOK {
		needsRefresh = true
		log.Printf("opkssh: no cert/key in vault for cred %s; will login", cfg.CredentialID)
	} else if cert, err := parseCert([]byte(certPEM)); err != nil {
		needsRefresh = true
		log.Printf("opkssh: vault cert parse failed for cred %s: %v; will refresh", cfg.CredentialID, err)
	} else if cert.ValidBefore != ssh.CertTimeInfinity {
		remaining := time.Until(time.Unix(int64(cert.ValidBefore), 0))
		threshold := time.Duration(cfg.MinRemainingBeforeRefreshMinutes) * time.Minute
		if remaining < threshold {
			log.Printf("opkssh: cert remaining %s < threshold %s; refreshing",
				remaining.Truncate(time.Second), threshold)
			needsRefresh = true
		} else {
			log.Printf("opkssh: cert valid for %s; no refresh needed", remaining.Truncate(time.Second))
		}
	} else {
		// "forever" cert - use stored issued_at for age check.
		maxAge := time.Duration(cfg.MaxCertAgeHours) * time.Hour
		var issuedAt time.Time
		if tsOK {
			if sec, err := strconv.ParseInt(issuedAtStr, 10, 64); err == nil {
				issuedAt = time.Unix(sec, 0)
			}
		}
		if issuedAt.IsZero() {
			log.Printf("opkssh: no issued_at in vault; forcing refresh")
			needsRefresh = true
		} else {
			age := time.Since(issuedAt)
			if age > maxAge {
				log.Printf("opkssh: cert age %s > max %s; refreshing",
					age.Truncate(time.Second), maxAge)
				needsRefresh = true
			} else {
				log.Printf("opkssh: cert age %s, max %s; no refresh needed",
					age.Truncate(time.Second), maxAge)
			}
		}
	}

	if needsRefresh {
		newKey, newCert, err := runOpksshLoginNative(ctx, cfg)
		if err != nil {
			return nil, err
		}
		now := strconv.FormatInt(time.Now().Unix(), 10)
		_ = vault.Put(opksshKeyAccount(cfg.CredentialID), string(newKey))
		_ = vault.Put(opksshCertAccount(cfg.CredentialID), string(newCert))
		_ = vault.Put(opksshIssuedAtAccount(cfg.CredentialID), now)
		keyPEM = string(newKey)
		certPEM = string(newCert)
	}

	signer, err := ssh.ParsePrivateKey([]byte(keyPEM))
	if err != nil {
		return nil, fmt.Errorf("opkssh: parse key: %w", err)
	}
	cert, err := parseCert([]byte(certPEM))
	if err != nil {
		return nil, fmt.Errorf("opkssh: parse cert: %w", err)
	}
	// Do NOT log cert.ValidPrincipals - for opkssh these are the
	// user's OIDC identity (typically an email address) and end up
	// in plaintext in the desktop log file on every connect. Log
	// only the count, plus the cert key fingerprint (already a hash).
	log.Printf("opkssh: cert fingerprint=%s principals_count=%d",
		ssh.FingerprintSHA256(cert.Key), len(cert.ValidPrincipals))
	certSigner, err := ssh.NewCertSigner(cert, signer)
	if err != nil {
		return nil, fmt.Errorf("opkssh: cert signer: %w", err)
	}
	return &OpksshAuth{Signer: certSigner}, nil
}

// runOpksshLoginNative performs OIDC authentication and SSH cert generation
// using the openpubkey library directly. No filesystem access - key and cert
// are returned as byte slices for the caller to store in the vault.
func runOpksshLoginNative(ctx context.Context, cfg *OpksshConfig) (keyPEM, certBytes []byte, err error) {
	log.Printf("opkssh: starting native login for cred %s", cfg.CredentialID)

	if cfg.ConfigYAML == "" {
		return nil, nil, fmt.Errorf("opkssh: no provider YAML configured - set it in the credential's opkssh config editor")
	}

	clientConfig, err := opkconfig.NewClientConfig([]byte(cfg.ConfigYAML))
	if err != nil {
		return nil, nil, fmt.Errorf("opkssh: parse provider config: %w", err)
	}
	if len(clientConfig.Providers) == 0 {
		return nil, nil, fmt.Errorf("opkssh: no providers found in config YAML")
	}

	// Select provider: explicit hint → default_provider alias → first in list.
	provCfg := selectProvider(clientConfig, cfg.ProviderHint)
	log.Printf("opkssh: using provider issuer=%s client_id=%s", provCfg.Issuer, provCfg.ClientID)

	// Refuse to drive the browser flow if the YAML points at a non-
	// loopback redirect or a non-https issuer - both are token /
	// identity exfiltration paths if user pasted hostile config.
	if err := validateOpksshProvider(provCfg); err != nil {
		return nil, nil, fmt.Errorf("opkssh: provider config rejected: %w", err)
	}

	provider, err := provCfg.ToProvider(true)
	if err != nil {
		return nil, nil, fmt.Errorf("opkssh: create provider: %w", err)
	}

	// On platforms where the default browser launcher (util.OpenUrl ->
	// xdg-open / open / start) does not exist - notably android - route
	// the OIDC login URL through the host hook instead. SetOpenBrowserOverride
	// lives on the concrete *providers.StandardOp, not the OpenIdProvider
	// interface, so reach it via a narrow type assertion.
	if BrowserOpenHook != nil {
		if so, ok := provider.(interface {
			SetOpenBrowserOverride(providers.BrowserOpenOverrideFunc)
		}); ok {
			so.SetOpenBrowserOverride(providers.BrowserOpenOverrideFunc(BrowserOpenHook))
		} else {
			log.Printf("opkssh: provider has no browser-open override; falling back to default launcher")
		}
	}

	// ECDSA P-256 - same default as the opkssh CLI.
	alg := jose.ES256
	signer, err := util.GenKeyPair(alg)
	if err != nil {
		return nil, nil, fmt.Errorf("opkssh: generate keypair: %w", err)
	}

	oc, err := opkclient.New(provider, opkclient.WithSigner(signer, alg))
	if err != nil {
		return nil, nil, fmt.Errorf("opkssh: create client: %w", err)
	}

	// Auth opens the browser and blocks until the OIDC callback is received.
	// The 5-minute ceiling is a child of the caller's ctx, so either the
	// timeout OR an explicit cancel (user hit Cancel on the connect) aborts
	// oc.Auth and frees this goroutine.
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	log.Printf("opkssh: opening browser for OIDC login (5-minute timeout)")
	pkt, err := oc.Auth(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("opkssh: OIDC auth failed: %w", err)
	}
	log.Printf("opkssh: OIDC auth complete")

	// Extract email from the ID token payload to use as the SSH cert principal.
	// This matches what older opkssh server-side setups expect: the AuthorizedPrincipalsCommand
	// (opkssh readhome / verify) returns the user's email, and sshd checks it against the
	// cert's ValidPrincipals. "opkssh-wildcard" only works with newer server-side opkssh.
	principals := principalsFromPKT(pkt)
	// Same redaction reason as the EnsureFreshCert log site - these
	// are user identifiers (emails) and must not land in logs.
	log.Printf("opkssh: cert principals_count=%d", len(principals))

	certBytes, keyPEM, err = makeSSHCert(pkt, signer, principals)
	if err != nil {
		return nil, nil, fmt.Errorf("opkssh: build SSH cert: %w", err)
	}

	log.Printf("opkssh: native login succeeded; cert+key in memory only")
	return keyPEM, certBytes, nil
}

// selectProvider picks the best ProviderConfig from a ClientConfig.
// Resolution order: explicit per-credential hint -> default_provider from
// the YAML -> first provider. "webchooser" is an opkssh-CLI sentinel for an
// interactive chooser page that this native client does not implement; if
// it (or any unknown alias) is the only selector, we fall back to the first
// provider but log a clear warning so the user knows to pin a provider on
// the credential instead of silently always getting Providers[0].
func selectProvider(cc *opkconfig.ClientConfig, hint string) *opkconfig.ProviderConfig {
	alias := hint
	if alias == "" {
		alias = cc.DefaultProvider
	}
	if alias != "" {
		if pm, err := cc.GetProvidersMap(); err == nil {
			if pc, ok := pm[alias]; ok {
				return &pc
			}
		}
		// Alias didn't resolve. webchooser is the common case (it's the
		// scaffold default_provider) - call it out specifically.
		first := firstAlias(&cc.Providers[0])
		if alias == "webchooser" {
			log.Printf("opkssh: default_provider 'webchooser' is not supported natively; "+
				"using first provider %q - pin a provider on the credential to choose another",
				first)
		} else {
			log.Printf("opkssh: provider alias %q not found in config; using first provider %q",
				alias, first)
		}
	}
	return &cc.Providers[0]
}

// firstAlias returns a provider's first alias for logging, or its issuer
// if it has no alias.
func firstAlias(pc *opkconfig.ProviderConfig) string {
	if len(pc.AliasList) > 0 {
		return pc.AliasList[0]
	}
	return pc.Issuer
}

// principalsFromPKT extracts the email claim from a PK token's payload.
// Falls back to "opkssh-wildcard" if the email is absent (newer servers handle it).
func principalsFromPKT(pkt *pktoken.PKToken) []string {
	var claims struct {
		Email string `json:"email"`
	}
	if err := json.Unmarshal(pkt.Payload, &claims); err == nil && claims.Email != "" {
		return []string{claims.Email, "opkssh-wildcard"}
	}
	return []string{"opkssh-wildcard"}
}

// makeSSHCert builds an SSH certificate from a PKToken + signer.
// Mirrors the unexported createSSHCertWithAccessToken in opkssh's commands package.
// Returns (certBytes, keyPEMBytes).
func makeSSHCert(pkt *pktoken.PKToken, signer crypto.Signer, principals []string) (certBytes, keyPEMBytes []byte, err error) {
	cert, err := sshcert.New(pkt, nil, principals)
	if err != nil {
		return nil, nil, err
	}

	sshSig, err := ssh.NewSignerFromSigner(signer)
	if err != nil {
		return nil, nil, err
	}

	var algos []string
	switch signer.(type) {
	case *ecdsa.PrivateKey:
		algos = []string{ssh.KeyAlgoECDSA256}
	case ed25519.PrivateKey:
		algos = []string{ssh.KeyAlgoED25519}
	default:
		return nil, nil, fmt.Errorf("unsupported signer type: %T", signer)
	}

	signerMas, err := ssh.NewSignerWithAlgorithms(sshSig.(ssh.AlgorithmSigner), algos)
	if err != nil {
		return nil, nil, err
	}

	sshCert, err := cert.SignCert(signerMas)
	if err != nil {
		return nil, nil, err
	}

	certBytes = ssh.MarshalAuthorizedKey(sshCert)
	certBytes = certBytes[:len(certBytes)-1] // strip trailing newline added by MarshalAuthorizedKey

	privBlock, err := ssh.MarshalPrivateKey(signer, "openpubkey cert")
	if err != nil {
		return nil, nil, err
	}
	keyPEMBytes = pem.EncodeToMemory(privBlock)
	return certBytes, keyPEMBytes, nil
}

func parseCert(certBytes []byte) (*ssh.Certificate, error) {
	pub, _, _, _, err := ssh.ParseAuthorizedKey(certBytes)
	if err != nil {
		return nil, err
	}
	cert, ok := pub.(*ssh.Certificate)
	if !ok {
		return nil, fmt.Errorf("not a certificate (got %T)", pub)
	}
	return cert, nil
}
