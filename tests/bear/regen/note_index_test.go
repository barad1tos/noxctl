// Package regen_test pins the goroutine-local note index that replaces the
// per-bucket findNoteByTitle scan in the hub/master upsert path. These tests
// live external to bear/regen (project rule: zero *_test.go in production
// package dirs) and reach the unexported index via the NewNoteIndexForTest
// seam, matching the directory-gap export convention documented at
// bear/engine/hashing.go::ComputeContentHash.
package regen_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/regen"
)

// note is a tiny corpus builder keeping the table rows readable.
func note(id, title string) domain.Note {
	return domain.Note{ID: id, Title: title}
}

// TestNoteIndexLookupParity pins first-match-wins lookup and "" on miss,
// byte-equivalent to findNoteByTitle (bear/regen/fetches.go:39-62).
func TestNoteIndexLookupParity(t *testing.T) {
	cases := []struct {
		name  string
		notes []domain.Note
		query string
		want  string
	}{
		{
			name:  "first match wins over later duplicate title",
			notes: []domain.Note{note("a", "X"), note("b", "X"), note("c", "Y")},
			query: "X",
			want:  "a",
		},
		{
			name:  "distinct title resolves to its own id",
			notes: []domain.Note{note("a", "X"), note("b", "X"), note("c", "Y")},
			query: "Y",
			want:  "c",
		},
		{
			name:  "absent title returns empty string, never errors",
			notes: []domain.Note{note("a", "X")},
			query: "missing",
			want:  "",
		},
		{
			name:  "nil corpus builds and misses cleanly",
			notes: nil,
			query: "anything",
			want:  "",
		},
		{
			name:  "empty corpus builds and misses cleanly",
			notes: []domain.Note{},
			query: "anything",
			want:  "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			idx := regen.NewNoteIndexForTest(tc.notes)
			if got := idx.Lookup(tc.query); got != tc.want {
				t.Errorf("lookup(%q) = %q, want %q", tc.query, got, tc.want)
			}
		})
	}
}

// TestNoteIndexLookupEveryFirstSeen proves a build over N notes resolves all
// N titles to their first-seen ID — the pre-size/parity contract expressed as
// behavior rather than an internal field assertion.
func TestNoteIndexLookupEveryFirstSeen(t *testing.T) {
	notes := []domain.Note{
		note("id-1", "Alpha"),
		note("id-2", "Beta"),
		note("id-dup", "Alpha"), // later duplicate must NOT win
		note("id-3", "Gamma"),
	}
	idx := regen.NewNoteIndexForTest(notes)

	want := map[string]string{"Alpha": "id-1", "Beta": "id-2", "Gamma": "id-3"}
	for title, wantID := range want {
		if got := idx.Lookup(title); got != wantID {
			t.Errorf("lookup(%q) = %q, want %q (first-seen)", title, got, wantID)
		}
	}
}

// TestNoteIndexPatchCreated proves a freshly-created note is found later in
// the same cycle without a re-list, and that patchCreated supersedes a prior
// mapping for the same title.
func TestNoteIndexPatchCreated(t *testing.T) {
	t.Run("patch makes an absent title resolvable", func(t *testing.T) {
		idx := regen.NewNoteIndexForTest(nil)
		if got := idx.Lookup("Fresh Hub"); got != "" {
			t.Fatalf("pre-patch lookup = %q, want empty", got)
		}
		idx.PatchCreated("Fresh Hub", "created-id")
		if got := idx.Lookup("Fresh Hub"); got != "created-id" {
			t.Errorf("post-patch lookup = %q, want created-id", got)
		}
	})

	t.Run("patch overwrites a prior mapping for the same title", func(t *testing.T) {
		idx := regen.NewNoteIndexForTest([]domain.Note{note("old-id", "Master")})
		if got := idx.Lookup("Master"); got != "old-id" {
			t.Fatalf("pre-patch lookup = %q, want old-id", got)
		}
		idx.PatchCreated("Master", "new-id")
		if got := idx.Lookup("Master"); got != "new-id" {
			t.Errorf("post-patch lookup = %q, want new-id (created supersedes)", got)
		}
	})
}
