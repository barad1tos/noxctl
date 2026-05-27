package domain

import (
	"strings"
	"testing"
)

func TestComputeMasterOverridesSkipsAmbiguousLegacyTitle(t *testing.T) {
	d := groupedMasterOverrideDomain()
	notes := []Note{
		duplicateMasterNote("- [[Same Title]]", "Beta"),
		duplicateAtomicNote("note-a", "Alpha"),
		duplicateAtomicNote("note-b", "Beta"),
	}

	overrides := d.computeMasterOverrides(notes)

	if len(overrides) != 0 {
		t.Fatalf("overrides = %v, want none for ambiguous legacy title bullets", overrides)
	}
}

func TestComputeMasterOverridesUsesDuplicateURLID(t *testing.T) {
	d := groupedMasterOverrideDomain()
	notes := []Note{
		duplicateMasterNote("[Same Title](bear://x-callback-url/open-note?id=note-a)", "Beta"),
		duplicateAtomicNote("note-a", "Alpha"),
		duplicateAtomicNote("note-b", "Beta"),
	}

	overrides := d.computeMasterOverrides(notes)

	if got, want := overrides["note-a"], "Beta"; got != want {
		t.Fatalf("override note-a = %q, want %q (all=%v)", got, want, overrides)
	}
	if _, ok := overrides["note-b"]; ok {
		t.Fatalf("note-b override present, want none (all=%v)", overrides)
	}
}

func groupedMasterOverrideDomain() *Domain {
	return &Domain{
		Tag:              "test/notes",
		CanonicalTag:     "#test/notes",
		IndexTitle:       "Index",
		UnknownBucket:    "Unknown",
		ParseMeta:        parseGroupedMasterOverrideMeta,
		ParseMasterTable: ParseMasterFlatGrouped,
	}
}

func duplicateMasterNote(bullet, bucket string) Note {
	return Note{
		ID:    "master",
		Title: "Index",
		Content: "# Index\n#test/notes\n---\n" +
			"## " + bucket + " (1)\n- " + bullet + "\n",
	}
}

func duplicateAtomicNote(id, bucket string) Note {
	return Note{
		ID:      id,
		Title:   "Same Title",
		Content: "# Same Title\n#test/notes | [[Index]] | " + bucket + "\n---\n",
		Tags:    []string{"#test/notes"},
	}
}

func parseGroupedMasterOverrideMeta(_ *Domain, body string) AtomicMeta {
	for _, bucket := range []string{"Alpha", "Beta"} {
		if strings.Contains(body, " | "+bucket+"\n") {
			return AtomicMeta{Bucket: bucket}
		}
	}
	return AtomicMeta{}
}
