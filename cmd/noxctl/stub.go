package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// stubCmd builds a Cobra command for a subcommand whose body is
// scheduled to land in a later phase (init/plan/apply/daemon/
// destroy/import). The returned *cobra.Command prints msg to stderr
// and returns nil (exit 0) per 01-CONTEXT.md §Discretion.
//
// Stubs deliberately skip the PersistentPreRunE preflight — a user
// running `noxctl plan` in a fresh directory expects the helpful
// Phase-Y notice ("Run `noxctl validate` to check the config."), not
// a config-load error pointing at a file they haven't created yet.
// When a stub gets a real Phase-N implementation it must wire its
// own RunE-level preflight (or PersistentPreRunE) so config errors
// surface cleanly at that point — not before.
//
// Extracted to dodge the dupl ≥ 30-token gate: all six stubs share
// the same Cobra-literal shape; inlining them per file produced 12
// dupl findings. Per "Never raise thresholds — above limit
// = extract a helper. The threshold is the point."
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
