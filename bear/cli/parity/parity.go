// Package parity implements the noxctl parity-check D-10/D-11 logic.
//
// It reads the most recent N days of <cache-dir>/<date>.json files
// (each a serialized engine.PlanResult written by the launchd cron at
// examples/launchd/io.barad1tos.noxctl-parity.plist), evaluates the
// clean-streak under STRICT RESET semantics (any single drift day →
// streak = 0; D-11), and reports PASS/FAIL.
//
// This is the deletion gate for D-12. The deletion task in
// plan 04-07 starts by asking the operator to paste this command's
// PASS output before proceeding.
package parity

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/barad1tos/noxctl/bear/engine"
)

// ErrFailed is the FAIL sentinel — the required clean streak hasn't
// been achieved yet. Could be (a) drift in the window, (b) calendar
// gap broke the streak, or (c) cache simply hasn't accumulated enough
// days yet. PrintReport distinguishes the three so the operator sees
// the actual cause; this sentinel just signals "not yet PASS" to the
// exit-code mapper. Callers (cmd/noxctl) map this to ExitError (=1).
var ErrFailed = errors.New("noxctl: parity check streak target not met")

// ErrCacheError is the ERROR sentinel — the cache directory is missing
// or unreadable. Callers map this to ExitDiffPresent (=2), reusing the
// existing exit-2 constant. The naming is overloaded for parity-check
// (D-15 "exit 2 = ERROR" overrides CLI-04's "exit 2 = drift exists"
// for this subcommand only — documented in help text).
var ErrCacheError = errors.New("noxctl: parity cache directory unreadable")

// MaxCalendarGap is the maximum acceptable delta between adjacent
// daily files for the streak to count them as "consecutive calendar
// days" (WR-02). Daylight savings shifts ±1h twice a year; launchd
// jitter rarely exceeds a minute. 25h grants a 1h cushion; anything
// larger means the operator skipped a day (machine asleep, lid
// closed, etc.) — D-11 strict-reset.
const MaxCalendarGap = 25 * time.Hour

// dailyFileDateFormat is the Go reference layout (YYYY-MM-DD) shared
// by ParseDateFromFilename and PrintDailyRow. Matches the production
// filename contract written by the launchd cron at
// examples/launchd/io.barad1tos.noxctl-parity.plist
// (`$(date -u +%Y-%m-%d).json`).
const dailyFileDateFormat = "2006-01-02"

// Row captures the per-day state for the printed report. Clean is the
// predicate computed by ReadDaily; ParseErr is non-nil when the JSON
// failed to decode (treated as drift, conservatively).
type Row struct {
	Date        time.Time
	Path        string
	Clean       bool
	DomainCount int
	DriftCount  int
	DriftTags   []string
	ParseErr    error
}

// DefaultCacheDir resolves ~/.cache/noxctl-parity. Falls back to the
// relative path when UserHomeDir errors — which is essentially never
// on a real Mac, but gives a deterministic value for help-text
// rendering when the test process has no HOME (CI, sandboxed builds).
func DefaultCacheDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ".cache/noxctl-parity"
	}
	return filepath.Join(home, ".cache", "noxctl-parity")
}

// Run is the parity-check orchestrator. Wires the directory listing,
// per-file parse, streak computation, and the PASS/FAIL report.
// Errors surface as the two sentinels (ErrFailed, ErrCacheError) so
// the caller's exit-code mapper can route them.
func Run(w io.Writer, cacheDir string, days int) error {
	files, err := ListDailyFiles(cacheDir)
	if err != nil {
		return fmt.Errorf("%w: %s: %v", ErrCacheError, cacheDir, err)
	}
	rows := ReadRowsNewestFirst(files, days)
	streak, gapBreakIndex := ComputeStreak(rows)
	PrintReport(w, rows, streak, days, gapBreakIndex)
	if streak >= days {
		return nil
	}
	return ErrFailed
}

