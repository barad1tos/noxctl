package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli/plan"
	"github.com/barad1tos/noxctl/bear/engine"
)

// errDriftDetected is the cmd-level sentinel that main.go's exit
// mapper recognizes. plan.Run returns plan.ErrDriftDetected; runPlan
// translates so the rest of the cmd/noxctl error-mapping code stays
// unchanged (CLI-04 ExitDiffPresent=2 contract).
var errDriftDetected = errors.New("noxctl: drift detected")

// CLI-state for plan-specific flags.
var (
	planColor        string // --color=auto|always|never (default "auto")
	planOutput       string // -o text|json (default "text")
	planConfigSource string // --config-source=toml|hardcoded|both (D-01, D-04)
)

// planCmd is the real `noxctl plan` subcommand. Replaces the
// stub. Read-only preview of what `noxctl apply` would change.
var planCmd = &cobra.Command{
	Use:   "plan [tag]",
	Short: "Preview changes against the current Bear corpus",
	Long: `Plan reads noxctl.toml, fetches the current Bear state read-only,
and prints what 'noxctl apply' would change. NEVER mutates Bear,
NEVER updates state.json.

With no positional argument, plans every domain in the closed catalog.
With one argument, plans only that domain (e.g. 'noxctl plan library/poetry').

Output:
  text (default) — Terraform-style colored diff on TTY, plain when piped
  json (-o json) — stable schema for tooling, see README

Glyphs:
  ~ replace   + create   ✓ clean   ✗ error   ⚠ Untracked tag families

Color override (auto by default):
  --color=auto    detect TTY + honor NO_COLOR
  --color=always  force ANSI even when piped
  --color=never   plain ASCII

Config source (D-04 default toml; bridge-window opt-ins per D-01):
  --config-source=toml       (default) reads noxctl.toml
  --config-source=hardcoded  reads the hardcoded domain registry ( bridge)
  --config-source=both       renders both paths and reports per-domain parity

Plan reports the next apply tick's delta only (single-pass per CONTEXT D-06).
On a freshly-edited corpus, expect drift to remain after the first apply;
rerun 'noxctl plan' after 'noxctl apply' to confirm convergence.

Exit codes: 0=no drift, 2=drift exists, 1=error, 130=interrupted.
With --config-source=both, exit 2 also signals any parity mismatch.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlan,
}

// runPlan is the thin RunE shim that adapts cobra.Command state +
// cmd-level globals into a plan.Options struct, then delegates to
// plan.Run. All test-worthy business logic lives in bear/cli/plan.
func runPlan(cmd *cobra.Command, args []string) error {
	color, err := engine.ParseColorMode(planColor)
	if err != nil {
		return err
	}
	src, err := engine.ParseConfigSource(planConfigSource)
	if err != nil {
		return err
	}
	if err = plan.ValidateOutput(planOutput); err != nil {
		return err
	}

	legacyPath, target := pinPaths()
	runErr := plan.Run(cmd.Context(), plan.Options{
		Color:     color,
		Output:    planOutput,
		Source:    src,
		Args:      args,
		CfgPath:   cfgPath,
		PinLegacy: legacyPath,
		PinTarget: target,
		Verbose:   verbose,
		Stdout:    os.Stdout,
		Stderr:    os.Stderr,
	})
	// Map plan-layer sentinels back to cmd-layer sentinels so main.go's
	// exit-code dispatch (errors.Is on errDriftDetected, errInterrupted)
	// continues to fire — keeps the CLI-04 / CLI-08 contracts unchanged.
	switch {
	case errors.Is(runErr, plan.ErrDriftDetected):
		return errDriftDetected
	case errors.Is(runErr, plan.ErrInterrupted):
		return errInterrupted
	}
	return runErr
}

// registerEnumCompletion wires a static list of valid values as the
// shell-completion source for flagName. Extracted because three
// near-identical RegisterFlagCompletionFunc blocks crossed the dupl
// threshold; the helper folds them to single-line registrations.
func registerEnumCompletion(cmd *cobra.Command, flagName string, values []string) {
	_ = cmd.RegisterFlagCompletionFunc(flagName,
		func(_ *cobra.Command, _ []string, _ string) ([]string, cobra.ShellCompDirective) {
			return values, cobra.ShellCompDirectiveNoFileComp
		})
}

func init() {
	planCmd.Flags().StringVar(&planColor, "color", "auto",
		"color output: auto|always|never (auto detects TTY + honors NO_COLOR)")
	planCmd.Flags().StringVarP(&planOutput, "output", "o", "text",
		"output format: text|json")
	planCmd.Flags().StringVar(&planConfigSource, "config-source", "toml",
		"config source: toml (default) | hardcoded | both")
	registerEnumCompletion(planCmd, "color", []string{"auto", "always", "never"})
	registerEnumCompletion(planCmd, "output", []string{"text", "json"})
	registerEnumCompletion(planCmd, "config-source", []string{"toml", "hardcoded", "both"})
	rootCmd.AddCommand(planCmd)
}
