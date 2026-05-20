// Package bear_test — generic master-section renderer coverage.
//
// User-scenario framing: every test mimics what an operator
// declares in TOML `[[domain.master_section]]` blocks and what they
// expect to see in the rendered master output. The renderer covers
// three orthogonal axes:
//
//  1. Selection rule — explicit `buckets` / `script` predicate /
//     catch-all (neither set).
//  2. Cross-section precedence — earlier sections claim buckets,
//     later catch-alls only sweep the remainder.
//  3. Presentation knobs — count_mode (notes vs buckets) and
//     show_bullet_counts (with/without `(N)` suffix per bullet).
//
// Tests drive `domain.BuildMasterSections` directly so the assertions
// stay close to the predicate logic without going through the full
// rendering pipeline.
package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
)

// fakeNote returns N synthetic notes whose only field that matters
// for section counting is the slice length.
func fakeNotes(count int) []domain.Note {
	out := make([]domain.Note, count)
	for i := range out {
		out[i] = domain.Note{ID: "id", Title: "t"}
	}
	return out
}

// domainWithSections is a minimal *domain.Domain shaped just enough
// for BuildMasterSections — Tag/Title/UnknownBucket primitives plus
// the MasterSections slice the renderer reads.
func domainWithSections(unknownBucket string, sections []domain.MasterSection) *domain.Domain {
	return &domain.Domain{
		Tag:            "test",
		UnknownBucket:  unknownBucket,
		MasterSections: sections,
	}
}

// TestBuildMasterSections_ExplicitBuckets_OperatorPicksColumns —
// the typical "I want these languages in column X" case. Operator
// lists explicit bucket names; renderer emits them sorted by title
// regardless of declaration order.
func TestBuildMasterSections_ExplicitBuckets_OperatorPicksColumns(t *testing.T) {
	groups := map[string][]domain.Note{
		"python": fakeNotes(3),
		"go":     fakeNotes(2),
		"rust":   fakeNotes(1),
	}
	d := domainWithSections("", []domain.MasterSection{{
		Title:            "Languages",
		Buckets:          []string{"rust", "python", "go"},
		ShowBulletCounts: true,
	}})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len(sections) = %d, want 1", len(out))
	}
	if !strings.HasPrefix(out[0].Header, "Languages (6)") {
		t.Errorf("header = %q, want notes-mode count 6", out[0].Header)
	}
	want := []string{"[[go]] (2)", "[[python]] (3)", "[[rust]] (1)"}
	for i, w := range want {
		if i >= len(out[0].Bullets) || out[0].Bullets[i] != w {
			t.Errorf("bullets[%d] = %q, want %q", i, out[0].Bullets[i], w)
		}
	}
}

// TestBuildMasterSections_ClaimPrecedence_LaterSectionsSkipClaimedBuckets
// — the load-bearing invariant: section A's explicit buckets stay
// exclusive even when section B is a catch-all that would otherwise
// sweep them.
func TestBuildMasterSections_ClaimPrecedence_LaterSectionsSkipClaimedBuckets(t *testing.T) {
	groups := map[string][]domain.Note{
		"python":  fakeNotes(1),
		"infra":   fakeNotes(2),
		"unknown": fakeNotes(3),
	}
	d := domainWithSections("", []domain.MasterSection{
		{Title: "Dev", Buckets: []string{"python"}},
		{Title: "Other"}, // catch-all
	})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	if !strings.Contains(strings.Join(out[0].Bullets, " "), "python") {
		t.Errorf("Dev should claim python; got %v", out[0].Bullets)
	}
	for _, b := range out[1].Bullets {
		if strings.Contains(b, "python") {
			t.Errorf("Other catch-all swept already-claimed python: %v", out[1].Bullets)
		}
	}
}

// TestBuildMasterSections_ScriptLatin_NonLatinBinaryPartition — the
// lyrics-style "Latin artists here, everything else there" pattern.
// Cyrillic / Greek / digit-leading buckets all land in non-latin.
func TestBuildMasterSections_ScriptLatin_NonLatinBinaryPartition(t *testing.T) {
	groups := map[string][]domain.Note{
		"AC/DC":    fakeNotes(1), // Latin
		"Sting":    fakeNotes(1), // Latin
		"Кіно":     fakeNotes(1), // Cyrillic
		"葉志田":      fakeNotes(1), // CJK
		"123Group": fakeNotes(1), // digit
	}
	d := domainWithSections("", []domain.MasterSection{
		{Title: "Latin", Script: "latin"},
		{Title: "Other", Script: "non-latin"},
	})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2", len(out))
	}
	latinBullets := strings.Join(out[0].Bullets, " ")
	otherBullets := strings.Join(out[1].Bullets, " ")
	if !strings.Contains(latinBullets, "AC/DC") || !strings.Contains(latinBullets, "Sting") {
		t.Errorf("Latin section missing Latin buckets: %v", out[0].Bullets)
	}
	for _, nonLatin := range []string{"Кіно", "葉志田", "123Group"} {
		if !strings.Contains(otherBullets, nonLatin) {
			t.Errorf("non-latin section missing %q: %v", nonLatin, out[1].Bullets)
		}
		if strings.Contains(latinBullets, nonLatin) {
			t.Errorf("Latin section leaked non-Latin %q: %v", nonLatin, out[0].Bullets)
		}
	}
}

