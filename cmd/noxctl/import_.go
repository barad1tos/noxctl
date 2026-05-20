package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli/importcmd"
)

// importCmd scans an untracked Bear tag and prints a candidate
// [[domain]] stanza for noxctl.toml. Filename `import_.go` (trailing
// underscore) avoids the Go keyword collision with the `import`
// identifier.
//
// Read-only: import lists notes via bearcli, applies a blueprint
// heuristic, and emits the suggested stanza to stdout. It never
// edits noxctl.toml — the operator copies the stanza in after
// reviewing, since blueprint choice and bucket naming benefit from
// human judgment.
var importCmd = &cobra.Command{
	Use:   "import <bear-tag>",
	Short: "Print a candidate [[domain]] stanza for an existing Bear tag",
	Long: `import scans every Bear note carrying the supplied tag
and prints a [[domain]] block you can paste into noxctl.toml. The
blueprint is chosen heuristically:

  - 0 notes               → flat-list (lowest-friction starter)
  - Uniform sub-tag       → flat-table with the observed buckets
  - Many ## H2 headers    → hub-routed (author-grouped shape)
  - Anything else         → flat-list (safe fallback)

import never writes to noxctl.toml. After reviewing the suggested
fields (index_title, bucket names, hub_h2_prefix), paste the block
into your config and run 'noxctl validate' to confirm the schema.`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

// runImport is the import RunE.
func runImport(cmd *cobra.Command, args []string) error {
	return runWithSignalContext(cmd, func(ctx context.Context) error {
		return importcmd.Run(ctx, importcmd.Options{
			Tag:    args[0],
			Stdout: os.Stdout,
		})
	})
}

func init() { rootCmd.AddCommand(importCmd) }
