# Contributing to ssh-tool

Thanks for your interest. Short version: read `CLAUDE.md` first - it
is the project orientation (stack, layout, how to run, live gotchas)
and applies to humans as much as to AI assistants.

## Getting started

- Go 1.26+, Node 20+, Wails v3 CLI (`wails3`).
- Dev loop, builds, tests: see "How to run" in `CLAUDE.md`.
- Before opening a PR: `go build ./...`, the package tests listed
  there, and `cd frontend && npm run check` (0 errors expected).

## Conventions

- Conventional commits (`feat:`, `fix:`, `docs:`, `chore:`).
- Commit bodies explain the why; see recent history for the style.
- No emojis, no em-dashes (ASCII punctuation only) - in code,
  comments, docs, commits, and UI strings.
- No real email addresses, hostnames, or other personal data anywhere
  in the repo, including examples and tests. Use `example.com`.
- The app must run natively on Windows without WSL - no WSL-specific
  paths.

## Scope notes

- opkssh support is a core requirement; changes must not break
  certificate auth with `valid_before = u64::MAX` ("forever") certs.
- Desktop (Windows/Linux/macOS) and Android share the Go core.
  Anything mobile-specific stays behind build tags or `isMobile`
  checks; desktop builds stay `CGO_ENABLED=0`. See the Android
  gotchas in `CLAUDE.md` before touching that area.
