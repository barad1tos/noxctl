package main

// initSubCmd is the `noxctl init` stub. ships an interactive
// wizard that scaffolds a starter noxctl.toml. Filename `init_.go`
// (trailing underscore) avoids the Go keyword collision per D-03.
var initSubCmd = stubCmd(
	"init",
	"Generate a starter noxctl.toml interactively",
	"init not yet implemented. Run `noxctl validate` to check the config.",
	nil,
)

func init() { rootCmd.AddCommand(initSubCmd) }
