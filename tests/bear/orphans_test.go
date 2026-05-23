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
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
)

// orphanFakeBearcli is a minimal bearcli.Backend fake for the corpus-
// level orchestrator tests in this file. It returns a canned payload
// for `list` verbs, records every `tag` call in callsTag, and lets
// each test override the list-side response (payload or error) plus
// the per-noteID tag-side response (error or success).
//
// Mirrors the autotag fake pattern at tests/bear/autotag_test.go —
// same Backend.Run signature, same context-stamping seam via
// domain.ContextWithBackend — so future maintainers find the fixture
// shape via the same grep.
type orphanFakeBearcli struct {
	listPayload []byte
	listErr     error
	// tagErrByNoteID maps noteID → error to return on AddTag for that
	// note. Missing key means success. Used to simulate partial-failure
	// runs without a global "always fail" toggle.
	tagErrByNoteID map[string]error

	mu       sync.Mutex
	callsTag []orphanTagCall
}

type orphanTagCall struct {
	NoteID string
	Tag    string
}

func (f *orphanFakeBearcli) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("orphanFakeBearcli: empty args")
	}
	switch args[0] {
	case "list":
		if f.listErr != nil {
			return nil, f.listErr
		}
		return f.listPayload, nil
	case "tag":
		// args = ["tag", noteID, tag]
		if len(args) < 3 {
			return nil, errors.New("orphanFakeBearcli: tag requires noteID + tag")
		}
		f.mu.Lock()
		f.callsTag = append(f.callsTag, orphanTagCall{NoteID: args[1], Tag: args[2]})
		f.mu.Unlock()
		if f.tagErrByNoteID != nil {
			if err, ok := f.tagErrByNoteID[args[1]]; ok {
				return nil, err
			}
		}
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

// armOrphanFakeBearcli installs the supplied fake backend on a fresh
// context (with bearcli pool armed at cap=2 so corpus calls actually
// execute) and registers cleanup. Returns the test context — pass it
// into the orchestrator under test.
func armOrphanFakeBearcli(t *testing.T, fake *orphanFakeBearcli) context.Context {
	t.Helper()
	domain.ResetBearcliPoolForTest(2)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })
	return domain.ContextWithBackend(t.Context(), fake)
}

// orphanFixture mirrors the bearcli `list --fields id,title,tags` JSON
// shape used by ScanUntracked + the orphan-family detector. Local
// alias of the cross-test noteFixture keeps each test file self-
// describing about its fixture columns; mustMarshalNotes + managedSet
// (declared in untracked_test.go, same package bear_test) are reused
// directly — no parallel helper infrastructure.
type orphanFixture = noteFixture

// assertDetailContains is the shared substring assertion: every case
// that checks Detail tokens uses it so the per-case body stays under
// the dupl token threshold (≥50 tokens). Naming the case-specific
// context via `ctx` keeps failure messages actionable without inlining
// the 5-line for-range-Fatalf block in each test.
func assertDetailContains(t *testing.T, ctx, detail string, fragments []string) {
	t.Helper()
	for _, frag := range fragments {
		if !strings.Contains(detail, frag) {
			t.Fatalf("Detail must contain %q (%s); got %q", frag, ctx, detail)
		}
	}
}

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
	assertDetailContains(t, "stray-tag detection",
		got.Detail,
		[]string{"#quicknotes/daily", "quicknotes", "tag-as-orphans candidate"})
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
	assertDetailContains(t, "family = first segment",
		got.Detail,
		[]string{"#scratch/area/temp", "scratch"})
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
	assertDetailContains(t, "comma-joined multi-stray context",
		got.Detail,
		[]string{
			"#quicknotes/daily", "#scratch/temp",
			"quicknotes", "scratch",
			"tag-as-orphans candidate",
		})
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
	assertDetailContains(t, "reproducer context",
		got.Detail,
		[]string{"#quicknotes/daily", "quicknotes"})
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

// ---------------------------------------------------------------------
// Corpus-level orchestrator tests (ScanOrphanFamilies +
// ApplyOrphanFamilies). These exercise the production-side I/O wrappers
// that the CLI integration tests in tests/bear/cli/lint/ depend on, but
// at finer granularity — error paths that the integration tests cannot
// easily exercise via the cli.RunLint surface.
// ---------------------------------------------------------------------

