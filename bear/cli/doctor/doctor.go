// Package doctor implements the `noxctl doctor` read-only environment
// preflight subcommand.
//
// doctor answers one operator question — "is my environment ready to
// run noxctl against my Bear vault?" — before any mutation happens. It
// assembles five groups of checks (System / Bear DB / Config / State /
// Daemon) into a diag.Result, renders it via diag.Render(..., "doctor",
// ...), and returns a verdict.
//
// Hard invariant: doctor MUTATES NOTHING. It never invokes bearcli (it
// only stats bearcli.BinaryPath); it only runs the read-only `launchctl
// print` subcommand (never the service-lifecycle subcommands); it
// proves DB readability with os.Open + immediate close; and it
// delegates config validity to config.Load + config.FormatLoadError
// rather than re-implementing schema validation.
//
// Exit-code contract (CONTEXT D-locked): blocking problems — no
// Bear.app, DB unreadable, invalid config, missing bearcli — surface as
// an error-status check, which makes Run return ErrNotReady → exit 1.
// Optional/missing infra — daemon not loaded, first run, stale state,
// Bear running — surface as warn and never fail the gate (exit 0).
package doctor

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/barad1tos/noxctl/bear/cli/diag"
)

// ErrNotReady is returned by Run when at least one check is StatusError
// — a blocking problem the operator must fix before mutating their
// vault. The cmd layer maps it to exit 1 (Cobra's default for a
// returned error), distinct from the warnings-only path which returns
// nil → exit 0.
var ErrNotReady = errors.New("noxctl doctor: environment not ready")

// StaleThreshold is how old state.LastApply / the daemon log may be
// before doctor warns that they are stale. Seven days brackets a normal
// editing cadence: a vault touched within the last week is "fresh",
// anything older earns a non-blocking nudge to re-apply. Warn-only —
// staleness never fails the gate.
const StaleThreshold = 7 * 24 * time.Hour

// defaultStatePath mirrors the per-project state location every other
// subcommand uses (./.noxctl/state.json, the Terraform-style
// per-project layout).
const defaultStatePath = "./.noxctl/state.json"

// bearDatabaseFile is the SQLite file doctor opens (read-only) under
// BearDBDir to prove the database is readable.
const bearDatabaseFile = "database.sqlite"

// Options carries the resolved inputs plus injectable seams so tests
// need no real Bear, launchctl, or filesystem. Every seam's zero value
// falls back to the real implementation, so production callers fill
// only the resolved paths + Output + IO sinks.
type Options struct {
	// ConfigPath is the noxctl.toml path passed to config.Load for the
	// config.found / config.valid checks.
	ConfigPath string
	// BearDBDir is the resolved Bear DB directory. The cmd layer fills
	// it via resolveBearDB so doctor stays catalog-agnostic.
	BearDBDir string
	// StatePath is the state.json location; empty defaults to
	// ./.noxctl/state.json.
	StatePath string
	// LogPath is the daemon log location; empty defaults to
	// ~/.cache/regen-watchd.log (the same path verify resolves).
	LogPath string
	// LogPathError is a non-fatal daemon-config/log-path resolution
	// problem. doctor reports it as daemon.log warn instead of aborting
	// before the diagnostic report can render.
	LogPathError error
	// Output picks text|json; diag.ValidateOutput enforces it.
	Output string
	// Stdout / Stderr are the IO sinks (nil defaults to the process
	// streams).
	Stdout io.Writer
	Stderr io.Writer

	// StatFn stats a path; nil defaults to os.Stat.
	StatFn func(string) (os.FileInfo, error)
	// OpenFn opens a file read-only; nil defaults to os.Open. doctor
	// closes the handle immediately — it never reads contents.
	OpenFn func(string) (*os.File, error)
	// LaunchctlPrintFn inspects the launchd service read-only; nil
	// defaults to a fixed-argv `launchctl print gui/<uid>/<label>` that
	// returns the command output plus the exec error.
	LaunchctlPrintFn func(label string) (string, error)
	// ProcessRunningFn reports whether a named process is running; nil
	// defaults to a fixed-argv read-only `pgrep -x <name>` probe.
	ProcessRunningFn func(name string) (bool, error)
	// GOOS overrides runtime.GOOS (test seam); empty defaults to it.
	GOOS string
}

