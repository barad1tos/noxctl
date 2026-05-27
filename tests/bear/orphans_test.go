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

	mu        sync.Mutex
	callsList [][]string
	callsTag  []orphanTagCall
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
		f.mu.Lock()
		f.callsList = append(f.callsList, append([]string(nil), args...))
		f.mu.Unlock()
		if f.listErr != nil {
			return nil, f.listErr
		}
		return f.listPayload, nil
	case "tags":
		// args = ["tags", "add", noteID, tag]
		if len(args) < 4 || args[1] != "add" {
			return nil, errors.New("orphanFakeBearcli: tags requires [add noteID tag]")
		}
		f.mu.Lock()
		f.callsTag = append(f.callsTag, orphanTagCall{NoteID: args[2], Tag: args[3]})
		f.mu.Unlock()
		if f.tagErrByNoteID != nil {
			if err, ok := f.tagErrByNoteID[args[2]]; ok {
				return nil, err
			}
		}
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

func (f *orphanFakeBearcli) sawListFields(fields string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, args := range f.callsList {
		if argsValueAfter(args, "--fields") == fields {
			return true
		}
	}
	return false
}

func argsValueAfter(args []string, key string) string {
	for index, arg := range args {
		if arg == key && index+1 < len(args) {
			return args[index+1]
		}
	}
	return ""
}

// armOrphanFakeBearcli installs the supplied fake backend on a fresh
// context (with bearcli pool armed at cap=2 so corpus calls actually
// execute) and registers cleanup that resets capacity to 1. Sibling
// of armBearcliPool in tests/bear/cli/lint/lint_test.go — the
// cleanup-to-1 sentinel matches that helper's rationale: a missing
// arm in a later test shows up as a deterministic block rather than
// spurious concurrency surprises. Returns the test context — pass it
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

// assertDetailContains asserts every fragment is present in detail,
// failing fast with the missing one and the supplied label. The label
// scopes the failure message to the calling test case so a multi-case
// failure points at the right scenario without a stack hunt.
func assertDetailContains(t *testing.T, label, detail string, fragments []string) {
	t.Helper()
	for _, frag := range fragments {
		if !strings.Contains(detail, frag) {
			t.Fatalf("Detail must contain %q (%s); got %q", frag, label, detail)
		}
	}
}

// TestAggregateOrphanFamilies_StrayTagDetected pins the core detection
// contract: an atom with a managed tag plus a stray-family tag produces
// exactly one finding for that stray.
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

func TestAggregateOrphanFamilies_DuplicateTitleTagDoesNotSkip(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-dup-title",
		Title: "Duplicate triage only",
		Tags:  []string{"#quicknotes/daily", "#orphans/duplicate-title"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got, want := len(findings), 1; got != want {
		t.Fatalf("findings len = %d, want %d (#orphans/duplicate-title is not orphan-family triage); got %v",
			got, want, findings)
	}
	assertDetailContains(t, findings[0].NoteID, findings[0].Detail, []string{"#quicknotes/daily", "quicknotes"})
	if strings.Contains(findings[0].Detail, "#orphans/duplicate-title") {
		t.Fatalf("duplicate-title audit tag leaked into orphan detail: %q", findings[0].Detail)
	}
}

func TestAggregateOrphanFamilies_DuplicateTitleOnlyTagIgnored(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-dup-only",
		Title: "Duplicate triage only",
		Tags:  []string{"#llm/tips", "#orphans/duplicate-title"},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (#orphans/duplicate-title is not a stray family); got %v",
			got, findings)
	}
}

// TestAggregateOrphanFamilies_ManagedFamiliesOnly_NoFindings: when
// every tag's family root is in the managed set, the detector emits
// zero findings — no false positives on a fully managed corpus.
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

// TestAggregateOrphanFamilies_MultipleStrayTags_SingleFindingWithJoinedDetail:
// when an atom carries multiple stray-family tags, the detector emits
// ONE finding (per atom) with Detail comma-joining all strays. One
// #orphans tag will be added at apply time regardless of how many
// strays the atom carries.
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

