// Package importcmd_test covers the blueprint heuristic in
// bear/cli/importcmd that drives `noxctl import <bear-tag>`. The
// bearcli-side scan (ListNotesForTag) is exercised end-to-end via
// the cobra smoke test; this file pins the inference contract for
// each input shape.
//
// The heuristic itself lives in the unexported `infer` function;
// tests reach it through `cli.EmitWithNotesForTest`, a thin
// production-package seam that runs the same emit pass over a
// caller-supplied note set (no bearcli round trip required).
package importcmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// importFake is a bearcli.Backend for the RunImport orchestration tests:
// serves a canned `list --tag` payload or fails the list when listErr is set.
type importFake struct {
	listPayload []byte
	listErr     error
}

func (f *importFake) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) > 0 && args[0] == "list" {
		if f.listErr != nil {
			return nil, f.listErr
		}
		return f.listPayload, nil
	}
	return []byte("[]"), nil
}

// importListJSON marshals bearcli `list` rows for the import fixture.
func importListJSON(t *testing.T, rows ...map[string]any) []byte {
	t.Helper()
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal import list: %v", err)
	}
	return payload
}

// TestEmitWithNotes_EmptyTag covers the 0-notes branch: import
// should suggest flat-list as the lowest-friction starter and call
// it out in the rationale.
func TestEmitWithNotes_EmptyTag(t *testing.T) {
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "research/papers", nil)
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

// TestEmitWithNotes_GroupedVerticalShape pins the sub-tag →
// grouped-vertical inference: when every note carries the same
// `#tag/<bucket>` sub-tag pattern, the suggestion is grouped-vertical
// with the observed bucket set populated.
func TestEmitWithNotes_GroupedVerticalShape(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "Note A", Tags: []string{"#research/papers", "#research/papers/Math"}},
		{ID: "2", Title: "Note B", Tags: []string{"#research/papers", "#research/papers/Physics"}},
		{ID: "3", Title: "Note C", Tags: []string{"#research/papers", "#research/papers/Math"}},
	}
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "research/papers", notes)
	out := buf.String()
	for _, want := range []string{
		`blueprint   = "grouped-vertical"`,
		`buckets        = ["Math", "Physics"]`,
		`unknown_bucket = "Other"`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("grouped-vertical output missing %q\n%s", want, out)
		}
	}
}

// TestEmitWithNotes_AtomH2NotInferredAsHub locks the design call
// that atom-body H2 sections do NOT signal hub-routed. The H2s
// belong to the operator's note content (Sections, References,
// quotes); the catalog blueprint must not be inferred from them.
// Notes carrying multiple H2 headers without a uniform sub-tag fall
// through to flat-list, and the rationale steers the operator to
// pick hub-routed manually if they want bucket-per-hub routing.
func TestEmitWithNotes_AtomH2NotInferredAsHub(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "A", Tags: []string{"#library/quotes"}, Content: "# A\n## Shakespeare\nquote\n"},
		{ID: "2", Title: "B", Tags: []string{"#library/quotes"}, Content: "# B\n## Plato\nquote\n"},
		{ID: "3", Title: "C", Tags: []string{"#library/quotes"}, Content: "# C\n## Aristotle\nquote\n"},
	}
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "library/quotes", notes)
	out := buf.String()
	if !strings.Contains(out, `blueprint   = "flat-list"`) {
		t.Errorf("atom-body H2s should NOT infer hub-routed; got:\n%s", out)
	}
	if strings.Contains(out, "hub-routed") && !strings.Contains(out, "manually") {
		t.Errorf("output mentions hub-routed without the manual-switch hint:\n%s", out)
	}
}

// TestEmitWithNotes_Fallback covers the safe-default branch: notes
// with no shared sub-tag and fewer than three H2 headers fall back
// to flat-list with an explicit "safe fallback" rationale.
func TestEmitWithNotes_Fallback(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "A", Tags: []string{"#inbox"}, Content: "# A\nplain body\n"},
		{ID: "2", Title: "B", Tags: []string{"#inbox"}, Content: "# B\nmore body\n"},
	}
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "inbox", notes)
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
	cli.EmitWithNotesForTest(&buf, "library/quotes", nil)
	if !strings.Contains(buf.String(), `index_title = "✱ Quotes"`) {
		t.Errorf("expected ✱-capitalized leaf segment in index_title; got:\n%s", buf.String())
	}
}

// TestRunImport_EmitsFlatListStanza_WhenTagEmpty drives the full `noxctl
// import` journey through bearcli (not just the EmitWithNotesForTest seam):
// an empty tag yields a flat-list starter stanza on stdout. User-facing bug
// if this regresses: `noxctl import <newtag>` either errors or prints nothing
// when the operator points it at a fresh, empty tag.
func TestRunImport_EmitsFlatListStanza_WhenTagEmpty(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := &importFake{listPayload: []byte("[]")}
	ctx := domain.ContextWithBackend(context.Background(), fake)

	var buf bytes.Buffer
	if err := cli.RunImport(ctx, cli.ImportOptions{Tag: "research/papers", Stdout: &buf}); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `blueprint   = "flat-list"`) {
		t.Errorf("empty-tag import should suggest flat-list; got:\n%s", out)
	}
	if !strings.Contains(out, "research/papers") {
		t.Errorf("stanza missing the imported tag; got:\n%s", out)
	}
}

// TestRunImport_EmitsGroupedVerticalStanza_WhenNotesShareSubTag drives the
// grouped-vertical inference end-to-end: notes all carrying `#tag/<bucket>`
// produce a grouped-vertical stanza with the observed buckets. User-facing
// bug if this regresses: a tag the operator already sub-categorized imports
// as a flat list, dropping the bucket structure they built.
//
//cyrillic:permit
func TestRunImport_EmitsGroupedVerticalStanza_WhenNotesShareSubTag(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := &importFake{listPayload: importListJSON(t,
		map[string]any{"id": "1", "title": "Праця А", "tags": []string{"#research/papers", "#research/papers/Math"}},
		map[string]any{"id": "2", "title": "Праця Б", "tags": []string{"#research/papers", "#research/papers/Physics"}},
	)}
	ctx := domain.ContextWithBackend(context.Background(), fake)

	var buf bytes.Buffer
	if err := cli.RunImport(ctx, cli.ImportOptions{Tag: "research/papers", Stdout: &buf}); err != nil {
		t.Fatalf("RunImport: %v", err)
	}
	if !strings.Contains(buf.String(), `blueprint   = "grouped-vertical"`) {
		t.Errorf("shared-sub-tag import should suggest grouped-vertical; got:\n%s", buf.String())
	}
}

// TestRunImport_ReturnsError_WhenListFails pins the failure path: when bearcli
// cannot list the tag, RunImport surfaces a wrapped error rather than emitting
// a misleading empty-tag flat-list stanza. User-facing bug if this regresses:
// a bearcli failure looks identical to "this tag has no notes".
func TestRunImport_ReturnsError_WhenListFails(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := &importFake{listErr: errors.New("bearcli list: simulated failure")}
	ctx := domain.ContextWithBackend(context.Background(), fake)

	var buf bytes.Buffer
	err := cli.RunImport(ctx, cli.ImportOptions{Tag: "research/papers", Stdout: &buf})
	if err == nil {
		t.Fatalf("RunImport returned nil despite a list failure; want a wrapped error")
	}
	if !strings.Contains(err.Error(), "list notes") {
		t.Errorf("error should identify the list failure; got %v", err)
	}
}
