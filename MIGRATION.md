# Migration: Hardcoded Go → TOML

This document records the closed-catalog mapping that completed
of noxctl: every domain that previously lived as a `var FooDomain =
&bear.Domain{...}` literal in Go code was migrated to a `[[domain]]`
stanza in `examples/roman.toml` (or your own `noxctl.toml`). After 7
consecutive clean days reported by `noxctl parity-check`, the original
hardcoded packages (`library/`, `llm/`, `it/`, `personal/`,
`quicknote/`, `registry/`) were deleted in a single atomic commit
(D-12).

This file is the audit trail — frozen as of the deletion commit. Future
schema changes (new blueprints, new custom renderers) update this file
in the same commit as the code change.

## Domain → Blueprint Mapping

| Tag                | Blueprint              | Notes                                                                                |
|--------------------|------------------------|--------------------------------------------------------------------------------------|
| library/poetry     | hub-routed             | `LegacyAuthorFallback=true`, `StripLegacyAuthorH2=true`, `OwnGroup="Моя поезія"`    |
| library/aphorisms  | flat-table             | `buckets=["Книги", "Кіно", "Ігри"]`                                                  |
| library/articles   | hub-routed             | `LegacyAuthorFallback=false`, `StripLegacyAuthorH2=false`, `OwnGroup="Мої статті"`  |
| library/lyrics     | custom (lyrics)        | Ships custom renderer; see "Custom Renderers" below                                  |
| library/prose      | flat-table             | `buckets=["Мої", "Чужі"]`                                                            |
| library/quotes     | custom (quotes)        | Ships custom renderer; see "Custom Renderers" below                                  |
| llm/agents         | custom (agents)        | Ships custom renderer with 5-column grid                                             |
| llm/characters     | flat-list              |                                                                                      |
| llm/rules          | flat-list              |                                                                                      |
| llm/tips           | flat-list              |                                                                                      |
| it/domains         | flat-list              |                                                                                      |
| it/vendors         | flat-table             |                                                                                      |
| it/technologies    | flat-table             |                                                                                      |
| claude             | hub-routed-with-subtag | Sub-tag preservation: `#claude/<bucket>` carried verbatim to atom canonical          |
| english            | grouped-vertical       |                                                                                      |
| health             | grouped-vertical       |                                                                                      |
| leisure            | grouped-vertical       |                                                                                      |
| humor              | grouped-vertical       |                                                                                      |
| work               | grouped-vertical       |                                                                                      |
| instagram          | grouped-vertical       |                                                                                      |
| travel             | grouped-vertical       |                                                                                      |
| development        | grouped-vertical       |                                                                                      |
| quicknote/daily    | flat-list              |                                                                                      |
| quicknote/weekly   | flat-list              |                                                                                      |
| quicknote/monthly  | flat-list              |                                                                                      |
| quicknote/yearly   | flat-list              |                                                                                      |
| quicknote/decadal  | flat-list              |                                                                                      |
| it                 | umbrella               | Children: domains, vendors, technologies                                             |
| library            | umbrella               | Children: poetry, aphorisms, articles, lyrics, prose, quotes                         |
| llm                | umbrella               | Children: agents, characters, rules, tips                                            |
| quicknote          | umbrella               | Children: daily, weekly, monthly, yearly, decadal                                    |

**Total: 27 leaves + 4 umbrellas = 31 domains.** Every row in this
table corresponds to exactly one `[[domain]]` stanza in
`examples/roman.toml`. The leaf tags `claude`, `english`, `health`,
`leisure`, `humor`, `work`, `instagram`, `travel`, and `development`
are top-level (no `personal/` prefix) — that's how the legacy
hardcoded `personal/*` package registered them, preserved verbatim in
the migration so Bear sidebar trees stay byte-identical.

## Custom Renderers

Three domains ship Go-side renderers under `bear/custom/`:

