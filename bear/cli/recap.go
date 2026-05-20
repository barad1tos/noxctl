package cli

// recap.go renders the Ansible-style PRE-PASSES + PLAY RECAP block
// to an io.Writer based on an engine.ApplyResult. Pure formatting
// helper extracted from cmd/noxctl/recap.go so the unit tests can
// live under tests/bear/cli/recap/ as an external package.

import (
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"

	"github.com/barad1tos/noxctl/bear/engine"
)

// RenderRecap writes the PRE-PASSES + PLAY RECAP blocks to out as
// structured stdout output. When quiet is true, the section
// headers and OK rows are suppressed but FAILURE rows still emit so
// the operator never misses a non-zero failed=N count.
//
// Format mirrors Ansible's PLAY RECAP shape — text/tabwriter elastic
// tabstops produce aligned columns from tab-separated text. Pipe-safe
// (no ANSI escapes; padding via spaces only).
//
// Map iteration is deterministic via sortedKeys — Go map iteration
// randomization would otherwise cause false diffs in the rendered
// output.
//
// Nil result is a no-op: callers may pass result==nil when engine.Apply
// returned a top-level error before populating any counts.
func RenderRecap(out io.Writer, result *engine.ApplyResult, quiet bool) {
	if result == nil {
		return
	}

	if !quiet {
		_, _ = fmt.Fprintln(out, "")
		_, _ = fmt.Fprintln(out, "PRE-PASSES "+strings.Repeat("*", 50))
	}
	pw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, name := range sortedKeys(result.PrePasses) {
		c := result.PrePasses[name]
		if quiet && c.Failed == 0 {
			continue
		}
		_, _ = fmt.Fprintf(pw, "%s\t: ok=%d\tchanged=%d\tfailed=%d\n",
			name, c.OK, c.Changed, c.Failed)
	}
	_ = pw.Flush()

	if !quiet {
		_, _ = fmt.Fprintln(out, "")
		_, _ = fmt.Fprintln(out, "PLAY RECAP "+strings.Repeat("*", 50))
	}
	rw := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	for _, tag := range sortedKeys(result.Domains) {
		c := result.Domains[tag]
		if quiet && c.Failed == 0 {
			continue
		}
		_, _ = fmt.Fprintf(rw, "%s\t: created=%d\tchanged=%d\tunchanged=%d\tfailed=%d\n",
			tag, c.Created, c.Changed, c.Unchanged, c.Failed)
	}
	_ = rw.Flush()
}

// sortedKeys returns map keys in sort.Strings ascending order. Used
// to deterministically iterate ApplyResult maps so the rendered diff
// stays stable across runs.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
