# Credential management

Dedicirani dokument za credential lifecycle. Komplementira `03-data-model.md` (schema) i `06-security.md` (storage rules).

## Mental model

- **Credential ≠ User.** Username živi na konekciji/folderu, credential je odvojen. Isti SSH key se koristi kao `root@A` i `deploy@B`.
- **Credential library je first-class koncept.** Vlastiti tab u sidebar-u, ne dialog ispod connection editora.
- **Pairing pri konektu**: `(resolved.username, resolved.auth_ref) → handshake`. Credential može predložit `default_username`, user override OK.

## Storage modes

| Mode | Gdje je secret | Mi smo source of truth? | Delete briše secret? |
|---|---|---|---|
| `managed` | OS keychain pod `vault_key` | Da | Da |
| `file_ref` | Datoteka na disku (npr. `~/.ssh/id_ed25519`) | Ne, samo referenciramo | Ne (samo SQLite row) |
| `external` | Drugi proces (ssh-agent, opkssh in `~/.ssh/`) | Ne, invoke-amo | Ne |

**Default odluke:**
- Nove SSH keyeve koje **mi** generiramo → `managed`
- Postojeće SSH keyeve iz `~/.ssh/` → `file_ref` (ne dupliciramo bytes)
- Passwordi → uvijek `managed`
- Agent / opkssh → uvijek `external`

## SSH key lifecycle

### Generate u app-u
1. UI: "New credential" → "SSH key" → "Generate"
2. Bira: tip (ed25519/rsa/ecdsa), bitovi (ako rsa/ecdsa), passphrase (optional), comment
3. `internal/creds/keys.go` generates in-memory via
   `golang.org/x/crypto/ssh` + `crypto/ed25519`/`rsa`/`ecdsa`
4. Private key bytes → vault under `vault_key`
5. Public key → `credential_refs.public_key` (text field, display + copy button)
6. `storage_mode = 'managed'`, `last_rotated_at = now()`

### Import iz filea
1. UI: "Import" → file picker
2. Otvori file, parse public dio (`ssh.ParseAuthorizedKey` /
   `ssh.ParseRawPrivateKey`), izvuci comment kao default name suggestion
3. UI pita: "Keep file in place (file_ref) ili copy to vault (managed)?"
   - **File-ref**: spremi `key_path` u config_json, ne čitaj bytes uopće. Passphrase (ako postoji) → vault.
   - **Managed**: čitaj bytes, spremi u vault, ostavi file na disku (user briše ako želi).

### Import iz paste
1. UI: textarea, paste private key
2. Parse, validate
3. Forsiraj `managed` mode (paste implicira nema file na disku)
4. Save u keychain

### Rotation
- "Rotate key" akcija = generiraj novi par, **stari ostaje aktivan dok user ne potvrdi deploy**
- Flow: rotate → app prikaže novi public key → user deploy-a na servere → klik "Confirm deployed" → stari se briše iz keychaina
- Bez "Confirm deployed", user može cancellat (vrati stari)
- `credential_history` zapis: `note = "rotated, old key replaced"`, `rotated_by = 'user'`, `has_value = 0`

### Passphrase change
- Zaseban od key rotation
- Re-encrypt private key bytes s novom passphrase, replace u keychainu
- History zapis: `note = "passphrase changed"`

### Delete
- Provjeri reverse lookup. Ako konekcije referenciraju → blokiraj, prikaži listu
- User mora ili reassignat sve konekcije, ili obrisat te konekcije
- Onda: managed → briši keychain entry i SQLite row. file_ref → samo SQLite row (file ostaje).

## Password lifecycle

### Create
1. UI: "New credential" → "Password"
2. Polja: name, password (masked input), hint, `default_username` (optional), tags, `rotation_reminder_days` (optional)
3. Save u keychain pod `vault_key`
4. `last_rotated_at = now()`
5. Strength score (zxcvbn) prikaz: weak/fair/good/strong

### Rotate
1. UI: "Rotate password" → novi password input
2. `snapshotPreviousSecret` (`internal/creds/service.go`) reads
   the current vault value, seals it under a fresh
   `credhist:<random hex>` vault account, and writes a
   `credential_secret_history` row (migration 14). No opt-in;
   every password / API-token rotation snapshots automatically.
3. Vault put new value
4. `credential_history` metadata row (`note = "password rotated"`)
5. Retention: prune older than keep-last-5 (default; configurable
   slider follow-up) from both DB and vault
6. `last_rotated_at = now()`

Reveal: `CredentialsRevealSecretHistory(historyID)` returns the
sealed plaintext on demand, with the same 30-second clipboard
auto-clear the live reveal uses. Reveal + delete operations
each record an audit log row.

### Rotation reminder
- Backend timer (npr. 1x na sat) prolazi kroz credentiale s ne-null `rotation_reminder_days`
- Ako `now - last_rotated_at > rotation_reminder_days * 86400` → trigger UI badge
- Badge: žuti dot na credential listi, hover prikaže "X days since rotation"

## opkssh detalji

### Profile vs credential

opkssh u našem store-u = **profile**. Ima:
- `name` - human label ("Work M365", "Customer X")
- `key_basename` - `opkssh_server_group1`, default `id_ecdsa` ako jedan profil
- `config_path` - null (system) ili path do custom config.yml
- `provider_hint` - UI badge text
- `default_username` - predlaže se pri uparivanju
- `max_cert_age_hours` - gornja granica iznad server lifetimea
- `min_remaining_before_refresh_minutes` - kad počet refresh