- **lyrics** (`bear/custom/lyrics.go`) — splits artist buckets into
  Latin and Cyrillic sub-sections; renders a 2-column-style master.
  Source: pre-Phase-4 `library/lyrics.go::renderLyricsMaster`.
- **quotes** (`bear/custom/quotes.go`) — special-cases the Бaш and It
  Happens buckets; renders a flat list per source. Source: pre-Phase-4
  `library/quotes.go::renderQuotesMaster`.
- **agents** (`bear/custom/agents.go`) — renders a 5-column grid
  grouping agents by domain (knowledge, ops, scaffolding, lang
  specialist, governance). Source: pre-Phase-4
  `llm/agents.go::renderAgentsMaster`.

This is **NOT an open scripting hatch** — the registry at
`bear/custom/registry.go` is closed. Adding a new custom renderer
requires:

1. A new file under `bear/custom/<name>.go` that calls `Register` in
   its `init()` block.
2. A test under `bear/custom/<name>_test.go` covering at least one
   shape of the renderer's output.
3. A new row in this table mapping the source domain to
   `blueprint = "custom"`, `renderer = "<name>"`.
4. An update to the equivalence-test fixture under
   `tests/bear/testdata/equivalence/` so byte-equality is locked.

The closed-catalog principle from `` is preserved:
adding a renderer is a Go-code change with the same review gate as
adding a new declarative blueprint.

## Rollback Procedure

If post-deletion production breaks, rollback is standard `git revert`:

1. `git revert <deletion-commit-sha>` — restores all hardcoded packages
   in one atomic operation.
2. `go install ./cmd/regen-watchd` — re-installs the legacy daemon
   binary at `~/go/bin/regen-watchd`.
3. `launchctl kickstart -k gui/$(id -u)/com.bear.regen-watchd` —
   restarts the legacy daemon under launchd.
4. Spot-check 3 domains in Bear (one hub-routed, one grouped-vertical,
   one custom) to verify the vault is unchanged.

The deletion commit is the single rollback point; no special tags or
backup branches are needed (D-13). The pre-deletion commit's SHA is
also the canonical "last known good" reference for diff comparisons.

## What Was Deleted

The atomic deletion commit removed five top-level Go packages and a
small set of supporting test/codegen artifacts:

- `library/` — 6 leaf domains (poetry, aphorisms, articles, lyrics, prose, quotes)
- `llm/` — 4 leaf domains (agents, characters, rules, tips)
- `it/` — 3 leaf domains (domains, vendors, technologies)
- `personal/` — 9 leaf domains (claude, english, health, leisure, humor, work, instagram, travel, development)
- `quicknote/` — 5 leaf domains (daily, weekly, monthly, yearly, decadal)
- `registry/` —  bridge-window seam (one import-site for hardcoded domains)
- `cmd/noxctl-codegen/` — one-shot codegen tool used to author `examples/roman.toml`
- `tests/bear/codegen_test.go` — codegen acceptance test
- `tests/bear/config/roman_corpus_test.go` — superseded by
  `tests/bear/equivalence_test.go`

The post-deletion `cmd/regen-watchd/main.go` is a small shim that
exec's `noxctl daemon` (or `noxctl apply` on `--once`) to preserve the
existing launchd plist + `~/bin/regen-watchd` symlink without operator
intervention.

## Cross-References

- **7-day parity gate:** `noxctl parity-check`. Daily logs at
  `~/.cache/noxctl-parity/<date>.json`. Install via
  `examples/launchd/io.barad1tos.noxctl-parity.plist` (see that
  directory's `README.md` for bootstrap steps).
- **Equivalence test:**
  `tests/bear/equivalence_test.go::TestDomainEquivalence` asserts
  byte-equal master rendering across all 31 domains — the acceptance
  gate that ran for the entire bridge window.
- **Closed-catalog principle:** see `bear/custom/registry.go` for the
  custom-renderer registry; `bear/config/blueprint.go` for the
  declarative blueprint enumeration.
