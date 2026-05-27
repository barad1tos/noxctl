package domain

import "testing"

func TestParseHubOrderExtractsURLFormNoteIDs(t *testing.T) {
	autoZone := "# Hub\n#test/notes | [[Index]]\n---\n" +
		"### Items (2)\n" +
		"- [Same Title](bear://x-callback-url/open-note?id=note-b)\n" +
		"- [Same Title](bear://x-callback-url/open-note?id=note-a)\n"

	got := parseHubOrder(autoZone)["Items"]
	assertNoteIDs(t, got, []string{"note-b", "note-a"})
}

func TestReorderForOutputMatchesDuplicateURLIDs(t *testing.T) {
	notes := []Note{
		{ID: "note-a", Title: "Same Title"},
		{ID: "note-b", Title: "Same Title"},
	}

	got := reorderForOutput(notes, []string{"note-b", "note-a"})

	assertNoteIDs(t, noteIDs(got), []string{"note-b", "note-a"})
}

func assertNoteIDs(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("ids len = %d, want %d (%v)", len(got), len(want), got)
	}
	for index, wantID := range want {
		if got[index] != wantID {
			t.Fatalf("ids[%d] = %q, want %q (all=%v)", index, got[index], wantID, got)
		}
	}
}

func noteIDs(notes []Note) []string {
	ids := make([]string, 0, len(notes))
	for _, note := range notes {
		ids = append(ids, note.ID)
	}
	return ids
}
