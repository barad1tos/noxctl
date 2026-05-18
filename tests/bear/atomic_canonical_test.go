// Package bear_test — atomic_canonical_test.go locks the sticky-H1
// spec's component-4 contract for umbrella master URL emission.
//
// The umbrella master's trailing "Нова нотатка" link must encode the
// umbrella's DefaultChild tag (a leaf domain) rather than the bare
// umbrella tag. Clicking the link in Bear creates a note pre-tagged
// with the leaf's `#<tag>`, so the resulting atom enters its canonical
// regen pipeline. Without this, SkipAtomicsPass=true on the umbrella
// would leave the new note orphaned.
package bear_test

import (
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// TestWriteMasterHeader_UmbrellaUsesDefaultChild locks spec component 4:
// the umbrella master's "Нова нотатка" link must encode the leaf-domain
// tag (DefaultChild), not the umbrella's bare tag. Clicks on the
// umbrella master should land in a tagged leaf domain so the resulting
// atom enters its canonical regen pipeline.
func TestWriteMasterHeader_UmbrellaUsesDefaultChild(t *testing.T) {
	leaf := &bear.Domain{
		Tag:          "quicknote/daily",
		CanonicalTag: "#quicknote/daily",
		IndexTitle:   "✱ Quicknote Daily",
		ParseMeta:    bear.DefaultParseMetaCanonical,
		RenderMaster: bear.DefaultRenderMasterFlat,
	}
	umbrella := bear.NewUmbrellaDomain("quicknote", "✱ Quicknote", "quicknote/daily",
		[]*bear.Domain{leaf})

	body := umbrella.RenderMaster(umbrella, map[string][]bear.Note{})

	// Bootstrap form: the inner `tags=` parameter lives doubly-encoded
	// inside the outer `text=` payload. DefaultChild tag (quicknote/daily)
	// must appear as `tags%3Dquicknote%252Fdaily` — `=` → `%3D`, `/` → `%2F`
	// → `%252F` after the outer encoding pass.
	if !strings.Contains(body, "tags%3Dquicknote%252Fdaily") {
		t.Errorf("umbrella master bootstrap URL must encode DefaultChild leaf tag:\n%s", body)
	}
	// The umbrella's bare `quicknote` tag must NOT appear as the inner
	// tags= value. After double-encoding it would surface as
	// `tags%3Dquicknote%26` (delimited by `&` post-encoding) or
	// `tags%3Dquicknote%29` (delimited by `)`). Neither must occur.
	if strings.Contains(body, "tags%3Dquicknote%26") || strings.Contains(body, "tags%3Dquicknote%29") {
		t.Errorf("umbrella master bootstrap URL must NOT carry the bare umbrella tag:\n%s", body)
	}
}

// TestUpsertAtomic_StampsH1WhenAbsent covers spec component 2 + 8: when
// an atom's body lacks a recognized H1 and the daemon canonicalises it,
// the rendered canonical body must lead with a stamped `# <NOW>` H1.
// Old behavior (synthesizing H1 from noteTitle) is replaced by
// deterministic datetime stamping.
func TestUpsertAtomic_StampsH1WhenAbsent(t *testing.T) {
	fixedNow := time.Date(2026, 5, 13, 15, 25, 0, 0, time.Local)
	bear.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	d := bear.NewFlatListDomain("library/quotes", "✱ Quotes")
	d.UnknownBucket = "_unknown"

	in := "#library/quotes\nbody content\n"
	out := bear.RenderAtomicCanonicalForTest(t, d, "Note Title", "_unknown", in)

	wantHead := "# 13 May 2026 at 15:25"
	if !strings.HasPrefix(out, wantHead+"\n") {
		t.Errorf("canonical body did not start with stamped H1:\n  got first line: %q\n  want: %q",
			strings.SplitN(out, "\n", 2)[0], wantHead)
	}
}

// TestUpsertAtomic_PreservesNonTagPreamble covers spec component 5:
// non-tag-line content above the canonical tag-line must be preserved
// in place after canonicalisation — between the H1 and the tag-line,
// NOT pushed below `---`. Pre-fix the rebuild logic moved preamble to
// the body zone; this regression test locks the new contract.
func TestUpsertAtomic_PreservesNonTagPreamble(t *testing.T) {
	fixedNow := time.Date(2026, 5, 13, 15, 25, 0, 0, time.Local)
	bear.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	d := bear.NewFlatListDomain("library/quotes", "✱ Quotes")
	d.UnknownBucket = "_unknown"

	in := "# Existing title\nuser preamble line\n#library/quotes | [[✱ Quotes]]\n---\nmain content\n"
	out := bear.RenderAtomicCanonicalForTest(t, d, "Note Title", "_unknown", in)

	lines := strings.Split(out, "\n")
	if len(lines) < 4 {
		t.Fatalf("canonical body too short:\n%s", out)
	}
	if lines[0] != "# Existing title" {
		t.Errorf("H1 not preserved: got %q", lines[0])
	}
	if lines[1] != "user preamble line" {
		t.Errorf("preamble not preserved between H1 and tag-line: got %q", lines[1])
	}
	if !strings.HasPrefix(lines[2], "#library/quotes ") {
		t.Errorf("tag-line not at expected position: got %q", lines[2])
	}
	if lines[3] != "---" {
		t.Errorf("separator not at expected position: got %q", lines[3])
	}
	if !strings.Contains(out, "main content") {
		t.Errorf("main content lost from canonical body")
	}
}

// TestRenderAtomicCanonical_EmptyBodyEndsWithSingleTrailingNewline locks the
// caret-position contract: when an atom has no body content, the canonical
// rendering ends with exactly `\n---\n\n` (HR + one trailing empty line for
// the caret). Pre-fix the format string produced three trailing newlines,
// pushing the caret one row too far below the HR in Bear's editor.
func TestRenderAtomicCanonical_EmptyBodyEndsWithSingleTrailingNewline(t *testing.T) {
	fixedNow := time.Date(2026, 5, 16, 0, 35, 0, 0, time.Local)
	bear.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	d := bear.NewFlatListDomain("quicknote/daily", "✱ Daily")
	d.UnknownBucket = "_flat"

	out := bear.RenderAtomicCanonicalForTest(t, d, "ignored", "_flat", "")

	if !strings.HasSuffix(out, "\n---\n\n") {
		t.Fatalf("empty-body canonical must end with `\\n---\\n\\n`; got tail %q\n  full:\n%s",
			out[max(0, len(out)-12):], out)
	}
	if strings.HasSuffix(out, "\n---\n\n\n") {
		t.Errorf("empty-body canonical has THREE trailing newlines — caret will land one row too far")
	}
}
