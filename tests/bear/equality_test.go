package bear_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear"
)

// TestEqualVariants_StaleVsFreshURL distinguishes the two equality
// flavors per (SSOT refactor):
// - Non-strict (atomic flavor): strips every new-note URL decoration
// and compares the residual body. ANY URL drift is ignored —
// atomic canonicalization migrates lazily as atoms are touched
// for other reasons. Used by upsertAtomicBacklink's fallback
// check and promoteAtomToDomain.
// - Strict (master/hub/cross-domain flavor): scans every new-note
// URL via FindAllNewNoteURLsInBody and compares position-by-position
// via NewNoteURL.Equals. ANY structural drift (Form, Label,
// Backlink, PlaceholderH1, Tag, CanonicalTag, Inner) triggers
// rewrite. This is the SSOT mechanism that catches the
// [[_umbrella]] placeholder leak + future URL shape evolution
// without per-bug predicate extensions.
//
// Stale = pre-bootstrap form (FormSimple or FormLegacyTitle).
// Fresh = bootstrap form (FormBootstrap, with non-nil Inner).
func TestEqualVariants_StaleVsFreshURL(t *testing.T) {
	stale := "#tag | [[Bucket]] | [Нова нотатка](bear://x-callback-url/create?tags=t&title=x&open_note=yes)"
	fresh := "#tag | [[Bucket]] | [Нова нотатка](bear://x-callback-url/create?text=%23%20Quicknote%0A&edit=yes&open_note=yes)"

	if !bear.EqualIgnoringNewNoteLinkForTest(stale, fresh) {
		t.Errorf("non-strict: stale vs fresh should compare equal (URL drift ignored)")
	}
	if bear.EqualIgnoringNewNoteLinkStrictForTest(stale, fresh) {
		t.Errorf("strict: stale (FormLegacyTitle) vs fresh (FormBootstrap) must compare UNEQUAL — Form drift forces rewrite")
	}

	// Identical URLs compare EQUAL under both predicates (steady state).
	sameStale := "#tag | [[Bucket]] | [Нова нотатка](bear://x-callback-url/create?tags=t&title=x&open_note=yes)"
	if !bear.EqualIgnoringNewNoteLinkForTest(stale, sameStale) {
		t.Errorf("non-strict: byte-identical URLs should equal")
	}
	if !bear.EqualIgnoringNewNoteLinkStrictForTest(stale, sameStale) {
		t.Errorf("strict: byte-identical URLs should equal (no drift)")
	}

	// Note: label-drift-on-bootstrap-URLs coverage lives in
	// new_note_url_test.go::TestEqualIgnoringNewNoteLinkStrict_RejectsURLDriftWithBodyMatch
	// where the bootstrap shape is properly constructed via NewNoteURL.Emit
	// rather than ad-hoc string literals (which would fail to parse).
}
