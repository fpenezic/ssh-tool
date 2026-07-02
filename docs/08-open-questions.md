# Open questions

Stvari odgodjene za odluku kad dođe vrijeme. Ne blokira start implementacije.

## Auth

- ~~opkssh: koji OIDC provider primary?~~ **Odgovor:** vlastita konfiguracija s M365 (custom OIDC). Browser flow na localhost:3000.
- ~~Multiple identities per session?~~ **Odgovor:** da, per-hop credentials u jump chainu obavezno.
- **Vault integration prioritet?** Ako tim već koristi HashiCorp Vault za SSH OTP/CA, integracija je vrijedna ranije.
- **opkssh edge cases za Phase 3 spike:**
  - Što ako port 3000 zauzet kad opkssh login krene? Pukne, auto-pick, ili treba flag?
  - Headless okruženje (WSL bez xdg-open) - fallback flow?
  - User zatvori browser bez logina - subprocess hang detection?
  - Više opkssh login-a paralelno (npr. konekt 5 servera odjednom, svi trigeraju refresh) - race condition na key file pisanju?
- **SSH key deployment helper (post-MVP):** ssh-copy-id ekvivalent. Treba existing auth (password ili stari key) za inicijalni deploy. UX kako prikazat status (X od N succeeded)?
- **Hardware keys (FIDO2 / YubiKey):** `golang.org/x/crypto/ssh` has limited native support; ssh-agent forwarding to the platform agent may be the realistic path. UX flow is its own thing. Revisit post-MVP.

## Tree / UX

- **Smart folders / saved searches?** Folder koji je query (npr. "all production tagged web") umjesto statičkog grupiranja. Korisno za 300+ konekcija ili overkill?
- **Connection templates?** "Create new connection from template" - predefiniran skup settingsa.
- **Multiple instances ssh-tool-a paralelno?** SQLite lock issue. WAL mode pomaže, ali možda treba file lock.

## Terminal

- **Local shell tab?** Otvori lokalni bash/zsh/pwsh tab uz SSH tabove. Korisno (kopiraj-zalijepi između lokalnog i remote), ali znači ConPTY/unix PTY backend širi scope. Odluka: post-MVP.
- **Ligature support?** xterm.js ima limitiranu podršku. Korisnici font ligatura će tražit.
- **Sixel / image inline rendering?** xterm.js addon postoji za sixel. Niche feature.
- **GPU rendering full canvas vs WebGL addon?** WebGL je dobro, al ima edge cases s nekim driverima.

## Window mgmt

- **Što ako se split pane undockira?** Trenutna odluka: samo taj pane ide u novi prozor, ostali ostaju. Razmotri alternative ako UX bude wonky.
- **Persistence layout-a između app restarta?** Ako da, koliko detalja (samo tabovi vs i split layout)?
- **Tab groups (kao Chrome)?** Vizualna organizacija aktivnih tabova nezavisno od foldera u storeu.

## Broadcast

- **Pattern-based broadcast?** "Send to all tabs matching tag X" umjesto eksplicitnih grupa. Power user feature.
- **Broadcast s output diff view?** Vidiš output svih sesija side-by-side da uočiš razlike. Nice za fleet ops.

## Export / Sync

- **TOML vs JSON za export?** Trenutno TOML. JSON je univerzalniji za alat-to-alat. Možda support oba?
- **Git-as-sync UX:** auto-pull on launch? Auto-commit on change? Konflikti?
- **Encrypted bundle: age recipients ili passphrase?** Ovisi o team flow. Možda oboje.

## Platform-specific

- **Windows: ConPTY za lokalni shell?** Ako i kad dodajemo lokalni shell.
- **macOS: notarization?** Treba Apple developer account ($99/yr). Bez toga user mora "right-click → open" da bypassa Gatekeeper. Acceptable za v0.1.
- **Linux: AppImage vs Flatpak vs deb/rpm?** Plan: AppImage za max kompatibilnost + deb za apt korisnike. Flatpak ako bude potrebe.

## Performance budget

- **Cijena WebGL renderer-a s 10 paralelnih xterm-a?** Testirati rano u Phase 4.
- **SQLite scale: kad počinje bolit?** 300 konekcija je trivijalno. 10k? 100k? Vjerojatno samo UI rendering, ne SQLite sam.
- **Memory footprint cilj?** 200MB s 5 aktivnih sesija. Treba mjerit, ne pretpostavljat.

## Brand / project

- **Ime projekta?** "ssh-tool" je placeholder. Treba neuptaknuti, lako za pretragu.
- **License?** Vjerojatno MIT ili Apache-2.0. GPL ako želimo copyleft (možda bolje da firme ne fork-aju closed).
- **Public repo od dana 1 ili lokalni dok ne bude funkcionalno?** Preporuka: privatni dok Phase 2 ne radi, onda public za feedback.

## Testing strategija

- **Integration testovi za SSH?** Testirati protiv kojeg servera? Docker s `linuxserver/openssh-server` u CI je realno.
- **E2E test undock flow?** Wails v3 has no headless mode. Manual smoke test only for the foreseeable future.
- **Property-based testovi inheritance resolvera?** Generiraj random tree, provjeri invariante (npr. resolved settings deterministicni).
