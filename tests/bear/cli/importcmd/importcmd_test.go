// Package importcmd_test covers the blueprint heuristic in
// bear/cli/importcmd that drives `noxctl import <bear-tag>`. The
// bearcli-side scan (ListNotesForTag) is exercised end-to-end via
// the cobra smoke test; this file pins the inference contract for
// each input shape.
//
// The heuristic itself lives in the unexported `infer` function;
// each test reaches it through the exported `RunWithNotes` test
// seam in importcmd_export_test.go (kept out of the production tree
// per the project's no-in-package-tests rule).
package importcmd_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/cli/importcmd"
)

// TestEmitWithNotes_EmptyTag covers the 0-notes branch: import
// should suggest flat-list as the lowest-friction starter and call
// it out in the rationale.
func TestEmitWithNotes_EmptyTag(t *testing.T) {
	var buf bytes.Buffer
	importcmd.EmitWithNotesForTest(&buf, "research/papers", nil)
	out := buf.String()
	for _, want := range []string{
		"research/papers",
		`blueprint   = "flat-list"`,
		"lowest-friction starter",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("empty-tag output missing %q\n%s", want, out)
		}
	}
}

// TestEmitWithNotes_FlatTableShape pins the sub-tag → flat-table
// inference: when every note carries the same `#tag/<bucket>`
// sub-tag pattern, the suggestion is flat-table with the observed
// bucket set populated.
func TestEmitWithNotes_FlatTableShape(t *testing.T) {
	notes := []bear.Note{
		{ID: "1", Title: "Note A", Tags: []string{"#research/papers", "#research/papers/Math"}},
		{ID: "2", Title: "Note B", Tags: []string{"#research/papers", "#research/papers/Physics"}},
		{ID: "3", Title: "Note C", Tags: []string{"#research/papers", "#research/papers/Math"}},
	}
	var buf bytes.Buffer
	importcmd.EmitWithNotesForTest(&buf, "research/papers", notes)
	out := buf.String()
	for _, want := range []string{
		`blueprint   = "flat-table"`,
		`buckets        = ["Math", "Physics"]`,
		`unknown_bucket = "Other"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("flat-table output missing %q\n%s", want, out)
		}
	}
}

// TestEmitWithNotes_HubRoutedShape covers the author-grouped fallback:
// notes carry the tag but no sub-tag pattern, and three or more
// distinct `## <Author>` H2 headers exist across them.
func TestEmitWithNotes_HubRoutedShape(t *testing.T) {
	notes := []bear.Note{
		{ID: "1", Title: "A", Tags: []string{"#library/quotes"}, Content: "# A\n## Shakespeare\nquote\n"},
		{ID: "2", Title: "B", Tags: []string{"#library/quotes"}, Content: "# B\n## Plato\nquote\n"},
		{ID: "3", Title: "C", Tags: []string{"#library/quotes"}, Content: "# C\n## Aristotle\nquote\n"},
	}
	var buf bytes.Buffer
	importcmd.EmitWithNotesForTest(&buf, "library/quotes", notes)
	out := buf.String()
	for _, want := range []string{
		`blueprint   = "hub-routed"`,
		`hub_h2_prefix  = "Group"`,
		"hub-routed groups them per author",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("hub-routed output missing %q\n%s", want, out)
		}
	}
}

// TestEmitWithNotes_Fallback covers the safe-default branch: notes
// with no shared sub-tag and fewer than three H2 headers fall back
// to flat-list with an explicit "safe fallback" rationale.
func TestEmitWithNotes_Fallback(t *testing.T) {
	notes := []bear.Note{
		{ID: "1", Title: "A", Tags: []string{"#inbox"}, Content: "# A\nplain body\n"},
		{ID: "2", Title: "B", Tags: []string{"#inbox"}, Content: "# B\nmore body\n"},
	}
	var buf bytes.Buffer
	importcmd.EmitWithNotesForTest(&buf, "inbox", notes)
	out := buf.String()
	for _, want := range []string{
		`blueprint   = "flat-list"`,
		"safe fallback",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("fallback output missing %q\n%s", want, out)
		}
	}
}

// TestEmitWithNotes_IndexTitleCapitalize pins the index-title
// suggestion: the last `/`-separated tag segment is capitalized and
// prefixed with the `✱ ` glyph the existing catalogs use.
func TestEmitWithNotes_IndexTitleCapitalize(t *testing.T) {
	var buf bytes.Buffer
	importcmd.EmitWithNotesForTest(&buf, "library/quotes", nil)
	if !strings.Contains(buf.String(), `index_title = "✱ Quotes"`) {
		t.Errorf("expected ✱-capitalized leaf segment in index_title; got:\n%s", buf.String())
	}
}

// _ keeps the context import live for tests that wire the seam
// through context.Background()-style noop arguments; importing
// without using it would fail the unused-import lint.
var _ = context.Background
