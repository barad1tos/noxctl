package verify

import (
	"encoding/json"
	"fmt"
	"io"
)

// render dispatches on opts.Output. Text rendering is the
// human-friendly default; JSON is the tooling-friendly machine read.
func render(opts Options, result *Result) error {
	if err := ValidateOutput(opts.Output); err != nil {
		return err
	}
	if opts.Output == "json" {
		return renderJSON(opts.Stdout, result)
	}
	return renderText(opts.Stdout, result)
}

// renderJSON emits the full Result as indented JSON. Schema is locked
// at v1 via Result.SchemaVersion — bumping the field signals an
// incompatible output change to scripted consumers.
func renderJSON(w io.Writer, result *Result) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(result)
}

// renderText prints a human-readable per-check status block followed
// by a final PASS / FAIL / ERROR verdict.
func renderText(w io.Writer, result *Result) error {
	for _, c := range result.Checks {
		if _, err := fmt.Fprintf(w, "%s %s — %s\n", statusGlyph(c.Status), c.Name, c.Message); err != nil {
			return err
		}
		for _, d := range c.Details {
			if _, err := fmt.Fprintf(w, "    %s\n", d); err != nil {
				return err
			}
		}
	}
	verdict := overallVerdict(result.Summary)
	_, err := fmt.Fprintf(w, "\nverify: %s (%d pass / %d fail / %d skipped / %d error)\n",
		verdict, result.Summary.Pass, result.Summary.Fail,
		result.Summary.Skipped, result.Summary.Error)
	return err
}

// statusGlyph maps per-check status to a one-glyph prefix matching
// the convention used by `noxctl plan`/`apply` recap (`✓` / `✗` / `~`
// / `•`). Plain ASCII fallback would be nice; current code matches
// the rest of the project's UTF-8-only output. `⚠` distinguishes
// StatusError (verify couldn't make a verdict) from StatusFail
// (verify made a verdict and the answer is no) — the operator
// remediation is different (fix infrastructure vs fix drift).
func statusGlyph(s Status) string {
	switch s {
	case StatusPass:
		return "✓"
	case StatusFail:
		return "✗"
	case StatusSkipped:
		return "•"
	default: // StatusError
		return "⚠"
	}
}

// RenderForTest exposes render to the external test package.
func RenderForTest(opts Options, result *Result) error {
	return render(opts, result)
}

// overallVerdict reduces the summary to a single bold label. Error
// trumps fail (verify couldn't make a verdict ⇒ tell the operator
// loudly); fail trumps pass; otherwise PASS.
func overallVerdict(s Summary) string {
	switch {
	case s.Error > 0:
		return "ERROR"
	case s.Fail > 0:
		return "FAIL"
	}
	return "PASS"
}
