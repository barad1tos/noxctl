package parity_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/cli/parity"
	"github.com/barad1tos/noxctl/bear/engine"
)

// fixedDomainCount is the seed value for the canonical 31-domain
// catalog (27 leaves + 4 umbrellas — registry.All). Hoisted to a
// constant so the test fixtures don't pass the same magic number.
const fixedDomainCount = 31

// ---------- helpers ----------

// seedClean writes a JSON PlanResult representing a "clean" day: 0
// parity-mismatched domains AND 0 errored domains. The file is named
// "<date>.json" — production filename shape produced by
// examples/launchd's cron.
func seedClean(t *testing.T, dir, date string) {
	t.Helper()
	pr := engine.PlanResult{
		SchemaVersion: 1,
		StartedAt:     time.Now(),
		CompletedAt:   time.Now(),
		Domains:       make([]engine.DomainPlan, 0),
		Errors:        make([]engine.PlanError, 0),
		Summary: engine.PlanSummary{
			DomainsTotal:          fixedDomainCount,
			DomainsClean:          fixedDomainCount,
			DomainsDrift:          0,
			DomainsError:          0,
			DomainsParityMismatch: 0,
		},
	}
	writeJSON(t, filepath.Join(dir, date+".json"), pr)
}

// seedDrift writes a JSON PlanResult flagged as drifted: each tag in
// parityMismatchTags becomes a DomainPlan with Status==StatusParityMismatch,
// and Summary.DomainsParityMismatch is set accordingly. Used by tests
// to simulate a single-day drift breaking the streak.
func seedDrift(t *testing.T, dir, date string, parityMismatchTags []string) {
	t.Helper()
	domains := make([]engine.DomainPlan, 0, len(parityMismatchTags))
	for _, tag := range parityMismatchTags {
		domains = append(domains, engine.DomainPlan{
			Tag:     tag,
			Status:  engine.StatusParityMismatch,
			Changes: make([]engine.Diff, 0),
		})
	}
	pr := engine.PlanResult{
		SchemaVersion: 1,
		StartedAt:     time.Now(),
		CompletedAt:   time.Now(),
		Domains:       domains,
		Errors:        make([]engine.PlanError, 0),
		Summary: engine.PlanSummary{
			DomainsTotal:          31,
			DomainsClean:          31 - len(parityMismatchTags),
			DomainsParityMismatch: len(parityMismatchTags),
		},
	}
	writeJSON(t, filepath.Join(dir, date+".json"), pr)
}

// seedCorrupt writes raw garbage bytes to "<date>.json". The contract
// under test: a malformed file is treated conservatively as a drift
// day (the streak resets) rather than silently inflating the streak
// count.
func seedCorrupt(t *testing.T, dir, date string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, date+".json"),
		[]byte("not-a-json-document {{{"), 0o644); err != nil {
		t.Fatalf("seedCorrupt: %v", err)
	}
}

