package cli

// importcmd.go implements the `noxctl import <bear-tag>` subcommand
// body. Scans an untagged-by-noxctl Bear tag, classifies its note
// shape via the recommend engine, and emits a candidate [[domain]]
// stanza to stdout.
//
// import never edits noxctl.toml. The operator copy-pastes the
// suggested stanza into their config after reviewing — keeps the
// catalog under operator authorship and lets them tweak the
// generated values (index_title localization, bucket names,
// blueprint choice) before commit.

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/recommend"
)

// ImportOptions is the input bundle for RunImport.
type ImportOptions struct {
	Tag    string    // REQUIRED — Bear tag to scan, e.g. "research/papers"
	Stdout io.Writer // stanza + summary land here (typically os.Stdout)
}

// RunImport scans the supplied Bear tag, infers a likely blueprint via
// the recommend engine, and writes a candidate [[domain]] stanza plus
// a rationale comment to opts.Stdout.
// Never writes to noxctl.toml — the operator owns that file.
func RunImport(ctx context.Context, opts ImportOptions) error {
	// Standalone read command: initialize the bearcli pool before listing.
	// The daemon does this in its startup path; one-shot commands must do it
	// themselves (mirrors plan / lint / destroy).
	bearcli.SetConcurrency(engine.DefaultBearcliConcurrency)

	notes, err := bearcli.ListNotesForTag(ctx, opts.Tag)
	if err != nil {
		return fmt.Errorf("import: list notes for tag %q: %w", opts.Tag, err)
	}

	m := recommend.ComputeMetrics(opts.Tag, notes, nil)
	r := recommend.Recommend(m)
	emit(opts.Stdout, opts.Tag, len(notes), r, m.Buckets)
	return nil
}

// EmitWithNotesForTest runs the inference pass over a caller-supplied
// note set and writes the suggested stanza to w. Exposes the
// orchestrator's render path to external tests under
// tests/bear/cli/importcmd/ without requiring a live bearcli round
// trip (project rule forbids in-package tests). Production callers
// reach the same logic through RunImport.
func EmitWithNotesForTest(w io.Writer, tag string, notes []domain.Note) {
	m := recommend.ComputeMetrics(tag, notes, nil)
	r := recommend.Recommend(m)
	emit(w, tag, len(notes), r, m.Buckets)
}

// emit writes the candidate stanza with a rationale comment header.
// The stanza is plain TOML so the operator can pipe the command
// output straight into their config or copy a slice.
func emit(w io.Writer, tag string, noteCount int, r recommend.Recommendation, buckets []string) {
	_, _ = fmt.Fprintf(w, "# noxctl import %s — %d notes scanned\n", tag, noteCount)
	_, _ = fmt.Fprintf(w, "# recommend: %s (confidence: %s; deciding metric: %s) — %s\n",
		r.Blueprint, r.Confidence, r.DecidingMetric, r.Rationale)
	if r.Alternative != "" {
		_, _ = fmt.Fprintf(w, "# alternative: %s (choose it if your intent differs)\n", r.Alternative)
	}
	if strings.Count(tag, "/") == 0 && needsBuckets(r.Blueprint) {
		_, _ = fmt.Fprintln(w, "# note: if these sub-tags are themselves managed domains, this may be")
		_, _ = fmt.Fprintln(w, "#   an umbrella — a future vault-wide pass detects umbrellas automatically.")
	}
	_, _ = fmt.Fprintln(w, "#\n# Paste the [[domain]] block below into your noxctl.toml.")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "[[domain]]")
	_, _ = fmt.Fprintf(w, "  tag         = %q\n", tag)
	_, _ = fmt.Fprintf(w, "  index_title = %q\n", suggestIndexTitle(tag))
	_, _ = fmt.Fprintf(w, "  blueprint   = %q\n", r.Blueprint)
	if needsBuckets(r.Blueprint) {
		_, _ = fmt.Fprintf(w, "  buckets        = %s\n", tomlStringSlice(buckets))
	}
	if needsUnknownBucket(r.Blueprint) {
		_, _ = fmt.Fprintln(w, `  unknown_bucket = "Other"`)
	}
	if needsHubH2(r.Blueprint) {
		_, _ = fmt.Fprintln(w, `  hub_h2_prefix  = "Items"`)
	}
}

func needsBuckets(bp string) bool { return bp == "grouped-vertical" || bp == "hub-routed-with-subtag" }

// needsUnknownBucket reports whether the blueprint requires an unknown_bucket
// field. hub-routed, hub-routed-with-subtag, and grouped-vertical all require
// it per the dispatch contract; hub-routed does NOT take a buckets array.
func needsUnknownBucket(bp string) bool {
	return bp == "grouped-vertical" || bp == "hub-routed-with-subtag" || bp == "hub-routed"
}

func needsHubH2(bp string) bool { return bp == "hub-routed" }

// suggestIndexTitle generates a master-note title from the tag.
// Picks the last `/`-separated segment, title-cases it, and prepends
// the `✱ ` glyph the existing catalogs use to mark managed masters.
func suggestIndexTitle(tag string) string {
	parts := strings.Split(tag, "/")
	leaf := parts[len(parts)-1]
	if leaf == "" {
		return "✱ " + tag
	}
	return "✱ " + capitalize(leaf)
}

// capitalize uppercases the first rune of s; safe for empty strings.
func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// tomlStringSlice renders a Go string slice as a TOML array literal,
// e.g. ["Books", "Films"]. Single-line on purpose — keeps the
// emitted stanza compact for short bucket sets.
func tomlStringSlice(items []string) string {
	if len(items) == 0 {
		return "[]"
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = fmt.Sprintf("%q", item)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}
