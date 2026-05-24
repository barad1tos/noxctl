// Package audit_test exercises the per-atom lint + auto-fix contract that
// backs `noxctl lint` and `noxctl lint --apply`.
//
// LintAtom and AutoFixAtom are the audit package's public boundary: the
// linter decides what the operator sees in the report, the auto-fixer
// produces the body that `noxctl lint --apply` overwrites into Bear. These
// tests drive that boundary with real-vault-shaped notes (the #work
// grouped-vertical domain, Cyrillic titles, `✱ Робота` master) and assert
// the observable output — the rewritten body and the fixable verdict —
// never the private reconstruction helpers, which are covered through
// AutoFixAtom.
package audit_test

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
)

// overwriteFake is a bearcli.Backend that serves a stub hash for `show`
// (so OverwriteWithRetry can read a base hash) and either succeeds or fails
// the `overwrite` verb. overwrites counts how many overwrite attempts ran.
// Used by the AutoFixDomain apply-path tests; mirrors the show/overwrite
// shape of fakeBearcli in tests/bear/cli/lint/.
type overwriteFake struct {
	overwriteErr error
	overwrites   int
}

func (f *overwriteFake) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("{}"), nil
	}
	switch args[0] {
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		f.overwrites++
		if f.overwriteErr != nil {
			return nil, f.overwriteErr
		}
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

// workDomain returns the minimal #work domain the lint/auto-fix functions
// read: only Tag drives canonical-line detection and orphan-token
// stripping; IndexTitle keeps the master note out of the atom set. Mirrors
// Roman's grouped-vertical #work setup.
//
//cyrillic:permit
func workDomain() *domain.Domain {
	return &domain.Domain{Tag: "work", CanonicalTag: "#work", IndexTitle: "✱ Робота"}
}

// findingByCategory returns the first finding of the given category, or
// (zero, false) when none fired.
func findingByCategory(findings []audit.Finding, cat audit.LintCategory) (audit.Finding, bool) {
	for _, f := range findings {
		if f.Category == cat {
			return f, true
		}
	}
	return audit.Finding{}, false
}

// TestAutoFixAtom_ReconstructsCanonical_WhenLineMalformedButHasTagAndWikilink
// pins the malformed-canonical recovery path: a note whose canonical line
// was mangled (leading H1 fragment shoved the tag off the line start, so it
// no longer parses as canonical) but still carries the tag token, a pipe,
// and the master wikilink. The operator runs `noxctl lint --apply` and the
// body must come back with a clean canonical line — the leading garbage
// stripped. User-facing bug if this regresses: a UI-mangled note never
// recanonicalizes and silently drops out of its master forever.
//
//cyrillic:permit
func TestAutoFixAtom_ReconstructsCanonical_WhenLineMalformedButHasTagAndWikilink(t *testing.T) {
	d := workDomain()
	content := "# Якась нотатка\n\n❋ leftover fragment #work/tasks | [[✱ Робота]]\nтіло нотатки\n"

	got, fixed := audit.AutoFixAtom(d, content)

	if !fixed {
		t.Fatalf("AutoFixAtom reported no fix; want reconstruction. Body unchanged:\n%s", got)
	}
	if !strings.Contains(got, "#work/tasks | [[✱ Робота]]") {
		t.Errorf("rebuilt body missing clean canonical line; got:\n%s", got)
	}
	if strings.Contains(got, "❋ leftover fragment #work/tasks") {
		t.Errorf("rebuilt body still carries the mangled leading fragment; got:\n%s", got)
	}
}

// TestAutoFixAtom_KeepsFirstCanonical_WhenMultipleCanonicalLines pins the
// multi-canonical collapse: a note that accumulated two canonical-shape
// lines (e.g. a half-applied earlier overwrite) must come back with only
// the first. User-facing bug if this regresses: ParseMeta sees two
// canonical lines and the render layer ping-pongs the atom between buckets.
//
//cyrillic:permit
func TestAutoFixAtom_KeepsFirstCanonical_WhenMultipleCanonicalLines(t *testing.T) {
	d := workDomain()
	content := "# Подвійний канонік\n#work/tasks | [[✱ Робота]]\n#work/health | [[✱ Робота]]\nтіло\n"

	got, fixed := audit.AutoFixAtom(d, content)

	if !fixed {
		t.Fatalf("AutoFixAtom reported no fix; want multi-canonical collapse. Body:\n%s", got)
	}
	if !strings.Contains(got, "#work/tasks | [[✱ Робота]]") {
		t.Errorf("first canonical line was dropped; got:\n%s", got)
	}
	if strings.Contains(got, "#work/health") {
		t.Errorf("second canonical line survived; auto-fix must keep only the first. got:\n%s", got)
	}
}

// TestAutoFixAtom_StripsOrphanTokens_WhenCanonicalPresentPlusStrayTags pins
// orphan-token cleanup: a note with a valid canonical line PLUS a stray
// `#work/<sub>` token loose in the body. The stray token must be scrubbed
// while the surrounding prose survives. User-facing bug if this regresses:
// the stray token re-triggers a finding every audit run and never clears.
//
//cyrillic:permit
func TestAutoFixAtom_StripsOrphanTokens_WhenCanonicalPresentPlusStrayTags(t *testing.T) {
	d := workDomain()
	content := "# Чиста нотатка\n#work/tasks | [[✱ Робота]]\n---\nдеякий текст #work/health у тілі\n"

	got, fixed := audit.AutoFixAtom(d, content)

	if !fixed {
		t.Fatalf("AutoFixAtom reported no fix; want orphan-token strip. Body:\n%s", got)
	}
	if !strings.Contains(got, "#work/tasks | [[✱ Робота]]") {
		t.Errorf("canonical line must survive orphan scrub; got:\n%s", got)
	}
	if strings.Contains(got, "#work/health") {
		t.Errorf("stray #work/health token was not stripped; got:\n%s", got)
	}
	if !strings.Contains(got, "деякий текст") {
		t.Errorf("surrounding prose must be preserved around the stripped token; got:\n%s", got)
	}
}

// TestAutoFixAtom_NoOp_WhenAtomAlreadyClean pins the idempotency contract:
// a note with exactly one canonical line and no stray tokens must come back
// unchanged with fixed=false, so `noxctl lint --apply` issues no overwrite.
// User-facing bug if this regresses: every apply run rewrites clean atoms,
// churning bearcli traffic and breaking the ≤3-pass idempotency gate.
//
//cyrillic:permit
func TestAutoFixAtom_NoOp_WhenAtomAlreadyClean(t *testing.T) {
	d := workDomain()
	content := "# Чиста нотатка\n#work/tasks | [[✱ Робота]]\n---\nтіло без stray тегів\n"

	got, fixed := audit.AutoFixAtom(d, content)

	if fixed {
		t.Errorf("AutoFixAtom fixed a clean atom; want no-op. Rewrote to:\n%s", got)
	}
	if got != content {
		t.Errorf("clean atom body mutated:\n got:  %q\n want: %q", got, content)
	}
}

// TestAutoFixAtom_BailsToManualReview_WhenMalformedButNoWikilink pins the
// failure path: an orphan `#work` token with no canonical line AND no
// wikilink to rebuild from. Auto-fix cannot safely reconstruct, so it must
// bail (fixed=false, body untouched) and leave the atom for the operator.
// User-facing bug if this regresses: auto-fix invents a canonical line from
// insufficient signal and mis-buckets the atom.
//
//cyrillic:permit
func TestAutoFixAtom_BailsToManualReview_WhenMalformedButNoWikilink(t *testing.T) {
	d := workDomain()
	content := "# Без вікілінку\n\nрядок з #work/tasks але без вікілінку тут\n"

	got, fixed := audit.AutoFixAtom(d, content)

	if fixed {
		t.Errorf("AutoFixAtom reconstructed without a wikilink; want manual-review bail. Rewrote to:\n%s", got)
	}
	if got != content {
		t.Errorf("manual-review bail must not mutate the body:\n got:  %q\n want: %q", got, content)
	}
}

// TestLintAtom_FlagsMalformedCanonical_Reconstructible_WhenTagWikilinkPresent
// pins the report the operator reads: a mangled-but-reconstructible note
// surfaces as a malformed-canonical finding marked fixable, so the operator
// knows `--apply` will auto-resolve it.
//
//cyrillic:permit
func TestLintAtom_FlagsMalformedCanonical_Reconstructible_WhenTagWikilinkPresent(t *testing.T) {
	d := workDomain()
	note := domain.Note{
		ID:      "note-malformed",
		Title:   "Якась нотатка",
		Content: "# Якась нотатка\n\n❋ leftover #work/tasks | [[✱ Робота]]\nтіло\n",
	}

	finding, ok := findingByCategory(audit.LintAtom(d, note), audit.LintMalformedCanonical)
	if !ok {
		t.Fatalf("LintAtom did not flag malformed-canonical; got %+v", audit.LintAtom(d, note))
	}
	if !finding.Fixable {
		t.Errorf("reconstructible malformed-canonical must be Fixable=true; got %+v", finding)
	}
	if !strings.Contains(finding.Detail, "auto-rebuild") {
		t.Errorf("detail should signal auto-rebuild availability; got %q", finding.Detail)
	}
}

// TestLintAtom_FlagsMultiCanonical_WhenTwoCanonicalLines pins the
// multi-canonical finding: the operator sees a fixable finding naming the
// duplicate canonical lines.
//
//cyrillic:permit
func TestLintAtom_FlagsMultiCanonical_WhenTwoCanonicalLines(t *testing.T) {
	d := workDomain()
	note := domain.Note{
		ID:      "note-multi",
		Title:   "Подвійний канонік",
		Content: "# Подвійний канонік\n#work/tasks | [[✱ Робота]]\n#work/health | [[✱ Робота]]\n",
	}

	finding, ok := findingByCategory(audit.LintAtom(d, note), audit.LintMultiCanonical)
	if !ok {
		t.Fatalf("LintAtom did not flag multi-canonical; got %+v", audit.LintAtom(d, note))
	}
	if !finding.Fixable {
		t.Errorf("multi-canonical must be Fixable=true; got %+v", finding)
	}
}

// TestLintAtom_ReturnsNoFindings_WhenAtomClean pins the clean case: a
// well-formed atom produces zero findings, so it never appears in the
// operator's report. Guards against a linter that flags healthy notes.
//
//cyrillic:permit
func TestLintAtom_ReturnsNoFindings_WhenAtomClean(t *testing.T) {
	d := workDomain()
	note := domain.Note{
		ID:      "note-clean",
		Title:   "Чиста нотатка",
		Content: "# Чиста нотатка\n#work/tasks | [[✱ Робота]]\n---\nтіло\n",
	}

	if findings := audit.LintAtom(d, note); len(findings) != 0 {
		t.Errorf("clean atom produced findings; want none. got %+v", findings)
	}
}

// TestLintAtom_FlagsBrokenH1_WhenTitleStartsWithPipe pins the broken-H1
// finding: a note whose title leaked a canonical fragment (starts with
// `|`) is structurally corrupt — the original H1 is lost. The operator
// must see this as a non-fixable finding needing manual review.
//
//cyrillic:permit
func TestLintAtom_FlagsBrokenH1_WhenTitleStartsWithPipe(t *testing.T) {
	d := workDomain()
	note := domain.Note{
		ID:      "note-broken",
		Title:   "| [[✱ Робота]] | tasks",
		Content: "#work/tasks | [[✱ Робота]]\nтіло\n",
	}

	finding, ok := findingByCategory(audit.LintAtom(d, note), audit.LintBrokenH1)
	if !ok {
		t.Fatalf("LintAtom did not flag broken-h1 for a pipe-leading title; got %+v", audit.LintAtom(d, note))
	}
	if finding.Fixable {
		t.Errorf("broken-h1 is not auto-fixable (original title is lost); got Fixable=true")
	}
}

// TestLintAtom_FlagsUnsafeTitle_WhenTitleHasBrackets pins the unsafe-title
// finding: a title containing `[` `]` `|` forces the render layer to emit
// the bullet as a bear:// URL instead of a `[[wikilink]]`. The operator
// sees the finding so they can rename the note.
//
//cyrillic:permit
func TestLintAtom_FlagsUnsafeTitle_WhenTitleHasBrackets(t *testing.T) {
	d := workDomain()
	note := domain.Note{
		ID:      "note-unsafe",
		Title:   "Нотатка [з дужками]",
		Content: "#work/tasks | [[Нотатка [з дужками]]]\nтіло\n",
	}

	if _, ok := findingByCategory(audit.LintAtom(d, note), audit.LintUnsafeTitle); !ok {
		t.Fatalf("LintAtom did not flag unsafe-title for a bracketed title; got %+v", audit.LintAtom(d, note))
	}
}

// TestAutoFixDomain_ReportsFixed_WhenOverwriteSucceeds pins the happy
// apply path: a domain with one fixable atom yields fixed=1, failed=0 and
// issues exactly one bearcli overwrite. This is what `noxctl lint --apply`
// reports per domain on a successful sweep.
//
//cyrillic:permit
func TestAutoFixDomain_ReportsFixed_WhenOverwriteSucceeds(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	d := workDomain()
	fake := &overwriteFake{}
	ctx := domain.ContextWithBackend(context.Background(), fake)
	notes := []domain.Note{{
		ID:      "n-multi",
		Title:   "Подвійний канонік",
		Content: "# Подвійний канонік\n#work/tasks | [[✱ Робота]]\n#work/health | [[✱ Робота]]\n",
	}}

	fixed, failed := audit.AutoFixDomain(ctx, d, notes)
	if fixed != 1 || failed != 0 {
		t.Errorf("AutoFixDomain = (fixed=%d, failed=%d); want (1, 0)", fixed, failed)
	}
	if fake.overwrites != 1 {
		t.Errorf("expected exactly 1 bearcli overwrite; got %d", fake.overwrites)
	}
}

// TestAutoFixDomain_ReportsFailure_WhenOverwriteFails pins the failure
// path: when bearcli overwrite errors mid-sweep, AutoFixDomain counts it as
// failed (not fixed) and continues. `noxctl lint --apply` surfaces the
// failed count and exits non-zero — the operator must not be told the fix
// landed when bearcli rejected the write.
//
//cyrillic:permit
func TestAutoFixDomain_ReportsFailure_WhenOverwriteFails(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	d := workDomain()
	fake := &overwriteFake{overwriteErr: errors.New("bearcli overwrite: simulated rejection")}
	ctx := domain.ContextWithBackend(context.Background(), fake)
	notes := []domain.Note{{
		ID:      "n-multi",
		Title:   "Подвійний канонік",
		Content: "# Подвійний канонік\n#work/tasks | [[✱ Робота]]\n#work/health | [[✱ Робота]]\n",
	}}

	fixed, failed := audit.AutoFixDomain(ctx, d, notes)
	if fixed != 0 || failed != 1 {
		t.Errorf("AutoFixDomain = (fixed=%d, failed=%d); want (0, 1) on overwrite rejection", fixed, failed)
	}
}

// TestLogAuditFindings_EmitsSummaryAndPerFinding pins the daemon pre-pass
// audit log: a summary line with fixable/manual counts plus one line per
// finding. This is the only surface where the operator sees audit findings
// without invoking `noxctl audit` — the daemon logs them on each cycle.
func TestLogAuditFindings_EmitsSummaryAndPerFinding(t *testing.T) {
	var lines []string
	logf := func(format string, args ...any) {
		lines = append(lines, fmt.Sprintf(format, args...))
	}
	findings := []audit.Finding{
		{DomainTag: "work", NoteID: "a", Title: "Fixable one", Category: audit.LintMultiCanonical, Detail: "two canonical", Fixable: true},
		{DomainTag: "work", NoteID: "b", Title: "Manual one", Category: audit.LintBrokenH1, Detail: "title lost", Fixable: false},
	}

	audit.LogAuditFindings(findings, logf)

	joined := strings.Join(lines, "\n")
	if !strings.Contains(joined, "2 findings (1 auto-fixable") || !strings.Contains(joined, "1 need manual review") {
		t.Errorf("summary line missing or wrong counts; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Fixable one") || !strings.Contains(joined, "[fixable]") {
		t.Errorf("fixable finding line missing its [fixable] marker; got:\n%s", joined)
	}
	if !strings.Contains(joined, "Manual one") {
		t.Errorf("non-fixable finding line missing; got:\n%s", joined)
	}
}

// TestLogAuditFindings_Silent_WhenEmpty pins the no-op contract: zero
// findings emits zero log lines, so a clean daemon cycle stays quiet.
func TestLogAuditFindings_Silent_WhenEmpty(t *testing.T) {
	called := false
	audit.LogAuditFindings(nil, func(string, ...any) { called = true })
	if called {
		t.Errorf("LogAuditFindings emitted a log line for zero findings; want silence")
	}
}

// TestAggregateUntrackedFromJSON_ReturnsEmptyReportAndError_OnMalformedJSON
// pins the failure path of the untracked-residue scan: when bearcli hands
// back malformed JSON, the aggregator must return a wrapped parse error AND
// a well-formed empty report (non-nil TagFamilies slice so it still
// JSON-serializes for `noxctl plan -o json`) — never a nil-slice panic or a
// silent zero-finding success that hides the bearcli breakage.
func TestAggregateUntrackedFromJSON_ReturnsEmptyReportAndError_OnMalformedJSON(t *testing.T) {
	report, err := audit.AggregateUntrackedFromJSON([]byte("{not valid json"), map[string]struct{}{})

	if err == nil {
		t.Fatalf("want a parse error for malformed JSON; got nil")
	}
	if !strings.Contains(err.Error(), "parse") {
		t.Errorf("error should identify the parse failure; got %v", err)
	}
	if report.TagFamilies == nil {
		t.Errorf("empty report must carry a non-nil TagFamilies slice for JSON serialization")
	}
	if report.TotalNotes != 0 {
		t.Errorf("error report must report TotalNotes=0; got %d", report.TotalNotes)
	}
}
