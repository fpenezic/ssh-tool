# Requirements

Legenda: **[M]** must-have za MVP, **[S]** should-have post-MVP, **[N]** nice-to-have/maybe.

## Connection management

- [M] Folderi (hijerarhija, arbitrarna dubina)
- [M] Konekcije (host, port, username, auth ref, opcije)
- [M] Drag-drop reorganizacija tree-a
- [M] Clone konekcije
- [M] Clone foldera (rekurzivno, s ili bez credentialsa - bira korisnik)
- [M] Search po imenu, hostnameu, tagovima
- [M] Tagovi / boje za vizualnu organizaciju
- [S] Quick-launch (fuzzy palette, Ctrl+K)
- [S] Recent connections
- [S] Favorites
- [N] Connection notes / markdown opis po konekciji

## Inheritance

- [M] Folder settings se nasljeđuju na sve potomke
- [M] Child override roditeljskog settinga
- [M] UI prikaže "inherited from /Path" kraj polja koja su naslijeđena
- [M] Polja koja se nasljeđuju: jump_host, username, auth_ref, port, ssh_options, env_vars, color_tag, broadcast_group
- [S] Computed/resolved view ("show me final settings after inheritance")

## Auth

- [M] Password (in keychain)
- [M] SSH key (path + opcionalna passphrase u keychainu)
- [M] SSH agent
- [M] opkssh (OIDC → SSH cert), s podrškom za više profila
- [S] Vault integration (HashiCorp Vault, fetch credential pri konektu)
- [N] 1Password / Bitwarden CLI integracija
- [N] FIDO2 / hardware keys (golang.org/x/crypto/ssh has limited support; ssh-agent forwarding may be the realistic path)

## Credential management

- [M] Dedicirani "Credentials" tab u UI-ju (library view)
- [M] Generate SSH key u app-u (ed25519 default, ili rsa/ecdsa s odabranom veličinom)
- [M] Import SSH key iz filea (file-reference mode, key ostaje na disku)
- [M] Import SSH key iz paste (managed mode, key u keychainu)
- [M] Storage modes: managed (keychain) | file_ref (path na disku) | external (agent/opkssh)
- [M] Public key prikaz + copy-to-clipboard (za deploy na server)
- [M] Username odvojen od credential-a (credential ne pretpostavlja usera)
- [M] `default_username` per credential - samo prijedlog pri uparivanju, user override OK
- [M] Tagovi na credentialima ("prod", "personal", "client-X")
- [M] Reverse lookup: za svaki credential prikaži listu konekcija koje ga koriste
- [M] Brisanje credential-a blokirano dok ima referenci (UI traži reassignment)
- [M] Rotation reminder badge (kad `last_rotated_at` stariji od `rotation_reminder_days`)
- [M] Password history metadata (timestamp + note + tko rotirao)
- [S] Opt-in retain old password values (zahtjeva enabled SQLCipher)
- [S] Password strength score (zxcvbn) pri spremanju
- [S] "Open in system ssh" akcija (generiraj `ssh -o IdentitiesOnly=yes -i <key> user@host`)
- [N] SSH key deployment helper (one-click ssh-copy-id na N konekcija)

## opkssh specifics

- [M] Multiple opkssh profila, svaki s vlastitim `key_basename` (npr. `opkssh_server_group1`)
- [M] Per-profile `config_path` (use system config ili custom config.yml)
- [M] Config.yml import preko paste u UI textareu (validacija lokalno, sprema u app config dir)
- [M] Cert validity check prije connect-a (parsiraj `*-cert.pub`, čitaj `valid_before`)
- [M] Auto-spawn `opkssh login -i <basename> [--config <path>]` ako cert expired/missing
- [M] Per-credential `max_cert_age_hours` (gornja granica iznad server policy)
- [M] Per-credential `min_remaining_before_refresh_minutes` (refresh window)
- [M] UI badge "Cert expires in 2d 4h" na credential listi
- [M] Browser-handoff status u UI ("Opening browser... complete M365 login")
- [M] Subprocess timeout + cancel button (ako user zatvori browser bez logina)
- [S] Auto-refresh cert u backgroundu N hours prije expiry (opt-in)

## Jump host

