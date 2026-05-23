// Package bear_test holds external tests for github.com/barad1tos/noxctl/bear/domain.
//
// tag_overrides_test.go locks down the computeTagOverrides primitive — the
// third override layer sibling to computeMasterOverrides / computeHubOverrides.
// Tests reach the unexported method through the ComputeTagOverridesForTest
// seam (mirrors ProcessAtomicForTest at bear/domain/upserts.go:153).
//
// Algorithm coverage matrix (one t.Run per branch of the spec in
// .planning/phases/12-tag-override-layer/12-CONTEXT.md):
//
//  1. SingleNonCanonical_Fires — happy path, sidebar drag adds one whitelisted
//     sub-tag and the canonical header disagrees → override recorded.
//  2. MultipleNonCanonical_SkipsWithWarning — strict mode: two-plus
//     non-canonical sub-tags emit a warning and record no override.
//  3. NonWhitelistedSubTag_Ignored — sub-tag outside `Buckets ∪ {UnknownBucket}`
//     is filtered at step 3 of the algorithm.
//  4. TagsMatchCanonical_NoOverride — sub-tag agrees with canonical → no work.
//  5. NonSubTagBlueprint_EarlyReturn — `CanonicalTagFor == nil` short-circuits.
//  6. UnknownBucketAsSubTag_NoOverride — unknown bucket passes whitelist but
//     agrees with canonical → no override.
//  7. NoFamilyTagsAtAll_Skip — only the family tag, no sub-tags → no work.
//  8. MissingDomainTag_Skipped — domain-membership guard fires before step 3.
package bear_test

import (
	"bytes"
	"log"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
)

// buildWorkDomain returns a *domain.Domain mirroring Roman's `#work`
// grouped-vertical setup: family tag `work`, eight whitelisted buckets,
// "інше" as the unknown bucket, and a CanonicalTagFor closure that emits
// `#work/<bucket>` so the blueprint-gate at step 1 of computeTagOverrides
// passes. ParseMeta is wired to ParseMetaFromSubTag because the algorithm
// reads canonical bucket via that helper at step 4.
//
//cyrillic:permit
func buildWorkDomain() *domain.Domain {
	d := &domain.Domain{
		Tag:           "work",
		CanonicalTag:  "#work",
		IndexTitle:    "✱ Робота",
		UnknownBucket: "інше",
		Buckets: []string{
			"tasks", "development", "english", "health",
			"humor", "leisure", "instagram", "travel",
		},
		ParseMeta: domain.ParseMetaFromSubTag,
		CanonicalTagFor: func(d *domain.Domain, bucket string) string {
			if bucket == "" {
				return d.CanonicalTag
			}
			return d.CanonicalTag + "/" + bucket
		},
	}
	return d
}

// canonicalBody returns the minimal atomic-note body shape the algorithm's
// step-4 ParseMetaFromSubTag call recognizes: H1, canonical tag-line with
// `#work/<bucket> | [[✱ Робота]]`, separator, body.
//
//cyrillic:permit
func canonicalBody(bucket string) string {
	return "# Нова нотатка\n#work/" + bucket + " | [[✱ Робота]]\n---\n\nbody\n"
}

// captureTagOverrideLog redirects the package log to a buffer so tests
// can assert the strict-mode warning format. Mirrors captureLog at
// tests/bear/engine/mtime_poll_test.go:97 — same restore-on-cleanup
// discipline.
func captureTagOverrideLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})
	return &buf
}