// writeJSON marshals pr and writes it to path. Test-only helper.
func writeJSON(t *testing.T, path string, pr engine.PlanResult) {
	t.Helper()
	raw, err := json.MarshalIndent(pr, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err = os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// runCaptured runs parity.Run against the given cache dir + days,
// capturing stdout for assertions.
func runCaptured(t *testing.T, cacheDir string, days int) (stdout string, err error) {
	t.Helper()
	var buf bytes.Buffer
	err = parity.Run(&buf, cacheDir, days)
	return buf.String(), err
}

// ---------- tests ----------

func TestParityCheckPassOn7Clean(t *testing.T) {
	dir := t.TempDir()
	// Seed 7 consecutive clean days. Date strings are ordered ascending;
	// ListDailyFiles iterates newest-first via reverse-lexical sort.
	for _, d := range []string{
		"2026-01-04", "2026-01-05", "2026-01-06", "2026-01-07",
		"2026-01-08", "2026-01-09", "2026-01-10",
	} {
		seedClean(t, dir, d)
	}
	stdout, err := runCaptured(t, dir, 7)
	if err != nil {
		t.Fatalf("parity.Run unexpected error: %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: PASS") {
		t.Errorf("stdout missing 'parity-check: PASS'\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "7 consecutive clean days") {
		t.Errorf("stdout missing streak summary line\nstdout: %s", stdout)
	}
}

func TestParityCheckResetOnDrift(t *testing.T) {
	dir := t.TempDir()
	// 6 clean older + 1 drift on the newest date — strict reset (D-11):
	// streak measured newest-first, so it stops at the very first row.
	for _, d := range []string{
		"2026-01-04", "2026-01-05", "2026-01-06", "2026-01-07",
		"2026-01-08", "2026-01-09",
	} {
		seedClean(t, dir, d)
	}
	seedDrift(t, dir, "2026-01-10", []string{"library/poetry"})
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("parity.Run error = %v; want parity.ErrFailed\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: FAIL") {
		t.Errorf("stdout missing 'parity-check: FAIL'\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "Last clean streak: 0 day") {
		t.Errorf("stdout should report streak=0 for drift on newest date\nstdout: %s", stdout)
	}
}

func TestParityCheckResetOnMidStreakDrift(t *testing.T) {
	dir := t.TempDir()
	// Oldest is drift, newer 6 are clean. Iteration is newest-first, so
	// streak counts 6 then hits the drift and stops. With --days=7 that
	// means FAIL with streak=6.
	seedDrift(t, dir, "2026-01-04", []string{"llm/agents"})
	for _, d := range []string{
		"2026-01-05", "2026-01-06", "2026-01-07", "2026-01-08",
		"2026-01-09", "2026-01-10",
	} {
		seedClean(t, dir, d)
	}
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("parity.Run error = %v; want parity.ErrFailed\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: FAIL") {
		t.Errorf("stdout missing 'parity-check: FAIL'\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "Last clean streak: 6 day") {
		t.Errorf("stdout should report streak=6\nstdout: %s", stdout)
	}
}

func TestParityCheckMissingCacheDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "definitely-not-here")
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrCacheError) {
		t.Fatalf("parity.Run error = %v; want parity.ErrCacheError\nstdout: %s", err, stdout)
	}
	if !strings.Contains(err.Error(), "cache") {
		t.Errorf("error message should mention 'cache'; got %q", err.Error())
	}
}

func TestParityCheckSchemaParse(t *testing.T) {
	// Lock the predicate: a row's Clean state is derived from
	// DomainsParityMismatch + DomainsError, NOT DomainsDrift. Untracked
	// tag families never contribute (D-02).
	dir := t.TempDir()
	pr := engine.PlanResult{
		SchemaVersion: 1,
		Domains:       make([]engine.DomainPlan, 0),
		Errors:        make([]engine.PlanError, 0),
		Summary: engine.PlanSummary{
			DomainsTotal:          31,
			DomainsClean:          27,
			DomainsDrift:          4, // single-path drift — IGNORED by parity-check
			DomainsParityMismatch: 0,
			DomainsError:          0,
			UntrackedFamilies:     2, // residue — IGNORED by parity-check
		},
	}
	writeJSON(t, filepath.Join(dir, "2026-01-10.json"), pr)
	stdout, err := runCaptured(t, dir, 1)
	if err != nil {
		t.Fatalf("parity.Run unexpected error: %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: PASS") {
		t.Errorf("DomainsDrift>0 with DomainsParityMismatch==0 should still PASS for parity-check\nstdout: %s", stdout)
	}
}

func TestParityCheckIgnoresMalformedJSON(t *testing.T) {
	dir := t.TempDir()
	// Newest file is corrupt; the row is treated as drift (conservative
	// failsafe) and breaks the streak. Older clean files don't matter.
	seedClean(t, dir, "2026-01-09")
	seedCorrupt(t, dir, "2026-01-10")
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("corrupt newest day should FAIL; got err=%v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parse error") {
		t.Errorf("stdout should annotate the corrupt row with 'parse error'\nstdout: %s", stdout)
	}
}

// TestParityCheckGapBreaksStreak locks WR-02: the parity-check streak
// counts CONSECUTIVE CALENDAR DAYS, not files. A launchd-skipped day
// inflates the file count without inflating the streak; the gap-check
// in ComputeStreak (delta > MaxCalendarGap = 25h) reproducibly catches
// this so the operator can't claim the gate while missing a day of
// parity coverage.
func TestParityCheckGapBreaksStreak(t *testing.T) {
	t.Run("MissingDayInMiddle", runGapBreakSubtest(gapBreakSubtest{
		// 7 files spanning 8 calendar days: missing 2026-01-06.
		// Newest-first traversal: 11→10→09→08→07→05→04 → break at
		// rows[5]=05 (gap = 07-05 = 48h > 25h). Streak should count
		// only the contiguous newer half (11, 10, 09, 08, 07 = 5).
		dates: []string{
			"2026-01-04", "2026-01-05", "2026-01-07",
			"2026-01-08", "2026-01-09", "2026-01-10", "2026-01-11",
		},
		wantStreakSubstr: "Last clean streak: 5 day",
		wantGapBreak:     true,
	}))

	t.Run("MissingDayAtEdge", runGapBreakSubtest(gapBreakSubtest{
		// Gap right after the newest row: -11 (newest), then -09 next
		// (48h delta). streak collapses to 1 — only -11 survives.
		dates: []string{
			"2026-01-04", "2026-01-05", "2026-01-06",
			"2026-01-07", "2026-01-08", "2026-01-09", "2026-01-11",
		},
		wantStreakSubstr: "Last clean streak: 1 day",
		wantGapBreak:     true,
	}))

	t.Run("HappyPath7Consecutive", runGapBreakSubtest(gapBreakSubtest{
		// Regression lock: 7 contiguous calendar days (gap = 24h ≤
		// 25h between every adjacent pair) still PASS. Without this,
		// the gap-check might false-positive on the canonical happy
		// path.
		dates: []string{
			"2026-01-04", "2026-01-05", "2026-01-06", "2026-01-07",
			"2026-01-08", "2026-01-09", "2026-01-10",
		},
		wantPass: true,
	}))
}

// gapBreakSubtest is the table row for TestParityCheckGapBreaksStreak.
// Either wantPass=true (canonical PASS) or wantStreakSubstr/wantGapBreak
// (FAIL with a specific streak count and "gap-break" annotation).
type gapBreakSubtest struct {
	dates            []string
	wantStreakSubstr string
	wantGapBreak     bool
	wantPass         bool
}

// runGapBreakSubtest is the per-row body of TestParityCheckGapBreaksStreak.
// Extracted so the parent test stays under the gocognit ≤15 budget —
// every sub-test reuses the same seed → parity.Run → assert pipeline.
func runGapBreakSubtest(tc gapBreakSubtest) func(t *testing.T) {
	return func(t *testing.T) {
		dir := t.TempDir()
		for _, d := range tc.dates {
			seedClean(t, dir, d)
		}
		stdout, err := runCaptured(t, dir, 7)
		if tc.wantPass {
			assertParityPass(t, err, stdout)
			return
		}
		assertParityGapBreakFail(t, err, stdout, tc)
	}
}

// assertParityPass folds the happy-path assertions: nil error AND the
// PASS headline in stdout.
func assertParityPass(t *testing.T, err error, stdout string) {
	t.Helper()
	if err != nil {
		t.Fatalf("happy path should PASS; got %v\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: PASS") {
		t.Errorf("happy path missing PASS line; stdout=%s", stdout)
	}
}

// assertParityGapBreakFail folds the gap-break failure assertions:
// parity.ErrFailed sentinel + FAIL headline + expected streak summary +
// optional "gap-break" annotation in the per-day breakdown.
func assertParityGapBreakFail(t *testing.T, err error, stdout string, tc gapBreakSubtest) {
	t.Helper()
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("err = %v; want parity.ErrFailed\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: FAIL") {
		t.Errorf("expected FAIL; stdout=%s", stdout)
	}
	if tc.wantStreakSubstr != "" && !strings.Contains(stdout, tc.wantStreakSubstr) {
		t.Errorf("expected substring %q; stdout=%s", tc.wantStreakSubstr, stdout)
	}
	if tc.wantGapBreak && !strings.Contains(stdout, "gap-break") {
		t.Errorf("expected 'gap-break' annotation; stdout=%s", stdout)
	}
}

// TestParityCheckStreakMaturing locks the Day-1..Day-(N-1) UX: when
// the cache has fewer than `target` clean days but EVERY row is clean
// (no drift, no gap), the report says "No drift detected. Wait N more
// days for streak to mature." instead of the misleading
// "Resolve drift" wording. Day-1 baseline test for the 7-day cycle.
func TestParityCheckStreakMaturing(t *testing.T) {
	dir := t.TempDir()
	// Seed 3 consecutive clean days; target=7 means streak=3 < target,
	// no drift, no gap.
	for _, d := range []string{"2026-05-10", "2026-05-11", "2026-05-12"} {
		seedClean(t, dir, d)
	}
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("err = %v; want parity.ErrFailed\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "parity-check: FAIL") {
		t.Errorf("stdout missing 'parity-check: FAIL'\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "Last clean streak: 3 day") {
		t.Errorf("stdout missing streak count\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "No drift detected") {
		t.Errorf("stdout should say 'No drift detected' for clean-but-young streak\nstdout: %s", stdout)
	}
	if !strings.Contains(stdout, "streak to mature") {
		t.Errorf("stdout should say 'streak to mature' for clean-but-young streak\nstdout: %s", stdout)
	}
	if strings.Contains(stdout, "Resolve drift") {
		t.Errorf("stdout should NOT say 'Resolve drift' when all rows are clean\nstdout: %s", stdout)
	}
}

// TestParityCheckResolveDriftWording locks the inverse: when at least
// one row IS dirty, the report says "Resolve drift, then wait..." —
// the historical wording stays for the actual-drift case.
func TestParityCheckResolveDriftWording(t *testing.T) {
	dir := t.TempDir()
	for _, d := range []string{"2026-05-10", "2026-05-11"} {
		seedClean(t, dir, d)
	}
	seedDrift(t, dir, "2026-05-12", []string{"library/poetry"})
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("err = %v; want parity.ErrFailed\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "Resolve drift") {
		t.Errorf("stdout should say 'Resolve drift' when newest row has drift\nstdout: %s", stdout)
	}
	if strings.Contains(stdout, "No drift detected") {
		t.Errorf("stdout should NOT say 'No drift detected' when drift exists\nstdout: %s", stdout)
	}
}

// TestParityCheckGapBreakWording locks the gap-break case: all rows
// clean but a calendar gap interrupted the streak — message says
// "Calendar gap detected" not "Resolve drift" (no drift exists) and
// not "streak to mature" (data is older than the gap).
func TestParityCheckGapBreakWording(t *testing.T) {
	dir := t.TempDir()
	// 4 clean days with a 2-day gap (missing 2026-05-08): newest-first
	// traversal hits the gap at index 2 (05-09→05-07 = 48h).
	for _, d := range []string{"2026-05-05", "2026-05-06", "2026-05-07", "2026-05-09"} {
		seedClean(t, dir, d)
	}
	stdout, err := runCaptured(t, dir, 7)
	if !errors.Is(err, parity.ErrFailed) {
		t.Fatalf("err = %v; want parity.ErrFailed\nstdout: %s", err, stdout)
	}
	if !strings.Contains(stdout, "Calendar gap detected") {
		t.Errorf("stdout should say 'Calendar gap detected' on gap-break\nstdout: %s", stdout)
	}
	if strings.Contains(stdout, "Resolve drift") {
		t.Errorf("stdout should NOT say 'Resolve drift' when no row is dirty\nstdout: %s", stdout)
	}
	if strings.Contains(stdout, "streak to mature") {
		t.Errorf("stdout should NOT say 'streak to mature' on gap-break (different cause)\nstdout: %s", stdout)
	}
}

func TestParityCheckSentinelsDistinct(t *testing.T) {
	if parity.ErrFailed == nil {
		t.Fatal("parity.ErrFailed sentinel is nil")
	}
	if parity.ErrCacheError == nil {
		t.Fatal("parity.ErrCacheError sentinel is nil")
	}
	if errors.Is(parity.ErrFailed, parity.ErrCacheError) {
		t.Error("parity.ErrFailed should not match parity.ErrCacheError")
	}
	if errors.Is(parity.ErrCacheError, parity.ErrFailed) {
		t.Error("parity.ErrCacheError should not match parity.ErrFailed")
	}
}
