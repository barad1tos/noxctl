package main

import (
	"context"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli"
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
	Long: `import scans every Bear note carrying the supplied tag,
computes 7 structural metrics (bucket cardinality, sub-tag coverage,
author-body signal, etc.), and runs them through an ordered decision
tree that covers the blueprints a single-tag scan can infer:

  flat-list               no bucket signal detected
  grouped-vertical        few atoms per bucket — inline sections
  hub-routed-with-subtag  many atoms per sub-tag bucket — Tier-2 hubs
  hub-routed              2-level tag with strong author/source signal

Single-tag import does not auto-generate umbrella domains. Umbrellas depend
on multiple managed child domains, so configure them manually from the
vault-wide structure. When a top-level bucketed tag may really be an
umbrella, the emitted comments call that out.

The output includes a "# recommend:" comment with the chosen
blueprint, confidence grade, deciding metric, and rationale. When
the decision is close, an "# alternative:" line names the runner-up
and explains why the primary won.

import never writes to noxctl.toml. After reviewing the suggested
fields (index_title, bucket names), paste the block into your
config and run 'noxctl validate' to confirm the schema.`,
	Args: cobra.ExactArgs(1),
	RunE: runImport,
}

// runImport is the import RunE.
func runImport(cmd *cobra.Command, args []string) error {
	return runWithSignalContext(cmd, func(ctx context.Context) error {
		return cli.RunImport(ctx, cli.ImportOptions{
			Tag:    args[0],
			Stdout: os.Stdout,
		})
	})
}

func init() { rootCmd.AddCommand(importCmd) }
