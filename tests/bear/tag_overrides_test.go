// Package bear_test holds external tests for github.com/barad1tos/noxctl/bear/domain.
//
// tag_overrides_test.go locks down the computeTagOverrides primitive — the
// third override layer sibling to computeMasterOverrides / computeHubOverrides.
// Tests reach the unexported method through the ComputeTagOverridesForTest
// seam (mirrors ProcessAtomicForTest in bear/domain/upserts.go).
//
// Each sub-test name encodes the branch it covers (one t.Run per branch of
// the computeTagOverrides spec); the per-row name strings in this file are
// the single source of truth for algorithm coverage.
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
	return &domain.Domain{
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
// can assert the strict-mode warning format. Mirrors captureLog in
// tests/bear/engine/mtime_poll_test.go — same restore-on-cleanup
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

// tagOverrideCase declares one shape-only sub-test: each row carries only
// the bits that vary across the algorithm-branch matrix (note ID, tag
// array, canonical bucket in body, optional domain mutation). The shared
// noteFrom helper rebuilds the domain.Note literal so the per-row body
// stays small enough to keep the duplicate-block linter quiet.
type tagOverrideCase struct {
	name            string
	mutateDomain    func(d *domain.Domain)
	noteID          string
	noteTitle       string
	noteTags        []string
	canonicalInBody string
	wantOverrides   map[string]string
	wantNilMap      bool // distinguishes nil-return (blueprint gate) from empty map
}

// noteFrom assembles a single-note slice from row parameters. Centralizing
// construction strips the repeated domain.Note literal from every case body
// (each literal carries 50+ duplicate tokens, which trips the dupl linter
// once 4+ shape-only rows share the shape).
func noteFrom(tc tagOverrideCase) []domain.Note {
	return []domain.Note{{
		ID:      tc.noteID,
		Title:   tc.noteTitle,
		Tags:    tc.noteTags,
		Content: canonicalBody(tc.canonicalInBody),
	}}
}

// runShapeOnly executes one row's algorithm call and asserts the override
// map's shape. Extracted so TestComputeTagOverrides stays under the
// gocognit ≤15 budget — the per-row branch count stays at 3 (nil check,
// length check, value-by-key check), shared across every shape-only row.
func runShapeOnly(t *testing.T, tc tagOverrideCase) {
	t.Helper()
	d := buildWorkDomain()
	if tc.mutateDomain != nil {
		tc.mutateDomain(d)
	}
	got := d.ComputeTagOverridesForTest(noteFrom(tc))
	if tc.wantNilMap {
		if got != nil {
			t.Errorf("expected nil map, got %v", got)
		}
		return
	}
	if len(got) != len(tc.wantOverrides) {
		t.Errorf("override count mismatch: got %d (%v), want %d (%v)",
			len(got), got, len(tc.wantOverrides), tc.wantOverrides)
	}
	for id, wantBucket := range tc.wantOverrides {
		if got[id] != wantBucket {
			t.Errorf("note %s: got bucket %q, want %q (full map: %v)",
				id, got[id], wantBucket, got)
		}
	}
}

// TestComputeTagOverrides locks the algorithm contract for the third
// override layer.
//
// The strict-mode warning case lives in its own t.Run because it asserts
// log content; the other rows share runShapeOnly to keep the
// duplicate-block linter quiet and the test function's cognitive
// complexity below the ≤15 threshold.
//
//cyrillic:permit
func TestComputeTagOverrides(t *testing.T) {
	t.Run("MultipleNonCanonical_SkipsWithWarning", func(t *testing.T) {
		buf := captureTagOverrideLog(t)
		d := buildWorkDomain()
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
		assertWarningLog(t, buf.String())
	})

	shapeCases := []tagOverrideCase{
		{
			name:            "SingleNonCanonical_Fires",
			noteID:          "note-001",
			noteTitle:       "Daily task",
			noteTags:        []string{"#work", "#work/tasks"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{"note-001": "tasks"},
		},
		{
			name:            "NonWhitelistedSubTag_Ignored",
			noteID:          "note-003",
			noteTitle:       "Random tag",
			noteTags:        []string{"#work", "#work/randomstuff"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
		{
			name:            "TagsMatchCanonical_NoOverride",
			noteID:          "note-004",
			noteTitle:       "Already canonical",
			noteTags:        []string{"#work", "#work/tasks"},
			canonicalInBody: "tasks",
			wantOverrides:   map[string]string{},
		},
		{
			name:            "NonSubTagBlueprint_EarlyReturn",
			mutateDomain:    func(d *domain.Domain) { d.CanonicalTagFor = nil },
			noteID:          "note-005",
			noteTitle:       "Would normally fire",
			noteTags:        []string{"#work", "#work/tasks"},
			canonicalInBody: "інше",
			wantNilMap:      true,
		},
		{
			name:            "UnknownBucketAsSubTag_NoOverride",
			noteID:          "note-006",
			noteTitle:       "Unknown bucket sub-tag",
			noteTags:        []string{"#work", "#work/інше"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
		{
			name:            "NoFamilyTagsAtAll_Skip",
			noteID:          "note-007",
			noteTitle:       "Bare family tag only",
			noteTags:        []string{"#work"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
		{
			name:            "MissingDomainTag_Skipped",
			noteID:          "note-008",
			noteTitle:       "No domain tag",
			noteTags:        []string{"#work/tasks"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
	}
	for _, tc := range shapeCases {
		t.Run(tc.name, func(t *testing.T) { runShapeOnly(t, tc) })
	}
}

// assertWarningLog checks every required substring of the strict-mode
// warning. Lives outside TestComputeTagOverrides so the parent function's
// branch count stays low; the assertions themselves are linear.
//
//cyrillic:permit
func assertWarningLog(t *testing.T, logged string) {
	t.Helper()
	if !strings.Contains(logged, "ambiguous tag intent") {
		t.Errorf("missing strict-mode warning marker, got log: %q", logged)
	}
	if !strings.Contains(logged, "note-002") {
		t.Errorf("warning should name the offending note ID, got: %q", logged)
	}
	if !strings.Contains(logged, "keeping canonical=інше") {
		t.Errorf("warning should report the canonical bucket, got: %q", logged)
	}
	if !strings.Contains(logged, "tasks") || !strings.Contains(logged, "development") {
		t.Errorf("warning should list both non-canonical sub-tags, got: %q", logged)
	}
}