// TestScanOrphanFamilies_BearcliError_WrappedReturn pins the
// "bearcli list failed" error path: the corpus scan returns nil
// findings + a wrapped error whose message identifies the helper. No
// silent partial-success — corpus-level scans can't degrade the way
// per-domain Scan can, because there is no per-domain fallback to fall
// back to.
func TestScanOrphanFamilies_BearcliError_WrappedReturn(t *testing.T) {
	fake := &orphanFakeBearcli{listErr: errors.New("bearcli boom")}
	ctx := armOrphanFakeBearcli(t, fake)

	findings, err := audit.ScanOrphanFamilies(ctx, nil)
	if err == nil {
		t.Fatalf("ScanOrphanFamilies on bearcli error must return error; got nil + %v", findings)
	}
	if findings != nil {
		t.Fatalf("ScanOrphanFamilies on error must return nil findings; got %v", findings)
	}
	if !strings.Contains(err.Error(), "ScanOrphanFamilies list") {
		t.Fatalf("error must wrap with helper name for traceability; got %v", err)
	}
}

// TestScanOrphanFamilies_MalformedJSON_WrappedReturn pins the JSON
// parse failure path: bearcli returns 200 OK with garbage bytes (e.g.
// CLI version drift, stderr-on-stdout). Scan returns nil findings +
// wrapped error matching the helper name.
func TestScanOrphanFamilies_MalformedJSON_WrappedReturn(t *testing.T) {
	fake := &orphanFakeBearcli{listPayload: []byte("not-json")}
	ctx := armOrphanFakeBearcli(t, fake)

	findings, err := audit.ScanOrphanFamilies(ctx, nil)
	if err == nil {
		t.Fatalf("ScanOrphanFamilies on malformed JSON must return error; got nil + %v", findings)
	}
	if findings != nil {
		t.Fatalf("ScanOrphanFamilies on parse error must return nil findings; got %v", findings)
	}
	if !strings.Contains(err.Error(), "ScanOrphanFamilies parse") {
		t.Fatalf("error must wrap with helper name for traceability; got %v", err)
	}
}

// TestApplyOrphanFamilies_PartialFailure_Counts pins the log-and-
// continue contract: one tag fails, one succeeds, the function does NOT
// abort on first failure and returns (tagged=1, failed=1). The order
// of findings drives which atom fails — second finding's noteID is
// rigged to error.
func TestApplyOrphanFamilies_PartialFailure_Counts(t *testing.T) {
	fake := &orphanFakeBearcli{
		tagErrByNoteID: map[string]error{
			"note-fails": errors.New("bearcli tag boom"),
		},
	}
	ctx := armOrphanFakeBearcli(t, fake)

	findings := []audit.Finding{
		{
			NoteID:   "note-ok",
			Title:    "First (ok)",
			Category: audit.LintOrphanFamily,
		},
		{
			NoteID:   "note-fails",
			Title:    "Second (fails)",
			Category: audit.LintOrphanFamily,
		},
	}
	tagged, failed := audit.ApplyOrphanFamilies(ctx, findings)
	if tagged != 1 {
		t.Errorf("tagged = %d, want 1", tagged)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}
	// Both atoms must have had a tag call attempted — proves no
	// short-circuit on first failure.
	if got := len(fake.callsTag); got != 2 {
		t.Fatalf("AddTag attempted %d times, want 2 (no abort on first failure); calls=%v",
			got, fake.callsTag)
	}
	assertTagCall(t, "first", fake.callsTag[0], "note-ok", "orphans")
	assertTagCall(t, "second", fake.callsTag[1], "note-fails", "orphans")
}

// assertTagCall fails the test if the recorded AddTag call's
// (NoteID, Tag) tuple does not match the expected pair. Extracted so a
// run of per-call assertions stays under the dupl token threshold.
func assertTagCall(t *testing.T, label string, got orphanTagCall, wantID, wantTag string) {
	t.Helper()
	if got.NoteID != wantID || got.Tag != wantTag {
		t.Errorf("%s AddTag = %+v, want {NoteID:%s, Tag:%s}", label, got, wantID, wantTag)
	}
}
