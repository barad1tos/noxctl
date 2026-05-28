package domain_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
)

func TestComputeMasterOverridesSkipsAmbiguousLegacyTitle(t *testing.T) {
	d := groupedMasterOverrideDomain()
	notes := []domain.Note{
		duplicateMasterNote("- [[Same Title]]", "Beta"),
		duplicateAtomicNote("note-a", "Alpha"),
		duplicateAtomicNote("note-b", "Beta"),
	}

	routing := d.RouteAtomics(notes, nil)

	if routing.MasterClaims != 0 {
		t.Fatalf("master claims = %d, want 0 for ambiguous legacy title bullets", routing.MasterClaims)
	}
	if got := routingNoteIDs(routing.Groups["Alpha"]); !equalStrings(got, []string{"note-a"}) {
		t.Fatalf("Groups[Alpha] = %v, want [note-a]", got)
	}
	if got := routingNoteIDs(routing.Groups["Beta"]); !equalStrings(got, []string{"note-b"}) {
		t.Fatalf("Groups[Beta] = %v, want [note-b]", got)
	}
}

func TestComputeMasterOverridesUsesDuplicateURLID(t *testing.T) {
	d := groupedMasterOverrideDomain()
	notes := []domain.Note{
		duplicateMasterNote("[Same Title](bear://x-callback-url/open-note?id=note-a)", "Beta"),
		duplicateAtomicNote("note-a", "Alpha"),
		duplicateAtomicNote("note-b", "Beta"),
	}

	routing := d.RouteAtomics(notes, nil)

	if routing.MasterClaims != 1 {
		t.Fatalf("master claims = %d, want 1", routing.MasterClaims)
	}
	if got := routingNoteIDs(routing.Groups["Beta"]); !equalStrings(got, []string{"note-a", "note-b"}) {
		t.Fatalf("Groups[Beta] = %v, want [note-a note-b]", got)
	}
}

func groupedMasterOverrideDomain() *domain.Domain {
	return &domain.Domain{
		Tag:              "test/notes",
		CanonicalTag:     "#test/notes",
		IndexTitle:       "Index",
		UnknownBucket:    "Unknown",
		ParseMeta:        parseGroupedMasterOverrideMeta,
		ParseMasterTable: domain.ParseMasterFlatGrouped,
	}
}

func duplicateMasterNote(bullet, bucket string) domain.Note {
	return domain.Note{
		ID:    "master",
		Title: "Index",
		Content: "# Index\n#test/notes\n---\n" +
			"## " + bucket + " (1)\n- " + bullet + "\n",
	}
}

func duplicateAtomicNote(id, bucket string) domain.Note {
	return domain.Note{
		ID:      id,
		Title:   "Same Title",
		Content: "# Same Title\n#test/notes | [[Index]] | " + bucket + "\n---\n",
		Tags:    []string{"#test/notes"},
	}
}

func parseGroupedMasterOverrideMeta(_ *domain.Domain, body string) domain.AtomicMeta {
	for _, bucket := range []string{"Alpha", "Beta"} {
		if strings.Contains(body, " | "+bucket+"\n") {
			return domain.AtomicMeta{Bucket: bucket}
		}
	}
	return domain.AtomicMeta{}
}

func routingNoteIDs(notes []domain.Note) []string {
	ids := make([]string, 0, len(notes))
	for _, note := range notes {
		ids = append(ids, note.ID)
	}
	return ids
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}
