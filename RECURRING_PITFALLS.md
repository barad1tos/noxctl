# Recurring pitfalls — read before every non-trivial change

This catalog distils regressions that took multiple fix rounds in this repo's own history. Read it before touching `bear/engine/`, `bear/cli/apply.go`, the daemon poll loop, or `.github/dependabot.yml` / `.github/workflows/`.

Mined from `git log` on the noxctl repo (PRs #50, #70, #86). Patterns are deliberately generic — they apply to any daemon with a poll/retry loop, any file-based lock, and any CI dependency-bot config.

---

## Pattern A — Poll/retry baseline committed before the operation it represents succeeds

**Symptom.** The daemon's poll loop reads a DB mtime/content token, immediately writes it into `baseline`, and only then runs the pass (auto-tag write, regen cycle) that the token was supposed to gate. If that pass is gated/debounced, fails, or partially fails, the baseline has already moved — the daemon believes the change was handled and the next poll sees nothing new. PR #50 needed **six** separate `fix(daemon):` commits (`2507a8f`, `563944b`, `a869048`, `bfd85c7`, `e08aebc`, `49909a1`) to close every variant of this hole: gated events, debounced events, transient sqlite/bearcli failures, interrupted Apply cycles, and per-note fast-pass failures each independently swallowed a pending change before the fix landed for the previous variant.

**Rule.** A baseline/token/cursor that represents "I have seen and handled change X" MUST only advance after the handler for X has run AND reported success. Model it as `pending` vs `committed` state (see `databaseBaseline.markPending` / `.commit` in `bear/engine/events.go`) rather than a single mutable field. Partial failure (some notes ok, some failed) must still leave the token pending — success is per-unit, not per-tick.

**Bad:**
```go
mt := info.ModTime()
if !mt.After(baseline.modTime) {
    return
}
baseline.modTime = mt // committed before the pass even runs
d.handleEvent(fsnotify.Event{Op: fsnotify.Write, Name: dbPath}, quietTimer, maxTimer, burstActive)
```

**Good:**
```go
candidate, changed, _, err := d.nextDatabaseCandidate(dbPath, baseline)
if err != nil || !changed {
    return
}
baseline.markPending(candidate)
if ok := d.handleEvent(fsnotify.Event{Op: fsnotify.Write, Name: dbPath}, quietTimer, maxTimer, burstActive); ok {
    baseline.commit(candidate) // only advances once the handler actually succeeded
}
```

---

## Pattern B — Sentinel/lock file with no staleness check wedges the daemon forever

**Symptom.** `.apply-pending` is a file-based sentinel: present means "an apply is in flight, daemon should yield." A killed `apply` (SIGKILL, `go install` race, crash) never runs its cleanup, so the sentinel is left behind permanently. Before `#70` (`38a1ca3`), `IsApplyPending` treated ANY existing sentinel as pending — the daemon then skipped every regen cycle indefinitely, silently, with no error to grep for. Symptom in production: "atoms with a valid bucket missing from their hub", discovered only by noticing stale hub content, not by any log line.

**Rule.** Any file-based lock/sentinel used for coordination MUST carry a TTL and be checked against it on every read. A sentinel older than the maximum legitimate hold time (one flock-wait + one pass, generously bounded) is orphaned — remove it best-effort and proceed, and log the eviction (the PR #70 review follow-up, `d7d5adf`) so an operator can correlate an auto-recovery with a prior crash.

**Bad:**
```go
func IsApplyPending(lockPath string) bool {
    _, err := os.Stat(sentinel)
    return err == nil // a sentinel from a killed process blocks every future cycle, forever
}
```

**Good:**
```go
func IsApplyPending(lockPath string) bool {
    info, err := os.Stat(sentinel)
    if err != nil {
        return false
    }
    if age := time.Since(info.ModTime()); age > applyPendingTTL {
        log.Printf("apply-pending sentinel stale (age %s); removed, proceeding", age.Round(time.Second))
        _ = os.Remove(sentinel)
        return false
    }
    return true
}
```

---

## Pattern C — Recap/status structure defaults to success instead of deriving it from actual outcome

**Symptom.** `apply`'s per-domain recap row defaulted to `"unchanged"` unless something upstream explicitly marked it otherwise. A domain regen that returned an error still showed as a clean, unchanged row — the user had no signal that anything failed. PR #50's `fix(apply): surface domain regen failures` plus its own review-follow-up `fix(apply): close review gaps in failure reporting` both had to land before hub/master create-vs-unchanged counts, pre-pass writes, and daemon-vs-user resume state were all derived from the real `RegenResult` instead of assumed.

**Rule.** Status/outcome fields on a result struct must be **computed** from the structured counts the operation actually produced (failed/changed/created), never left at a zero-value default that happens to read as "ok". When adding a new outcome-producing path (fast-pass, pre-pass, daemon retry), thread its counts into the same `RegenResult`/`ApplyResult` aggregation rather than inventing a parallel summary that can drift.

**Bad:**
```go
func (r RegenResult) Row() ApplyRow {
    return ApplyRow{Status: "unchanged"} // zero-value default reads as success
}
```

**Good:**
```go
func (r RegenResult) Row() ApplyRow {
    if r.Failed > 0 {
        return ApplyRow{Status: "failed", FailedCount: r.Failed}
    }
    if r.Created > 0 || r.Updated > 0 {
        return ApplyRow{Status: "changed"}
    }
    return ApplyRow{Status: "unchanged"}
}
```

---

## Pattern D — Coupled Dependabot updates applied independently, and unbounded test parallelism treated as free

**Symptom.** `codeql-action/init` and `codeql-action/analyze` are two separate Dependabot PRs; when they land on different days the workflow runs `init@vX` against `analyze@vY`, which can break silently or produce partial SARIF. Separately, package tests running with Go's default parallelism raced for CPU on CI runners, producing gate flakiness that looked like a code regression. Both had already been merged into CI once each, then had to be fixed again in `#86` (`ae4193e`/`b7bad38`, `fix(ci): restore dependency and test gates`).

**Rule.** Group any actions/dependencies that must move in lockstep (matching major/minor across `init`/`analyze`, or any multi-package protocol pair) into one Dependabot group so they land in the same PR. Set update cooldowns to the security floor (7 days) rather than same-day auto-merge. Serialize test execution in CI/pre-commit/ship-gate (`go test -p=1 ./...`) when packages share machine resources — parallel-by-default is not free on a shared CI runner.

**Bad:**
```yaml
# dependabot.yml — init and analyze update independently, no cooldown
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule: { interval: "daily" }
```

**Good:**
```yaml
updates:
  - package-ecosystem: "github-actions"
    directory: "/"
    schedule: { interval: "daily" }
    cooldown: { default-days: 7 }
    groups:
      codeql-actions:
        patterns: ["github/codeql-action/*"] # init + analyze always advance together
```

---

## Quick checklist for every prompt

Before writing the first line of code, scan the diff for these:

- [ ] Any poll/retry baseline, cursor, or token only advances AFTER its handler reports success — partial/per-unit failure keeps it pending (Pattern A)
- [ ] Any file-based lock/sentinel has a TTL staleness check and logs when it self-heals an orphaned entry (Pattern B)
- [ ] Any result/recap status field is computed from real outcome counts, never left at a default that reads as success (Pattern C)
- [ ] Dependabot groups for actions/deps that must move in lockstep; CI test parallelism is deliberate, not left at the runner default (Pattern D)

If a fix maps to one of these patterns, name the pattern letter in the commit message body.
