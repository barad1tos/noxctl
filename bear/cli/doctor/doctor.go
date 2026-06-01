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
	"io"
	"os"
	"os/exec"
	"runtime"
	"strconv"
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
	// discards output and returns only the exec error.
	LaunchctlPrintFn func(label string) error
	// ProcessRunningFn reports whether a named process is running; nil
	// defaults to a fixed-argv read-only `pgrep -x <name>` probe.
	ProcessRunningFn func(name string) (bool, error)
	// GOOS overrides runtime.GOOS (test seam); empty defaults to it.
	GOOS string
}

// Run assembles the five check groups into a diag.Result, renders it,
// and returns the verdict: ErrNotReady when any check is StatusError,
// nil otherwise. Warnings never fail the gate.
func Run(_ context.Context, opts Options) error {
	defaults(&opts)

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
// and resolves the path defaults. Idempotent.
func defaults(opts *Options) {
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
		opts.LaunchctlPrintFn = launchctlPrint
	}
	if opts.ProcessRunningFn == nil {
		opts.ProcessRunningFn = processRunning
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
}

// launchctlPrint runs `launchctl print gui/<uid>/<label>` read-only,
// discarding stdout/stderr and returning only the exec error. FIXED
// argv: the subcommand is always "print" and the label is the
// compile-time engine.LaunchdServiceLabel constant — never operator
// input. uid comes from os.Getuid(), not user input. There is no code
// path here that selects any service-lifecycle subcommand.
func launchctlPrint(label string) error {
	uid := strconv.Itoa(os.Getuid())
	target := "gui/" + uid + "/" + label
	// Fixed argv: subcommand is always "print" (read-only); target is
	// built from os.Getuid + the compile-time label, never user input.
	cmd := exec.Command("launchctl", "print", target) //nolint:gosec // fixed argv, read-only
	return cmd.Run()
}

// processRunning reports whether a process named name is running via a
// read-only `pgrep -x <name>` probe with fixed argv. pgrep exits 1 when
// no process matches — that is "not running", not an error.
func processRunning(name string) (bool, error) {
	cmd := exec.Command("pgrep", "-x", name) //nolint:gosec // fixed argv; name is a compile-time constant
	err := cmd.Run()
	if err == nil {
		return true, nil
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		return false, nil
	}
	return false, err
}
