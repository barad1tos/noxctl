// Package verify_test — renderText glyph + overallVerdict coverage.
//
// User-scenario framing: each test mirrors what the operator sees on
// stdout when verify produces a Result with a specific status mix —
// the per-check glyph (✓ ✗ • ⚠) AND the overall verdict line at the
// bottom of the report. Renderer is driven via `verify.RenderForTest`
// so the assertions stay decoupled from the upstream check logic
// (which lives behind realistic-backend hurdles tested separately).
package verify_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// renderToString drives the text renderer over the supplied Result and
// returns stdout as a string. Failing the test on render error keeps
// the per-case assertions tight.
func renderToString(t *testing.T, result *verify.Result) string {
	t.Helper()
	var buf bytes.Buffer
	err := verify.RenderForTest(verify.Options{Output: "text", Stdout: &buf}, result)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	return buf.String()
}

// TestRenderText_AllFourGlyphsSurface — operator's report should show
// the four distinct per-status glyphs side-by-side. Catches regressions
// where two statuses collapse to the same character.
func TestRenderText_AllFourGlyphsSurface(t *testing.T) {
	out := renderToString(t, buildAllFourStatusesResult())
	for _, want := range []string{"✓ plan-parity", "✗ daemon-log", "• apply-idempotency", "⚠ catalog-load"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in rendered output; got:\n%s", want, out)
		}
	}
}

// TestRenderText_DetailsBlockIndented — when a check has Details, each
// line should appear under the glyph row with a 4-space indent. The
// operator scans for indented lines under ✗/⚠ rows to triage; if the
// indent disappears, the report becomes one giant line salad.
func TestRenderText_DetailsBlockIndented(t *testing.T) {
	result := &verify.Result{
		Checks: []verify.Check{{
			Name:    "daemon-log",
			Status:  verify.StatusFail,
			Message: "2 warning(s)",
			Details: []string{
				"2026/05/18 10:01:00 LOOP detected for note A",
				"2026/05/18 10:02:00 LOOP detected for note B",
			},
		}},
		Summary: verify.Summary{Fail: 1},
	}
	out := renderToString(t, result)
	for _, want := range []string{
		"    2026/05/18 10:01:00 LOOP detected for note A",
		"    2026/05/18 10:02:00 LOOP detected for note B",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected indented detail %q; got:\n%s", want, out)
		}
	}
}

// TestRenderText_OverallVerdict — table-driven matrix of summary
// counts → expected verdict. ERROR trumps FAIL; FAIL trumps PASS;
// otherwise PASS. Encodes the operator-visible aggregation contract
// callers branch on (CI exit logic, dashboard color).
func TestRenderText_OverallVerdict(t *testing.T) {
	cases := []struct {
		name        string
		summary     verify.Summary
		wantVerdict string
	}{
		{"AllPass", verify.Summary{Pass: 3}, "verify: PASS"},
		{"FailTrumpsPass", verify.Summary{Pass: 2, Fail: 1}, "verify: FAIL"},
		{"ErrorTrumpsFail", verify.Summary{Pass: 1, Fail: 1, Error: 1}, "verify: ERROR"},
		{"ErrorWithoutFail", verify.Summary{Pass: 2, Error: 1}, "verify: ERROR"},
		{"SkippedOnlyIsPass", verify.Summary{Skipped: 3}, "verify: PASS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := &verify.Result{Summary: c.summary}
			out := renderToString(t, result)
			if !strings.Contains(out, c.wantVerdict) {
				t.Errorf("expected %q in summary line; got:\n%s", c.wantVerdict, out)
			}
		})
	}
}

// TestRenderText_SummaryCountersAppear — the summary line surfaces the
// raw counters in parens: "(N pass / N fail / N skipped / N error)".
// Operators paste this line into Slack to telegraph cycle state.
func TestRenderText_SummaryCountersAppear(t *testing.T) {
	result := &verify.Result{
		Summary: verify.Summary{Pass: 5, Fail: 2, Skipped: 1, Error: 3},
	}
	out := renderToString(t, result)
	want := "(5 pass / 2 fail / 1 skipped / 3 error)"
	if !strings.Contains(out, want) {
		t.Errorf("expected counter signature %q; got:\n%s", want, out)
	}
}

// TestRender_JSONFormatDispatch — render() with Output="json" must
// route to renderJSON, producing the indented schema_version=1 payload
// CI consumers branch on. Wraps the existing JSON shape tests at the
// renderer-layer so the dispatch path itself is exercised through
// the public RenderForTest seam.
func TestRender_JSONFormatDispatch(t *testing.T) {
	var buf bytes.Buffer
	result := &verify.Result{
		SchemaVersion: 1,
		Checks: []verify.Check{
			{Name: "x", Status: verify.StatusPass, Message: "ok"},
		},
	}
	err := verify.RenderForTest(verify.Options{Output: "json", Stdout: &buf}, result)
	if err != nil {
		t.Fatalf("render json: %v", err)
	}
	if !strings.Contains(buf.String(), `"schema_version": 1`) {
		t.Errorf("expected indented schema_version=1; got:\n%s", buf.String())
	}
}

// TestRender_InvalidOutputRejected — render() rejects unknown -o
// values before writing. ValidateOutput is the gatekeeper; this test
// pins that contract at the renderer entry point.
func TestRender_InvalidOutputRejected(t *testing.T) {
	var buf bytes.Buffer
	err := verify.RenderForTest(
		verify.Options{Output: "yaml", Stdout: &buf},
		&verify.Result{},
	)
	if err == nil {
		t.Fatalf("expected error for -o yaml; got nil")
	}
	if buf.Len() > 0 {
		t.Errorf("renderer wrote %d bytes before validation rejection; expected zero output", buf.Len())
	}
}
