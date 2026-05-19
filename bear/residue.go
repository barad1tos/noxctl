// Package bear residue scanner — corpus-level walk that surfaces tags
// outside the closed catalog of TOML-managed domains. Used by the
// plan engine (bear/engine/plan.go via wiring) to populate
// the `⚠ Untracked` section of plan output.
//
// Lives in a NEW peer file (NOT bear/lint.go) because lint.go is at
// its gocognit/dupl ceiling; this scanner has a different shape
// (corpus-level aggregate, not per-atom) and would tip lint.go's
// complexity budget.
//
// The bearcli list call replicates bear/foreign_tag.go:103-115 — same
// field set, same response shape — but the two are NOT shared at
// runtime: foreign-tag is an apply pre-pass; plan does NOT run
// pre-passes; runtime overlap is impossible.
package bear

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// UntrackedFamily is one tag-family entry in the residue report.
// Wire-tags match the engine.UntrackedFamily shape declared in
// bear/engine/plan_result.go. wires the
// boundary translation at engine.Plan to avoid an import cycle:
// bear/engine imports bear (for *bear.Domain), so bear/residue.go
// cannot import bear/engine for the report type.
type UntrackedFamily struct {
	Tag       string `json:"tag"`
	NoteCount int    `json:"note_count"`
}

// UntrackedReport groups untracked notes by tag, with a corpus-level
// total. Empty TagFamilies is `[]UntrackedFamily{}` (never nil) so
// JSON serialization at the engine boundary never emits null.
type UntrackedReport struct {
	TagFamilies []UntrackedFamily `json:"tag_families"`
	TotalNotes  int               `json:"total_notes"`
}

// ScanUntracked walks every note in the `notes` location, groups by
// the most-specific tag, and reports the families whose top-level
// segment is NOT covered by any of the supplied managed domains.
// Returns per-family note counts, sorted alphabetically by tag.
//
// The set of "managed top-level segments" is computed from
// domains[].Tag — for tag "library/poetry", the segment is
// "library". Multiple domains sharing the same root (umbrella +
// children) collapse to one managed segment.
//
// Notes carrying multiple tags contribute to MULTIPLE families if
// none of their tag-roots are managed. Each tag is recorded at its
// MOST-SPECIFIC form (e.g. "claude/sessions/2026-05-10" stays as
// "claude/sessions/2026-05-10", NOT collapsed to "claude") —
// preserves the operator's hierarchy preference.
//
// Read-only: never writes to bearcli; never mutates any input. The
// scan is info-only and never contributes to the plan exit-code.
func ScanUntracked(ctx context.Context, domains []*Domain) (UntrackedReport, error) {
	managed := managedRoots(domains)

	out, err := runBearcli(ctx,
		[]string{"list", "--location", "notes", flagFormat, formatJSON, flagFields, "id,title,tags"},
		"")
	if err != nil {
		return emptyUntrackedReport(), fmt.Errorf("ScanUntracked list: %w", err)
	}

	var notes []autoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return emptyUntrackedReport(), fmt.Errorf("ScanUntracked parse: %w", err)
	}

	return aggregateUntracked(notes, managed), nil
}

// AggregateUntrackedFromJSON is the test-only seam over aggregateUntracked:
// it accepts a bearcli-shaped JSON payload (`[{id,title,tags},...]`) and
// the same managed-roots map ScanUntracked computes, then runs the pure
// aggregation logic. Exposed in production code (rather than via an
// in-package _test.go) because external tests at tests/bear/ build a
// separate test binary and cannot reach in-package _test.go symbols —
// the precedent is documented at bear/engine/export_test.go. Returns
// the same JSON-stable empty report on parse error that ScanUntracked
// uses on bearcli error.
func AggregateUntrackedFromJSON(jsonBytes []byte, managed map[string]struct{}) (UntrackedReport, error) {
	var notes []autoTagNote
	if err := json.Unmarshal(jsonBytes, &notes); err != nil {
		return emptyUntrackedReport(), fmt.Errorf("AggregateUntrackedFromJSON parse: %w", err)
	}
	return aggregateUntracked(notes, managed), nil
}

// managedRoots collects the unique top-level tag segments the supplied
// domains cover. Nil-tagged or zero-value domains are skipped silently
// (defensive against partially-constructed catalogs).
func managedRoots(domains []*Domain) map[string]struct{} {
	roots := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		if d == nil || d.Tag == "" {
			continue
		}
		roots[topLevelSegment(d.Tag)] = struct{}{}
	}
	return roots
}

// aggregateUntracked is the pure logic core of ScanUntracked, factored
// out so AggregateUntrackedFromJSON can drive it from tests without
// hitting bearcli. Counts each tag at its most-specific form, then
// sorts the resulting families alphabetically — never depend on Go
// map iteration order downstream.
//
// Splits ScanUntracked at the I/O boundary: ScanUntracked owns
// bearcli, aggregateUntracked owns the data transformation. Both
// hold under gocognit ≤ 15 individually.
func aggregateUntracked(notes []autoTagNote, managed map[string]struct{}) UntrackedReport {
	perFamily := make(map[string]int)
	for _, n := range notes {
		for _, tag := range n.Tags {
			if tag == "" {
				continue
			}
			if _, ok := managed[topLevelSegment(tag)]; ok {
				continue
			}
			perFamily[tag]++
		}
	}
	families := make([]UntrackedFamily, 0, len(perFamily))
	total := 0
	for tag, count := range perFamily {
		families = append(families, UntrackedFamily{Tag: tag, NoteCount: count})
		total += count
	}
	sort.Slice(families, func(i, j int) bool {
		return families[i].Tag < families[j].Tag
	})
	return UntrackedReport{TagFamilies: families, TotalNotes: total}
}

// emptyUntrackedReport returns the canonical zero-value report —
// non-nil empty TagFamilies plus zero TotalNotes. Used on every error
// return so JSON serialization at the engine boundary never emits
// null arrays.
func emptyUntrackedReport() UntrackedReport {
	return UntrackedReport{TagFamilies: make([]UntrackedFamily, 0), TotalNotes: 0}
}
