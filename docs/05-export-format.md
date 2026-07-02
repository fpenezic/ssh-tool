# Export / Import format

## Ciljevi

- Sharing folder subtree-a bez credentialsa
- Human-readable (TOML), git-diff friendly
- Deterministic ordering za stable diffs
- Forward-compat via `schema_version`

## Filename convention

`<root_folder_name>.ssh-tool.toml` (npr. `production.ssh-tool.toml`)

## Struktura

```toml
schema_version = "1"
exported_at = "2026-05-18T10:00:00Z"
generator = "ssh-tool 0.1.0"

# Optional: tko/odakle. Može se izostaviti za anon share.
[meta]
exported_by = "anonymous"
description = "Production fleet connections"

# Folders - ravna lista. Hijerarhija kroz parent reference (folder id).
# IDs u exportu ne moraju match-at lokalne UUIDs; pri importu se mapira.

[[folder]]
id = "f-prod"
parent = null
name = "Production"
sort_order = 0

  [folder.settings]
  username = "ops"
  port = 22
  auth_ref = "cr-opkssh-google"
  keepalive_interval = 30

    [folder.settings.jump_host]
    hostname = "bastion.prod.example.com"
    port = 22
    username = "ops"
    auth_ref = "cr-opkssh-google"

    [folder.settings.ssh_options]
    StrictHostKeyChecking = "ask"
    ServerAliveInterval = "60"

[[folder]]
id = "f-prod-web"
parent = "f-prod"
name = "Web tier"
sort_order = 0
# settings prazan -> sve naslijedeno

# Connections
[[connection]]
id = "c-web-01"
folder = "f-prod-web"
name = "web-01"
hostname = "10.0.1.10"
sort_order = 0
tags = ["nginx", "frontend"]
notes = "Primary web node"
# Nema overrides => sve od foldera

[[connection]]
id = "c-web-02"
folder = "f-prod-web"
name = "web-02"
hostname = "10.0.1.11"
sort_order = 1
tags = ["nginx", "frontend"]

  [connection.overrides]
  port = 2222  # ovaj host na ne-default portu

[[connection.port_forward]]
connection_id = "c-web-01"
kind = "local"
local_addr = "127.0.0.1"
local_port = 5432
remote_host = "db.prod.internal"
remote_port = 5432
auto_start = false
description = "Postgres tunnel"

# Credential references - refs only, no secrets.
[[credential_ref]]
id = "cr-opkssh-google"
name = "Ops Google OIDC"
kind = "opkssh"
hint = "Google OIDC via opkssh, prod cluster"

  [credential_ref.config]
  provider = "google"
  # cert_path, audience itd. - non-secret config

[[credential_ref]]
id = "cr-key-deploy"
name = "Deploy SSH key"
kind = "key"
hint = "Ed25519 key for deploy automation. Lokalno: ~/.ssh/id_ed25519_deploy"

  [credential_ref.config]
  # key_path se NE exportira (osobni filesystem)
  # Pri importu, user mora mappirat na svoj lokalni key
  needs_mapping = true
```

## Što NIKAD ne ide u export

- `vault_key` vrijednosti
- Stvarni passwordi, key file contenti, OIDC tokeni
- `key_path` ako sadrži home directory (npr. `/home/john/.ssh/...`) - replace s placeholderom ili izostavi
- `last_used_at` (privacy, otkriva kad je netko zadnji put konektao)

## Sensitive flags

Korisnik može markirati folder ili konekciju kao `sensitive = true` u UI-ju → preskoči se pri exportu. Default `false`.

## Import flow

1. User bira TOML file ili paste-a sadržaj
2. Parser validira schema_version (može triggerirat migration ako stara verzija)
3. **Dry-run preview**: prikaži UI tabelu
   - Folders to create
   - Connections to create
   - Credential refs needing mapping (i njihovi hintovi)
   - Conflicts (postojeća folder/connection s istim path)
4. User za svaki credential_ref bira:
   - Map na postojeći lokalni credential_ref
   - Create new (otvara credential UI)
   - Skip (konekcije koje ga koriste import-ane su ali bez auth-a - user mora kasnije popraviti)
5. Conflict strategy:
   - **Skip**: ne import-aj duplicate
   - **Replace**: prepiši lokalno
   - **Merge**: za foldere - spoji settings (import override-a lokalno za polja koja import ima)
   - **Rename**: import dobije suffix " (imported)"
6. Commit transakcijski (sve ili ništa)

## ID remapping

Export IDs su scoped na taj file. Pri importu generiraju se novi UUIDs lokalno, ali se zadrže reference (npr. `parent`) konzistentne - uses lookup tablicu old_id → new_id tijekom importa.

## ssh_config import

Standardni OpenSSH config:
```
Host bastion
    HostName bastion.example.com
    User ops

Host prod-*
    ProxyJump bastion
    User deploy

Host prod-web-01
    HostName 10.0.1.10
```

Mapiranje:
- `Host pattern` s wildcardom → folder (`prod-*` postaje folder `prod`, podelementi ulaze pod njega)
- Specific `Host name` → connection
- `ProxyJump` → `jump_host`
- `User`/`Port`/`IdentityFile` → settings ili overrides
- `IdentityFile` postaje `credential_ref` kind=`key` s `key_path` (lokalno only, ne export)

## RDM import (post-MVP, S)

Devolutions exporta XML. Pisat će se parser koji čita njegovu strukturu, mapira na folders/connections. Plan kad dođe vrijeme:
1. Eksportaj RDM data
2. Pošalji uzorak (anonimiziran) za parser dev
3. Napisati XML→internal model converter

## Encrypted bundle (post-MVP, N)

Ako tim ipak želi share s credentialsa:
- Format: `*.ssh-tool.age` (binary, age-encrypted)
- Sadrži TOML kao gore + dodatni `[[secret]]` blokovi (password values, key file bytes, ...)
- Enkripcija: age symmetric (passphrase) ili age recipients (public keys recipienata)
- UI flow: import age file → prompt za passphrase ili identity key → dekriptira → standardni dry-run preview
