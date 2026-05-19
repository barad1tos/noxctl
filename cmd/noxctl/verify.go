package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
)

// errVerifyFailed and errVerifyError are cmd-level sentinels for
// main.go's exit-code mapper. verify.Run returns
// verify.ErrVerifyFailed / verify.ErrVerifyRuntimeError; runVerify
// translates so the rest of the cmd/noxctl error-mapping code stays
// unchanged.
var (
	errVerifyFailed  = errors.New("noxctl: verify gate failed")
	errVerifyRuntime = errors.New("noxctl: verify runtime error")
)

// verify-specific flag state.
var (
	verifyWithApply bool
	verifyLogPath   string
	verifyStrict    bool
	verifyOutput    string
)

// verifyCmd is the `noxctl verify` hard-gate subcommand. Composes
// three vault-bound checks (plan-parity, daemon-log scan, opt-in
// apply-idempotency) into a single PASS/FAIL signal that gates ship
// / release / migration cuts.
var verifyCmd = &cobra.Command{
	Use:   "verify",
	Short: "Hard gate: verify catalog ↔ vault alignment",
	Long: `Verify runs three vault-bound checks and returns a single PASS/FAIL signal:

  1. plan-parity      — noxctl plan against the configured vault must
                        report zero drift across every domain.
  2. daemon-log       — ~/.cache/regen-watchd.log since the most recent
                        "regen-watchd starting" line must have zero
                        LOOP detected / EMERGENCY DISABLE / ERROR: lines.
  3. apply-idempotency — (opt-in via --with-apply) run apply twice;
                        the second pass must be a strict no-op. Destructive:
                        the first pass writes to the vault if drifted.

Read-only by default. CI's build.yml already covers the hermetic tier
(go build / vet / lint / test / codegen / equivalence); verify is the
operator-side counterpart that touches Bear.

Exit codes: 0 = PASS, 2 = FAIL (drift / warnings / non-idempotent), 1 =
ERROR (a check could not run — bearcli unreachable, log absent, etc.).`,
	RunE: runVerify,
}

// runVerify is the thin RunE shim that adapts cobra state into a
// verify.Options struct and delegates to verify.Run. Mirrors plan.go's
// shape — all business logic lives in bear/cli/verify.
//
// When `--with-apply` is set, runVerify also primes the
// `verify.Options.ApplyOpts` template with the same Pins / paths /
// Features `noxctl apply` would use. Without this priming the
// idempotency check errors at flock-acquire ("AcquireApply open …")
// before the second pass runs. Catalog load happens here (cmd
// layer) because `featuresFromCatalog` lives in cmd/noxctl/preflight.go
// and the engine→config import direction is forbidden.
func runVerify(cmd *cobra.Command, _ []string) error {
	// Output validation happens inside `verify.Run → render`; we
	// don't duplicate the check at the cmd layer (single owner).
	opts := verify.Options{
		ConfigPath: cfgPath,
		WithApply:  verifyWithApply,
		LogPath:    verifyLogPath,
		Strict:     verifyStrict,
		Output:     verifyOutput,
		Stdout:     os.Stdout,
		Stderr:     os.Stderr,
	}
	if verifyWithApply {
		applyTemplate, err := buildVerifyApplyTemplate()
		if err != nil {
			return err
		}
		opts.ApplyOpts = applyTemplate
	}
	runErr := verify.Run(cmd.Context(), opts)
	switch {
	case errors.Is(runErr, verify.ErrVerifyInterrupted):
		// `errInterrupted` is the existing cmd-level sentinel for
		// SIGINT-during-mutation; main.go maps it to
		// `ExitInterrupted = 130` (POSIX 128 + SIGINT). Without this
		// arm, a Ctrl-C during `--with-apply` surfaces as a generic
		// runtime error (exit 1) instead of the project-wide
		// convention `apply`/`plan` already honor.
		return errInterrupted
	case errors.Is(runErr, verify.ErrVerifyFailed):
		return errVerifyFailed
	case errors.Is(runErr, verify.ErrVerifyRuntimeError):
		return errVerifyRuntime
	}
	return runErr
}

// buildVerifyApplyTemplate primes `engine.ApplyOpts` with the
// catalog-derived features + pin registry + project-local state/lock
// paths that `noxctl apply` uses. Returns the template (Domains and
// Stderr are filled in by verify at call time).
//
// Errors only when the catalog itself can't be loaded — pin load is
// best-effort (nil-safe registry per bear/pins.go), matching
// runApply's behavior in cmd/noxctl/apply.go.
func buildVerifyApplyTemplate() (engine.ApplyOpts, error) {
	_, cat, loadErr := config.Load(cfgPath)
	if loadErr != nil {
		return engine.ApplyOpts{}, fmt.Errorf("verify --with-apply: %s",
			config.FormatLoadError(loadErr, cfgPath))
	}
	_, pinTarget := pinPaths()
	pins, _ := bear.LoadPinRegistry(pinTarget)
	return engine.ApplyOpts{
		Pins:            pins,
		StatePath:       "./.noxctl/state.json",
		LockPath:        "./.noxctl/.lock",
		Features:        featuresFromCatalog(cat),
		AuditEnabled:    false,
		DailyDefaultTag: dailyDefaultTagFromCatalog(cat),
		PromotionRules:  promotionRulesFromCatalog(cat),
	}, nil
}

func init() {
	verifyCmd.Flags().BoolVar(&verifyWithApply, "with-apply", false,
		"include destructive apply-twice idempotency check (writes to vault)")
	verifyCmd.Flags().StringVar(&verifyLogPath, "log-path", "",
		"override daemon log path (default ~/.cache/regen-watchd.log)")
	verifyCmd.Flags().BoolVar(&verifyStrict, "strict", false,
		"fail on warnings (untracked tag-families)")
	verifyCmd.Flags().StringVarP(&verifyOutput, "output", "o", "text",
		"output format: text|json")
	registerEnumCompletion(verifyCmd, "output", []string{"text", "json"})
	rootCmd.AddCommand(verifyCmd)
}
