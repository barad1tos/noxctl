// Package bear_test orphan-family detector tests — exercise
// audit.AggregateOrphanFamiliesFromJSON (the exported test-seam over
// aggregateOrphanFamilies), driving the pure detector with realistic
// bearcli-shaped JSON fixtures so the tests run without bearcli
// installed.
//
// Test-seam rationale matches untracked_test.go: external tests at
// tests/bear/ build a separate test binary and cannot reach in-package
// unexported symbols. AggregateOrphanFamiliesFromJSON is the
// production-side seam (precedent at audit.AggregateUntrackedFromJSON).
package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/audit"
)

// orphanFixture mirrors the bearcli `list --fields id,title,tags` JSON
// shape used by ScanUntracked + the orphan-family detector. Local
// alias of the cross-test noteFixture keeps each test file self-
// describing about its fixture columns; mustMarshalNotes + managedSet
// (declared in untracked_test.go, same package bear_test) are reused
// directly — no parallel helper infrastructure.
type orphanFixture = noteFixture

// TestAggregateOrphanFamilies_StrayTagDetected covers truth (1) from the
// 13-CONTEXT.md specifics block: an atom with a managed tag plus a
// stray-family tag produces exactly one finding for that stray.
func TestAggregateOrphanFamilies_StrayTagDetected(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-1",
		Title: "Stray tag carrier",
		Tags:  []string{"#llm/tips", "#quicknotes/daily"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got, want := len(findings), 1; got != want {
		t.Fatalf("findings len = %d, want %d (%v)", got, want, findings)
	}
	got := findings[0]
	if got.Category != audit.LintOrphanFamily {
		t.Fatalf("Category = %q, want %q", got.Category, audit.LintOrphanFamily)
	}
	if got.NoteID != "note-1" {
		t.Fatalf("NoteID = %q, want %q", got.NoteID, "note-1")
	}
	if got.Title != "Stray tag carrier" {
		t.Fatalf("Title = %q, want %q", got.Title, "Stray tag carrier")
	}
	if got.DomainTag != "" {
		t.Fatalf("DomainTag = %q, want empty (corpus-level finding)", got.DomainTag)
	}
	if !got.Fixable {
		t.Fatalf("Fixable = false, want true (apply step adds #orphans via bearcli)")
	}
	for _, frag := range []string{"#quicknotes/daily", "quicknotes", "tag-as-orphans candidate"} {
		if !strings.Contains(got.Detail, frag) {
			t.Fatalf("Detail must contain %q; got %q", frag, got.Detail)
		}
	}
}

// TestAggregateOrphanFamilies_AlreadyTaggedOrphans_Skipped covers truth
// (2): an atom already carrying #orphans (with or without sub-tag)
// produces zero findings — idempotency contract that lets `noxctl lint
// --apply` run repeatedly without re-firing on already-triaged atoms.
func TestAggregateOrphanFamilies_AlreadyTaggedOrphans_Skipped(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-2",
		Title: "Already orphaned",
		Tags:  []string{"#llm/tips", "#quicknotes/daily", "#orphans"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (already-tagged atom is idempotent skip); got %v",
			got, findings)
	}
}

// TestAggregateOrphanFamilies_AlreadyTaggedOrphansSub_Skipped extends
// the idempotency contract to the `#orphans/<sub>` form so an operator
// who later sub-categorizes the orphan bucket (e.g. `#orphans/quicknotes`)
// does not get the atom re-flagged on the next lint sweep.
func TestAggregateOrphanFamilies_AlreadyTaggedOrphansSub_Skipped(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-3",
		Title: "Sub-orphaned",
		Tags:  []string{"#quicknotes/daily", "#orphans/quicknotes"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (#orphans/<sub> also counts as already-tagged); got %v",
			got, findings)
	}
}

// TestAggregateOrphanFamilies_ManagedFamiliesOnly_NoFindings covers
// truth (3): when every tag's family root is in the managed set, the
// detector emits zero findings. Negative path for SC-06 (managed-only
// corpus → no false positives).
func TestAggregateOrphanFamilies_ManagedFamiliesOnly_NoFindings(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-4",
		Title: "All managed",
		Tags:  []string{"#work/tasks", "#llm/tips"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("work", "llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (all family roots managed); got %v",
			got, findings)
	}
}

