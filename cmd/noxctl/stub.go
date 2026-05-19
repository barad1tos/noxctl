package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// stubCmd builds a Cobra command for a subcommand whose body is
// scheduled to land in a later release (init/destroy/import).
// The returned *cobra.Command prints msg to stderr and returns nil
// (exit 0) so the binary stays useful while the verb is incomplete.
//
// Stubs deliberately skip the PersistentPreRunE preflight — a user
// running a stub in a fresh directory expects the helpful "not yet
// implemented" notice, not a config-load error pointing at a file
// they haven't created yet. When a stub gets a real implementation
// it must wire its own RunE-level preflight so config errors surface
// cleanly at that point.
//
// Extracted to dodge the dupl ≥ 30-token gate: every stub would
// otherwise repeat the same Cobra-literal shape. Centralizing also
// keeps the "not yet implemented" wording consistent.
//
// args may be nil for stubs that accept any number of positional
// arguments, or cobra.ExactArgs(N) for `destroy`/`import` which
// require a positional Bear-tag.
func stubCmd(use, short, msg string, args cobra.PositionalArgs) *cobra.Command {
	return &cobra.Command{
		Use:   use,
		Short: short,
		Args:  args,
		RunE: func(_ *cobra.Command, _ []string) error {
			_, _ = fmt.Fprintln(os.Stderr, msg)
			return nil
		},
	}
}