### Multi-profile primjer

```
Credentials:
├─ "Work M365"           kind=opkssh  key_basename=id_ecdsa            provider=M365/acme.com
├─ "Customer X M365"     kind=opkssh  key_basename=opkssh_customer_x   provider=M365/clientcorp.com
└─ "Personal Google"     kind=opkssh  key_basename=opkssh_personal     provider=Google
```

Konekcije:
```
Work/
  settings.auth_ref = "Work M365"
  Production/
    web-01    → naslijedi Work M365
    db-01     → naslijedi Work M365
Customers/
  Customer X/
    settings.auth_ref = "Customer X M365"
    app-01    → naslijedi Customer X M365
Personal/
  homelab/
    settings.auth_ref = "Personal Google"
```

### Config.yml import flow

1. UI: "New credential" → "opkssh"
2. Tabs: "Use system config" | "Custom config"
3. Custom config tab: textarea, paste YAML
4. Validate lokalno:
   - YAML parse OK
   - Required keys present (provider, client_id, ...)
   - Bez network call-a
5. Save: YAML body lives directly in `credential_refs.config_json`
   under the `opkssh_config_yaml` key. Nothing is written to
   `~/.ssh/` or `~/.opk/`.

### Connect flow s opkssh

The implementation is native - no external binary, no filesystem.
`internal/ssh/opkssh.go` uses `github.com/openpubkey/openpubkey` +
`github.com/openpubkey/opkssh` as Go libraries.

On connect:

1. The credential's `opkssh_config_yaml` is parsed.
2. The cached cert+key (stored in the vault under
   `cred:{credentialID}`) is checked. opkssh certs have
   `valid_before = u64::MAX` ("forever") - server-side enforces
   lifetime separately, so we additionally track an `issued_at`
   timestamp in the credential and refresh based on
   `max_cert_age_hours` + `min_remaining_before_refresh_minutes`.
3. If refresh is needed, `opkclient.Auth(ctx)` opens the default
   browser, waits for the localhost callback, and returns the
   fresh key+cert as byte slices.
4. New key+cert are written back into the vault.
5. `credential_refs.last_rotated_at` is updated; `expires_at`
   stays as the `issued_at + max_cert_age_hours` upper bound.
6. The SSH layer hands the key+cert as `ssh.AuthMethod` to the
   standard `golang.org/x/crypto/ssh` client.

### Per-hop opkssh u jump chain

```
chain: [
  hop1 { hostname: bastion, auth_ref: "Work M365" },
  hop2 { hostname: customer-mid, auth_ref: "Customer X M365" },
],
target: { hostname: app-01, auth_ref: "Customer X M365" }
```

Engine:
1. Resolve credential za hop1 → opkssh refresh check → maybe spawn login
2. SSH handshake na hop1
3. direct-tcpip kanal od hop1 do hop2
4. Resolve credential za hop2 → opkssh refresh check (zaseban key_basename!)
5. SSH handshake preko kanala
6. direct-tcpip od hop2 do target
7. Resolve credential za target (može biti isti kao hop2, no problem)
8. SSH handshake

**Edge case:** isti opkssh profile koristi se za 2 hopa. Cert refresh trigeran prvi put, drugi put već valid, skip. Cache `last_validity_check_at` per credential u memoriji da ne re-parse-amo cert 50 puta.

## Credential library UI spec

### Sidebar tab structure
```
┌─ Connections  ─┐
├─ Credentials  ─┤  ← novi tab
└─ Settings    ─┘
```

### Credentials list view
- Tabela: Name | Kind | Provider/Hint | Tags | Last rotated | Used by (N) | Status
- Status kolumna: badge "Expires in 2d" za opkssh, "Rotate me" za stale passwords, "OK" inače
- Search/filter po name, kind, tag
- Sort po any kolumni
- Right-click context menu: Edit, Rotate, Delete, Duplicate, View usage
- Multi-select za bulk operations (post-MVP)

### Credential edit dialog
- Tabs po kind: General | Specific (Key/Password/opkssh fields) | Advanced | History
- General: name, hint, tags, default_username, rotation_reminder_days
- Specific: kind-specific polja
- Advanced: storage_mode (samo display, ne mijenja se nakon kreiranja bez delete+recreate), retain_history toggle (greyed out ako nema SQLCipher)
- History: lista changed_at + note + rotated_by, klik na entry prikaže "restore" gumb ako has_value=1

### Reverse lookup panel
- Selectaš credential → desna panel prikaže "Used by N connections":
  - `Production/Web/web-01` (target auth)
  - `Customers/X/app-02` (jump host hop2)
  - ...
- Klik na entry → otvori connection u editoru

## Bulk operations (post-MVP, Phase 11)

- "Select 10 connections" → "Reassign credential" → bulk update auth_ref
- "Rotate password for these 5 credentials" → wizard sequential per credential
- Export credential names + hints (no values) za audit/compliance

## NEMA u MVP

- ❌ Credential sharing između usera unutar firme (post-MVP, vidi 05-export-format encrypted bundle)
- ❌ Server-side credential broker
- ❌ SSH key deployment helper (post-MVP)
- ❌ FIDO2 / hardware keys (post-MVP)
- ❌ Vault dynamic secrets (post-MVP, kind=vault placeholder u schemi)
