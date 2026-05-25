// Package importcmd_test covers the blueprint heuristic in
// bear/cli/importcmd that drives `noxctl import <bear-tag>`. The
// bearcli-side scan (ListNotesForTag) is exercised end-to-end via
// the cobra smoke test; this file pins the inference contract for
// each input shape.
//
// The inference lives in the bear/recommend engine; import delegates to
// it. Tests reach the emit path through `cli.EmitWithNotesForTest`, a thin
// production-package seam that runs the same compute+recommend+emit pass
// over a caller-supplied note set (no bearcli round trip required).
package importcmd_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/config"
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
// should suggest flat-list and include a recommend: comment line.
func TestEmitWithNotes_EmptyTag(t *testing.T) {
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "research/papers", nil)
	out := buf.String()
	for _, want := range []string{
		"research/papers",
		`blueprint   = "flat-list"`,
		"recommend:",
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

// TestEmitWithNotes_AtomH2NotInferredAsHub locks the design call that
// atom-body H2 sections without a matching sub-tag do NOT signal
// hub-routed. When notes have H2s but no sub-tag bucket signal, the
// engine finds BucketCardinality=0 and falls to flat-list.
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
		t.Errorf("H2s without sub-tag bucket signal should NOT infer hub-routed; got:\n%s", out)
	}
}

// TestEmitWithNotes_Fallback covers the no-bucket-signal branch: notes
// with no sub-tags and no canonical bucket lines fall back to flat-list.
// The recommend: comment line must be present.
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
		"recommend:",
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

// TestRunImport_HubRoutedFromAuthorBodies verifies that a 2-level tag whose notes
// carry author-H2 bodies AND canonical bucket lines is inferred as hub-routed.
// The author-signal metric clears authorMinSignal (0.5), overriding the low
// bucket cardinality, so the engine picks Tier-2 hubs over inline sections.
func TestRunImport_HubRoutedFromAuthorBodies(t *testing.T) {
	notes := []domain.Note{
		{
			ID: "1", Title: "P1", Tags: []string{"#library/poetry"},
			Content: "#library/poetry | [[✱ Poetry]] | Frost\n---\n## Frost\n- x",
		},
		{
			ID: "2", Title: "P2", Tags: []string{"#library/poetry"},
			Content: "#library/poetry | [[✱ Poetry]] | Rilke\n---\n## Rilke\n- y",
		},
	}
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "library/poetry", notes)
	out := buf.String()
	if !strings.Contains(out, `blueprint   = "hub-routed"`) {
		t.Errorf("author-rich 2-level tag should infer hub-routed; got:\n%s", out)
	}
	if !strings.Contains(out, "recommend:") {
		t.Errorf("emit should include the rationale comment; got:\n%s", out)
	}
	if !strings.Contains(out, `hub_h2_prefix  = "Items"`) {
		t.Errorf("hub-routed blueprint should include hub_h2_prefix; got:\n%s", out)
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

// TestEmitWithNotes_TopLevelBucketedHintsUmbrella: a bucketed top-level tag gets
// a grouped-vertical recommendation plus an umbrella hint — import cannot see
// sibling tags, so it cannot tell sub-tag buckets from child domains and points
// the operator at the future vault-wide pass rather than guessing umbrella.
func TestEmitWithNotes_TopLevelBucketedHintsUmbrella(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "A", Tags: []string{"#reading", "#reading/books"}},
		{ID: "2", Title: "B", Tags: []string{"#reading", "#reading/talks"}},
	}
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "reading", notes)
	out := buf.String()
	if !strings.Contains(out, "umbrella") || !strings.Contains(out, "vault-wide") {
		t.Errorf("top-level bucketed tag should hint umbrella + vault-wide pass; got:\n%s", out)
	}
}

// TestEmitWithNotes_NoUmbrellaHintForNestedTag is the negative pair of the test
// above: a 2-level bucketed tag (e.g. research/papers) cannot be an umbrella —
// its sub-tags would be 3-level, forbidden by the tag-flatness rule — so emit
// must NOT print the umbrella hint. The hint is gated on the tag being
// top-level (strings.Count(tag, "/") == 0); this pins that gate.
func TestEmitWithNotes_NoUmbrellaHintForNestedTag(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "A", Tags: []string{"#research/papers", "#research/papers/Math"}},
		{ID: "2", Title: "B", Tags: []string{"#research/papers", "#research/papers/Physics"}},
	}
	var buf bytes.Buffer
	cli.EmitWithNotesForTest(&buf, "research/papers", notes)
	out := buf.String()
	if !strings.Contains(out, `blueprint   = "grouped-vertical"`) {
		t.Fatalf("nested bucketed tag should infer grouped-vertical; got:\n%s", out)
	}
	if strings.Contains(out, "umbrella") || strings.Contains(out, "vault-wide") {
		t.Errorf("2-level tag must NOT emit the umbrella hint; got:\n%s", out)
	}
}

