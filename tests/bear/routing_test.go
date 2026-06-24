// Package bear_test holds external tests for github.com/barad1tos/noxctl/bear/domain.
//
// routing_test.go locks down the groupAtomics, overrideForNote, and
// DetectAuthor routing decisions, especially the ExplicitlyUncategorized
// guards that prevent empty-bucket notes from polluting groups or
// triggering spurious master overrides.
package bear_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
)

// parseMetaRecorder is a test helper that returns deterministic AtomicMeta
// based on the body string it receives. Used to simulate
// ExplicitlyUncategorized without relying on the real ParseMeta
// implementations.
type parseMetaRecorder struct {
	matches map[string]domain.AtomicMeta
}

// makeParseMeta returns a domain.ParseMeta callback that consults
// rec.matches for the full body string, returning AtomicMeta{} when no
// match is found.
func (rec *parseMetaRecorder) makeParseMeta() func(d *domain.Domain, body string) domain.AtomicMeta {
	return func(_ *domain.Domain, body string) domain.AtomicMeta {
		if meta, ok := rec.matches[body]; ok {
			return meta
		}
		return domain.AtomicMeta{}
	}
}

// TestGroupAtomics_ExplicitlyUncategorized_Dropped verifies the
// groupAtomics loop drops notes whose DetectAuthor returns
// ExplicitlyUncategorized: true, while still routing broken notes to
// UnknownBucket and real-bucket notes to their canonical bucket.
//
// RED in task 16-02-02 (test exists, code lacks guard), GREEN in
// 16-02-03 (guard added).
func TestGroupAtomics_ExplicitlyUncategorized_Dropped(t *testing.T) {
	uncatBody := "# Uncategorized\n#test | [[]]\n---\n\nuncategorized body\n"
	brokenBody := "# Broken\n\nno canonical header line at all.\n"
	realBody := "# Real\n#test/Поезії | [[Index]]\n---\n\nreal body\n"

	rec := &parseMetaRecorder{
		matches: map[string]domain.AtomicMeta{
			uncatBody: {ExplicitlyUncategorized: true},
			realBody:  {Bucket: "Поезії"},
			// brokenBody => AtomicMeta{} (zero value, no match)
		},
	}

	d := &domain.Domain{
		Tag:           "test",
		CanonicalTag:  "#test",
		IndexTitle:    "✱ Index",
		UnknownBucket: "Невідомі",
		ParseMeta:     rec.makeParseMeta(),
		// Leave ParseMasterTable, RenderHub, CanonicalTagFor nil so no
		// override layers fire — groupAtomics operates purely on
		// canonical headers.
	}

	notes := []domain.Note{
		{ID: "uncat-1", Title: "Uncategorized", Content: uncatBody},
		{ID: "broken-1", Title: "Broken", Content: brokenBody},
		{ID: "real-1", Title: "Real", Content: realBody},
	}

	result := d.RouteAtomics(notes, nil)
	groups := result.Groups

	// 1. Uncategorized note must NOT appear in any bucket.
	for bucket, groupNotes := range groups {
		for _, n := range groupNotes {
			if n.ID == "uncat-1" {
				t.Errorf("uncategorized note found in bucket %q — should be dropped", bucket)
			}
		}
	}

	// 2. Broken note (no canonical header) must appear under UnknownBucket.
	foundBroken := false
	for _, n := range groups[d.UnknownBucket] {
		if n.ID == "broken-1" {
			foundBroken = true
			break
		}
	}
	if !foundBroken {
		t.Errorf("broken note (no canonical header) should be in %q bucket", d.UnknownBucket)
	}

	// 3. Real-bucket note must appear under its bucket.
	foundReal := false
	for _, n := range groups["Поезії"] {
		if n.ID == "real-1" {
			foundReal = true
			break
		}
	}
	if !foundReal {
		t.Errorf("real-bucket note should be in %q bucket", "Поезії")
	}
}

// TestOverrideForNote_Uncategorized_SkipsMasterOverride verifies that
// overrideForNote returns ("", false) when a note is explicitly
// uncategorized (ExplicitlyUncategorized: true) even if the master
// table places it in a bucket. This is the "leave in master as-is"
// policy — the master override does not fight a user's deliberate
// empty-bucket choice, eliminating bidirectional ping-pong.
func TestOverrideForNote_Uncategorized_SkipsMasterOverride(t *testing.T) {
	uncatBody := "# Uncategorized\n#test | [[]]\n---\n\nbody\n"
	masterBody := "# ✱ Index\n#test\n---\n\n## OldBucket (1)\n- [[Uncategorized]]\n"

	rec := &parseMetaRecorder{
		matches: map[string]domain.AtomicMeta{
			uncatBody: {ExplicitlyUncategorized: true},
		},
	}

	d := &domain.Domain{
		Tag:           "test",
		CanonicalTag:  "#test",
		IndexTitle:    "✱ Index",
		UnknownBucket: "Невідомі",
		ParseMeta:     rec.makeParseMeta(),
		ParseMasterTable: func(_ *domain.Domain, _ string) map[string]string {
			// Simulate master table parsing: the uncategorized note
			// appears under "OldBucket" in the master.
			return map[string]string{"Uncategorized": "OldBucket"}
		},
	}

	notes := []domain.Note{
		{ID: "master-1", Title: "✱ Index", Content: masterBody},
		{ID: "uncat-1", Title: "Uncategorized", Content: uncatBody},
	}

	result := d.RouteAtomics(notes, nil)

	// MasterClaims must be 0 — the uncategorized note's override was
	// skipped because ExplicitlyUncategorized guards overrideForNote.
	if result.MasterClaims != 0 {
		t.Errorf("MasterClaims = %d, want 0 — uncategorized note should not trigger master override",
			result.MasterClaims)
	}
}