- [M] Single jump (A → bastion → target)
- [M] Naslijedeni jump iz foldera
- [M] Chain jumps (A → B → C → D), arbitrarna dubina
- [M] **Per-hop credentials** - svaki hop u chainu ima vlastiti username + auth_ref
- [M] Per-connection override naslijedenog jumpa (atomic - cijeli chain replace)
- [M] UI: "Add hop" button, drag-reorder, vizualizacija chaina
- [S] Per-hop opkssh refresh trigger (ako hop2 cert expired, refresh prije hop2 handshakea)

## Terminal

- [M] xterm.js renderer s WebGL addonom
- [M] 256-color + true color
- [M] Copy/paste s custom shortcuts (Ctrl+Shift+C/V default)
- [M] Resize prati container
- [M] Configurable font + size
- [S] Theme support (dark, light, solarized, custom)
- [S] Search u terminal bufferu
- [S] Scrollback limit configurable

## Window management

- [M] Tabs (jedan window, više konekcija)
- [M] Split view (horizontalna + vertikalna podjela panea, ala terminator)
- [M] Sakriva se sidebar
- [M] Undock tab na novi prozor (drag-out ili context menu)
- [M] Re-dock tab nazad
- [S] Save/restore window layout per session
- [S] Multiple "workspaces" (set otvorenih tabova + layout)
- [N] Tiling presets (2x2 grid, 1+2 itd.)

## Broadcast

- [M] Označavanje skupa terminala kao broadcast grupa
- [M] Toggle broadcast on/off
- [M] Visual indikator (border boja) aktivne broadcast grupe
- [S] Multiple broadcast grupe paralelno
- [S] "Type once, send to all matching tagove X"

## Port forwarding

- [M] Local forward (L: local_port → remote_host:remote_port)
- [M] Remote forward (R: remote_port → local_host:local_port)
- [M] Dynamic forward / SOCKS proxy (D: local_port)
- [M] Pokreni/zaustavi forward bez restarta sesije
- [S] Auto-start forwards pri konektu (per-connection flag)
- [S] Status indikator aktivnih forwarda

## SFTP

- [S] Dual-pane file browser (local | remote)
- [S] Drag-drop upload/download
- [S] Edit-in-place (otvori remote file u $EDITOR, sync nazad)
- [N] Bookmark remote paths

## Import / export

- [M] Export folder subtree u TOML (bez credentialsa, samo refs)
- [M] Import TOML s dry-run + diff preview
- [M] Import iz `~/.ssh/config`
- [S] Import iz Devolutions RDM XML exporta
- [S] Import iz Termius backupa
- [N] Encrypted bundle (s credentialsa, age/passphrase enkripcija)

## Sync (post-MVP)

- [N] Git-backed sync (commitaj TOML export na shared repo, pulla na ostalim mašinama)
- [N] Webhook za team notification kad netko commitira novu konekciju
- [N] CRDT sync (Automerge) - vjerojatno overkill

## Dynamic inventory

- [M] Folders čiji se sadržaj live-povlači iz vanjskog izvora
- [M] Provideri: Proxmox VE, Hetzner Cloud, DigitalOcean, Linode,
      Vultr, Scaleway, AWS EC2, Ansible static inventory
- [M] Per-folder refresh interval; ručni refresh; cache pri grešci
- [M] Filter (kinds, tag whitelist/blacklist, hide stopped)
- [S] Per-host vars iz Ansible inventory mapiraju se u SSH overrides
      (ansible_user / ansible_port / ansible_host / jump chain iz
      `ansible_ssh_common_args` ili `_extra_args` - `-J`,
      `ProxyJump=`, `ProxyCommand=ssh -W`)
- [S] Per-folder jump-host credential (target creds rijetko rade na
      bastionu)
- [S] Per-connect override jump host + jump credential

## Audit & history

- [M] Append-only local audit log (vault unlock/lock/rotate, backup
      create/restore, SSH connect/disconnect, credential reveal,
      forward start/stop)
- [M] Filter + sort + retention slider, CSV export
- [S] Sealed password / API-token history (keep last N rotations,
      reveal/copy s 30s clipboard auto-clear)

## Misc

- [M] Settings page (font, theme, shortcuts, default ssh options)
- [M] Logging (debug log za troubleshoot, nikad credentialse)
- [M] Toast notification system (non-blocking confirmations,
      auto-dismiss 2.5s)
- [M] Auto-update (Win i Linux), in-app release notes preview
- [S] Plugin/hook system za custom auth ili pre-connect skripte
- [N] Telemetry - NIKAD bez explicit opt-in, default OFF
