# noxctl

[![Install](https://img.shields.io/badge/install-go%20install-2E86AB?style=for-the-badge&logo=go&logoColor=white)](#quick-start)
[![Status](https://img.shields.io/badge/status-pre--1.0-FFAD66?style=for-the-badge)](#status--scope)

[![CI](https://github.com/barad1tos/noxctl/actions/workflows/build.yml/badge.svg?branch=main)](https://github.com/barad1tos/noxctl/actions/workflows/build.yml)
[![CodeQL](https://github.com/barad1tos/noxctl/actions/workflows/codeql.yml/badge.svg?branch=main)](https://github.com/barad1tos/noxctl/actions/workflows/codeql.yml)
[![Coverage](https://codecov.io/gh/barad1tos/noxctl/graph/badge.svg)](https://codecov.io/gh/barad1tos/noxctl)
[![Go Version](https://img.shields.io/github/go-mod/go-version/barad1tos/noxctl)](go.mod)
[![License](https://img.shields.io/github/license/barad1tos/noxctl)](LICENSE)
[![Platform](https://img.shields.io/badge/platform-macOS-blue)](#status--scope)
[![Go Reference](https://pkg.go.dev/badge/github.com/barad1tos/noxctl.svg)](https://pkg.go.dev/github.com/barad1tos/noxctl)

Declarative macOS CLI for Bear notes structure management — *Terraform for Bear notes*. Describe your Bear-vault layout (tags, hubs, masters, buckets) in a TOML file and `noxctl` keeps the vault matching that description idempotently.

Brownfield — descended from a personal FSEvents-driven daemon (`regen-watchd`) that managed a 28-domain Bear corpus. The closed catalog of six rendering blueprints (`flat-list`, `flat-table`, `grouped-vertical`, `hub-routed`, `hub-routed-with-subtag`, `umbrella`) covers every shape that production used.

## Quick start

> **Already have Bear tags you want managed?** Skip to [From existing vault](#from-existing-vault) for a `noxctl import`-based bootstrap, then come back here at Step 3.

**Step 0 — pick your first tags.** Open Bear and look at the tag sidebar. Pick 1-3 tags you want noxctl to manage first; incremental adoption is normal and you can add more later. The smallest useful catalog is a single tag.

```bash
# 1. Install
go install github.com/barad1tos/noxctl/cmd/noxctl@latest

# 2. Write a starter catalog (no network; no overwrite of existing files)
mkdir -p ~/.config/noxctl
noxctl init ~/.config/noxctl/noxctl.toml

# 3. Confirm the schema parses without touching Bear
noxctl validate ~/.config/noxctl/noxctl.toml

# 4. Preview what apply would do
noxctl plan --config ~/.config/noxctl/noxctl.toml

# 5. Apply once you're happy with the diff
noxctl apply --config ~/.config/noxctl/noxctl.toml
```

`noxctl init` writes a 3-blueprint showcase catalog ready to edit. Replace the example `[[domain]]` blocks with your own tags (Step 0). If you want the absolute minimum starter (1 domain), see [`examples/minimal.toml`](examples/minimal.toml).

Optional: run `noxctl daemon --config ~/.config/noxctl/noxctl.toml` to keep the vault reconciled live as you edit notes in Bear.

## Status & scope

- **Platform:** macOS only. Bear is macOS-only; the watcher uses FSEvents via `fsnotify`'s Darwin backend; the CLI bridge is `bearcli` at `/Applications/Bear.app/Contents/MacOS/bearcli`.
- **Runtime:** Go ≥ 1.26. The only non-stdlib runtime dependency is `github.com/fsnotify/fsnotify`. Adding a runtime dep is deliberate and requires justification.
- **Acceptance test:** byte-equivalent vault output against the legacy daemon for the maintainer's 28-domain corpus.
- **License:** MIT.

## Subcommands

```
noxctl validate [<config>]   strict TOML schema + dispatch checks (no Bear I/O)
noxctl plan                  Terraform-style diff vs the live vault
noxctl apply                 write the diff back to the vault (one-shot)
noxctl daemon                long-running FSEvents-driven watcher
noxctl audit                 read-only lint sweep across every managed tag
noxctl lint [--apply]        report or auto-fix structural defects
noxctl verify                hard gate: catalog ↔ vault alignment check
noxctl daemon-config         inspect resolved daemon configuration
noxctl destroy <tag>         remove all atoms + hub for a managed tag
noxctl import <bear-tag>     bootstrap a noxctl.toml stanza from Bear
noxctl init                  interactive wizard for a fresh config
noxctl version               print version + build metadata
```

`apply` is the one-shot reconciliation; the daemon runs the same engine on a debounce-2s FSEvents signal plus an `mtime` poll fallback for cases where Bear defers a SQLite WAL commit past the file-system event window. `audit` and `lint` operate on note structure (broken-H1 titles, malformed canonical tag-lines) without touching the hub/master layout `apply` owns.

## Idempotency contract

Every change to the engine must keep `noxctl apply` reaching `unchanged` for every hub and master after at most three passes. Order-stabilization passes count toward that three — anything that needs more is a bug. The integration suite under `tests/bear/engine/` pins this contract.

## Configuration shape

```toml
# noxctl.toml — minimal example
[meta]
  version = "1"
  locale  = "uk"

[[domain]]
  tag         = "library/poetry"
  index_title = "✱ Poetry"
  blueprint   = "hub-routed"
  unknown_bucket = "Unknown"
  hub_h2_prefix  = "Poems"

[[domain]]
  tag            = "library/aphorisms"
  index_title    = "✱ Aphorisms"
  blueprint      = "flat-table"
  buckets        = ["Books", "Films", "Games"]
  unknown_bucket = "Unknown"
```

See `examples/minimal.toml` for a tested starter and `examples/personal.toml` for the maintainer's full 28-domain catalog covering every blueprint.

`noxctl validate` runs the loader plus every `Domain.Validate()` rule and exits zero in well under a second with zero `bearcli` calls. A typo'd field surfaces as `noxctl.toml:LINE:COL: unknown field` and aggregates every problem in one run.

## Build & gates

```bash
go build ./...                       # ~1 s
go vet ./...
golangci-lint run                    # gocognit/gocyclo ≤ 15, lll ≤ 120
go test ./... -count=1               # ~10 s, all packages
```

Pre-commit hooks live in `.pre-commit-config.yaml` — install once with `pre-commit install`.

## Deploy (maintainer's setup)

```bash
go install ./cmd/noxctl             # writes ~/go/bin/noxctl
launchctl bootout gui/$(id -u)/com.bear.regen-watchd 2>/dev/null
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.bear.regen-watchd.plist
```

`~/bin/noxctl` is a symlink to `~/go/bin/noxctl`; the launchd plist `ProgramArguments` points at `~/bin/noxctl daemon --config <path>`, so every `go install` is picked up without editing the plist. The launchd label still says `com.bear.regen-watchd` for continuity with operator history — only the program target moved.

## What this is not

- Not a Bear backup tool — it MUTATES notes in place.
- Not cross-platform — Bear, FSEvents, and `bearcli` are macOS-only.
- Not a general note-management framework — it operates on a closed catalog of six blueprints.

## License

MIT — see [LICENSE](LICENSE).
