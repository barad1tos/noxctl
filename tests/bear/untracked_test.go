// Package bear_test residue tests — exercises domain.AggregateUntrackedFromJSON
// (the exported test-seam over aggregateUntracked), driving the pure
// aggregation logic with realistic bearcli-shaped JSON fixtures so the
// tests run without bearcli installed.
//
// Test-seam rationale: bear/untracked.go's aggregateUntracked is unexported
// and operates on the unexported autoTagNote shape. External tests at
// tests/bear/ build a separate test binary and cannot reach in-package
// symbols (precedent: bear/engine/export_test.go documents this same
// failure mode; re-confirmed). The production-side seam
// AggregateUntrackedFromJSON keeps fixture construction faithful to the
// real wire format (bearcli list --format json).
package bear_test

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
)

// noteFixture mirrors the bearcli `list --fields id,title,tags` JSON
// shape — id and title are not used by aggregation, but emitting the
// full shape keeps fixtures one Marshal away from real bearcli output.
type noteFixture struct {
	ID    string   `json:"id"`
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}

func mustMarshalNotes(t *testing.T, notes []noteFixture) []byte {
	t.Helper()
	out, err := json.Marshal(notes)
	if err != nil {
		t.Fatalf("marshal fixture: %v", err)
	}
	return out
}

func managedSet(roots ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(roots))
	for _, r := range roots {
		m[r] = struct{}{}
	}
	return m
}

// twoFamilyFixture is the shared fixture for Tests 1 + 2 — two notes
// under library/poetry plus one under claude/sessions. Centralizing it
// keeps the dupl threshold happy and ensures the all-managed and
// nothing-managed cases run against the same input shape.
func twoFamilyFixture() []noteFixture {
	return []noteFixture{
		{ID: "1", Title: "n1", Tags: []string{"library/poetry"}},
		{ID: "2", Title: "n2", Tags: []string{"library/poetry"}},
		{ID: "3", Title: "n3", Tags: []string{"claude/sessions"}},
	}
}

// TestScanUntrackedEmptyDomainsLeavesEverythingUntracked covers truth (1):
// an empty managed-roots set surfaces every distinct tag as untracked,
// each at its most-specific form, with the per-tag count equal to the
// number of fixture notes carrying that tag.
func TestScanUntrackedEmptyDomainsLeavesEverythingUntracked(t *testing.T) {
	report, err := domain.AggregateUntrackedFromJSON(
		mustMarshalNotes(t, twoFamilyFixture()), managedSet(),
	)
	if err != nil {
		t.Fatalf("AggregateUntrackedFromJSON: %v", err)
	}
	if got, want := len(report.TagFamilies), 2; got != want {
		t.Fatalf("TagFamilies len = %d, want %d (got %v)", got, want, report.TagFamilies)
	}
	if got, want := report.TagFamilies[0].Tag, "claude/sessions"; got != want {
		t.Fatalf("TagFamilies[0].Tag = %q, want %q (alphabetical sort)", got, want)
	}
	if got, want := report.TagFamilies[0].NoteCount, 1; got != want {
		t.Fatalf("claude/sessions NoteCount = %d, want %d", got, want)
	}
	if got, want := report.TagFamilies[1].Tag, "library/poetry"; got != want {
		t.Fatalf("TagFamilies[1].Tag = %q, want %q (alphabetical sort)", got, want)
	}
	if got, want := report.TagFamilies[1].NoteCount, 2; got != want {
		t.Fatalf("library/poetry NoteCount = %d, want %d", got, want)
	}
	if got, want := report.TotalNotes, 3; got != want {
		t.Fatalf("TotalNotes = %d, want %d", got, want)
	}
}

// TestScanUntrackedAllManagedReturnsEmpty covers truth (2): every
// fixture tag's top-level segment is in the managed set, so the
// report is empty. Crucially TagFamilies must be `[]` not `null` —
// JSON marshaling must not emit "null" so downstream tooling can
// rely on the array shape.
func TestScanUntrackedAllManagedReturnsEmpty(t *testing.T) {
	report, err := domain.AggregateUntrackedFromJSON(
		mustMarshalNotes(t, twoFamilyFixture()), managedSet("library", "claude"),
	)
	if err != nil {
		t.Fatalf("AggregateUntrackedFromJSON: %v", err)
	}
	if got := len(report.TagFamilies); got != 0 {
		t.Fatalf("TagFamilies must be empty when every tag root is managed; got %d (%v)",
			got, report.TagFamilies)
	}
	if got := report.TotalNotes; got != 0 {
		t.Fatalf("TotalNotes must be 0 when nothing is untracked; got %d", got)
	}
	// Cross-check: the JSON marshal must NOT contain "null" — the
	// engine boundary depends on `[]` for TagFamilies.
	out, err := json.Marshal(report)
	if err != nil {
		t.Fatalf("marshal report: %v", err)
	}
	if strings.Contains(string(out), "null") {
		t.Fatalf("marshaled report must not contain \"null\" (got %s); empty TagFamilies must serialize as []",
			string(out))
	}
}