// TestAggregateOrphanFamilies_DeepTag_FirstSegmentFamily covers truth
// (4) per the planner's clarification: depth-3 tags (`#X/Y/Z`) still
// participate — family extraction uses segment-before-first-`/`, so
// family is `X`. If `X` is NOT managed, the atom is an orphan
// regardless of how deep the sub-segments go.
func TestAggregateOrphanFamilies_DeepTag_FirstSegmentFamily(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-5",
		Title: "Deep tag carrier",
		Tags:  []string{"#scratch/area/temp"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got, want := len(findings), 1; got != want {
		t.Fatalf("findings len = %d, want %d (deep tag still classified by first segment); got %v",
			got, want, findings)
	}
	got := findings[0]
	for _, frag := range []string{"#scratch/area/temp", "scratch"} {
		if !strings.Contains(got.Detail, frag) {
			t.Fatalf("Detail must contain %q (family = first segment); got %q", frag, got.Detail)
		}
	}
}

// TestAggregateOrphanFamilies_BareTopLevel_Ignored covers truth (5):
// bare top-level tags (no `/`) are NOT in scope for orphan-family
// detection — that concern belongs to LintUntracked. The detector
// fires only for the `#<family>/<sub>` shape.
func TestAggregateOrphanFamilies_BareTopLevel_Ignored(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-6",
		Title: "Bare top-level only",
		Tags:  []string{"#randomthing"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet(),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (bare top-level out of scope; LintUntracked handles it); got %v",
			got, findings)
	}
}

// TestAggregateOrphanFamilies_MultipleStrayTags_SingleFindingWithJoinedDetail
// covers truth (6) per CONTEXT.md decision (d.2): when an atom carries
// multiple stray-family tags, the detector emits ONE finding (per atom)
// with Detail comma-joining all strays. One #orphans tag will be added
// at apply time regardless of how many strays the atom carries.
func TestAggregateOrphanFamilies_MultipleStrayTags_SingleFindingWithJoinedDetail(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-7",
		Title: "Multi-stray",
		Tags:  []string{"#quicknotes/daily", "#scratch/temp"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet(),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got, want := len(findings), 1; got != want {
		t.Fatalf("findings len = %d, want %d (one per atom, not per stray tag); got %v",
			got, want, findings)
	}
	got := findings[0]
	for _, frag := range []string{
		"#quicknotes/daily", "#scratch/temp",
		"quicknotes", "scratch",
		"tag-as-orphans candidate",
	} {
		if !strings.Contains(got.Detail, frag) {
			t.Fatalf("Detail must contain %q (comma-joined multi-stray context); got %q",
				frag, got.Detail)
		}
	}
}

// TestAggregateOrphanFamilies_Reproducer_LLMTipsWithQuicknotesDaily is
// the live reproducer from 13-CONTEXT.md: the "System Prompt For Coding
// Agents" atom must be detected when managed = {llm}. Wire-shape
// equivalence to a real bearcli payload.
func TestAggregateOrphanFamilies_Reproducer_LLMTipsWithQuicknotesDaily(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-systemprompt",
		Title: "System Prompt For Coding Agents",
		Tags:  []string{"#llm/tips", "#quicknotes/daily"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got, want := len(findings), 1; got != want {
		t.Fatalf("reproducer findings len = %d, want %d", got, want)
	}
	got := findings[0]
	if got.NoteID != "note-systemprompt" {
		t.Fatalf("NoteID = %q, want %q", got.NoteID, "note-systemprompt")
	}
	if got.Title != "System Prompt For Coding Agents" {
		t.Fatalf("Title = %q, want %q", got.Title, "System Prompt For Coding Agents")
	}
	for _, frag := range []string{"#quicknotes/daily", "quicknotes"} {
		if !strings.Contains(got.Detail, frag) {
			t.Fatalf("Detail must contain %q (reproducer context); got %q", frag, got.Detail)
		}
	}
}

// TestAggregateOrphanFamilies_ParseError covers the JSON-seam error
// path: malformed input returns nil + wrapped error mirroring
// AggregateUntrackedFromJSON's contract.
func TestAggregateOrphanFamilies_ParseError(t *testing.T) {
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		[]byte("not-json"), managedSet(),
	)
	if err == nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON on bad input must return error; got nil + %v", findings)
	}
	if !strings.Contains(err.Error(), "AggregateOrphanFamiliesFromJSON") {
		t.Fatalf("error must wrap with helper name for traceability; got %v", err)
	}
}
