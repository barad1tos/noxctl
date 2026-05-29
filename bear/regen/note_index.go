package regen

// noteIndex is a goroutine-local title->ID lookup built once per regen.Run
// from the initial listNotes result. It replaces the per-bucket
// findNoteByTitle scan (each of which issued its own `bearcli list`) in the
// hub/master upsert path: for a hub-routed domain with B buckets that drops
// B+1 redundant list calls per no-op cycle.
//
// No mutex: the index is owned by a single regen.Run goroutine (engine
// runDomainAndSave runs one regen.Run per domain on its own goroutine — see
// bear/engine/families.go). It never escapes that goroutine.

import "github.com/barad1tos/noxctl/bear/domain"

// noteIndex maps note title -> note ID with first-match-wins semantics.
type noteIndex struct {
	idByTitle map[string]string
}

// newNoteIndex builds the index over a listNotes result using FIRST-match-wins:
// the first note seen for a title owns the mapping. This deliberately diverges
// from domain.DomainsByTag (which is LAST-wins) to stay byte-equivalent with
// findNoteByTitle (bear/regen/fetches.go), whose loop returns the first
// matching title. The map is pre-sized from len(notes) to avoid rehashing.
func newNoteIndex(notes []domain.Note) noteIndex {
	idByTitle := make(map[string]string, len(notes))
	for _, n := range notes {
		if _, seen := idByTitle[n.Title]; !seen {
			idByTitle[n.Title] = n.ID
		}
	}
	return noteIndex{idByTitle: idByTitle}
}

// lookup returns the ID mapped to title, or "" on miss. Parity with
// findNoteByTitle: a miss is "", never an error.
func (idx noteIndex) lookup(title string) string {
	return idx.idByTitle[title]
}

// patchCreated records the ID of a note created during this regen cycle so a
// later hub/master lookup resolves it without a re-list. A created note
// supersedes any prior mapping for the same title.
func (idx noteIndex) patchCreated(title, id string) {
	idx.idByTitle[title] = id
}

// NewNoteIndexForTestResult wraps the unexported note index so external tests
// (tests/bear/regen/) can exercise it, matching the directory-gap export
// convention documented at bear/engine/hashing.go::ComputeContentHash: the
// project's tests live in a separate directory, so an in-package _test.go seam
// cannot bridge unexported symbols. The ForTest suffix flags this as a
// test-only surface that production callers must not use.
type NewNoteIndexForTestResult struct {
	idx noteIndex
}

// NewNoteIndexForTest builds the index under test.
func NewNoteIndexForTest(notes []domain.Note) NewNoteIndexForTestResult {
	return NewNoteIndexForTestResult{idx: newNoteIndex(notes)}
}

// Lookup proxies noteIndex.lookup for tests.
func (r NewNoteIndexForTestResult) Lookup(title string) string {
	return r.idx.lookup(title)
}

// PatchCreated proxies noteIndex.patchCreated for tests.
func (r NewNoteIndexForTestResult) PatchCreated(title, id string) {
	r.idx.patchCreated(title, id)
}
