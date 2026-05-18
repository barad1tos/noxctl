package bear_test

import (
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// TestStampDatetimeH1_RecognitionTable covers every row of the H1
// recognition table in spec component 2. Each case asserts: given an
// input body, does the helper stamp `# <fixed-now>` at top, or leave
// the body unchanged? Stamp uses the package's time seam so the result
// is deterministic.
func TestStampDatetimeH1_RecognitionTable(t *testing.T) {
	fixedNow := time.Date(2026, 5, 13, 15, 25, 0, 0, time.Local)
	bear.SetNowForNewNoteLinkForTest(t, func() time.Time { return fixedNow })

	const stamp = "# 13 May 2026 at 15:25"

	cases := []struct {
		name     string
		body     string
		wantHead string // first non-blank line in result
	}{
		{
			name:     "real H1 left alone",
			body:     "# Real title\n#tag | [[hub]]\n",
			wantHead: "# Real title",
		},
		{
			name:     "empty H1 content stamps datetime",
			body:     "# \n#tag | [[hub]]\n",
			wantHead: stamp,
		},
		{
			name:     "tag-only line not H1, stamp above",
			body:     "#tag\nbody\n",
			wantHead: stamp,
		},
		{
			name:     "canonical tag-line not H1, stamp above",
			body:     "#tag | [[hub]] | section\nbody\n",
			wantHead: stamp,
		},
		{
			name:     "H2 only, stamp above H2",
			body:     "## Heading\nbody\n",
			wantHead: stamp,
		},
		{
			name:     "leading blanks then real H1 preserved",
			body:     "\n\n# Real title\nbody\n",
			wantHead: "# Real title",
		},
		{
			name:     "prose then later H1, stamp at top",
			body:     "Some prose\n# Title later\nbody\n",
			wantHead: stamp,
		},
		{
			name:     "H1 with extra leading whitespace recognized",
			body:     "#  Real title with double space\n",
			wantHead: "#  Real title with double space",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := bear.StampDatetimeH1(c.body)
			firstLine := ""
			for line := range strings.SplitSeq(out, "\n") {
				if strings.TrimSpace(line) == "" {
					continue
				}
				firstLine = line
				break
			}
			if firstLine != c.wantHead {
				t.Errorf("first non-blank line:\n  got:  %q\n  want: %q\n  full body:\n%s",
					firstLine, c.wantHead, out)
			}
		})
	}
}
