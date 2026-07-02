# Vision

## Problem

Trenutno: Devolutions RDM. 300+ konekcija. Spor, RAM hog, closed-source, ne baš povjerenja vrijedan za credential storage.

Ostale opcije ne pogađaju sweet spot:
- Termius - closed, cloud-locked
- mRemoteNG - Win-only
- WindTerm - solidan al ne open, ne podržava sve što treba
- raw `~/.ssh/config` + tmux - funkcionalno al bez GUI tree-a, bez nasljeđivanja na razini foldera (samo Host patterns), bez integriranog credential UX-a
- Snowflake/Muon - funkcionalno, ružno, Java

## Što gradimo

Native-ish desktop SSH connection manager s:
- Hijerarhijskim tree-om konekcija (folderi → subfolderi → konekcije)
- Inheritance settings-a (jump host, credentials, opcije naslijeđuju se niz tree)
- File-based encrypted vault (Argon2id + XChaCha20-Poly1305), s
  opcionalnim machine-bound auto-unlockom kroz OS keychain
- Integriranim terminalom (tabs, splits, undock na drugi monitor, broadcast)
- opkssh OIDC integracijom (native - bez vanjskog binary-a)
- SFTP browserom
- Dinamičkim inventory folderima (Proxmox VE, Hetzner Cloud,
  DigitalOcean, Linode, Vultr, Scaleway, AWS EC2, Ansible static
  inventory - auto-populate iz vanjskog izvora)
- HTTP / SOAP probe alatom (rute kroz aktivni SOCKS forward)
- Live tcpdump panelom
- Workspaces (named bundles tabova)
- Export/import format bez credentialsa za sharing unutar tima

## Non-goals (eksplicitno)

- Team sync server / multi-user RBAC - nije planirano u core aplikaciji
- Integrirani web browser kroz SOCKS - SOCKS endpoint da, ali browser launcha system Chrome/Firefox
- RDP / Telnet / Serial - samo SSH (i SFTP kao SSH subprotocol), VNC konzola dodana
- iOS - Android port (v0.36.0+) dijeli build tagove s iOS-om ali iOS build se ne radi; Android je jedini mobilni target
- Cloud sync vlastiti - koristi git ili user-managed file sync (Syncthing, Dropbox folder)

Napomena: "Mobile - desktop only" je bio non-goal do v0.36.0. Android sada radi nativno (isti Go core). Vidi docs/07-roadmap.md (Mobile sekcija).

## Success criteria

MVP je gotov kad:
1. Import 300+ konekcija iz RDM exporta uspijeva ✅ (689-conn import
   testiran, see CHANGELOG-history).
2. Konekt + jump host + opkssh radi na linux/win/mac ✅ (Linux+Win
   confirmed; macOS Taskfile postoji, nije smoke-testiran).
3. RAM s 5 aktivnih sesija < 200MB ✅ (~120MB tipično u v0.9.0).
4. Tabovi/splits/undock/broadcast rade ✅ (sve shipped; multi-window
   detach+redock dodatno).
5. Export folder subtree-a u TOML/JSON i import na drugom računalu
   radi ✅ (TOML/JSON, round-trip testiran).

MVP postignut. Trenutni fokus je polish, dodatne provider integracije,
i packaging (vidi `TODO.md`).
