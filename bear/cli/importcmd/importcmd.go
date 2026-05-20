// Package importcmd implements the `noxctl import <bear-tag>`
// subcommand body. Scans an untagged-by-noxctl Bear tag, classifies
// its note shape, and emits a candidate [[domain]] stanza to stdout.
//
// import never edits noxctl.toml. The operator copy-pastes the
// suggested stanza into their config after reviewing — keeps the
// catalog under operator authorship and lets them tweak the
// generated values (index_title localization, bucket names,
// blueprint choice) before commit.
//
// Package name is `importcmd` to avoid the Go reserved word.
package importcmd

import (
	"context"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Options is the input bundle for Run.
type Options struct {
	Tag    string    // REQUIRED — Bear tag to scan, e.g. "research/papers"
	Stdout io.Writer // stanza + summary land here (typically os.Stdout)
}

// Run scans the supplied Bear tag, infers a likely blueprint based
// on note count and structural shape, and writes a candidate
// [[domain]] stanza plus a one-paragraph rationale to opts.Stdout.
// Never writes to noxctl.toml — the operator owns that file.
//
// Heuristic order:
//
//   - 0 notes → emit flat-list, lowest-friction starter.
//   - All notes share a common single-segment sub-tag (e.g. every
//     note carries `#tag/Books`) → emit flat-table with the observed
//     sub-tag set as `buckets`.
//   - Otherwise → emit flat-list and note the fallback.
//
// hub-routed deliberately NOT auto-detected: the structural signal
// (Tier-2 hub notes routing buckets via canonical lines) is the
// same shape flat-table emits, so a reliable disambiguation needs
// operator input. Notes inside a hub-routed atom carry their own
// H2 sections (Sections, References, etc.) — counting those would
// classify content structure as catalog structure. Operators who
// want hub-routed pick it themselves after running import.
func Run(ctx context.Context, opts Options) error {
	notes, err := domain.ListNotesForTag(ctx, opts.Tag)
	if err != nil {
		return fmt.Errorf("import: list notes for tag %q: %w", opts.Tag, err)
	}

	suggestion := infer(opts.Tag, notes)
	emit(opts.Stdout, opts.Tag, len(notes), suggestion)
	return nil
}

// EmitWithNotesForTest runs the inference pass over a caller-
// supplied note set and writes the suggested stanza to w. Exposes
// the orchestrator's render path to external tests under
// tests/bear/cli/importcmd/ without requiring a live bearcli round
// trip (project rule forbids in-package tests). Production callers
// reach the same logic through Run, which calls domain.ListNotesFor
// Tag for real and then routes the result through the identical
// infer + emit pair.
func EmitWithNotesForTest(w io.Writer, tag string, notes []domain.Note) {
	emit(w, tag, len(notes), infer(tag, notes))
}

// suggestion captures the inferred stanza shape. Kept as a typed
// value (not raw TOML) so future renderer changes don't have to
// re-derive the choice from a string.
type suggestion struct {
	blueprint string
	rationale string
	buckets   []string // populated for flat-table
}

// infer picks a blueprint based on observable structure across the
// note set.
func infer(tag string, notes []domain.Note) suggestion {
	if len(notes) == 0 {
		return suggestion{
			blueprint: "flat-list",
			rationale: "tag has no notes yet — flat-list is the lowest-friction starter.",
		}
	}
	if buckets, ok := commonSubTagBuckets(tag, notes); ok {
		return suggestion{
			blueprint: "flat-table",
			rationale: fmt.Sprintf(
				"every note carries a single-segment sub-tag (%d distinct buckets observed).",
				len(buckets)),
			buckets: buckets,
		}
	}
	return suggestion{
		blueprint: "flat-list",
		rationale: "no obvious bucket structure detected — flat-list is the safe fallback. " +
			"Switch to hub-routed manually if you want Tier-2 hub notes routing buckets.",
	}
}

// commonSubTagBuckets checks whether every note carries the same
// `#<tag>/<sub>` prefix and returns the sorted distinct sub-tag set
// when the answer is yes. Empty input or any note that misses the
// prefix returns `_, false` — the caller falls back to the next
// heuristic.
func commonSubTagBuckets(tag string, notes []domain.Note) ([]string, bool) {
	if len(notes) == 0 {
		return nil, false
	}
	prefix := tag + "/"
	seen := make(map[string]struct{})
	for _, n := range notes {
		bucket := ""
		for _, t := range n.Tags {
			clean := strings.TrimPrefix(t, "#")
			if sub, ok := strings.CutPrefix(clean, prefix); ok && sub != "" && !strings.Contains(sub, "/") {
				bucket = sub
				break
			}
		}
		if bucket == "" {
			return nil, false
		}
		seen[bucket] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for b := range seen {
		out = append(out, b)
	}
	sort.Strings(out)
	return out, true
}

// emit writes the candidate stanza followed by a rationale + manual
// next-step hint. The stanza is plain TOML so the operator can pipe
// the command output straight into their config or copy a slice.
func emit(w io.Writer, tag string, noteCount int, s suggestion) {
	_, _ = fmt.Fprintf(w, "# noxctl import %s — %d notes scanned\n", tag, noteCount)
	_, _ = fmt.Fprintf(w, "# %s\n", s.rationale)
	_, _ = fmt.Fprintln(w, "#")
	_, _ = fmt.Fprintln(w, "# Paste the [[domain]] block below into your noxctl.toml")
	_, _ = fmt.Fprintln(w, "# (review the field values first — they are educated guesses).")
	_, _ = fmt.Fprintln(w)
	_, _ = fmt.Fprintln(w, "[[domain]]")
	_, _ = fmt.Fprintf(w, "  tag         = %q\n", tag)
	_, _ = fmt.Fprintf(w, "  index_title = %q\n", suggestIndexTitle(tag))
	_, _ = fmt.Fprintf(w, "  blueprint   = %q\n", s.blueprint)
	if s.blueprint == "flat-table" {
		_, _ = fmt.Fprintf(w, "  buckets        = %s\n", tomlStringSlice(s.buckets))
		_, _ = fmt.Fprintln(w, `  unknown_bucket = "Other"`)
	}
}

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
