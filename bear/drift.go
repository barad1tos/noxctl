package bear

// Content-equality predicates with new-note URL tolerance. The
// idempotency contract (≤ 3 passes to `unchanged`) hinges on these:
// equalIgnoringNewNoteLink strips the trailing `[Нова нотатка]` URL
// segment before comparing, so a fresh-timestamp render against an
// unchanged body returns equal and skips the overwrite. The strict
// variant performs the structural NewNoteURL.Equals comparison and
// catches every drift class (label drift, placeholder leak, count
// mismatch).

import "strings"

// equalIgnoringNewNoteLink is the non-strict (atomic) body compare:
// strip the trailing `[Нова нотатка]` URL segment, normalize trailing
// whitespace, then string-equal. Lets timestamp-only drift on
// otherwise-identical bodies skip overwrite. The strict variant adds
// a structural URL diff on top.
func equalIgnoringNewNoteLink(a, b string) bool {
	stripA := strings.TrimRight(StripNewNoteURLsFromBody(a), " \n")
	stripB := strings.TrimRight(StripNewNoteURLsFromBody(b), " \n")
	return stripA == stripB
}

// equalIgnoringNewNoteLinkStrict (master/hub/cross-domain flavor) is
// equalIgnoringNewNoteLink PLUS a structural URL drift check: bodies
// are compared position-by-position via FindAllNewNoteURLsInBody and
// NewNoteURL.Equals. ANY structural change (Backlink, PlaceholderH1,
// Label, Tag, CanonicalTag, Form, Inner) triggers rewrite — that's
// the URL-emission SSOT contract that ends the recurring-bug pattern.
//
// The non-strict body compare runs as a fallback so trailing-whitespace
// drift on otherwise-identical bodies doesn't loop-rewrite.
func equalIgnoringNewNoteLinkStrict(a, b string) bool {
	urlsA := FindAllNewNoteURLsInBody(a)
	urlsB := FindAllNewNoteURLsInBody(b)
	if len(urlsA) != len(urlsB) {
		return false
	}
	for i := range urlsA {
		if !urlsA[i].Equals(urlsB[i]) {
			return false
		}
	}
	return equalIgnoringNewNoteLink(a, b)
}

// EqualIgnoringNewNoteLinkForTest exposes the non-strict predicate to tests/bear.
func EqualIgnoringNewNoteLinkForTest(a, b string) bool {
	return equalIgnoringNewNoteLink(a, b)
}

// EqualIgnoringNewNoteLinkStrictForTest exposes the strict predicate to tests/bear.
func EqualIgnoringNewNoteLinkStrictForTest(a, b string) bool {
	return equalIgnoringNewNoteLinkStrict(a, b)
}

// EqualIgnoringNewNoteLink is the exported wrapper around the
// unexported equalIgnoringNewNoteLink predicate (non-strict, atomic
// flavor). plan engine and parity check read this from outside
// package bear. Internal callers continue using the lowercase original
// to keep the export footprint minimal.
//
// Non-strict semantics: URL-shape drift (legacy title= vs current
// no-title=) is ignored. Master/hub diffs should call
// EqualIgnoringNewNoteLinkStrict instead.
func EqualIgnoringNewNoteLink(a, b string) bool {
	return equalIgnoringNewNoteLink(a, b)
}

// EqualIgnoringNewNoteLinkStrict is the exported wrapper around the
// strict (master/hub) variant. Used by bear/engine/plan to surface
// URL-shape drift as a real desired-state diff so the engine forces a
// one-shot rewrite of master canonical lines on the first regen cycle
// after the URL emission change deploys.
func EqualIgnoringNewNoteLinkStrict(a, b string) bool {
	return equalIgnoringNewNoteLinkStrict(a, b)
}
