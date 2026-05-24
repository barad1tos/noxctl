// Package bear_test holds external tests for github.com/barad1tos/noxctl/bear/domain.
//
// tag_overrides_test.go locks down the computeTagOverrides primitive — the
// third override layer sibling to computeMasterOverrides / computeHubOverrides.
// Tests reach the unexported method through the ComputeTagOverridesForTest
// seam (mirrors ProcessAtomicForTest in bear/domain/upserts.go).
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
// noteFrom helper rebuilds the domain.Note literal — shared to keep `dupl` quiet.
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

// noteFrom assembles a single-note slice from row parameters — shared to
// keep `dupl` quiet by stripping the repeated domain.Note literal from
// every case body.
func noteFrom(tc tagOverrideCase) []domain.Note {
	return []domain.Note{{
		ID:      tc.noteID,
		Title:   tc.noteTitle,
		Tags:    tc.noteTags,
		Content: canonicalBody(tc.canonicalInBody),
	}}
}

// runShapeOnly executes one row's algorithm call and asserts the override
// map's shape — per-row checks stay at three: nil-map, count match, and
// value-by-key match.
func runShapeOnly(t *testing.T, tc tagOverrideCase) {
	t.Helper()
	d := buildWorkDomain()
	if tc.mutateDomain != nil {
		tc.mutateDomain(d)
	}
	got, conflicts := d.ComputeTagOverridesForTest(noteFrom(tc))
	if tc.wantNilMap {
		if got != nil {
			t.Errorf("expected nil map, got %v", got)
		}
		return
	}
	if conflicts != 0 {
		t.Errorf("shape-only case must not trigger conflicts, got count=%d", conflicts)
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
// duplicate-block linter quiet.
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
		got, conflicts := d.ComputeTagOverridesForTest(notes)
		if len(got) != 0 {
			t.Errorf("override map must be empty when the only atom is ambiguous, got %v", got)
		}
		if _, present := got["note-002"]; present {
			t.Errorf("note-002 should NOT be in override map, got %v", got)
		}
		if conflicts != 1 {
			t.Errorf("conflict count = %d, want 1 (ambiguous-intent branch must increment counter)", conflicts)
		}
		assertWarningLog(t, buf.String())
	})

	t.Run("NonWhitelistedSubTag_LogsFilterReason", runNonWhitelistedSubTagCase)
	t.Run("MultipleAtomsConflict_CountsTwo", runMultipleAtomsConflictCase)
	t.Run("NoCanonicalInBody_FallbackToUnknownBucket", runUnknownBucketFallbackCase)

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
		{
			// Guards against a factory regression that drops the Buckets
			// whitelist: with no entries to consult, every drag becomes a
			// silent no-op.
			name:            "EmptyBucketsWhitelist_NoOverrides",
			mutateDomain:    func(d *domain.Domain) { d.Buckets = nil },
			noteID:          "note-009",
			noteTitle:       "Drag with empty whitelist",
			noteTags:        []string{"#work", "#work/tasks"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
		{
			// nil Tags is a real value bearcli can serve when an atom has
			// no tags assigned during a transient index residue. The
			// slices.Contains nil-safety must keep us on the empty path.
			name:            "NilTags_Skipped",
			noteID:          "note-010",
			noteTitle:       "Atom without tags",
			noteTags:        nil,
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
		{
			// Pins the TrimPrefix tolerance — a sibling helper that
			// passes hash-stripped tags here must still trigger the
			// override path.
			name:            "TagsWithoutHashPrefix_Tolerated",
			noteID:          "note-011",
			noteTitle:       "No hash on tag",
			noteTags:        []string{"work", "work/tasks"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{"note-011": "tasks"},
		},
		{
			// Depth-3 sub-tags are out of the 2-level Bear tag-tree
			// invariant; gatherWhitelistedSubTags rejects them at the
			// strings.Contains("/") check and the override must not fire.
			name:            "DeepSubTag_Ignored",
			noteID:          "note-012",
			noteTitle:       "Three-level sub-tag",
			noteTags:        []string{"#work", "#work/tasks/urgent"},
			canonicalInBody: "інше",
			wantOverrides:   map[string]string{},
		},
	}
	for _, tc := range shapeCases {
		t.Run(tc.name, func(t *testing.T) { runShapeOnly(t, tc) })
	}
}

// runNonWhitelistedSubTagCase verifies the non-whitelist filter emits
// its operator-facing log line without bumping the conflict counter.
// Extracted from TestComputeTagOverrides so the parent function stays
// under gocognit ≤15.
//
//cyrillic:permit
func runNonWhitelistedSubTagCase(t *testing.T) {
	buf := captureTagOverrideLog(t)
	d := buildWorkDomain()
	notes := []domain.Note{{
		ID:      "note-nonwhitelist",
		Title:   "Drag with unknown sub-tag",
		Tags:    []string{"#work", "#work/randomstuff"},
		Content: canonicalBody("інше"),
	}}
	got, conflicts := d.ComputeTagOverridesForTest(notes)
	if len(got) != 0 {
		t.Errorf("non-whitelist sub-tag must NOT produce override, got %v", got)
	}
	if conflicts != 0 {
		t.Errorf("non-whitelist filter must not bump conflict counter, got %d", conflicts)
	}
	if !strings.Contains(buf.String(), "non-whitelist sub-tag") {
		t.Errorf("missing non-whitelist filter log line, got: %q", buf.String())
	}
}

// runMultipleAtomsConflictCase asserts that two ambiguous atoms each
// increment the conflict counter to a total of 2 (per-atom counting,
// not per-pair). Extracted from TestComputeTagOverrides for gocognit.
//
//cyrillic:permit
func runMultipleAtomsConflictCase(t *testing.T) {
	buf := captureTagOverrideLog(t)
	d := buildWorkDomain()
	ambiguous := []string{"#work", "#work/tasks", "#work/development"}
	notes := []domain.Note{
		{ID: "atom-A", Title: "Ambiguous A", Tags: ambiguous, Content: canonicalBody("інше")},
		{ID: "atom-B", Title: "Ambiguous B", Tags: ambiguous, Content: canonicalBody("інше")},
	}
	got, conflicts := d.ComputeTagOverridesForTest(notes)
	if len(got) != 0 {
		t.Errorf("both ambiguous atoms should skip override, got %v", got)
	}
	if conflicts != 2 {
		t.Errorf("conflict count = %d, want 2 (one per ambiguous atom)", conflicts)
	}
	if !strings.Contains(buf.String(), "ambiguous tag intent") {
		t.Errorf("missing ambiguous-intent warning, got log: %q", buf.String())
	}
}

// runUnknownBucketFallbackCase covers the fallback branch inside
// computeTagOverrides where ParseMetaFromSubTag returns an empty bucket
// (the atom body has no canonical tag-line yet — fresh UI-created
// note pre-fast-pass). The algorithm must substitute d.UnknownBucket
// as the canonical baseline before calling decideOverride so the
// drag-tagged sub-tag still triggers a re-bucket override. Without
// this fallback a freshly-created note with a sidebar-drag chip would
// silently no-op until canonicalization caught up on a later cycle.
//
//cyrillic:permit
func runUnknownBucketFallbackCase(t *testing.T) {
	d := buildWorkDomain()
	notes := []domain.Note{{
		ID:      "note-no-canonical",
		Title:   "Fresh UI note, no canonical tag-line in body",
		Tags:    []string{"#work", "#work/tasks"},
		Content: "# Fresh heading\n\nbody without any canonical line.\n",
	}}
	got, conflicts := d.ComputeTagOverridesForTest(notes)
	if conflicts != 0 {
		t.Errorf("conflict count = %d, want 0 (single whitelisted sub-tag must not be ambiguous)", conflicts)
	}
	if len(got) != 1 {
		t.Fatalf("override count = %d, want 1; got map %v", len(got), got)
	}
	if got["note-no-canonical"] != "tasks" {
		t.Errorf("note-no-canonical bucket = %q, want %q (fallback canonical=UnknownBucket → decideOverride must fire)",
			got["note-no-canonical"], "tasks")
	}
}

// assertWarningLog asserts the strict-mode warning marker is present.
// One substring keeps the test resilient to message reformatting; the
// behavior assertions (override map empty, conflict counter == 1) at
// the call site lock the algorithm's externally observable effect.
//
//cyrillic:permit
func assertWarningLog(t *testing.T, logged string) {
	t.Helper()
	if !strings.Contains(logged, "ambiguous tag intent") {
		t.Errorf("missing strict-mode warning marker, got log: %q", logged)
	}
}