// TestAggregateOrphanFamilies_Reproducer_LLMTipsWithQuicknotesDaily
// reproduces the original vault scenario that motivated the detector:
// an atom tagged `#llm/tips` plus `#quicknotes/daily` must surface when
// managed = {llm}. Wire-shape equivalent to a real bearcli payload.
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

// Corpus-level orchestrator tests (ScanOrphanFamilies +
// ApplyOrphanFamilies). These exercise the production-side I/O wrappers
// that the CLI integration tests in tests/bear/cli/lint/ depend on, but
// at finer granularity — error paths that the integration tests cannot
// easily exercise via the cli.RunLint surface.

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

	findings := orphanFindings(
		orphanFindingSpec{NoteID: "note-ok", Title: "First (ok)"},
		orphanFindingSpec{NoteID: "note-fails", Title: "Second (fails)"},
	)
	tagged, failed, err := audit.ApplyOrphanFamilies(ctx, findings)
	if err != nil {
		t.Errorf("err = %v, want nil (partial failure must not surface as ctx error)", err)
	}
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

// orphanFindingSpec is the minimal triple ApplyOrphanFamilies needs.
// Used by orphanFindings to keep test fixture construction declarative
// without each call site repeating the audit.Finding struct literal —
// the repetition tripped the dupl threshold across this file's three
// apply-mode cases (partial-failure, ctx-cancel, defensive-filter).
type orphanFindingSpec struct {
	NoteID   string
	Title    string
	Category audit.LintCategory
}

// orphanFindings builds a []audit.Finding from a slice of specs,
// defaulting Category to LintOrphanFamily when the spec leaves it
// blank — the common case. Callers exercising the defensive Category
// filter pass an explicit Category like audit.LintBrokenH1.
func orphanFindings(specs ...orphanFindingSpec) []audit.Finding {
	out := make([]audit.Finding, len(specs))
	for i, spec := range specs {
		category := spec.Category
		if category == "" {
			category = audit.LintOrphanFamily
		}
		out[i] = audit.Finding{
			NoteID:   spec.NoteID,
			Title:    spec.Title,
			Category: category,
		}
	}
	return out
}

// TestAggregateOrphanFamilies_NilTags_NoFinding covers the empty-input
// defense: a note with a nil Tags slice produces zero findings rather
// than panicking on the range. Bear's JSON shape always sends `tags`,
// but tests/external consumers may construct notes by hand.
func TestAggregateOrphanFamilies_NilTags_NoFinding(t *testing.T) {
	notes := []orphanFixture{{ID: "note-nil", Title: "Nil tags", Tags: nil}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (nil Tags is no-finding); got %v", got, findings)
	}
}

