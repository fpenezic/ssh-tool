package main

// One-time codes from the external password managers: a credential that
// points at a Bitwarden / KeePass item also carries that item's TOTP secret,
// so the ssh layer can answer a keyboard-interactive "Verification code"
// question itself (internal/ssh kiAutofill).

import (
	"fmt"
	"time"

	"ssh-tool/internal/bitwarden"
	"ssh-tool/internal/store"
	"ssh-tool/internal/totp"
)

// externalTOTPSecret backs sshlayer.TOTPSecretHook. "" when the credential
// is not external, the item has no TOTP, or the backend cannot be read: the
// user is then asked for the code as before.
func (a *App) externalTOTPSecret(cred *store.CredentialRef) string {
	if cred == nil {
		return ""
	}
	if ref := store.ParseBitwardenRef(cred.Config); ref != nil && a.bitwarden != nil {
		r := *ref
		r.Field = bitwarden.FieldTotp
		s, _, err := a.bitwarden.Resolve(r)
		if err != nil {
			return ""
		}
		return s
	}
	if ref := store.ParseKeepassRef(cred.Config); ref != nil && a.keepass != nil {
		get := func(field string) string {
			r := *ref
			r.Field = field
			s, _, err := a.keepass.Resolve(r)
			if err != nil {
				return ""
			}
			return s
		}
		// KeePassXC keeps an otpauth URI in "otp"; KeePass 2 has its own fields.
		if s := get("otp"); s != "" {
			return s
		}
		return totp.FromKeePassFields(get)
	}
	return ""
}

// TotpCode is the current one-time code of a credential's external item.
type TotpCode struct {
	Code      string `json:"code"`
	Remaining int    `json:"remaining"` // seconds the code stays valid
}

// CredentialsTotpCode returns the current code for a Bitwarden / KeePass
// credential whose item carries a TOTP secret. Audited like a reveal.
func (a *App) CredentialsTotpCode(id string) (*TotpCode, error) {
	cred, err := a.db.GetCredential(id)
	if err != nil {
		return nil, err
	}
	secret := a.externalTOTPSecret(cred)
	if secret == "" {
		return nil, fmt.Errorf("this item has no one-time code (TOTP) set")
	}
	sp, err := totp.Parse(secret)
	if err != nil {
		return nil, err
	}
	now := time.Now()
	a.recordAudit("credential.totp", id, nil)
	return &TotpCode{Code: sp.Code(now), Remaining: sp.Remaining(now)}, nil
}