// TestScanUntrackedMixedTagsCountsBothScopes covers truth (3): a note
// with tags ["library/poetry","claude/sessions","dev/notes"] under
// managed=["library"] contributes one count to claude/sessions AND one
// count to dev/notes — the managed-side library/poetry is excluded
// (handled by the per-domain diff elsewhere), the residue side picks up
// every other tag.
func TestScanUntrackedMixedTagsCountsBothScopes(t *testing.T) {
	notes := []noteFixture{
		{ID: "1", Title: "n1", Tags: []string{
			"library/poetry", "claude/sessions", "dev/notes",
		}},
	}
	report, err := domain.AggregateUntrackedFromJSON(mustMarshalNotes(t, notes), managedSet("library"))
	if err != nil {
		t.Fatalf("AggregateUntrackedFromJSON: %v", err)
	}
	if got, want := len(report.TagFamilies), 2; got != want {
		t.Fatalf("TagFamilies len = %d, want %d (%v)", got, want, report.TagFamilies)
	}
	wantCounts := map[string]int{"claude/sessions": 1, "dev/notes": 1}
	for _, fam := range report.TagFamilies {
		want, ok := wantCounts[fam.Tag]
		if !ok {
			t.Fatalf("unexpected family %q in report (managed root \"library\" should have excluded library/poetry)",
				fam.Tag)
		}
		if fam.NoteCount != want {
			t.Fatalf("family %q NoteCount = %d, want %d", fam.Tag, fam.NoteCount, want)
		}
	}
	if got, want := report.TotalNotes, 2; got != want {
		t.Fatalf("TotalNotes = %d, want %d (one note × two unmanaged tags)", got, want)
	}
}

// TestScanUntrackedSortStability covers truth (4): with 50+ tag
// families assembled from a deliberately-out-of-order fixture stream,
// the report's TagFamilies must come out sorted ascending by.Tag.
// Stability is the contract for downstream JSON consumers and TTY
// renderers that depend on a deterministic family order.
func TestScanUntrackedSortStability(t *testing.T) {
	const families = 60
	notes := make([]noteFixture, 0, families)
	// Construct tags in reverse-alphabetical order to deliberately
	// stress the aggregator's sort step. Each tag is unique so
	// per-family counts are 1.
	for i := families - 1; i >= 0; i-- {
		tag := fmt.Sprintf("zone-%03d/sub", i)
		notes = append(notes, noteFixture{
			ID:    fmt.Sprintf("id-%d", i),
			Title: fmt.Sprintf("note-%d", i),
			Tags:  []string{tag},
		})
	}
	report, err := domain.AggregateUntrackedFromJSON(mustMarshalNotes(t, notes), managedSet())
	if err != nil {
		t.Fatalf("AggregateUntrackedFromJSON: %v", err)
	}
	if got, want := len(report.TagFamilies), families; got != want {
		t.Fatalf("TagFamilies len = %d, want %d", got, want)
	}
	sortedAscending := sort.SliceIsSorted(report.TagFamilies, func(i, j int) bool {
		return report.TagFamilies[i].Tag < report.TagFamilies[j].Tag
	})
	if !sortedAscending {
		t.Fatalf("TagFamilies must be sorted ascending by .Tag; got %v",
			tagsOf(report.TagFamilies))
	}
}

// TestScanUntrackedSpecificTagPathRecording covers truth (5): a tag
// like "claude/sessions/2026-05-10" stays at its most-specific form
// in the report — the aggregator must NOT collapse it to the
// top-level "claude". Preserving hierarchy is the operator-facing
// affordance ("record at the most-specific tag") and is what makes
// the residue section actionable: the user sees which exact sub-tag
// was missed.
func TestScanUntrackedSpecificTagPathRecording(t *testing.T) {
	notes := []noteFixture{
		{ID: "1", Title: "n1", Tags: []string{"claude/sessions/2026-05-10"}},
	}
	report, err := domain.AggregateUntrackedFromJSON(mustMarshalNotes(t, notes), managedSet())
	if err != nil {
		t.Fatalf("AggregateUntrackedFromJSON: %v", err)
	}
	if got, want := len(report.TagFamilies), 1; got != want {
		t.Fatalf("TagFamilies len = %d, want %d (%v)", got, want, report.TagFamilies)
	}
	if got, want := report.TagFamilies[0].Tag, "claude/sessions/2026-05-10"; got != want {
		t.Fatalf("TagFamilies[0].Tag = %q, want %q (must NOT collapse to \"claude\")", got, want)
	}
	if got, want := report.TagFamilies[0].NoteCount, 1; got != want {
		t.Fatalf("NoteCount = %d, want %d", got, want)
	}
}

// tagsOf is a debug helper for failure messages — pulls just the tag
// names so test output stays readable on a 60-family report.
func tagsOf(families []domain.UntrackedFamily) []string {
	out := make([]string, 0, len(families))
	for _, fam := range families {
		out = append(out, fam.Tag)
	}
	return out
}