// TestBuildMasterSections_CatchAll_SkipsUnknownBucket — the documented
// contract for `UnknownBucket`: it never auto-appears in script-class
// or catch-all sections. Operators wanting it surfaced must include
// it in an explicit Buckets list.
func TestBuildMasterSections_CatchAll_SkipsUnknownBucket(t *testing.T) {
	groups := map[string][]domain.Note{
		"normal":   fakeNotes(1),
		"Невідомі": fakeNotes(5),
	}
	d := domainWithSections("Невідомі", []domain.MasterSection{
		{Title: "Catch-all"},
	})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	for _, b := range out[0].Bullets {
		if strings.Contains(b, "Невідомі") {
			t.Errorf("unknown bucket leaked into catch-all: %v", out[0].Bullets)
		}
	}
}

// TestBuildMasterSections_CatchAll_AcceptsUnknownBucketWhenExplicit —
// the escape hatch: explicit `buckets = ["Невідомі"]` overrides the
// skip-unknown default so operators who want it surfaced can ask
// loudly.
func TestBuildMasterSections_CatchAll_AcceptsUnknownBucketWhenExplicit(t *testing.T) {
	groups := map[string][]domain.Note{
		"Невідомі": fakeNotes(2),
	}
	d := domainWithSections("Невідомі", []domain.MasterSection{
		{Title: "Misc", Buckets: []string{"Невідомі"}},
	})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len(sections) = %d, want 1", len(out))
	}
	if len(out[0].Bullets) != 1 {
		t.Fatalf("len(bullets) = %d, want 1", len(out[0].Bullets))
	}
	if !strings.Contains(out[0].Bullets[0], "Невідомі") {
		t.Errorf("explicit Невідомі bucket not surfaced: %v", out[0].Bullets)
	}
}

// TestBuildMasterSections_EmptySection_DropsOut — every renderer-side
// branch that produces zero buckets must drop the section so the
// operator never sees a stale `## Section (0)` placeholder.
func TestBuildMasterSections_EmptySection_DropsOut(t *testing.T) {
	groups := map[string][]domain.Note{"present": fakeNotes(1)}
	d := domainWithSections("", []domain.MasterSection{
		{Title: "Missing", Buckets: []string{"absent"}},
		{Title: "Present", Buckets: []string{"present"}},
	})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1 (empty section must drop)", len(out))
	}
	if !strings.HasPrefix(out[0].Header, "Present") {
		t.Errorf("surviving header = %q, want Present", out[0].Header)
	}
}

// TestBuildMasterSections_CountModeBuckets_ReportsBucketCount — the
// lyrics-style "how many artists" header instead of "how many notes".
func TestBuildMasterSections_CountModeBuckets_ReportsBucketCount(t *testing.T) {
	groups := map[string][]domain.Note{
		"a": fakeNotes(10),
		"b": fakeNotes(20),
		"c": fakeNotes(30),
	}
	d := domainWithSections("", []domain.MasterSection{{
		Title:     "All",
		CountMode: domain.CountModeBuckets,
	}})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if !strings.HasPrefix(out[0].Header, "All (3)") {
		t.Errorf("header = %q, want bucket-count 3 (not note-count 60)", out[0].Header)
	}
}

// TestBuildMasterSections_ShowBulletCountsFalse_EmitsPlainWikilinks —
// when the operator opts out of bullet counts, every bullet stays
// `[[bucket]]` without the `(N)` suffix.
func TestBuildMasterSections_ShowBulletCountsFalse_EmitsPlainWikilinks(t *testing.T) {
	groups := map[string][]domain.Note{
		"alpha": fakeNotes(7),
		"beta":  fakeNotes(3),
	}
	d := domainWithSections("", []domain.MasterSection{{
		Title:            "Plain",
		ShowBulletCounts: false,
	}})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	for _, b := range out[0].Bullets {
		if strings.Contains(b, "(") || strings.Contains(b, ")") {
			t.Errorf("bullet has count suffix despite ShowBulletCounts=false: %q", b)
		}
	}
}

