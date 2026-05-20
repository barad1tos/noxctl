// Package destroy_test covers the pure helpers inside
// bear/cli/destroy that drive the strip/trash sweep. The
// bearcli-side mutations (TrashNote / OverwriteNoteContent) are
// exercised end-to-end via the cobra smoke test; this file pins the
// string-shape contracts that the orchestrator depends on.
package destroy_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/destroy"
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
