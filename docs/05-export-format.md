# Export / Import format

Sharing a folder subtree between machines or people. The canonical
definition is `internal/exporter/exporter.go` (`Archive` and `Options`);
this file explains the shape and the decisions behind it. When the two
disagree, the code wins - it is what actually reads and writes archives.

## Goals

- Share a folder subtree, with or without credentials.
- Human-readable and git-diff friendly.
- Deterministic ordering, so re-exporting unchanged data produces an
  identical file.
- Forward-compatible through `schema_version`.

## Encodings

TOML and JSON carry the *same* structure - only the encoding differs, so
an archive can be converted between them without loss. TOML is the
default for sharing (readable, diffable); JSON is there for tooling.

Filename convention: `<root_folder_name>.ssh-tool.toml`.

## Structure

Top-level fields: `schema_version` (an integer, bumped on any breaking
change), `generated_at`, `generated_by`, then the sections:

| Section | Holds |
|---|---|
| `folders` | connection-tree folders, flat, hierarchy via a parent id |
| `connections` | connections, each referencing its folder |
| `credentials` | optional; present only with `IncludeCredentials` |
| `credential_folders` | the credential tree's own hierarchy - separate from connection folders |
| `images` | custom icon blobs, base64, content-addressed by md5 on import |
| `forwards` | port forwards per connection; SOCKS bookmarks travel inline |
| `encrypted_secrets` | credential secrets, `credentialID -> base64(nonce \|\| ciphertext)` |

Ids inside an archive are self-consistent but need not match anything
locally; import maps them onto fresh local ids.

Every optional section is genuinely optional: an archive written before
a section existed still imports, just without that data. `images` is the
clearest case - an older archive simply lands on default Lucide icons.

## Secrets

Credentials are excluded by default. `IncludeCredentials` requires a
passphrase and writes the secrets into `encrypted_secrets`, sealed
separately from the readable structure - so the archive stays diffable
while the secret material does not travel in the clear.

## Sanitising a share

`Options` carries strip flags for exports leaving your control, each
answering a real leak: `StripNotes` (notes hold ticket numbers, owner
contacts, internal docs), `StripTags` (your taxonomy), `StripColor` and
`StripIcon` (your visual conventions), `StripLocal` (local-shell entries
are machine-specific and rarely portable), and
`ConvertAuthRefToInherit`, which drops per-connection credential
references so the recipient supplies their own through folder
inheritance.
