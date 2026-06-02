package diag

import (
	"encoding/json"
	"fmt"
	"io"
)

// Render dispatches on output after validating it. Text is the
// human-friendly default; JSON is the machine read. verb is the
// leading label on the text verdict line ("verify" / "doctor").
func Render(out io.Writer, output, verb string, result *Result) error {
	if err := ValidateOutput(output); err != nil {
		return err
	}
	if output == "json" {
		return RenderJSON(out, result)
	}
	return RenderText(out, verb, result)
}

// RenderJSON emits the full Result as indented JSON. Byte-identical to
// verify's original renderJSON so verify's JSON consumers are
// unaffected by the move onto diag.
func RenderJSON(out io.Writer, result *Result) error {
	enc := json.NewEncoder(out)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// RenderText prints a per-check status block followed by the final
// verdict line.
//
// Byte-stability invariant: for a Result whose checks all have empty
// Group and whose Summary.Warn == 0, output is byte-identical to what
// verify printed before diag existed. The group header is emitted only
// when a check's Group is non-empty and differs from the previous
// check's group, so verify's empty-group checks never trigger it; the
// warn segment is appended to the verdict line only when warn > 0.
func RenderText(out io.Writer, verb string, result *Result) error {
	previousGroup := ""
	for _, check := range result.Checks {
		if err := renderGroupHeader(out, check.Group, &previousGroup); err != nil {
			return err
		}
		if err := renderCheck(out, check); err != nil {
			return err
		}
	}
	return renderVerdictLine(out, verb, result.Summary)
}

// renderGroupHeader prints a header line when group is non-empty and
// changed since the last printed group, updating previousGroup in
// place. A no-op for empty or unchanged groups — this is what keeps
// verify's flat output byte-stable.
func renderGroupHeader(out io.Writer, group string, previousGroup *string) error {
	if group == "" || group == *previousGroup {
		return nil
	}
	*previousGroup = group
	_, err := fmt.Fprintf(out, "%s\n", group)
	return err
}

// renderCheck prints one check's glyph line plus its indented details.
func renderCheck(out io.Writer, check Check) error {
	if _, err := fmt.Fprintf(out, "%s %s — %s\n", StatusGlyph(check.Status), check.Name, check.Message); err != nil {
		return err
	}
	for _, detail := range check.Details {
		if _, err := fmt.Fprintf(out, "    %s\n", detail); err != nil {
			return err
		}
	}
	if check.Remediation != "" {
		if _, err := fmt.Fprintf(out, "    Fix: %s\n", check.Remediation); err != nil {
			return err
		}
	}
	return nil
}

// renderVerdictLine prints the trailing verdict. The legacy four
// counters keep verify's exact "(p pass / f fail / s skipped / e
// error)" shape; the warn count is appended only when warn > 0 so
// verify's zero-warn line stays byte-identical.
func renderVerdictLine(out io.Writer, verb string, summary Summary) error {
	warnSegment := ""
	if summary.Warn > 0 {
		warnSegment = fmt.Sprintf(" / %d warn", summary.Warn)
	}
	_, err := fmt.Fprintf(out, "\n%s: %s (%d pass / %d fail / %d skipped / %d error)%s\n",
		verb, overallVerdict(summary), summary.Pass, summary.Fail,
		summary.Skipped, summary.Error, warnSegment)
	return err
}

// StatusGlyph maps a status to its one-glyph prefix. The four legacy
// glyphs (✓ ✗ • ⚠) are byte-identical to verify's; "!" for StatusWarn
// is the only addition.
func StatusGlyph(status Status) string {
	switch status {
	case StatusPass:
		return "✓"
	case StatusWarn:
		return "!"
	case StatusFail:
		return "✗"
	case StatusSkipped:
		return "•"
	default: // StatusError
		return "⚠"
	}
}

// overallVerdict reduces the summary to a single label. Error trumps
// fail trumps pass — identical precedence to verify for any zero-warn
// input, so verify's verdict tests keep passing. Warn alone does not
// produce a distinct verdict (doctor maps warn to a passing exit);
// warn is surfaced in the counter segment, not the verdict word.
func overallVerdict(summary Summary) string {
	switch {
	case summary.Error > 0:
		return "ERROR"
	case summary.Fail > 0:
		return "FAIL"
	}
	return "PASS"
}
