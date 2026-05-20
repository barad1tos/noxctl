package bear_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
)

func TestHasForeignQuicknoteTag(t *testing.T) {
	cases := []struct {
		name string
		tags []string
		want bool
	}{
		{"only quicknote/daily", []string{"quicknote/daily"}, false},
		{"daily + weekly (both quicknote)", []string{"quicknote/daily", "quicknote/weekly"}, false},
		{"daily + important", []string{"quicknote/daily", "important"}, true},
		{"daily + project/x", []string{"quicknote/daily", "project/x"}, true},
		{"daily + quicknotes/typo (top-segment != quicknote)", []string{"quicknote/daily", "quicknotes/typo"}, true},
		{"empty tags", []string{}, false},
		{"nothing quicknote at all", []string{"library/poetry"}, false},
		{"bearcli # prefix: daily + important", []string{"#quicknote/daily", "#important"}, true},
		{"bearcli # prefix: only quicknote", []string{"#quicknote/daily"}, false},
		{"bearcli # prefix: nothing quicknote", []string{"#library/poetry"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.HasForeignQuicknoteTag(tc.tags); got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestSubstituteQuicknoteInBody(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		replacement string
		want        string
	}{
		{
			// Canonical case: token swap inside a canonical line.
			// Surrounding ` | [[hub]] | [link]` structure preserved
			// byte-for-byte. Downstream development.RunRegen rewrites
			// backlink + new-note URL on its own pass.
			name: "canonical tag-line surgical swap",
			in: "# Title\n#quicknote/daily | [[✱ Daily]] | " +
				"[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)" +
				"\n---\nbody text\n",
			replacement: "#development/noxctl",
			want: "# Title\n#development/noxctl | [[✱ Daily]] | " +
				"[Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)" +
				"\n---\nbody text\n",
		},
		{
			name:        "standalone tag-only line swapped",
			in:          "body content\n\n#quicknote/daily\n",
			replacement: "#development/noxctl",
			want:        "body content\n\n#development/noxctl\n",
		},
		{
			name:        "multiple canonical lines all swapped",
			in:          "# Title\n#quicknote/daily | [[✱ Daily]]\n#quicknote/weekly | [[✱ Weekly]]\nbody\n",
			replacement: "#development/noxctl",
			want:        "# Title\n#development/noxctl | [[✱ Daily]]\n#development/noxctl | [[✱ Weekly]]\nbody\n",
		},
		{
			name:        "non-quicknote tag untouched",
			in:          "# Title\n#library/poetry | [[✱ Бібліотека]]\n---\nbody\n",
			replacement: "#development/noxctl",
			want:        "# Title\n#library/poetry | [[✱ Бібліотека]]\n---\nbody\n",
		},
		{
			name:        "leading whitespace preserved on swap",
			in:          "# Title\n   #quicknote/daily | [[✱ Daily]]\n---\nbody\n",
			replacement: "#development/noxctl",
			want:        "# Title\n   #development/noxctl | [[✱ Daily]]\n---\nbody\n",
		},
		{
			name:        "no quicknote in body — unchanged",
			in:          "# Title\nplain body\n",
			replacement: "#development/noxctl",
			want:        "# Title\nplain body\n",
		},
		{
			name:        "mixed line foreign token preserved",
			in:          "body\n#quicknote/daily #archive\nmore body\n",
			replacement: "#development/noxctl",
			want:        "body\n#development/noxctl #archive\nmore body\n",
		},
		{
			name:        "two quicknote tokens on one line both swapped",
			in:          "#quicknote/daily #quicknote/weekly\n",
			replacement: "#development/noxctl",
			want:        "#development/noxctl #development/noxctl\n",
		},
		{
			// Regression for the May 2026 drag-to-development scenario:
			// the user dragged onto #development/noxctl, Bear added a
			// standalone tag-line. After surgical swap, the canonical
			// line carries the new tag; the standalone line remains
			// (downstream canonicalizer absorbs it).
			name: "drag-to-development scenario — pure swap",
			in: "# 14 May 2026 at 01:02\n" +
				"#development/noxctl\n" +
				"#quicknote/daily | [[✱ Daily]] | [Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n",
			replacement: "#development/noxctl",
			want: "# 14 May 2026 at 01:02\n" +
				"#development/noxctl\n" +
				"#development/noxctl | [[✱ Daily]] | [Нова нотатка](bear://x-callback-url/create?tags=quicknote%2Fdaily&open_note=yes)\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.SubstituteQuicknoteInBody(tc.in, tc.replacement)
			if got != tc.want {
				t.Errorf("got:\n%s\nwant:\n%s",
					strings.ReplaceAll(got, "\n", "\\n\n"),
					strings.ReplaceAll(tc.want, "\n", "\\n\n"))
			}
		})
	}
}

func TestFindForeignTagInBody(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "drag-insert pattern — finds standalone non-quicknote tag",
			in:   "# Title\n#development/noxctl\n#quicknote/daily | [[hub]]\n",
			want: "#development/noxctl",
		},
		{
			name: "no foreign tag — empty",
			in:   "# Title\n#quicknote/daily\n",
			want: "",
		},
		{
			name: "canonical line ignored (has ` | `)",
			in:   "# Title\n#development/noxctl | [[hub]]\n",
			want: "",
		},
		{
			name: "multiple foreign tags — first wins",
			in:   "#archive\n#development/noxctl\n#quicknote/daily\n",
			want: "#archive",
		},
		{
			name: "non-tag standalone line skipped",
			in:   "plain prose\n#development/noxctl\n",
			want: "#development/noxctl",
		},
	}
	check := func(in, want string) func(*testing.T) {
		return func(t *testing.T) {
			t.Helper()
			if got := domain.FindForeignTagInBody(in); got != want {
				t.Errorf("FindForeignTagInBody(%q) = %q, want %q", in, got, want)
			}
		}
	}
	for _, tc := range cases {
		t.Run(tc.name, check(tc.in, tc.want))
	}
}
