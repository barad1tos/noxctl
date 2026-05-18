package main

import "github.com/spf13/cobra"

// destroyCmd is the `noxctl destroy <tag>` stub. ships the
// teardown logic (delete master, strip canonical lines from atomics).
// `cobra.ExactArgs(1)` makes Cobra reject `noxctl destroy` (no arg)
// with the standard "accepts 1 arg(s), received 0" error before the
// stub body runs — useful as a smoke-test invariant.
var destroyCmd = stubCmd(
	"destroy <tag>",
	"Remove a managed Bear tag (canonical lines + master)",
	"destroy not yet implemented. Run `noxctl validate` to check the config.",
	cobra.ExactArgs(1),
)

func init() { rootCmd.AddCommand(destroyCmd) }
