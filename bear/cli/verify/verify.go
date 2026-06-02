// Package verify implements the `noxctl verify` hard-gate subcommand.
//
// Composes three vault-bound checks into a single PASS/FAIL signal that
// gates ship/release/migration cuts:
//
//  1. Plan parity — `engine.Plan` against the configured vault must
//     return zero drift. Catches catalog—reality divergence; requires
//     the plan-vs-apply parity fix in `bear/engine/plan.go` to be a
//     truthful signal.
//  2. Daemon log scan — `~/.cache/regen-watchd.log` since the most
//     recent `regen-watchd starting` line must have zero occurrences
//     of `LOOP detected`, `EMERGENCY DISABLE`, or `ERROR:`. Catches
//     runtime issues the per-cycle log surfaces but plan can't see.
//  3. Apply idempotency (opt-in via `--with-apply`) — runs `apply`
//     twice; the second pass must report zero changes across every
//     domain. Catches non-idempotent renderers that produce drift on
//     each cycle. Destructive (writes to vault); opt-in only.
//
// CI's `build.yml` already covers the hermetic tier (build/vet/lint
// /test/codegen/equivalence). This package is the operator-side
// counterpart that touches Bear directly.
package verify

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli/diag"
	"github.com/barad1tos/noxctl/bear/cliutil"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

// Options carries the resolved CLI inputs for Run.
type Options struct {
	// ConfigPath is the path to the noxctl.toml catalog (passed
	// through to `config.Load`). Empty falls back to the loader's
	// search order (./noxctl.toml then $NOXCTL_CONFIG).
	ConfigPath string
	// WithApply opts into the destructive apply-twice idempotency
	// check. Default false — verify stays read-only.
	WithApply bool
	// ApplyOpts is the template `engine.Apply` invocation verify
	// uses when `WithApply` is true. The caller fills Pins,
	// StatePath, LockPath, Features (typically via the catalog→Features
	// bridge in `bear/cliutil`) — verify overrides Domains and
	// Stderr at call time. Required when WithApply is true; ignored
	// otherwise. Without these the underlying `engine.Apply` errors
	// at flock-acquire with "AcquireApply open : no such file or
	// directory" before the idempotency check can even begin.
	ApplyOpts engine.ApplyOpts
	// LogPath overrides the daemon log location. Empty defaults to
	// `~/.cache/regen-watchd.log`.
	LogPath string
	// Strict promotes the "untracked notes detected" informational
	// warning to a FAIL. Default off.
	Strict bool
	// Output picks text|json; ValidateOutput enforces.
	Output string
	// Stdout / Stderr — io sinks (test injection).
	Stdout io.Writer
	Stderr io.Writer
}

// Status is the per-check verdict. Aliased to diag.Status so verify and
// doctor share one model; the JSON-stable string values are unchanged.
type Status = diag.Status

const (
	// StatusPass — check ran and the contract holds.
	StatusPass = diag.StatusPass
	// StatusFail — check ran and the contract is broken (e.g. drift
	// detected, log has warnings, idempotency violated).
	StatusFail = diag.StatusFail
	// StatusSkipped — check intentionally not run (e.g.
	// apply-idempotency without --with-apply).
	StatusSkipped = diag.StatusSkipped
	// StatusError — check could not run (e.g. bearcli unreachable,
	// config file missing, daemon log absent). Distinct from FAIL —
	// signals "verify can't make a verdict", not "verdict is no".
	StatusError = diag.StatusError
)

// Check is one named verification result. Aliased to diag.Check; verify
// leaves diag's additive Group/Remediation fields zero, so its JSON
// stays byte-identical.
type Check = diag.Check

// Summary aggregates per-status counts for the JSON output. Aliased to
// diag.Summary; verify never increments the additive Warn counter.
type Summary = diag.Summary

// Result is the full Run output — every check plus the summary. Aliased
// to diag.Result.
type Result = diag.Result

// ErrVerifyFailed is returned when one or more checks FAIL. Maps to
// CLI exit code 2 (same convention as `noxctl plan -detailed-exitcode`).
var ErrVerifyFailed = errors.New("noxctl verify: gate failed")

// ErrVerifyRuntimeError is returned when one or more checks could not
// run (StatusError). Maps to CLI exit code 1. Distinct from
// ErrVerifyFailed so callers can tell "stop, the gate said no" apart
// from "stop, I couldn't ask the question".
var ErrVerifyRuntimeError = errors.New("noxctl verify: runtime error")

