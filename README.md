# noxctl

Declarative macOS CLI for Bear notes structure management — *Terraform for Bear notes*. Describe your Bear-vault layout (tags, hubs, masters, buckets) in a TOML file and `noxctl` keeps the vault matching that description idempotently.

Brownfield — descended from a personal FSEvents-driven daemon (`regen-watchd`) that managed a 28-domain Bear corpus. The closed catalog of six rendering blueprints (`flat-list`, `flat-table`, `grouped-vertical`, `hub-routed`, `hub-routed-with-subtag`, `umbrella`) covers every shape that production used.

> Status: pre-1.0. The acceptance test is byte-equivalent vault output against the existing daemon for the maintainer's 28-domain corpus.

## Status & scope

- **Platform:** macOS only. Bear is macOS-only; the watcher uses FSEvents via `fsnotify`'s Darwin backend; the CLI bridge is `bearcli` at `/Applications/Bear.app/Contents/MacOS/bearcli`.
- **Runtime:** Go ≥ 1.26. The only non-stdlib runtime dependency is `github.com/fsnotify/fsnotify`. Adding a runtime dep is deliberate and requires justification.
- **License:** MIT.

## Subcommands

```
noxctl validate [<config>]   strict TOML schema + dispatch checks
noxctl plan                  Terraform-style diff vs the live vault
noxctl apply [--once]        write the diff back to the vault
noxctl daemon                long-running FSEvents-driven watcher
noxctl destroy <tag>         remove all atoms + hub for a managed tag
noxctl import <bear-tag>     bootstrap a noxctl.toml stanza from Bear
noxctl init                  interactive wizard for a fresh config
```

`apply --once` is the one-shot reconciliation; the daemon runs the same engine on a debounce-2s FSEvents signal plus an `mtime` poll fallback for cases where Bear defers a SQLite WAL commit past the file-system event window.

## Idempotency contract

Every change to the engine must keep `noxctl apply --once` reaching `unchanged` for every hub and master after at most three passes. Order-stabilization passes count toward that three — anything that needs more is a bug. The integration suite under `tests/bear/engine/` pins this contract.

## Configuration shape

```toml
# noxctl.toml — minimal example
[[domains]]
tag        = "library/poetry"
index      = "✱ Поезія"
blueprint  = "hub-routed-with-subtag"

[[domains]]
tag        = "library/aphorisms"
index      = "✱ Афоризми"
blueprint  = "flat-table"
buckets    = ["Книги", "Кіно", "Ігри"]
unknown    = "Невідомі"
```

`noxctl validate` runs the loader against the file plus every `Domain.Validate()` rule and exits zero in well under a second with zero `bearcli` calls. A typo'd field surfaces as `noxctl.toml:LINE:COL: unknown field` and aggregates every problem in one run.

## Build & gates

```bash
go build ./...                       # ~1 s
go vet ./...
golangci-lint run                    # gocognit/gocyclo ≤ 15, lll ≤ 150
go test ./... -count=1               # ~10 s, all packages
```

Pre-commit hooks live in `.pre-commit-config.yaml` — install once with `pre-commit install`.

## Deploy (maintainer's setup)

```bash
go install ./cmd/regen-watchd       # writes ~/go/bin/regen-watchd
launchctl bootout gui/$(id -u)/com.bear.regen-watchd 2>/dev/null
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.bear.regen-watchd.plist
```

`~/bin/regen-watchd` is a symlink to `~/go/bin/regen-watchd`; the launchd plist points at `~/bin/`, so every `go install` is picked up without editing the plist.

## What this is not

- Not a Bear backup tool — it MUTATES notes in place.
- Not cross-platform — Bear, FSEvents, and `bearcli` are macOS-only.
- Not a general note-management framework — it operates on a closed catalog of six blueprints.

## License

MIT — see [LICENSE](LICENSE).