// ListDailyFiles returns sorted (newest-first) entries matching
// "*.json" under cacheDir. Empty directory returns an empty slice with
// nil error; missing directory returns the underlying os.ReadDir error
// (which the caller wraps into ErrCacheError).
//
// Sort key: reverse-lexical on the basename. Files follow the
// "YYYY-MM-DD.json" naming contract — that's lexically equivalent to
// chronological, so reverse-lexical == newest-first without parsing
// dates twice.
func ListDailyFiles(cacheDir string) ([]string, error) {
	entries, err := os.ReadDir(cacheDir)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		out = append(out, filepath.Join(cacheDir, e.Name()))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out, nil
}

// ReadRowsNewestFirst reads up to maxDays files (newest-first) into
// Row slices. Stops early on the first DIRTY row — D-11 strict reset
// means older rows don't matter once the streak is broken. The dirty
// row IS included in the output so the report can show the operator
// what tripped the gate.
func ReadRowsNewestFirst(files []string, maxDays int) []Row {
	rows := make([]Row, 0, maxDays)
	for _, f := range files {
		if len(rows) >= maxDays {
			break
		}
		row := ReadDaily(f)
		rows = append(rows, row)
		if !row.Clean {
			break
		}
	}
	return rows
}

// ReadDaily parses one daily file into a Row. Malformed or unreadable
// files are returned with ParseErr set and Clean=false — the
// conservative failsafe (Pitfall: a "clean" day silently reconstructed
// from a corrupt JSON would inflate the streak, leading to a false
// PASS at the deletion gate).
func ReadDaily(path string) Row {
	row := Row{Path: path}
	row.Date = ParseDateFromFilename(filepath.Base(path))

	raw, err := os.ReadFile(path)
	if err != nil {
		row.ParseErr = err
		return row
	}
	var pr engine.PlanResult
	if err = json.Unmarshal(raw, &pr); err != nil {
		row.ParseErr = err
		return row
	}
	row.DomainCount = pr.Summary.DomainsTotal
	row.DriftCount = pr.Summary.DomainsParityMismatch + pr.Summary.DomainsError
	row.Clean = row.DriftCount == 0
	if !row.Clean {
		row.DriftTags = CollectDriftTags(&pr)
	}
	return row
}

// ParseDateFromFilename extracts YYYY-MM-DD from "<date>.json". Returns
// the zero time on parse failure — PrintReport falls back to the
// basename so the operator can still identify the file.
func ParseDateFromFilename(base string) time.Time {
	name := strings.TrimSuffix(base, ".json")
	t, err := time.Parse(dailyFileDateFormat, name)
	if err != nil {
		return time.Time{}
	}
	return t
}

// CollectDriftTags walks PlanResult.Domains and returns the sorted set
// of tags whose Status is non-clean. The output is part of the per-row
// FAIL diagnostic so the operator can trace which domain misbehaved.
func CollectDriftTags(pr *engine.PlanResult) []string {
	out := make([]string, 0)
	for _, d := range pr.Domains {
		if d.Status != engine.StatusClean {
			out = append(out, d.Tag)
		}
	}
	sort.Strings(out)
	return out
}

// ComputeStreak returns the count of CONSECUTIVE clean rows from the
// start of rows (which is newest-first), and the row index that
// reproducibly broke the streak via a calendar-day gap (or -1 if the
// streak was broken by a dirty row, by running out of rows, or not
// broken at all).
//
// Streak invariants:
// - First dirty row breaks the streak (D-11 strict reset).
// - Gap between adjacent calendar dates > MaxCalendarGap also
// breaks the streak (WR-02 — daily cron skipped a day; the file
// count must NOT inflate the consecutive-day count).
// - rows is expected newest-first; the gap check compares the older
// date (current row) against the newer date (previous row).
func ComputeStreak(rows []Row) (streak, gapBreakIndex int) {
	gapBreakIndex = -1
	for index, current := range rows {
		if !current.Clean {
			break
		}
		if index > 0 && !StreakKeepsAlive(rows[index-1], current) {
			gapBreakIndex = index
			break
		}
		streak++
	}
	return streak, gapBreakIndex
}

// StreakKeepsAlive reports whether the calendar gap between two
// adjacent rows (newer = previous iteration value, older = current
// iteration value) stays within MaxCalendarGap. Zero Date (corrupt
// filename that failed ParseDateFromFilename) breaks streak too —
// the operator must investigate the malformed file before claiming
// the gate.
func StreakKeepsAlive(newer, older Row) bool {
	if newer.Date.IsZero() || older.Date.IsZero() {
		return false
	}
	gap := newer.Date.Sub(older.Date)
	return gap > 0 && gap <= MaxCalendarGap
}