// TestEmitWithNotes_DispatchContract_I4 verifies that every blueprint the
// recommender can emit produces a stanza that config.Dispatch accepts without
// error. This catches mismatches between emit field names/presence and the
// dispatch contract (e.g. hub-routed missing unknown_bucket).
func TestEmitWithNotes_DispatchContract_I4(t *testing.T) {
	ptr := func(s string) *string { return &s }
	buckets := []string{"A", "B"}
	unknown := "Other"
	cases := []struct {
		name   string
		stanza config.Stanza
	}{
		{
			name: "flat-list",
			stanza: config.Stanza{
				Tag: "research/papers", IndexTitle: "✱ Papers", Blueprint: "flat-list",
			},
		},
		{
			name: "grouped-vertical",
			stanza: config.Stanza{
				Tag: "english", IndexTitle: "✱ English", Blueprint: "grouped-vertical",
				Buckets: &buckets, UnknownBucket: &unknown,
			},
		},
		{
			name: "hub-routed",
			stanza: config.Stanza{
				Tag: "library/poetry", IndexTitle: "✱ Poetry", Blueprint: "hub-routed",
				UnknownBucket: ptr("Other"), HubH2Prefix: new("Items"),
			},
		},
		{
			name: "hub-routed-with-subtag",
			stanza: config.Stanza{
				Tag: "claude", IndexTitle: "✱ Claude", Blueprint: "hub-routed-with-subtag",
				Buckets: &buckets, UnknownBucket: &unknown,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := config.Dispatch(tc.stanza, nil)
			if err != nil {
				t.Errorf("Dispatch(%s) error: %v", tc.name, err)
			}
		})
	}
}

// TestEmitWithNotes_DispatchRoundTrip closes the gap the I4 contract test leaves
// open: I4 dispatches hand-built Stanzas, so it cannot catch emit printing the
// wrong field set for a blueprint (the I2 bug class — hub-routed missing
// unknown_bucket). This drives emit over note shapes that reach each blueprint
// the import path can produce, decodes emit's ACTUAL TOML output back into a
// config.Catalog, and dispatches the decoded stanza — pinning emit's field
// selection (needsBuckets / needsUnknownBucket / needsHubH2) against the live
// dispatch contract end to end. umbrella is unreachable here: import passes nil
// childFamilies, so ChildFamilies is always 0.
func TestEmitWithNotes_DispatchRoundTrip(t *testing.T) {
	groupedNotes := []domain.Note{
		{ID: "1", Title: "A", Tags: []string{"#research/papers", "#research/papers/Math"}},
		{ID: "2", Title: "B", Tags: []string{"#research/papers", "#research/papers/Physics"}},
		{ID: "3", Title: "C", Tags: []string{"#research/papers", "#research/papers/Math"}},
	}
	authorNotes := []domain.Note{
		{ID: "1", Title: "P1", Tags: []string{"#library/poetry"}, Content: "#library/poetry | [[✱ Poetry]] | Frost\n---\n## Frost\n- x"},
		{ID: "2", Title: "P2", Tags: []string{"#library/poetry"}, Content: "#library/poetry | [[✱ Poetry]] | Rilke\n---\n## Rilke\n- y"},
	}
	// 8 atoms in one top-level sub-tag bucket: AtomsPerBucket == hubMinPerBucket,
	// so recommendTopLevel forks to hub-routed-with-subtag (a Tier-2 hub per bucket).
	subtagHubNotes := make([]domain.Note, 0, 8)
	for i := range 8 {
		id := strconv.Itoa(i)
		subtagHubNotes = append(subtagHubNotes, domain.Note{
			ID: id, Title: "R" + id, Tags: []string{"#recipes", "#recipes/dinner"},
		})
	}

	cases := []struct {
		name          string
		tag           string
		notes         []domain.Note
		wantBlueprint string
	}{
		{"flat-list", "research/papers", nil, "flat-list"},
		{"grouped-vertical", "research/papers", groupedNotes, "grouped-vertical"},
		{"hub-routed", "library/poetry", authorNotes, "hub-routed"},
		{"hub-routed-with-subtag", "recipes", subtagHubNotes, "hub-routed-with-subtag"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			cli.EmitWithNotesForTest(&buf, tc.tag, tc.notes)

			var cat config.Catalog
			if _, err := toml.Decode(buf.String(), &cat); err != nil {
				t.Fatalf("decode emitted stanza: %v\n%s", err, buf.String())
			}
			if len(cat.Domains) != 1 {
				t.Fatalf("decoded %d domains, want 1\n%s", len(cat.Domains), buf.String())
			}
			got := cat.Domains[0]
			if got.Blueprint != tc.wantBlueprint {
				t.Fatalf("emitted blueprint = %q, want %q\n%s", got.Blueprint, tc.wantBlueprint, buf.String())
			}
			if _, err := config.Dispatch(got, nil); err != nil {
				t.Errorf("Dispatch(emitted %s) error: %v\n%s", tc.name, err, buf.String())
			}
		})
	}
}
