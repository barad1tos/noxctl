# Migration: Hardcoded Go → TOML

This document records the closed-catalog mapping: every domain that
previously lived as a `var FooDomain = &bear.Domain{...}` literal in
Go code was migrated to a `[[domain]]` stanza in
`examples/personal.toml` (or your own `noxctl.toml`). The original
hardcoded packages (`library/`, `llm/`, `it/`, `personal/`,
`quicknote/`, `registry/`) were deleted in a single atomic commit
after the daemon ran cleanly on the TOML catalog for the cutover
window.

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
`examples/personal.toml`. The leaf tags `claude`, `english`, `health`,
`leisure`, `humor`, `work`, `instagram`, `travel`, and `development`
are top-level (no `personal/` prefix) — that's how the legacy
hardcoded `personal/*` package registered them, preserved verbatim in
the migration so Bear sidebar trees stay byte-identical.

## Custom Renderers

Three domains ship Go-side renderers under `bear/custom/`:

- **lyrics** (`bear/custom/lyrics.go`) — splits artist buckets into
  Latin and Cyrillic sub-sections; renders a 2-column-style master.
- **quotes** (`bear/custom/quotes.go`) — special-cases the Бaш and It
  Happens buckets; renders a flat list per source.
- **agents** (`bear/custom/agents.go`) — renders a 5-column grid
  grouping agents by domain (knowledge, ops, scaffolding, lang
  specialist, governance).

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

The closed-catalog principle stated above is preserved: adding a
renderer is a Go-code change with the same review gate as adding a
new declarative blueprint.

## Rollback Procedure

If post-deletion production breaks, rollback is standard `git revert`:

1. `git revert <deletion-commit-sha>` — restores all hardcoded packages
   and the legacy daemon binary in one atomic operation.
2. `go install ./cmd/regen-watchd` — re-installs the legacy daemon
   binary at `~/go/bin/regen-watchd` (the revert restores its source).
3. Repoint `~/Library/LaunchAgents/com.bear.regen-watchd.plist`
   `ProgramArguments` back to `/Users/cloud/bin/regen-watchd`, then
   `launchctl bootout && launchctl bootstrap` to swap the running
   daemon process.
4. Spot-check 3 domains in Bear (one hub-routed, one grouped-vertical,
   one custom) to verify the vault is unchanged.

The deletion commit is the single rollback point; no special tags or
backup branches are needed. The pre-deletion commit's SHA is also
the canonical "last known good" reference for diff comparisons.

## What Was Deleted

The atomic deletion commit removed five top-level Go packages, the
legacy daemon binary, the codegen tool, the bridge-window parity
scaffolding, and the test files that pinned bridge-only invariants:

- `library/` — 6 leaf domains (poetry, aphorisms, articles, lyrics, prose, quotes)
- `llm/` — 4 leaf domains (agents, characters, rules, tips)
- `it/` — 3 leaf domains (domains, vendors, technologies)
- `personal/` — 9 leaf domains (claude, english, health, leisure, humor, work, instagram, travel, development)
- `quicknote/` — 5 leaf domains (daily, weekly, monthly, yearly, decadal)
- `registry/` — bridge-window seam (one import-site for hardcoded domains)
- `cmd/regen-watchd/` — legacy daemon binary, superseded by `noxctl daemon`
- `cmd/noxctl-codegen/` — one-shot codegen tool used to author `examples/personal.toml`
- `cmd/noxctl/parity_check.go` + `bear/cli/parity/` + `bear/engine/parity.go` — bridge-window parity dispatcher, superseded by `noxctl verify`
- `tests/bear/codegen_test.go`, `tests/bear/equivalence_test.go`, `tests/bear/config/roman_corpus_test.go`, `tests/bear/engine/parity_test.go`, `tests/bear/engine/plan_parity_test.go`, `tests/registry/`, `tests/bear/cli/parity/`, `tests/cmd/regen-watchd/` — every test that pinned an invariant on the deleted code paths

Operator-side cutover: the launchd plist (`~/Library/LaunchAgents/com.bear.regen-watchd.plist`) was repointed from `/Users/cloud/bin/regen-watchd` to `noxctl daemon --config <path>` before this commit landed, so the daemon process flips to the new binary without operator intervention. The `~/bin/regen-watchd` symlink may be removed at leisure.

## Cross-References

- **Post-migration safety gate:** `noxctl verify`. Run it against the
  live vault to confirm plan-parity (0 drift) + daemon-log health
  (no LOOP / EMERGENCY / ERROR since last startup) +, with
  `--with-apply`, idempotency (a second apply pass writes nothing).
  `scripts/ship-gate.sh` chains the three checks for CI.
- **Closed-catalog principle:** see `bear/custom/registry.go` for the
  custom-renderer registry; `bear/config/blueprint.go` for the
  declarative blueprint enumeration.
