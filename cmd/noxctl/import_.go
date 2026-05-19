package main

import "github.com/spf13/cobra"

// importCmd is the `noxctl import <bear-tag>` stub for the adopt
// logic — create a stanza for an existing untracked Bear tag.
// Filename `import_.go` (trailing underscore) avoids the Go keyword
// collision with the `import` identifier.
var importCmd = stubCmd(
	"import <bear-tag>",
	"Adopt an existing untracked Bear tag into noxctl.toml",
	"import not yet implemented. Run `noxctl validate` to check the config.",
	cobra.ExactArgs(1),
)

func init() { rootCmd.AddCommand(importCmd) }
