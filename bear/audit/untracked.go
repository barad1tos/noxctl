package audit

// untracked.go is a corpus-level scanner — walks the corpus and
// surfaces tags outside the closed catalog of TOML-managed domains.
// Used by the plan engine (bear/engine/plan.go via wiring) to
// populate the `⚠ Untracked` section of plan output.
//
// The bearcli list call replicates bear/fastpass/foreigntag.go —
// same field set, same response shape — but the two are NOT shared
// at runtime: foreign-tag is an apply pre-pass; plan does NOT run
// pre-passes; runtime overlap is impossible.

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// UntrackedFamily is one tag-family entry in the residue report.
// Wire-tags match the engine.UntrackedFamily shape declared in
// bear/engine/plan_result.go. wires the
// boundary translation at engine.Plan to avoid an import cycle:
// bear/engine imports bear (for *bear.Domain), so bear/untracked.go
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
func ScanUntracked(ctx context.Context, domains []*domain.Domain) (UntrackedReport, error) {
	managed := ManagedRootsFromDomains(domains)

	out, err := bearcli.Run(ctx,
		[]string{
			"list", "--location", "notes",
			bearcli.FlagFormat, bearcli.FormatJSON,
			bearcli.FlagFields, "id,title,tags",
		},
		"")
	if err != nil {
		return emptyUntrackedReport(), fmt.Errorf("ScanUntracked list: %w", err)
	}

	var notes []domain.AutoTagNote
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
	var notes []domain.AutoTagNote
	if err := json.Unmarshal(jsonBytes, &notes); err != nil {
		return emptyUntrackedReport(), fmt.Errorf("AggregateUntrackedFromJSON parse: %w", err)
	}
	return aggregateUntracked(notes, managed), nil
}

// ManagedRootsFromDomains is the SSOT for "which tag families are
// catalog-managed": it collects the unique top-level tag segments the
// supplied domains cover. Consumed by both ScanUntracked (this file)
// and the orphan-family detector (bear/audit/orphans.go) — keeping the
// derivation in one exported helper prevents the two corpus-level
// scanners from drifting on what counts as a "managed family".
//
// Nil-tagged or zero-value domains are skipped silently (defensive
// against partially-constructed catalogs).
func ManagedRootsFromDomains(domains []*domain.Domain) map[string]struct{} {
	roots := make(map[string]struct{}, len(domains))
	for _, d := range domains {
		if d == nil || d.Tag == "" {
			continue
		}
		roots[domain.TopLevelSegment(d.Tag)] = struct{}{}
	}
	return roots
}

// aggregateUntracked is the pure logic core of ScanUntracked, factored
// out so domain.AggregateUntrackedFromJSON can drive it from tests without
// hitting bearcli. Counts each tag at its most-specific form, then
// sorts the resulting families alphabetically — never depend on Go
// map iteration order downstream.
//
// Splits ScanUntracked at the I/O boundary: ScanUntracked owns
// bearcli, aggregateUntracked owns the data transformation. Both
// hold under gocognit ≤ 15 individually.
func aggregateUntracked(notes []domain.AutoTagNote, managed map[string]struct{}) UntrackedReport {
	perFamily := make(map[string]int)
	for _, n := range notes {
		for _, tag := range n.Tags {
			if tag == "" {
				continue
			}
			if _, ok := managed[domain.TopLevelSegment(tag)]; ok {
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
