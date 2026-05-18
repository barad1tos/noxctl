package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// versionCmd ships `noxctl version` alongside the auto-wired
// `--version` flag. Both paths print the same string. Pitfall 6
// (--version subcommand-scoped collision) is avoided because Cobra's
// auto --version is rooted on rootCmd via the Version field — child
// commands don't redeclare it.
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print noxctl version",
	Run: func(cmd *cobra.Command, _ []string) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(),
			"noxctl %s (commit %s, built %s)\n", version, commit, date)
	},
}

func init() { rootCmd.AddCommand(versionCmd) }
