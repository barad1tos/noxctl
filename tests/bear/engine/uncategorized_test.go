// Package engine_test — integration test for the uncategorized note lifecycle.
//
// Validates the Phase-16 contract: notes with `ExplicitlyUncategorized: true`
// (canonical `[[]]` in the tag-line) are dropped from groupAtomics and never
// appear in master renders. When the user fills the bucket (e.g. `[[Реальне]]`),
// the note routes normally and appears in the correct group on the next regen
// pass.
//
// Test seam: pure in-memory — drives `RouteAtomics` with synthetic Note slices
// and a real catalog-loaded *Domain. No bearcli I/O, no synctest needed for
// the routing-only assertions.
//
//goland:noinspection SpellCheckingInspection
package engine_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestUncategorizedNoteLifecycle covers three stages of a single note:
//
//  1. Uncategorized (canonical `#library/poetry | [[]]`) → dropped from groups
//  2. Same note with a real bucket (`#library/poetry | [[Любові]]`) → appears
//     in groups["Любові"]
//  3. Idempotency: repeating the routing on the bucketed form produces the
//     same groups (no flapping).
func TestUncategorizedNoteLifecycle(t *testing.T) {
	d := testutil.Domain(t, "library/poetry")

	// ---------- Stage 1: empty wikilink → explicitly uncategorized ----------

	uncatNote := domain.Note{
		ID:      "uncat-1",
		Title:   "Вірш без категорії",
		Content: "# Title\n#library/poetry | [[]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}

	meta := d.DetectAuthor(uncatNote.Content)
	if !meta.ExplicitlyUncategorized {
		t.Fatalf("Stage 1: DetectAuthor = %+v, want ExplicitlyUncategorized=true", meta)
	}
	if meta.Bucket != "" {
		t.Errorf("Stage 1: Bucket = %q, want \"\"", meta.Bucket)
	}

	result1 := d.RouteAtomics([]domain.Note{uncatNote}, nil)
	for bucket, notes := range result1.Groups {
		for _, n := range notes {
			if n.ID == "uncat-1" {
				t.Errorf("Stage 1: uncategorized note leaked into group %q", bucket)
			}
		}
	}

	// ---------- Stage 2: real bucket → routed to Любові ----------

	bucketedNote := domain.Note{
		ID:      "uncat-1",
		Title:   "Вірш без категорії",
		Content: "# Title\n#library/poetry | [[Любові]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}

	meta2 := d.DetectAuthor(bucketedNote.Content)
	if meta2.Bucket != "Любові" {
		t.Fatalf("Stage 2: DetectAuthor = %+v, want Bucket=Любові", meta2)
	}
	if meta2.ExplicitlyUncategorized {
		t.Errorf("Stage 2: ExplicitlyUncategorized should be false for real bucket")
	}

	result2 := d.RouteAtomics([]domain.Note{bucketedNote}, nil)
	found := false
	for _, n := range result2.Groups["Любові"] {
		if n.ID == "uncat-1" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Stage 2: note not found in Любові group; groups=%v", groupSizes(result2.Groups))
	}

	// ---------- Stage 3: idempotency — re-running produces same groups ----------

	result3 := d.RouteAtomics([]domain.Note{bucketedNote}, nil)
	if len(result3.Groups["Любові"]) != len(result2.Groups["Любові"]) {
		t.Errorf("Stage 3: idempotency broken — Любові group size changed from %d to %d",
			len(result2.Groups["Любові"]), len(result3.Groups["Любові"]))
	}
}

// groupSizes returns a debug-friendly bucket → count map for assertions.
func groupSizes(groups map[string][]domain.Note) map[string]int {
	out := make(map[string]int, len(groups))
	for k, v := range groups {
		out[k] = len(v)
	}
	return out
}