// Run assembles the five check groups into a diag.Result, renders it,
// and returns the verdict: ErrNotReady when any check is StatusError,
// nil otherwise. Warnings never fail the gate.
//
// ctx is threaded into the launchctl/pgrep probe seams (exec.CommandContext)
// so a SIGINT during a hung probe is honored. Output is validated up front
// so an invalid -o short-circuits before any check — and before any
// subprocess probe — runs.
func Run(ctx context.Context, opts Options) error {
	defaults(ctx, &opts)

	// Fail-fast: reject an invalid -o BEFORE assembling any check, so a
	// bad flag never spawns the launchctl/pgrep subprocess probes.
	if err := diag.ValidateOutput(opts.Output); err != nil {
		return err
	}

	result := diag.Result{
		SchemaVersion: diag.SchemaVersion,
		StartedAt:     time.Now().UTC(),
		Checks:        make([]diag.Check, 0, 12),
	}
	result.Checks = append(result.Checks, systemChecks(opts)...)
	result.Checks = append(result.Checks, bearDBChecks(opts)...)
	result.Checks = append(result.Checks, configChecks(opts)...)
	result.Checks = append(result.Checks, stateChecks(opts)...)
	result.Checks = append(result.Checks, daemonChecks(opts)...)

	result.Summary = diag.Summarize(result.Checks)
	result.CompletedAt = time.Now().UTC()

	if err := diag.Render(opts.Stdout, opts.Output, "doctor", &result); err != nil {
		return err
	}
	if result.Summary.Error > 0 {
		return ErrNotReady
	}
	return nil
}

// defaults fills nil IO sinks + seams with their real implementations
// and resolves the path defaults. Idempotent. ctx is captured by the
// default probe seams so their subprocesses honor cancellation
// (exec.CommandContext).
func defaults(ctx context.Context, opts *Options) {
	if opts.Stdout == nil {
		opts.Stdout = os.Stdout
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	if opts.StatePath == "" {
		opts.StatePath = defaultStatePath
	}
	if opts.StatFn == nil {
		opts.StatFn = os.Stat
	}
	if opts.OpenFn == nil {
		opts.OpenFn = os.Open
	}
	if opts.LaunchctlPrintFn == nil {
		opts.LaunchctlPrintFn = func(label string) (string, error) { return launchctlPrint(ctx, label) }
	}
	if opts.ProcessRunningFn == nil {
		opts.ProcessRunningFn = func(name string) (bool, error) { return processRunning(ctx, name) }
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
}

// launchctlPrint runs `launchctl print gui/<uid>/<label>` read-only,
// returning stdout/stderr plus the exec error. FIXED argv: the
// subcommand is always "print" and label comes from compile-time
// constants — never operator input. uid comes from os.Getuid(), not
// user input. There is no code path here that selects any
// service-lifecycle subcommand. ctx cancels a hung probe
// (exec.CommandContext) so SIGINT is honored.
func launchctlPrint(ctx context.Context, label string) (string, error) {
	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid + "/" + label
	// Fixed argv: subcommand is always "print" (read-only); target is
	// built from os.Getuid + the compile-time label, never user input.
	cmd := exec.CommandContext(ctx, "/bin/launchctl", "print", target) //nolint:gosec // fixed argv, read-only
	output, err := cmd.CombinedOutput()
	return string(output), err
}

// processRunning reports whether a process named name is running via a
// read-only `pgrep -x <name>` probe with fixed argv. pgrep exits 1 with
// empty output when no process matches — that is "not running", not an
// error. If pgrep itself cannot read the process list, doctor falls
// back to a read-only `ps -axo comm=` exact-basename scan. ctx cancels
// hung probes (exec.CommandContext) so SIGINT is honored.
func processRunning(ctx context.Context, name string) (bool, error) {
	cmd := exec.CommandContext(ctx, "/usr/bin/pgrep", "-x", name) //nolint:gosec // fixed argv; name is constant
	output, err := cmd.CombinedOutput()
	if err == nil {
		return true, nil
	}
	if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
		isPgrepNoMatch := exitErr.ExitCode() == 1 && strings.TrimSpace(string(output)) == ""
		if isPgrepNoMatch {
			return false, nil
		}
	}
	processList, listErr := processList(ctx)
	if listErr != nil {
		return false, fmt.Errorf("pgrep probe failed: %w; ps fallback failed: %v", err, listErr)
	}
	return ProcessListContains(processList, name), nil
}

func processList(ctx context.Context) (string, error) {
	cmd := exec.CommandContext(ctx, "/bin/ps", "-axo", "comm=") //nolint:gosec // fixed argv, read-only
	output, err := cmd.Output()
	return string(output), err
}

// ProcessListContains reports whether ps -axo comm= style output contains a
// process whose executable basename exactly matches name.
func ProcessListContains(processList, name string) bool {
	for line := range strings.SplitSeq(processList, "\n") {
		commandPath := strings.TrimSpace(line)
		if commandPath == "" {
			continue
		}
		if filepath.Base(commandPath) == name {
			return true
		}
	}
	return false
}