// TestComputeTagOverrides locks the eight-case algorithm contract for the
// third override layer. RED-phase failure mode is "undefined:
// ComputeTagOverridesForTest"; GREEN phase requires every sub-test to pass.
func TestComputeTagOverrides(t *testing.T) {
	t.Run("SingleNonCanonical_Fires", func(t *testing.T) {
		d := buildWorkDomain()
		//cyrillic:permit
		notes := []domain.Note{{
			ID:      "note-001",
			Title:   "Daily task",
			Tags:    []string{"#work", "#work/tasks"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if want := "tasks"; got["note-001"] != want {
			t.Errorf("note-001: got %q, want %q (full map: %v)", got["note-001"], want, got)
		}
		if len(got) != 1 {
			t.Errorf("expected exactly one override, got %d (%v)", len(got), got)
		}
	})

	t.Run("MultipleNonCanonical_SkipsWithWarning", func(t *testing.T) {
		buf := captureTagOverrideLog(t)
		d := buildWorkDomain()
		//cyrillic:permit
		notes := []domain.Note{{
			ID:      "note-002",
			Title:   "Ambiguous",
			Tags:    []string{"#work", "#work/tasks", "#work/development"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if _, present := got["note-002"]; present {
			t.Errorf("note-002 should NOT be in override map, got %v", got)
		}
		logged := buf.String()
		if !strings.Contains(logged, "ambiguous tag intent") {
			t.Errorf("missing strict-mode warning marker, got log: %q", logged)
		}
		if !strings.Contains(logged, "note-002") {
			t.Errorf("warning should name the offending note ID, got: %q", logged)
		}
		//cyrillic:permit
		if !strings.Contains(logged, "keeping canonical=інше") {
			t.Errorf("warning should report the canonical bucket, got: %q", logged)
		}
		if !strings.Contains(logged, "tasks") || !strings.Contains(logged, "development") {
			t.Errorf("warning should list both non-canonical sub-tags, got: %q", logged)
		}
	})

	t.Run("NonWhitelistedSubTag_Ignored", func(t *testing.T) {
		d := buildWorkDomain()
		//cyrillic:permit
		notes := []domain.Note{{
			ID:      "note-003",
			Title:   "Random tag",
			Tags:    []string{"#work", "#work/randomstuff"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if len(got) != 0 {
			t.Errorf("non-whitelisted sub-tag must not produce overrides, got %v", got)
		}
	})

	t.Run("TagsMatchCanonical_NoOverride", func(t *testing.T) {
		d := buildWorkDomain()
		notes := []domain.Note{{
			ID:      "note-004",
			Title:   "Already canonical",
			Tags:    []string{"#work", "#work/tasks"},
			Content: canonicalBody("tasks"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if len(got) != 0 {
			t.Errorf("matching canonical must not produce overrides, got %v", got)
		}
	})

	t.Run("NonSubTagBlueprint_EarlyReturn", func(t *testing.T) {
		d := buildWorkDomain()
		// Disable the sub-tag preserving blueprint gate — algorithm step 1
		// must return nil regardless of input notes.
		d.CanonicalTagFor = nil
		notes := []domain.Note{{
			ID:      "note-005",
			Title:   "Would normally fire",
			Tags:    []string{"#work", "#work/tasks"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if got != nil {
			t.Errorf("non-sub-tag blueprint must return nil, got %v", got)
		}
	})

	t.Run("UnknownBucketAsSubTag_NoOverride", func(t *testing.T) {
		d := buildWorkDomain()
		// `інше` IS in Buckets ∪ {UnknownBucket} (the unknown bucket itself
		// passes whitelist step 3), but step 5 finds it agrees with canonical
		// → no override.
		//cyrillic:permit
		notes := []domain.Note{{
			ID:      "note-006",
			Title:   "Unknown bucket sub-tag",
			Tags:    []string{"#work", "#work/інше"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if len(got) != 0 {
			t.Errorf("unknown bucket matching canonical must not produce overrides, got %v", got)
		}
	})

	t.Run("NoFamilyTagsAtAll_Skip", func(t *testing.T) {
		d := buildWorkDomain()
		//cyrillic:permit
		notes := []domain.Note{{
			ID:      "note-007",
			Title:   "Bare family tag only",
			Tags:    []string{"#work"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if len(got) != 0 {
			t.Errorf("no sub-tags must not produce overrides, got %v", got)
		}
	})

	t.Run("MissingDomainTag_Skipped", func(t *testing.T) {
		d := buildWorkDomain()
		// Tags carry `#work/tasks` but NOT `#work` itself — the
		// domain-membership guard (step 2) fires before step 3.
		//cyrillic:permit
		notes := []domain.Note{{
			ID:      "note-008",
			Title:   "No domain tag",
			Tags:    []string{"#work/tasks"},
			Content: canonicalBody("інше"),
		}}
		got := d.ComputeTagOverridesForTest(notes)
		if len(got) != 0 {
			t.Errorf("notes missing the domain tag must not produce overrides, got %v", got)
		}
	})

}