// ErrVerifyInterrupted is returned when SIGINT or SIGTERM canceled
// the run mid-flight. Symmetric with `cli.ErrInterrupted`. The cmd
// layer maps this to `errInterrupted`, which `main.go` dispatches to
// `ExitInterrupted = 130` — the project-wide POSIX 128 + SIGINT
// convention. Without this, a Ctrl-C during `--with-apply` would
// surface as a generic StatusError — exit 1, hiding the operator's
// intent from any caller that branches on exit code.
//
// Takes priority over `ErrVerifyFailed` / `ErrVerifyRuntimeError` in
// `finalize`: the operator's "stop" signal trumps any check-level
// verdict.
var ErrVerifyInterrupted = errors.New("noxctl verify: interrupted")

// ValidateOutput rejects values other than "text" and "json". Hoisted
// to a public helper so cmd/noxctl can validate the flag before
// constructing Options. Delegates to diag.ValidateOutput so the error
// message stays identical to the shared model's.
func ValidateOutput(output string) error {
	return diag.ValidateOutput(output)
}

// Run is the verify orchestrator. Loads the catalog, runs each
// check, renders the result, and returns one of (nil, ErrVerifyFailed,
// ErrVerifyRuntimeError, render error).
//
// Initializes the bearcli semaphore at entry — `engine.Plan` and
// `engine.Apply` both consume the pool. Same constant as the daemon
// and apply paths (`engine.DefaultBearcliConcurrency`).
func Run(ctx context.Context, opts Options) error {
	defaultIOAndPool(&opts)

	// Bridge SIGINT / SIGTERM into ctx cancellation so the apply-
	// idempotency check's engine.Apply call can yield cleanly and
	// finalize can translate to ErrVerifyInterrupted (exit 130 at
	// the cmd layer). Mirrors cli.RunPlan's signal handling — verify
	// owns the same boundary.
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	result := Result{
		SchemaVersion: diag.SchemaVersion,
		StartedAt:     time.Now().UTC(),
		Checks:        make([]Check, 0, 3),
	}

	// Load the catalog once — every check needs it. A load error
	// fails the gate immediately (no point running plan without a
	// catalog).
	domains, cat, err := config.Load(opts.ConfigPath)
	if err != nil {
		result.Checks = append(result.Checks, Check{
			Name:    "catalog-load",
			Status:  StatusError,
			Message: fmt.Sprintf("config.Load(%q) failed: %v", opts.ConfigPath, err),
		})
		result.CompletedAt = time.Now().UTC()
		return finalize(sigCtx, opts, &result)
	}
	// Apply catalog-declared locale before plan-parity renders any
	// hub/master strings — otherwise verify diffs against the wrong
	// locale and false-reports drift on en/uk-mismatched vaults.
	if cat != nil && cat.Meta.Locale != "" {
		domain.SetLocale(cat.Meta.Locale)
	}
	opts.ApplyOpts.Features = cliutil.FeaturesFromCatalog(cat)

	result.Checks = append(
		result.Checks,
		checkPlanParity(sigCtx, opts, domains),
		checkDaemonLog(opts),
	)
	if opts.WithApply {
		result.Checks = append(result.Checks, checkApplyIdempotency(sigCtx, opts, domains))
	} else {
		result.Checks = append(result.Checks, Check{
			Name:    "apply-idempotency",
			Status:  StatusSkipped,
			Message: "destructive check; opt-in via --with-apply",
		})
	}

	result.CompletedAt = time.Now().UTC()
	return finalize(sigCtx, opts, &result)
}

// defaultIOAndPool fills nil Stdout/Stderr with the process streams
// and seeds the bearcli pool. Idempotent — sync.Once inside
// bearcli.SetConcurrency suppresses repeat calls.
func defaultIOAndPool(opts *Options) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	bearcli.SetConcurrency(engine.DefaultBearcliConcurrency)
}

// finalize computes the Summary, renders, and returns the
// INTERRUPTED/FAIL/ERROR/nil dispatch.
//
// Render happens BEFORE the ctx-cancellation check so the operator
// sees what completed before SIGINT arrived — matches cli.RunPlan's
// render-then-translate ordering and gives the operator
// post-mortem visibility instead of a silent stop.
func finalize(ctx context.Context, opts Options, result *Result) error {
	result.Summary = diag.Summarize(result.Checks)
	if err := diag.Render(opts.Stdout, opts.Output, "verify", result); err != nil {
		return err
	}
	// Interrupted trumps every other verdict. The operator's
	// Ctrl-C intent is "stop, signal exit 130" — not "I want to
	// know if drift was found before you stopped".
	if ctx.Err() != nil {
		return ErrVerifyInterrupted
	}
	switch {
	case result.Summary.Error > 0:
		return ErrVerifyRuntimeError
	case result.Summary.Fail > 0:
		return ErrVerifyFailed
	}
	return nil
}
