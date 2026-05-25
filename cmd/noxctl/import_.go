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
	Long: `import scans every Bear note carrying the supplied tag
and prints a [[domain]] block you can paste into noxctl.toml. The
blueprint is chosen by one of two paths:

  - 0 notes               → flat-list (lowest-friction starter)
  - Uniform sub-tag       → grouped-vertical with the observed buckets
  - Anything else         → flat-list (safe fallback)

The "uniform sub-tag" check is strict: EVERY note must carry the
same #tag/<bucket> pattern for grouped-vertical to win. A single
outlier (notes carrying only #tag with no sub-tag) makes the
heuristic fall through to the next branch. Clean up outliers in Bear
before re-running import if you expect grouped-vertical inference.

hub-routed is NOT auto-detected — its Tier-2 hub signal overlaps
with grouped-vertical at the canonical-line level, and atom-body H2
sections belong to the operator's content, not the catalog. Switch
to hub-routed manually in the emitted stanza if you want
bucket-per-hub routing.

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