// TestBuildMasterSections_CountModeNotes_ExplicitZeroValue — pin the
// zero-value contract: `CountMode: domain.CountModeNotes` (explicitly
// the iota=0 value) reports note counts. A future re-ordering of
// the iota block in bear/domain.go would silently flip the default
// without this guard.
func TestBuildMasterSections_CountModeNotes_ExplicitZeroValue(t *testing.T) {
	groups := map[string][]domain.Note{
		"a": fakeNotes(4),
		"b": fakeNotes(6),
	}
	d := domainWithSections("", []domain.MasterSection{{
		Title:     "Notes",
		CountMode: domain.CountModeNotes,
	}})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	if !strings.HasPrefix(out[0].Header, "Notes (10)") {
		t.Errorf("header = %q, want note-count 10 (not bucket-count 2)", out[0].Header)
	}
}

// TestBuildMasterSections_ScriptSection_ClaimedAlready_DropsOut —
// when an earlier explicit section claims every Latin bucket, the
// later `script = "latin"` section produces zero bullets and must
// drop out entirely. Pins the empty-script-section path that no
// other test in this file reaches.
func TestBuildMasterSections_ScriptSection_ClaimedAlready_DropsOut(t *testing.T) {
	groups := map[string][]domain.Note{
		"alpha": fakeNotes(1),
		"beta":  fakeNotes(1),
		"Кіно":  fakeNotes(1),
	}
	// "Claimed" eats every Latin bucket so the later "LatinSweep"
	// script section finds nothing and must drop out entirely.
	sections := []domain.MasterSection{
		{Title: "Claimed", Buckets: []string{"alpha", "beta"}},
		{Title: "LatinSweep", Script: "latin"},
		{Title: "NonLatin", Script: "non-latin"},
	}
	d := domainWithSections("", sections)
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 2 {
		t.Fatalf("len = %d, want 2 (LatinSweep must drop out)", len(out))
	}
	for _, sec := range out {
		if strings.HasPrefix(sec.Header, "LatinSweep") {
			t.Errorf("empty script section did not drop out: %+v", sec)
		}
	}
}

// TestBuildMasterSections_ZeroNoteBucket_StaleHubDroppedSilently —
// when a bucket key is present in the groups map but its slice is
// empty (e.g. operator drained every atomic from a Tier-2 hub
// between renders), every selection rule must skip it so the master
// never surfaces a stale `[[hub]] (0)` bullet. Exercises the
// `len(groups[bucket]) == 0` guard in selectByExplicit /
// selectByScript / selectCatchAll.
func TestBuildMasterSections_ZeroNoteBucket_StaleHubDroppedSilently(t *testing.T) {
	groups := map[string][]domain.Note{
		"alive": fakeNotes(2),
		"drain": {}, // present, but no atomics remain
	}
	// Explicit section names BOTH buckets — guard must filter `drain`
	// (zero notes) while keeping `alive`. Covers the
	// `len(groups[bucket]) == 0` branch in selectByExplicit; the
	// script/catch-all variants share the same guard implementation
	// in sectioned.go and don't need separate fixtures.
	d := domainWithSections("", []domain.MasterSection{
		{Title: "Hubs", Buckets: []string{"alive", "drain"}},
	})
	out := domain.BuildMasterSections(d, groups)
	if len(out) != 1 {
		t.Fatalf("len = %d, want 1", len(out))
	}
	bullets := strings.Join(out[0].Bullets, " ")
	if strings.Contains(bullets, "drain") {
		t.Errorf("stale (zero-note) bucket 'drain' surfaced: %v", bullets)
	}
	if !strings.Contains(bullets, "alive") {
		t.Errorf("alive bucket missing: %v", bullets)
	}
}

// TestSectionedMasterRenderer_Reinvocable — the closure returned by
// SectionedMasterRenderer must produce identical output across
// multiple invocations on the same domain + groups. Guards against
// someone adding hidden state (cache, counter) inside the closure.
func TestSectionedMasterRenderer_Reinvocable(t *testing.T) {
	groups := map[string][]domain.Note{
		"x": fakeNotes(2),
		"y": fakeNotes(3),
	}
	d := domainWithSections("", []domain.MasterSection{{
		Title: "All", Buckets: []string{"x", "y"}, ShowBulletCounts: true,
	}})
	render := domain.SectionedMasterRenderer()
	first := render(d, groups)
	second := render(d, groups)
	if first != second {
		t.Errorf("renderer produced divergent output on second call:\nfirst:\n%s\nsecond:\n%s", first, second)
	}
}
