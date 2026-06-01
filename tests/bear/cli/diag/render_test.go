// Package diag_test — external coverage of the diag renderer.
//
// User-scenario framing: the renderer is what an operator reads on
// stdout. These tests pin the grouped-header behavior doctor relies on
// AND the byte-stability invariant verify depends on: a Result whose
// checks all have empty Group renders flat (no header lines), exactly
// as verify printed before diag existed; a Result with real Groups
// prints one header per group change; and the new StatusWarn glyph is
// "!" while the four legacy glyphs are unchanged.
package diag_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/diag"
)

// renderTextToString drives RenderText over a Result with the given
// verb and returns stdout.
func renderTextToString(t *testing.T, verb string, result *diag.Result) string {
	t.Helper()
	var buf bytes.Buffer
	if err := diag.RenderText(&buf, verb, result); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	return buf.String()
}

// TestStatusGlyph_AllFive — every status maps to its glyph; "!" is the
// new warn glyph, the other four are byte-identical to verify's.
func TestStatusGlyph_AllFive(t *testing.T) {
	cases := []struct {
		status diag.Status
		want   string
	}{
		{diag.StatusPass, "✓"},
		{diag.StatusWarn, "!"},
		{diag.StatusFail, "✗"},
		{diag.StatusSkipped, "•"},
		{diag.StatusError, "⚠"},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			if got := diag.StatusGlyph(c.status); got != c.want {
				t.Errorf("StatusGlyph(%v) = %q, want %q", c.status, got, c.want)
			}
		})
	}
}

// TestRenderText_EmptyGroupRendersFlat — a Result whose checks all have
// empty Group must render with NO group header line. This is the
// byte-stability invariant: verify's checks have no Group, so verify's
// output stays flat exactly as before the refactor onto diag.
func TestRenderText_EmptyGroupRendersFlat(t *testing.T) {
	result := &diag.Result{
		Checks: []diag.Check{
			{Name: "plan-parity", Status: diag.StatusPass, Message: "ok"},
			{Name: "daemon-log", Status: diag.StatusPass, Message: "clean"},
		},
		Summary: diag.Summary{Pass: 2},
	}
	out := renderTextToString(t, "verify", result)
	// The per-check lines appear, indented with no group header above them.
	for _, want := range []string{"✓ plan-parity — ok", "✓ daemon-log — clean"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in flat output; got:\n%s", want, out)
		}
	}
	// No group header: every non-empty line before the verdict starts
	// with a glyph or detail indent, never a bare group label.
	for line := range strings.SplitSeq(out, "\n") {
		if line == "" || strings.HasPrefix(line, "    ") || strings.HasPrefix(line, "\n") {
			continue
		}
		if strings.HasPrefix(line, "verify:") {
			continue
		}
		firstRune := []rune(line)[0]
		if firstRune != '✓' && firstRune != '!' && firstRune != '✗' && firstRune != '•' && firstRune != '⚠' {
			t.Errorf("unexpected non-check, non-verdict line in flat output: %q", line)
		}
	}
}

// TestRenderText_GroupHeaderOnChange — two distinct Groups print
// exactly two header lines, each before its group's first check. A run
// of same-group checks shares one header.
func TestRenderText_GroupHeaderOnChange(t *testing.T) {
	result := &diag.Result{
		Checks: []diag.Check{
			{Name: "system.bear-app", Status: diag.StatusPass, Message: "found", Group: "System"},
			{Name: "system.bearcli", Status: diag.StatusPass, Message: "found", Group: "System"},
			{Name: "config.found", Status: diag.StatusPass, Message: "present", Group: "Config"},
		},
		Summary: diag.Summary{Pass: 3},
	}
	out := renderTextToString(t, "doctor", result)
	if got := strings.Count(out, "System"); got < 1 {
		t.Errorf("expected a System header; got:\n%s", out)
	}
	if got := strings.Count(out, "Config"); got < 1 {
		t.Errorf("expected a Config header; got:\n%s", out)
	}
	// Exactly two header lines: one per distinct group. Header lines are
	// the lines equal to the bare group name.
	systemHeaders, configHeaders := 0, 0
	for line := range strings.SplitSeq(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "System" {
			systemHeaders++
		}
		if trimmed == "Config" {
			configHeaders++
		}
	}
	if systemHeaders != 1 {
		t.Errorf("expected exactly 1 System header line, got %d:\n%s", systemHeaders, out)
	}
	if configHeaders != 1 {
		t.Errorf("expected exactly 1 Config header line, got %d:\n%s", configHeaders, out)
	}
}

