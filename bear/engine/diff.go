// Package engine diff — TTY-aware rendering for *PlanResult.
//
// Three concerns live in this file:
//
// 1. ColorMode enum + ParseColorMode: the --color flag's value
// ("auto"/"always"/"never").
// 2. useColor predicate: stdlib-only TTY detection via
// os.Stdout.Stat & os.ModeCharDevice (deliberately NOT
// golang.org/x/term — keeps the dep budget at one).
// 3. RenderText / RenderJSON: writers that take *PlanResult and emit
// human or machine output. RenderJSON has NO ColorMode parameter —
// ANSI escape codes can never leak into JSON output (hard contract).
//
// Streaming rule: every line goes through fmt.Fprintf(w,...) directly
// — never builds a strings.Builder and then dumps once. os.Stdout's
// runtime buffering handles cadence; explicit bufio.NewWriter is
// unnecessary at this scale.
package engine

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
)

// ColorMode controls how RenderText decides between ANSI-colored output
// and plain ASCII. Set via the --color flag at the CLI boundary; the
// default is ColorAuto (== iota 0) so a zero-value PlanOpts{}.ColorMode
// still gets the friendly auto-detect path.
type ColorMode int

const (
	// ColorAuto — TTY detection + NO_COLOR honored.
	ColorAuto ColorMode = iota
	// ColorAlways — force ANSI even when stdout is piped.
	ColorAlways
	// ColorNever — emit zero ANSI escapes regardless of TTY state.
	ColorNever
)

// ParseColorMode parses the --color flag's string value. Empty input
// (""), "auto" → ColorAuto+nil. "always"/"never" map to their enum
// values with nil error. Anything else returns ColorAuto (safe
// fallback) plus a non-nil error so the CLI shim can fail-fast on
// invalid input.
func ParseColorMode(s string) (ColorMode, error) {
	switch s {
	case "", "auto":
		return ColorAuto, nil
	case "always":
		return ColorAlways, nil
	case "never":
		return ColorNever, nil
	default:
		return ColorAuto, fmt.Errorf("invalid --color value %q (expected auto|always|never)", s)
	}
}

const (
	ansiReset  = "\x1b[0m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiRed    = "\x1b[31m"
)

// useColor decides whether to emit ANSI escapes given the resolved
// destination file and the user's ColorMode. Decision order:
//
// 1. ColorNever → false (unconditional).
// 2. ColorAlways → true (unconditional, even on a piped writer).
// 3. ColorAuto:
// a. NO_COLOR env set (any non-empty value) → false (no-color.org).
// b. stdout NOT a character device (piped/redirected) → false.
// c. otherwise → true.
func useColor(stdout *os.File, mode ColorMode) bool {
	switch mode {
	case ColorNever:
		return false
	case ColorAlways:
		return true
	case ColorAuto:
		// fall through to env + isatty heuristics below
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if stdout == nil {
		return false
	}
	stat, err := stdout.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}

// RenderJSON writes r to w as indented JSON. NO ColorMode parameter —
// JSON output never carries ANSI escapes (locked by
// TestRenderJSONNoANSI).
func RenderJSON(w io.Writer, r *PlanResult) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(r)
}

// RenderText writes r to w as human-readable, optionally ANSI-colored,
// per-domain output. Color is decided by useColor when w is *os.File;
// non-file writers (e.g. bytes.Buffer) get color only on ColorAlways.
//
// Output shape:
// - one line per domain ("✓ tag — clean", "~ tag — N change(s)", "✗ tag — error")
// - drift domains list each Diff.Summary indented two spaces
// - verbose adds Diff.Detail lines indented four spaces
// - residue block follows when any untracked tag families exist
// - footer line: "Plan: N drift, M changes, K errors"
func RenderText(w io.Writer, r *PlanResult, mode ColorMode, verbose bool) error {
	color := false
	if file, ok := w.(*os.File); ok {
		color = useColor(file, mode)
	} else if mode == ColorAlways {
		color = true
	}

	sorted := append([]DomainPlan(nil), r.Domains...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Tag < sorted[j].Tag })

	for _, dp := range sorted {
		if err := renderDomainLine(w, dp, color, verbose); err != nil {
			return err
		}
	}

	if len(r.Untracked.TagFamilies) > 0 {
		if err := renderUntrackedBlock(w, r.Untracked, color); err != nil {
			return err
		}
	}

	_, err := fmt.Fprintf(w, "\nPlan: %d drift, %d changes, %d errors\n",
		r.Summary.DomainsDrift, r.Summary.ChangesTotal, len(r.Errors))
	return err
}

func renderDomainLine(w io.Writer, dp DomainPlan, color, verbose bool) error {
	switch dp.Status {
	case "clean":
		return writeGlyphLine(w, color, ansiGreen, "✓", dp.Tag, "clean")
	case "drift":
		return renderDriftBlock(w, dp, color, verbose)
	case "error":
		return writeGlyphLine(w, color, ansiRed, "✗", dp.Tag, "error")
	}
	return nil
}

func renderDriftBlock(w io.Writer, dp DomainPlan, color, verbose bool) error {
	status := fmt.Sprintf("%d change(s)", len(dp.Changes))
	if err := writeGlyphLine(w, color, ansiYellow, "~", dp.Tag, status); err != nil {
		return err
	}
	for _, change := range dp.Changes {
		if _, err := fmt.Fprintf(w, "  %s\n", change.Summary); err != nil {
			return err
		}
		if !verbose {
			continue
		}
		for _, line := range change.Detail {
			if _, err := fmt.Fprintf(w, "    %s\n", line); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeGlyphLine emits a single status line — `<glyph> <tag> — <status>`.
// Centralizes the dupl-flagged clean/error pattern.
func writeGlyphLine(w io.Writer, color bool, ansi, glyph, tag, status string) error {
	_, err := fmt.Fprintf(w, "%s%s%s %s — %s\n",
		colorize(color, ansi), glyph, colorize(color, ansiReset), tag, status)
	return err
}

func renderUntrackedBlock(w io.Writer, u UntrackedReport, color bool) error {
	if _, err := fmt.Fprintf(w, "\n%s⚠%s Untracked — %d notes across %d tag families\n",
		colorize(color, ansiRed), colorize(color, ansiReset),
		u.TotalNotes, len(u.TagFamilies)); err != nil {
		return err
	}
	for _, fam := range u.TagFamilies {
		if _, err := fmt.Fprintf(w, "  - %s — %d note(s)\n", fam.Tag, fam.NoteCount); err != nil {
			return err
		}
	}
	return nil
}

func colorize(enabled bool, seq string) string {
	if !enabled {
		return ""
	}
	return seq
}
