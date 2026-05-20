// Package destroy_test covers the pure helpers inside
// bear/cli/destroy that drive the strip/trash sweep. The
// bearcli-side mutations (TrashNote / OverwriteNoteContent) are
// exercised end-to-end via the cobra smoke test; this file pins the
// string-shape contracts that the orchestrator depends on.
package destroy_test

import (
	"bytes"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/destroy"
	"github.com/barad1tos/noxctl/bear/domain"
)

// TestStripCanonical_RemovesTagLines covers the happy path: every
// line whose first token is the canonical tag (with or without a
// sub-tag) is dropped. Body content survives untouched.
func TestStripCanonical_RemovesTagLines(t *testing.T) {
	body := "# Title\n" +
		"#library/poetry | [[✱ Poetry]] | Shakespeare\n" +
		"The body line one.\n" +
		"Body line two.\n"

	got, changed := destroy.StripCanonical(body, "#library/poetry")
	if !changed {
		t.Fatal("expected changed=true when canonical line is present")
	}
	if strings.Contains(got, "library/poetry") {
		t.Errorf("canonical line still present after strip:\n%s", got)
	}
	for _, want := range []string{"# Title", "The body line one.", "Body line two."} {
		if !strings.Contains(got, want) {
			t.Errorf("body content lost: missing %q\n%s", want, got)
		}
	}
}

// TestStripCanonical_NoChange ensures changed=false when no canonical
// line matches. Avoids spurious overwrite writes when destroy
// double-passes an already-stripped note.
func TestStripCanonical_NoChange(t *testing.T) {
	body := "# Title\n#some/other/tag\nbody.\n"
	got, changed := destroy.StripCanonical(body, "#library/poetry")
	if changed {
		t.Error("expected changed=false when canonical tag is absent")
	}
	if got != body {
		t.Error("body should be returned unchanged when nothing to strip")
	}
}

// TestStripCanonical_PrefixGuard pins the prefix-collision defense:
// `#library` must NOT match `#libraryother` or `#library-prose`.
// The separator after the canonical tag (slash / pipe / space) is
// what keeps `#library` from eating unrelated sibling tags.
func TestStripCanonical_PrefixGuard(t *testing.T) {
	body := "# Title\n#libraryother | [[Other]]\n#library-prose\nbody.\n"
	got, changed := destroy.StripCanonical(body, "#library")
	if changed {
		t.Errorf("prefix-collision: #library matched #libraryother / #library-prose\nresult:\n%s", got)
	}
}

// TestPromptConfirm pins the type-to-confirm gate's accept / abort
// contract. Mismatched tag, empty line, EOF all collapse to
// ErrAborted; exact match (with surrounding whitespace tolerated)
// accepts. This is the human-side safety guard on a destructive
// verb — every case below should be considered load-bearing.
func TestPromptConfirm(t *testing.T) {
	cases := []struct {
		name      string
		stdin     string
		tag       string
		wantAbort bool
	}{
		{"exact match accepts", "library/poetry\n", "library/poetry", false},
		{"trailing whitespace tolerated", "  library/poetry  \n", "library/poetry", false},
		{"mismatch aborts", "library/poet\n", "library/poetry", true},
		{"empty line aborts", "\n", "library/poetry", true},
		{"EOF aborts", "", "library/poetry", true},
		{"prefix collision aborts", "library/poetry-extra\n", "library/poetry", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			err := destroy.PromptConfirmForTest(&out, strings.NewReader(tc.stdin), tc.tag)
			gotAbort := errors.Is(err, destroy.ErrAborted)
			if gotAbort != tc.wantAbort {
				t.Errorf("abort=%v err=%v want abort=%v", gotAbort, err, tc.wantAbort)
			}
			if !tc.wantAbort && err != nil {
				t.Errorf("expected accept; got err=%v", err)
			}
		})
	}
}

// TestRenderPreview_TruncatesAtomicsAboveFive pins the atomic-list
// overflow shape: at most 5 names are spelled out, the remainder
// rendered as "... and N more.". A regression that bumps the limit
// or changes the truncation copy must show up here.
func TestRenderPreview_TruncatesAtomicsAboveFive(t *testing.T) {
	atomics := make([]domain.Note, 8)
	for i := range atomics {
		atomics[i] = domain.Note{
			ID:    fmt.Sprintf("id%c", 'a'+i),
			Title: fmt.Sprintf("Atom%c", 'A'+i),
		}
	}
	var buf bytes.Buffer
	destroy.RenderPreviewForTest(&buf, "library/test", nil, atomics)
	out := buf.String()
	// First five names appear in full.
	for i := range 5 {
		want := fmt.Sprintf("Atom%c", 'A'+i)
		if !strings.Contains(out, want) {
			t.Errorf("preview missing first-five name %q\n%s", want, out)
		}
	}
	// Remainder collapsed.
	if !strings.Contains(out, "... and 3 more.") {
		t.Errorf("preview missing overflow line; got:\n%s", out)
	}
	// Sixth onward NOT listed by name.
	if strings.Contains(out, "AtomF") {
		t.Errorf("preview should not name AtomF (slot 6); got:\n%s", out)
	}
}

// TestRenderPreview_NoTruncationAtFiveOrFewer pins the inverse: at
// exactly 5 atomics the "and N more" line must NOT render. Catches
// off-by-one bugs in the overflow guard.
func TestRenderPreview_NoTruncationAtFiveOrFewer(t *testing.T) {
	atomics := []domain.Note{
		{ID: "1", Title: "A"},
		{ID: "2", Title: "B"},
		{ID: "3", Title: "C"},
		{ID: "4", Title: "D"},
		{ID: "5", Title: "E"},
	}
	var buf bytes.Buffer
	destroy.RenderPreviewForTest(&buf, "library/test", nil, atomics)
	if strings.Contains(buf.String(), "more.") {
		t.Errorf("5 atomics should not trigger overflow line; got:\n%s", buf.String())
	}
}

// TestStripCanonical_AcceptedShapes pins the two non-trivial
// canonical-line shapes that must still be recognized as a strip
// target:
//
//   - sub-tag form (`#library/poetry/Shakespeare | …`) — some
//     blueprints emit this; the slash separator after the canonical
//     tag is valid.
//   - leading whitespace (`  #library/poetry | …`) — pasted-from-
//     elsewhere notes sometimes carry indentation; TrimLeft inside
//     startsWithCanonical handles it.
func TestStripCanonical_AcceptedShapes(t *testing.T) {
	cases := []struct {
		label string
		body  string
	}{
		{"sub-tag form", "# Title\n#library/poetry/Shakespeare | [[Master]]\nbody.\n"},
		{"leading whitespace", "# Title\n  #library/poetry | [[Master]]\nbody.\n"},
	}
	for _, tc := range cases {
		t.Run(tc.label, func(t *testing.T) {
			got, changed := destroy.StripCanonical(tc.body, "#library/poetry")
			if !changed {
				t.Fatalf("%s: expected changed=true", tc.label)
			}
			if strings.Contains(got, "library/poetry") {
				t.Errorf("%s: canonical line still present:\n%s", tc.label, got)
			}
		})
	}
}