// TestRenderText_VerdictLineByteStable_ZeroWarn — for a zero-warn
// Result the verdict line is byte-identical to what verify printed:
// "<verb>: <VERDICT> (<p> pass / <f> fail / <s> skipped / <e> error)".
// No warn segment leaks in when warn==0.
func TestRenderText_VerdictLineByteStable_ZeroWarn(t *testing.T) {
	result := &diag.Result{
		Summary: diag.Summary{Pass: 5, Fail: 2, Skipped: 1, Error: 3},
	}
	out := renderTextToString(t, "verify", result)
	want := "\nverify: ERROR (5 pass / 2 fail / 1 skipped / 3 error)\n"
	if !strings.Contains(out, want) {
		t.Errorf("expected byte-stable verdict line %q; got:\n%s", want, out)
	}
	if strings.Contains(out, "warn") {
		t.Errorf("zero-warn output leaked a warn segment; got:\n%s", out)
	}
}

// TestRenderText_VerdictLineSurfacesWarn — when warn>0 the warn count
// IS surfaced (doctor's concern), appended so verify's zero-warn line
// stays untouched.
func TestRenderText_VerdictLineSurfacesWarn(t *testing.T) {
	result := &diag.Result{
		Summary: diag.Summary{Pass: 3, Warn: 2},
	}
	out := renderTextToString(t, "doctor", result)
	if !strings.Contains(out, "2 warn") {
		t.Errorf("expected warn count surfaced when warn>0; got:\n%s", out)
	}
}

// TestOverallVerdict_ErrorTrumpsFailTrumpsPass — verify's ERROR/FAIL/
// PASS precedence is preserved for zero-warn input so verify's existing
// verdict tests keep passing.
func TestOverallVerdict_Precedence(t *testing.T) {
	cases := []struct {
		name    string
		summary diag.Summary
		want    string
	}{
		{"AllPass", diag.Summary{Pass: 3}, "doctor: PASS"},
		{"FailTrumpsPass", diag.Summary{Pass: 2, Fail: 1}, "doctor: FAIL"},
		{"ErrorTrumpsFail", diag.Summary{Pass: 1, Fail: 1, Error: 1}, "doctor: ERROR"},
		{"ErrorWithoutFail", diag.Summary{Pass: 2, Error: 1}, "doctor: ERROR"},
		{"SkippedOnlyIsPass", diag.Summary{Skipped: 3}, "doctor: PASS"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out := renderTextToString(t, "doctor", &diag.Result{Summary: c.summary})
			if !strings.Contains(out, c.want) {
				t.Errorf("expected %q in verdict; got:\n%s", c.want, out)
			}
		})
	}
}

// TestRenderJSON_IndentedSchema — RenderJSON emits indented JSON with
// schema_version, byte-compatible with verify's renderJSON.
func TestRenderJSON_IndentedSchema(t *testing.T) {
	var buf bytes.Buffer
	result := &diag.Result{
		SchemaVersion: diag.SchemaVersion,
		Checks:        []diag.Check{{Name: "x", Status: diag.StatusPass, Message: "ok"}},
	}
	if err := diag.RenderJSON(&buf, result); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if !strings.Contains(buf.String(), `"schema_version": 1`) {
		t.Errorf("expected indented schema_version=1; got:\n%s", buf.String())
	}
}

// TestRender_DispatchAndValidate — Render routes on output after
// ValidateOutput, rejecting unknown formats before writing.
func TestRender_DispatchAndValidate(t *testing.T) {
	var buf bytes.Buffer
	result := &diag.Result{Checks: []diag.Check{{Name: "x", Status: diag.StatusPass, Message: "ok"}}}
	if err := diag.Render(&buf, "json", "doctor", result); err != nil {
		t.Fatalf("Render json: %v", err)
	}
	if !strings.Contains(buf.String(), `"schema_version"`) {
		t.Errorf("expected json dispatch; got:\n%s", buf.String())
	}

	var rejectBuf bytes.Buffer
	if err := diag.Render(&rejectBuf, "yaml", "doctor", result); err == nil {
		t.Fatalf("expected error for -o yaml; got nil")
	}
	if rejectBuf.Len() > 0 {
		t.Errorf("Render wrote %d bytes before validation rejection; want zero", rejectBuf.Len())
	}
}
