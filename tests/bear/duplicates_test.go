package bear_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// duplicateFixture mirrors the bearcli `list --fields id,title,tags`
// shape used by the corpus duplicate-title scanner.
type duplicateFixture = noteFixture

func duplicateCorpusFixture() []duplicateFixture {
	return []duplicateFixture{
		{ID: "note-a", Title: "Same Title", Tags: []string{"#work/tasks"}},
		{ID: "note-b", Title: "Same Title", Tags: []string{"#library/quotes"}},
		{ID: "note-c", Title: "Unique Title", Tags: []string{"#work/tasks"}},
	}
}

func TestAggregateDuplicateTitlesFromJSON_FlagsEveryDuplicateOwner(t *testing.T) {
	findings, err := audit.AggregateDuplicateTitlesFromJSON(
		mustMarshalNotes(t, duplicateCorpusFixture()),
	)
	if err != nil {
		t.Fatalf("AggregateDuplicateTitlesFromJSON: %v", err)
	}
	if got, want := len(findings), 2; got != want {
		t.Fatalf("findings len = %d, want %d (%v)", got, want, findings)
	}
	for _, finding := range findings {
		if finding.Category != audit.LintDuplicateTitle {
			t.Fatalf("Category = %q, want %q", finding.Category, audit.LintDuplicateTitle)
		}
		if finding.DomainTag != "" {
			t.Fatalf("DomainTag = %q, want empty corpus-level finding", finding.DomainTag)
		}
		if !finding.Fixable {
			t.Fatalf("Fixable = false, want true for duplicate-title triage tagging")
		}
		assertDetailContains(t, finding.NoteID, finding.Detail,
			[]string{"Same Title", "note-a", "note-b", "#work/tasks", "#library/quotes"})
	}
}

func TestAggregateDuplicateTitlesFromJSON_SkipsOnlyDuplicateTitleTaggedOwners(t *testing.T) {
	notes := []duplicateFixture{
		{ID: "note-a", Title: "Same Title", Tags: []string{"#work/tasks", "#orphans/duplicate-title"}},
		{ID: "note-b", Title: "Same Title", Tags: []string{"#orphans"}},
		{ID: "note-c", Title: "Same Title", Tags: []string{"#library/quotes"}},
	}
	findings, err := audit.AggregateDuplicateTitlesFromJSON(mustMarshalNotes(t, notes))
	if err != nil {
		t.Fatalf("AggregateDuplicateTitlesFromJSON: %v", err)
	}
	if got, want := len(findings), 2; got != want {
		t.Fatalf("findings len = %d, want %d (only duplicate-title-tagged note skipped); got %v",
			got, want, findings)
	}
	gotIDs := []string{findings[0].NoteID, findings[1].NoteID}
	wantIDs := []string{"note-b", "note-c"}
	for index, want := range wantIDs {
		if gotIDs[index] != want {
			t.Fatalf("remaining finding IDs = %v, want %v", gotIDs, wantIDs)
		}
	}
}

func TestApplyDuplicateTitles_AddsDuplicateTitleOrphanSubtag(t *testing.T) {
	fake := &orphanFakeBearcli{}
	ctx := armOrphanFakeBearcli(t, fake)
	findings := []audit.Finding{
		{NoteID: "note-a", Title: "Same Title", Category: audit.LintDuplicateTitle, Fixable: true},
		{NoteID: "note-b", Title: "Same Title", Category: audit.LintDuplicateTitle, Fixable: true},
		{NoteID: "note-c", Title: "Other", Category: audit.LintOrphanFamily, Fixable: true},
	}

	tagged, failed, err := audit.ApplyDuplicateTitles(ctx, findings)
	if err != nil {
		t.Fatalf("ApplyDuplicateTitles: %v", err)
	}
	if tagged != 2 || failed != 0 {
		t.Fatalf("ApplyDuplicateTitles = tagged %d failed %d, want 2/0", tagged, failed)
	}
	if got := len(fake.callsTag); got != 2 {
		t.Fatalf("tag calls = %d, want 2 (%v)", got, fake.callsTag)
	}
	assertTagCall(t, "first duplicate", fake.callsTag[0], "note-a", "orphans/duplicate-title")
	assertTagCall(t, "second duplicate", fake.callsTag[1], "note-b", "orphans/duplicate-title")
}

func TestScanDuplicateTitles_WrapsBearcliFailure(t *testing.T) {
	fake := &orphanFakeBearcli{listErr: errors.New("bearcli boom")}
	ctx := armOrphanFakeBearcli(t, fake)

	findings, err := audit.ScanDuplicateTitles(ctx, nil)

	if err == nil {
		t.Fatalf("ScanDuplicateTitles err = nil, want wrapped list error")
	}
	if findings != nil {
		t.Fatalf("findings = %v, want nil on scan failure", findings)
	}
	if !strings.Contains(err.Error(), "ScanDuplicateTitles list") {
		t.Fatalf("err = %v, want ScanDuplicateTitles list context", err)
	}
}

func TestScanDuplicateTitles_MarksManagedAuxNotesNonFixable(t *testing.T) {
	fake := &orphanFakeBearcli{listPayload: mustMarshalNotes(t, []duplicateFixture{
		{
			ID:    "note-atom",
			Title: "Same Title",
			Tags:  []string{"#test/notes"},
		},
		{
			ID:      "note-hub",
			Title:   "Same Title",
			Content: "# Same Title\n#test/notes | [[✱ Notes]]\n---\n## Items (1)\n- [[Atom]]\n",
			Tags:    []string{"#test/notes"},
		},
	})}
	ctx := armOrphanFakeBearcli(t, fake)
	d := render.NewHubRoutedDomain(
		"test/notes", "✱ Notes", "Unknown", "Items",
		func(_ *domain.Domain, _ map[string][]domain.Note) string { return "" },
	)

	findings, err := audit.ScanDuplicateTitles(ctx, []*domain.Domain{d})
	if err != nil {
		t.Fatalf("ScanDuplicateTitles: %v", err)
	}
	if got, want := len(findings), 2; got != want {
		t.Fatalf("findings len = %d, want %d (%v)", got, want, findings)
	}
	fixableByID := map[string]bool{}
	for _, finding := range findings {
		fixableByID[finding.NoteID] = finding.Fixable
	}
	if !fixableByID["note-atom"] {
		t.Fatalf("atom Fixable = false, want true")
	}
	if fixableByID["note-hub"] {
		t.Fatalf("managed hub Fixable = true, want false so lint --apply does not tag generated notes")
	}
}

func TestApplyDuplicateTitles_ContextCanceledStopsCleanly(t *testing.T) {
	fake := &orphanFakeBearcli{}
	parent := armOrphanFakeBearcli(t, fake)
	ctx, cancel := context.WithCancel(parent)
	cancel()

	_, _, err := audit.ApplyDuplicateTitles(ctx, []audit.Finding{{
		NoteID: "note-a", Title: "Same Title", Category: audit.LintDuplicateTitle, Fixable: true,
	}})

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ApplyDuplicateTitles err = %v, want context.Canceled", err)
	}
	if got := len(fake.callsTag); got != 0 {
		t.Fatalf("tag calls after canceled ctx = %d, want 0", got)
	}
}