// TestAggregateOrphanFamilies_EmptyTagsSlice_NoFinding mirrors the
// nil case for the explicit empty-slice variant — both are valid
// inputs and must produce zero findings without panic.
func TestAggregateOrphanFamilies_EmptyTagsSlice_NoFinding(t *testing.T) {
	notes := []orphanFixture{{ID: "note-empty", Title: "Empty tags", Tags: []string{}}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got := len(findings); got != 0 {
		t.Fatalf("findings len = %d, want 0 (empty Tags is no-finding); got %v", got, findings)
	}
}

// TestAggregateOrphanFamilies_MixedManagedAndStrays_OnlyStraysInDetail
// pins the per-tag classification contract: an atom carrying a mix of
// managed and stray-family tags surfaces ONE finding whose Detail
// joins only the stray tags (not the managed ones). Otherwise the
// operator would see noise about already-handled families in the
// triage report.
func TestAggregateOrphanFamilies_MixedManagedAndStrays_OnlyStraysInDetail(t *testing.T) {
	notes := []orphanFixture{{
		ID:    "note-mixed",
		Title: "Mixed",
		Tags: []string{
			"#llm/tips", "#work/tasks",
			"#scratch/temp", "#quicknotes/daily",
		},
	}}
	findings, err := audit.AggregateOrphanFamiliesFromJSON(
		mustMarshalNotes(t, notes), managedSet("llm", "work"),
	)
	if err != nil {
		t.Fatalf("AggregateOrphanFamiliesFromJSON: %v", err)
	}
	if got, want := len(findings), 1; got != want {
		t.Fatalf("findings len = %d, want %d (one per atom); got %v", got, want, findings)
	}
	got := findings[0]
	assertDetailContains(t, "stray-only Detail",
		got.Detail,
		[]string{"#scratch/temp", "#quicknotes/daily", "scratch", "quicknotes"})
	if strings.Contains(got.Detail, "#llm/tips") || strings.Contains(got.Detail, "#work/tasks") {
		t.Errorf("Detail must NOT mention managed tags; got %q", got.Detail)
	}
}

// TestAggregateOrphanFamilies_OrphansTagCaseInsensitive pins the
// case-insensitive idempotency guard: `#Orphans`, `#ORPHANS`, and
// `#orphans ` (with trailing space) all count as already-triaged.
// A byte-exact compare would skip these and double-tag on the next
// sweep.
func TestAggregateOrphanFamilies_OrphansTagCaseInsensitive(t *testing.T) {
	for _, alias := range []string{"#Orphans", "#ORPHANS", "#orphans ", "#Orphans/sub"} {
		notes := []orphanFixture{{
			ID:    "note-cased-" + alias,
			Title: "Cased orphan: " + alias,
			Tags:  []string{"#quicknotes/daily", alias},
		}}
		findings, err := audit.AggregateOrphanFamiliesFromJSON(
			mustMarshalNotes(t, notes), managedSet("llm"),
		)
		if err != nil {
			t.Fatalf("alias=%q AggregateOrphanFamiliesFromJSON: %v", alias, err)
		}
		if got := len(findings); got != 0 {
			t.Errorf("alias=%q findings len = %d, want 0 (case-insensitive idempotency skip)",
				alias, got)
		}
	}
}

// TestApplyOrphanFamilies_ContextCanceledMidLoop_StopsCleanly pins
// the ctx cancellation contract: a ctx canceled between iterations
// causes ApplyOrphanFamilies to return early with a wrapped ctx.Err
// so the caller can distinguish "ran clean" from "Ctrl-C mid-loop".
// The counters reflect the work actually completed before
// cancellation.
func TestApplyOrphanFamilies_ContextCanceledMidLoop_StopsCleanly(t *testing.T) {
	fake := &orphanFakeBearcli{}
	parent := armOrphanFakeBearcli(t, fake)
	ctx, cancel := context.WithCancel(parent)
	cancel() // canceled before the loop even starts — defensive lower bound

	findings := orphanFindings(
		orphanFindingSpec{NoteID: "note-1", Title: "First"},
		orphanFindingSpec{NoteID: "note-2", Title: "Second"},
	)
	tagged, failed, err := audit.ApplyOrphanFamilies(ctx, findings)
	if err == nil {
		t.Fatalf("err = nil, want wrapped ctx.Err on canceled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want errors.Is(err, context.Canceled)", err)
	}
	if tagged != 0 || failed != 0 {
		t.Errorf("counters = (%d, %d), want (0, 0) — loop must exit before first iteration",
			tagged, failed)
	}
	if got := len(fake.callsTag); got != 0 {
		t.Errorf("AddTag call count = %d, want 0 (canceled-ctx must short-circuit)", got)
	}
}

// TestApplyOrphanFamilies_SkipsNonOrphanFinding pins the defensive
// Category filter: a finding whose Category is NOT LintOrphanFamily
// must NOT trigger an AddTag call, even when mixed with a real orphan
// finding. Belt-and-suspenders against caller mis-filtering.
func TestApplyOrphanFamilies_SkipsNonOrphanFinding(t *testing.T) {
	fake := &orphanFakeBearcli{}
	ctx := armOrphanFakeBearcli(t, fake)

	findings := orphanFindings(
		orphanFindingSpec{NoteID: "note-broken-h1", Title: "Broken H1", Category: audit.LintBrokenH1},
		orphanFindingSpec{NoteID: "note-orphan", Title: "Real orphan"},
	)
	tagged, failed, err := audit.ApplyOrphanFamilies(ctx, findings)
	if err != nil {
		t.Fatalf("err = %v, want nil", err)
	}
	if tagged != 1 || failed != 0 {
		t.Errorf("counters = (%d, %d), want (1, 0)", tagged, failed)
	}
	if got := len(fake.callsTag); got != 1 {
		t.Fatalf("AddTag call count = %d, want 1 (only orphan finding should fire)", got)
	}
	if fake.callsTag[0].NoteID != "note-orphan" {
		t.Errorf("AddTag called with NoteID = %q, want %q",
			fake.callsTag[0].NoteID, "note-orphan")
	}
}

// TestApplyOrphanFamilies_TotalFailureBatch_AbortsWithSentinel pins
// the total-failure abort defense: when bearcli AddTag fails on
// every one of the first batchAbortThreshold findings (3 failures,
// zero successes), the loop aborts and returns ErrApplyAllFailed so
// a bearcli verb-rename or permissions regression surfaces as a
// hard error instead of a "tagged=0 failed=N" silent no-op.
func TestApplyOrphanFamilies_TotalFailureBatch_AbortsWithSentinel(t *testing.T) {
	fake := &orphanFakeBearcli{
		tagErrByNoteID: map[string]error{
			"note-1": errors.New("bearcli verb drift"),
			"note-2": errors.New("bearcli verb drift"),
			"note-3": errors.New("bearcli verb drift"),
			"note-4": errors.New("bearcli verb drift"),
		},
	}
	ctx := armOrphanFakeBearcli(t, fake)

	findings := orphanFindings(
		orphanFindingSpec{NoteID: "note-1", Title: "1"},
		orphanFindingSpec{NoteID: "note-2", Title: "2"},
		orphanFindingSpec{NoteID: "note-3", Title: "3"},
		orphanFindingSpec{NoteID: "note-4", Title: "4"},
	)
	tagged, failed, err := audit.ApplyOrphanFamilies(ctx, findings)
	if err == nil {
		t.Fatalf("err = nil, want ErrApplyAllFailed when first 3 attempts all fail")
	}
	if !errors.Is(err, audit.ErrApplyAllFailed) {
		t.Errorf("err = %v, want errors.Is(err, ErrApplyAllFailed)", err)
	}
	if tagged != 0 || failed != 3 {
		t.Errorf("counters = (%d, %d), want (0, 3) — aborts at threshold not after full sweep",
			tagged, failed)
	}
	if got := len(fake.callsTag); got != 3 {
		t.Errorf("AddTag call count = %d, want 3 (must abort before fourth attempt)", got)
	}
}

// TestManagedRootsFromDomains_SkipsNilAndEmpty pins the SSOT helper's
// defensive behavior: nil-pointer entries and zero-Tag entries in the
// supplied slice are silently skipped so a partially-constructed
// catalog cannot panic the corpus scanners.
func TestManagedRootsFromDomains_SkipsNilAndEmpty(t *testing.T) {
	domains := []*domain.Domain{
		nil,
		{Tag: ""},
		{Tag: "library/poetry"},
		{Tag: "library/articles"},
		{Tag: "llm/tips"},
	}
	roots := audit.ManagedRootsFromDomains(domains)
	if got, want := len(roots), 2; got != want {
		t.Fatalf("roots count = %d, want %d (library + llm); got %v", got, want, roots)
	}
	for _, fam := range []string{"library", "llm"} {
		if _, ok := roots[fam]; !ok {
			t.Errorf("roots missing family %q; got %v", fam, roots)
		}
	}
}
