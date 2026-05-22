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

## What noxctl does to your vault

For each managed tag, noxctl writes two things to Bear:

1. **A master note** that lists every atom under the tag as a wikilink bullet (shape depends on the blueprint — flat list, bucketed table, or Tier-2 hubs).
2. **A canonical tag-line** stamped onto every atom — `#tag | [[Bucket]] | [Open](bear://…)` — so the wikilink resolves bidirectionally and the master can pick atoms up on every regen pass.

Atoms keep their human-authored body; noxctl only owns the canonical line at the top and the master/hub layout. Below is what one `flat-list` tag looks like before and after a first `noxctl apply`.

**Before** — three atoms tagged `#library/books`, no master:

```markdown
# Sapiens
A book by Yuval Noah Harari about human history.

#library/books
```

**After** — same atom plus a new `✱ Books` master listing all three:

```markdown
# Sapiens

#library/books | [Open](bear://x-callback-url/open-note?title=%E2%9C%B1%20Books)
---

A book by Yuval Noah Harari about human history.
```

```markdown
# ✱ Books

#library/books
---

## Notes (3)
- [[Sapiens]]
- [[Foundation]]
- [[The Pragmatic Programmer]]
```

Same `#library/books` tag rendered in Bear, before and after `noxctl apply`:

| Before | After |
|---|---|
| ![Bear filtered to #nox-demo/books before apply: five atom notes, no master](docs/screenshots/before.png) | ![Bear filtered to #nox-demo/books after apply: master ✱ Books plus five atoms, canonical tag-line chips on each](docs/screenshots/after.png) |

(Demo vault is at `examples/demo-vault/` — `setup.sh` populates it under `#nox-demo/books` and the paired `noxctl.toml` manages exactly that tag.)

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

## Safety & recovery

**Q: Can I undo a `noxctl apply`?**
There is no built-in undo button — noxctl rewrites notes through `bearcli`, which writes directly to Bear's SQLite store. Recovery routes through Bear itself: trashed notes stay in Bear's trash until you manually empty it; an atom whose canonical tag-line you don't like can be edited in Bear like any other note (the next `apply` will reconcile, but a destructive rewrite can be reverted manually). For a hub or master you no longer want, `noxctl destroy <tag>` moves the auto-generated notes to Bear's trash and strips the canonical line from atoms in place — body content is preserved.

**Q: How do I back up before the first apply?**
Bear ships a built-in backup in **File → Backup Database…** — recommended before the first `noxctl apply` on a corpus you care about. The exported `.bearbackup` archive is a self-contained snapshot you can restore from. noxctl writes nothing outside Bear's database except its own state files (`~/.cache/regen-watchd.log` for daemon logs and `.noxctl/state.json` for per-domain content hashes); those are safe to delete and noxctl will rebuild them on the next run.

**Q: Where do destroyed notes go?**
`noxctl destroy <tag>` calls `bearcli` to trash the auto-generated master and any hubs under the tag. Trashed notes stay in Bear's trash (recoverable via the Bear UI) until you empty it manually. Atom notes are NOT trashed by `destroy` — only their canonical tag-line (the top-of-body `#tag | …` line) is stripped; the human-authored body below stays intact in place.

## From existing vault

If your Bear vault already has tags you want to put under noxctl management, run `noxctl import <bear-tag>` instead of editing the starter from scratch. It scans the notes under that tag, infers a likely blueprint based on observable structure (note count, sub-tag shape, body patterns), and prints a candidate `[[domain]]` stanza to stdout — copy-paste-ready into your `noxctl.toml`.

```bash
# After `noxctl init` (Step 2 in Quick Start) but before `noxctl validate`:
noxctl import library/poetry >> ~/.config/noxctl/noxctl.toml
noxctl import research/papers >> ~/.config/noxctl/noxctl.toml
# …repeat per tag, then open the file and tidy the inferred fields
# (index_title, bucket names, etc.) before running validate.
```

Output sample for one tag:

```toml
# noxctl import library/poetry — 47 notes scanned
# every note carries a single-segment sub-tag (4 distinct buckets observed).
#
# Paste the [[domain]] block below into your noxctl.toml
# (review the field values first — they are educated guesses).

[[domain]]
  tag         = "library/poetry"
  index_title = "✱ Poetry"
  blueprint   = "flat-table"
  buckets        = ["Frost", "Rilke", "Heaney", "Plath"]
  unknown_bucket = "Other"
```

Import is non-destructive — it never writes to `noxctl.toml` itself or touches your Bear notes. The operator owns the catalog file; `import` just generates pasteable text.

Bulk multi-tag import (`noxctl import --all`) is on the roadmap. For now, run `import` per tag and concatenate output.

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

## Choosing a blueprint

Six rendering blueprints, each fitting a distinct tag shape. Pick by walking the decision tree below; consult the table for the full comparison.

**Decision tree:**

- Are notes grouped under the tag at all?
  - **No** — every note is a peer, order doesn't matter → **`flat-list`**
  - **Yes** — what drives the grouping?
    - A pre-declared bucket name in the canonical tag-line (operator owns the bucket list) → table or vertical layout?
      - Horizontal table (columns per bucket) → **`flat-table`**
      - Vertical sections (H2 per bucket, bullets below) → **`grouped-vertical`**
    - A sub-tag on each atom (`#tag/bucket`) → bucket-discovery style?
      - Operator pre-declares the bucket set → **`hub-routed-with-subtag`**
      - Operator authors only the tag, atom bodies drive bucket names → **`hub-routed`**
- Want a top-level master that aggregates several other domains? → **`umbrella`**

**Comparison table:**

| Blueprint | When to use | Required fields beyond the basics | Bucket source | Output shape |
|---|---|---|---|---|
| `flat-list` | inbox / capture tags, no grouping | none | n/a | one master with bullet list of every atom |
| `flat-table` | bucketed collection, finite bucket set, small N | `buckets`, `unknown_bucket` | operator-declared | one master with markdown table, columns = buckets |
| `grouped-vertical` | bucketed collection, large N per bucket | `buckets`, `unknown_bucket` | operator-declared | one master with `## Bucket (N)` H2 per bucket |
| `hub-routed` | author / source grouping where bucket names live in atom bodies | `unknown_bucket`, `hub_h2_prefix` | atom canonical tag-line `[[Bucket Hub]]` | Tier-2: master lists hubs, each hub lists atoms |
| `hub-routed-with-subtag` | hub-style layout but bucket names are sub-tags | `buckets`, `unknown_bucket` | atom sub-tag `#tag/bucket` | Tier-2: master lists hubs, each hub lists atoms |
| `umbrella` | aggregate multiple existing domains under one master | `children`, `default_child` | n/a | master lists every child domain |

Required fields beyond the basics — every blueprint also needs `tag`, `index_title`, `blueprint`. See `examples/<blueprint>.toml` for a copy-pasteable starter per blueprint.

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