// PrintReport mirrors the D-10 example output. Headline (PASS/FAIL),
// per-day breakdown, then a closing summary line. The per-day
// breakdown shows each examined row including the one that broke the
// streak — so the operator sees exactly which date and which tags
// tripped the gate.
//
// gapBreakIndex carries the index of the row that broke the streak
// via a calendar-day gap > MaxCalendarGap, or -1 if no such break
// (streak completed or broken by a dirty row, not by a gap). For a
// gap-break row the per-day line includes a "gap-break" annotation
// naming the delta and the previous row's date.
func PrintReport(w io.Writer, rows []Row, streak, target, gapBreakIndex int) {
	// Project convention — `_, _ = fmt.Fprint…` for diagnostic writers
	// we don't need to handshake with. The alternative (returning the
	// error) would force every caller into error-handling boilerplate
	// for output that always succeeds in production (stdout / stderr /
	// a *bytes.Buffer in tests).
	if streak >= target {
		_, _ = fmt.Fprintln(w, "parity-check: PASS")
	} else {
		_, _ = fmt.Fprintln(w, "parity-check: FAIL")
	}
	for index, r := range rows {
		PrintDailyRow(w, rows, index, r, gapBreakIndex)
	}
	_, _ = fmt.Fprintln(w)
	if streak >= target {
		_, _ = fmt.Fprintf(w, "%d consecutive clean days.  deletion gate satisfied.\n", target)
		return
	}
	remaining := target - streak
	switch {
	case anyDirty(rows):
		_, _ = fmt.Fprintf(w, "Last clean streak: %d day. Resolve drift, then wait %d more days.\n",
			streak, remaining)
	case gapBreakIndex >= 0:
		_, _ = fmt.Fprintf(w, "Last clean streak: %d day. Calendar gap detected — wait %d more days (cron skipped a day).\n",
			streak, remaining)
	default:
		_, _ = fmt.Fprintf(w, "Last clean streak: %d day. No drift detected. Wait %d more days for streak to mature.\n",
			streak, remaining)
	}
}

// anyDirty reports whether any row has Clean=false (real drift, errored
// domain, or a corrupt/missing-file row that ReadDaily flagged via
// ParseErr). Used by PrintReport to distinguish "resolve drift" from
// "streak just maturing" — both states share the FAIL headline but mean
// different operator actions.
func anyDirty(rows []Row) bool {
	for _, r := range rows {
		if !r.Clean {
			return true
		}
	}
	return false
}

// PrintDailyRow renders one Row into the per-day breakdown. Extracted
// from PrintReport so the outer loop stays under gocognit ≤15 and the
// gap-break annotation logic lives next to the rest of the per-row
// formatting decisions.
func PrintDailyRow(w io.Writer, rows []Row, index int, r Row, gapBreakIndex int) {
	date := r.Date.Format(dailyFileDateFormat)
	if r.Date.IsZero() {
		date = filepath.Base(r.Path)
	}
	switch {
	case r.ParseErr != nil:
		_, _ = fmt.Fprintf(w, "  %s: parse error (%v)\n", date, r.ParseErr)
	case index == gapBreakIndex:
		// gapBreakIndex is always > 0 when set (the gap lives between
		// rows[i-1] and rows[i]); the row itself is clean, but the
		// calendar gap before it disqualified the streak.
		prev := rows[index-1].Date.Format(dailyFileDateFormat)
		delta := rows[index-1].Date.Sub(r.Date)
		_, _ = fmt.Fprintf(w, "  %s: gap-break (delta=%v exceeds %v since %s)\n",
			date, delta, MaxCalendarGap, prev)
	case r.Clean:
		_, _ = fmt.Fprintf(w, "  %s: clean (%d domains, 0 drift)\n", date, r.DomainCount)
	default:
		_, _ = fmt.Fprintf(w, "  %s: %d domain(s) drift (%v)\n", date, r.DriftCount, r.DriftTags)
	}
}
