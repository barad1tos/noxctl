package main

import (
	"errors"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/engine"
)

// errDriftDetected is the cmd-level sentinel that main.go's exit
// mapper recognizes. cli.RunPlan returns cli.ErrDriftDetected; runPlan
// translates so the rest of the cmd/noxctl error-mapping code stays
// unchanged (Terraform-style detailed-exitcode: 2 = diff present).
var errDriftDetected = errors.New("noxctl: drift detected")

// CLI-state for plan-specific flags.
var (
	planColor  string // --color=auto|always|never (default "auto")
	planOutput string // -o text|json (default "text")
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

Plan reports the next apply tick's delta only (single-pass).
On a freshly-edited corpus, expect drift to remain after the first apply;
rerun 'noxctl plan' after 'noxctl apply' to confirm convergence.

Exit codes: 0=no drift, 2=drift exists, 1=error, 130=interrupted.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runPlan,
}

// runPlan is the thin RunE shim that adapts cobra.Command state +
// cmd-level globals into a cli.PlanOptions struct, then delegates to
// cli.RunPlan. All test-worthy business logic lives in bear/cli/plan.
func runPlan(cmd *cobra.Command, args []string) error {
	color, err := engine.ParseColorMode(planColor)
	if err != nil {
		return err
	}
	if err = cli.ValidateOutput(planOutput); err != nil {
		return err
	}

	legacyPath, target := pinPaths()
	runErr := cli.RunPlan(cmd.Context(), cli.PlanOptions{
		Color:     color,
		Output:    planOutput,
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
	// continues to fire — keeps the detailed-exitcode contract intact.
	switch {
	case errors.Is(runErr, cli.ErrDriftDetected):
		return errDriftDetected
	case errors.Is(runErr, cli.ErrInterrupted):
		return errInterrupted
	}
	return runErr
}

// registerEnumCompletion wires a static list of valid values as the
// shell-completion source for flagName. Extracted because two
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
	registerEnumCompletion(planCmd, "color", []string{"auto", "always", "never"})
	registerEnumCompletion(planCmd, "output", []string{"text", "json"})
	rootCmd.AddCommand(planCmd)
}
