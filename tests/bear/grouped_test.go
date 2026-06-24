// Package bear_test holds external tests for github.com/barad1tos/noxctl/domain. Living
// outside the bear package itself keeps source files unmixed with test
// files; the trade-off is the tests can only exercise the exported API.
//
// The bear package's public surface is wide enough that everything below
// is reachable: callbacks (CanonicalTagFor, HubTitleFor,...) are exposed
// directly via Domain fields, so tests can invoke them without going
// through unexported helper methods.
package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

func TestParseMetaFromSubTag(t *testing.T) {
	d := &domain.Domain{Tag: "english", CanonicalTag: "#english", IndexTitle: "✱ English"}
	cases := []struct {
		name string
		body string
		want domain.AtomicMeta
	}{
		{
			name: "happy path with master backlink",
			body: "# Homework\n#english/homework | [[✱ English]]\n---\n\nbody",
			want: domain.AtomicMeta{Bucket: "homework"},
		},
		{
			name: "hub-routed canonical (target != master)",
			body: "# X\n#english/rules | [[english · rules]]\n---\n",
			want: domain.AtomicMeta{Bucket: "rules"},
		},
		{
			name: "bare tag with IndexTitle backlink — skipped, empty meta",
			body: "# X\n#english | [[✱ English]] | foo\n---\n",
			want: domain.AtomicMeta{},
		},
		{
			name: "bare tag with empty wikilink — explicitly uncategorized",
			body: "# X\n#english | [[]]\n---\n",
			want: domain.AtomicMeta{ExplicitlyUncategorized: true},
		},
		{
			name: "bare tag with real non-IndexTitle bucket — detected by secondary pass",
			body: "# X\n#english | [[RealBucket]]\n---\n",
			want: domain.AtomicMeta{Bucket: "RealBucket"},
		},
		{
			name: "extra tags before pipe",
			body: "# X\n#english/vocabulary #other | [[✱ English]]\n---\n",
			want: domain.AtomicMeta{Bucket: "vocabulary"},
		},
		{
			name: "with section in third segment",
			body: "# X\n#english/homework | [[✱ English]] | week-3\n---\n",
			want: domain.AtomicMeta{Bucket: "homework", Section: "week-3"},
		},
		{
			name: "wrong family — empty",
			body: "# X\n#claude/sessions | [[✱ Claude]]\n---\n",
			want: domain.AtomicMeta{},
		},
		{
			name: "no header zone separator — still scans",
			body: "# X\n#english/rules | [[✱ English]]\n",
			want: domain.AtomicMeta{Bucket: "rules"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ParseMetaFromSubTag(d, tc.body)
			if got != tc.want {
				t.Errorf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestRenderMasterFlatGrouped(t *testing.T) {
	d := &domain.Domain{Tag: "english", CanonicalTag: "#english", IndexTitle: "✱ English"}
	groups := map[string][]domain.Note{
		"homework": {{ID: "1", Title: "Verb tenses"}, {ID: "2", Title: "Adjectives"}},
		"rules":    {{ID: "3", Title: "Articles"}},
	}
	out := render.MasterFlatGrouped(d, groups, []string{"homework", "rules"})
	if !strings.HasPrefix(out, "# ✱ English\n#english | [Нова нотатка](") {
		t.Errorf("missing master header, got prefix: %q", out[:min(80, len(out))])
	}
	// Bootstrap URL form: outer URL carries `text=` (encoded canonical body)
	// with the inner `tags=english` doubly-encoded as `tags%3Denglish`.
	if !strings.Contains(out, "tags%3Denglish") {
		t.Error("master header bootstrap URL should carry the doubly-encoded inner tag")
	}
	if strings.Contains(out, "title=") {
		t.Error("master header link must NOT carry title= (spec component 1) — title is stamped via StampDatetimeH1 at regen time")
	}
	if !strings.Contains(out, "&open_note=yes") {
		t.Error("master header link should ask Bear to open the new note after creation")
	}
	if !strings.Contains(out, "## homework (2)") {
		t.Error("homework section header missing")
	}
	if !strings.Contains(out, "## rules (1)") {
		t.Error("rules section header missing")
	}
	adjPos := strings.Index(out, "Adjectives")
	verbPos := strings.Index(out, "Verb tenses")
	if adjPos < 0 || verbPos < 0 || adjPos > verbPos {
		t.Error("homework atoms not alphabetised")
	}
	if strings.Contains(out, "## absent") {
		t.Error("empty buckets should not render")
	}
}

func TestRenderMasterFlatGroupedSkipsEmptyBuckets(t *testing.T) {
	d := &domain.Domain{Tag: "x", CanonicalTag: "#x", IndexTitle: "✱ X"}
	out := render.MasterFlatGrouped(d, map[string][]domain.Note{}, []string{"a", "b"})
	if strings.Contains(out, "## a") || strings.Contains(out, "## b") {
		t.Error("empty groups should produce no `##` sections")
	}
}

func TestParseMasterFlatGrouped(t *testing.T) {
	master := "# ✱ English\n#english\n---\n\n## homework (2)\n- [[Verb tenses]]\n- [[Adjectives]]\n\n## rules (1)\n- [[Articles]]\n"
	got := domain.ParseMasterFlatGrouped(nil, master)
	want := map[string]string{
		"Verb tenses": "homework",
		"Adjectives":  "homework",
		"Articles":    "rules",
	}
	if len(got) != len(want) {
		t.Fatalf("size mismatch: got %d, want %d (got=%v)", len(got), len(want), got)
	}
	for key, expectedBucket := range want {
		if got[key] != expectedBucket {
			t.Errorf("%q: got %q, want %q", key, got[key], expectedBucket)
		}
	}
}

func TestParseMasterFlatGroupedExtractsBearURLIDs(t *testing.T) {
	master := "## sessions (1)\n- [Foo](bear://x-callback-url/open-note?id=ABC-123)\n"
	got := domain.ParseMasterFlatGrouped(nil, master)
	if got["ABC-123"] != "sessions" {
		t.Errorf("expected ABC-123 → sessions, got %v", got)
	}
}

func TestParseMasterFlatGroupedIgnoresH3(t *testing.T) {
	master := "## homework (3)\n- [[A]]\n### sub-section\n- [[B]]\n## rules (1)\n- [[C]]\n"
	got := domain.ParseMasterFlatGrouped(nil, master)
	// ## homework is current section; H3 doesn't switch buckets, so B stays
	// in homework. ## rules switches; C goes there.
	if got["A"] != "homework" || got["B"] != "homework" || got["C"] != "rules" {
		t.Errorf("got %v", got)
	}
}

func TestSubTagCanonical(t *testing.T) {
	d := &domain.Domain{Tag: "claude", CanonicalTag: "#claude"}
	if got := render.SubTagCanonical(d, "sessions"); got != "#claude/sessions" {
		t.Errorf("got %q", got)
	}
	if got := render.SubTagCanonical(d, ""); got != "#claude" {
		t.Errorf("empty bucket should fall back to top-level: got %q", got)
	}
}

// TestBucketFromSubTag locks the sticky-creation contract: when a note's
// Bear tag-tree carries a `<top>/<sub>` pair matching the domain family,
// BucketFromSubTag extracts `<sub>` so groupAtomics can use it as the
// bucket BEFORE falling back to d.UnknownBucket.
//
// Covers the original bug — user creates `#development/ayu-jetbrains` in
// Bear sidebar, daemon used to silently reroute the note into UnknownBucket
// (`інше`) because the body lacked a canonical-header line. With this fix
// the tag-tree signal is honored.
func TestBucketFromSubTag(t *testing.T) {
	d := &domain.Domain{Tag: "development"}
	cases := []struct {
		name string
		tags []string
		want string
	}{
		{"single sub-tag", []string{"development", "development/ayu-jetbrains"}, "ayu-jetbrains"},
		{"family only", []string{"development"}, ""},
		{"empty tags", nil, ""},
		{"unrelated family", []string{"library", "library/poetry"}, ""},
		{"deep tag rejected", []string{"development/a/b"}, ""},
		{"first match wins", []string{"development/noxctl", "development/genreupdater"}, "noxctl"},
		{"prefix-only no slash", []string{"development_extra"}, ""},
		{"bearcli # prefix sub-tag", []string{"#development", "#development/ayu-jetbrains"}, "ayu-jetbrains"},
		{"bearcli # prefix family only", []string{"#development"}, ""},
		{"bearcli # prefix deep reject", []string{"#development/a/b"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := domain.BucketFromSubTag(d, c.tags)
			if got != c.want {
				t.Errorf("BucketFromSubTag(tags=%v) = %q, want %q", c.tags, got, c.want)
			}
		})
	}
}

// TestBucketFromSubTag_NoOpForFlatBlueprint guards the no-op property for
// non-sub-tag blueprints: tags like `library/poetry` (a 2-segment family
// tag, not a sub-tag) never match BucketFromSubTag for a poetry domain
// since the prefix `library/poetry/` doesn't appear in any tag.
func TestBucketFromSubTag_NoOpForFlatBlueprint(t *testing.T) {
	d := &domain.Domain{Tag: "library/poetry"}
	got := domain.BucketFromSubTag(d, []string{"library/poetry"})
	if got != "" {
		t.Errorf("flat-blueprint poetry: BucketFromSubTag should be no-op, got %q", got)
	}
}
