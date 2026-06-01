package doctor

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli/diag"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

// Group labels — the human-readable section headers the diag package's
// grouped text renderer prints once per group change. Fixed order in Run
// keeps the output stable.
const (
	groupSystem = "System"
	groupBearDB = "Bear DB"
	groupConfig = "Config"
	groupState  = "State"
	groupDaemon = "Daemon"
)

// bearAppPath is the macOS install location doctor stats to confirm
// Bear itself is present. Stat-only — doctor never launches it.
const bearAppPath = "/Applications/Bear.app"

// Check names — the stable dotted IDs each check reports under. Centralized
// so the JSON/text contract has one source of truth and the same identifier
// is never spelled twice. Tests assert these literal values independently.
const (
	nameMacOS          = "system.macos"
	nameBearApp        = "system.bear-app"
	nameBearcli        = "system.bearcli"
	nameBearRunning    = "system.bear-running"
	nameDBDir          = "bear.db-dir"
	nameDBReadable     = "bear.db-readable"
	nameConfigFound    = "config.found"
	nameConfigValid    = "config.valid"
	nameStatePresent   = "state.present"
	nameStateFreshness = "state.freshness"
	nameDaemonService  = "daemon.service"
	nameDaemonLog      = "daemon.log"
)

// newCheck is the single-shot Check constructor every per-check
// function routes through. Hoisting the {Group, Name, Status, Message}
// literal here keeps each check a one-liner and folds the otherwise-
// identical pass/error constructors into one body (dupl threshold).
func newCheck(group, name string, status diag.Status, message string) diag.Check {
	return diag.Check{Group: group, Name: name, Status: status, Message: message}
}

// okCheck is the pass shortcut.
func okCheck(group, name, message string) diag.Check {
	return newCheck(group, name, diag.StatusPass, message)
}

// checkWithFix builds a Check carrying a remediation hint. The shared
// body for both the blocking-error and the soft-warn remediated
// constructors — they differ only in the status constant, so folding
// them keeps the dupl threshold happy.
func checkWithFix(group, name string, status diag.Status, message, remediation string) diag.Check {
	check := newCheck(group, name, status, message)
	check.Remediation = remediation
	return check
}

// errorCheckWithFix is the blocking-error counterpart to warnCheck: it
// attaches a concrete remediation hint so the exit-1 failures (the ones
// that actually stop the operator) carry a next-step, not just the
// soft advisories. Every blocking check routes through this — there is
// no remediation-less error constructor.
func errorCheckWithFix(group, name, message, remediation string) diag.Check {
	return checkWithFix(group, name, diag.StatusError, message, remediation)
}

// warnCheck additionally carries a remediation hint, the field that
// makes a warning actionable instead of merely informational.
func warnCheck(group, name, message, remediation string) diag.Check {
	return checkWithFix(group, name, diag.StatusWarn, message, remediation)
}

// systemChecks runs the System group: OS, Bear.app presence, bearcli
// presence, and the optional Bear-running advisory.
func systemChecks(opts Options) []diag.Check {
	return []diag.Check{
		checkSystemMacOS(opts),
		checkSystemBearApp(opts),
		checkSystemBearcli(opts),
		checkSystemBearRunning(opts),
	}
}

// checkSystemMacOS confirms doctor runs on macOS. Never errors — a
// non-darwin host still gets a full report, just flagged. Pass on
// darwin, warn otherwise (Bear is macOS-only, so the rest of the report
// is advisory off-platform).
func checkSystemMacOS(opts Options) diag.Check {
	if opts.GOOS == "darwin" {
		return okCheck(groupSystem, nameMacOS, "running on macOS")
	}
	return warnCheck(groupSystem, nameMacOS,
		fmt.Sprintf("host OS is %q, not darwin; Bear is macOS-only", opts.GOOS),
		"run noxctl on the macOS host where Bear is installed")
}

// checkSystemBearApp stats /Applications/Bear.app. Missing → error
// (blocking: no Bear, nothing to manage).
func checkSystemBearApp(opts Options) diag.Check {
	if _, err := opts.StatFn(bearAppPath); err != nil {
		return errorCheckWithFix(groupSystem, nameBearApp,
			fmt.Sprintf("Bear.app not found at %s: %v", bearAppPath, err),
			"install Bear from the App Store / verify the app bundle")
	}
	return okCheck(groupSystem, nameBearApp, "Bear.app installed")
}

// checkSystemBearcli stats bearcli.BinaryPath (the exported SSOT path).
// Stat ONLY — doctor never invokes bearcli. Missing or not-a-regular-
// file → error (blocking: no CLI, no apply path).
func checkSystemBearcli(opts Options) diag.Check {
	info, err := opts.StatFn(bearcli.BinaryPath)
	if err != nil {
		return errorCheckWithFix(groupSystem, nameBearcli,
			fmt.Sprintf("bearcli not found at %s: %v", bearcli.BinaryPath, err),
			"reinstall Bear; bearcli ships inside the app bundle")
	}
	if !info.Mode().IsRegular() {
		return errorCheckWithFix(groupSystem, nameBearcli,
			fmt.Sprintf("bearcli at %s is not a regular file", bearcli.BinaryPath),
			"reinstall Bear; bearcli ships inside the app bundle")
	}
	return okCheck(groupSystem, nameBearcli, "bearcli present")
}

// checkSystemBearRunning probes whether Bear is running. Running →
// warn (a live Bear may race the daemon / a manual apply on the same
// vault); not running → pass. Never errors — a probe failure degrades
// to "unknown", surfaced as a warn, not a blocking error.
func checkSystemBearRunning(opts Options) diag.Check {
	running, err := opts.ProcessRunningFn("Bear")
	if err != nil {
		return warnCheck(groupSystem, nameBearRunning,
			fmt.Sprintf("could not determine whether Bear is running: %v", err),
			"ignore unless apply behaves unexpectedly")
	}
	if running {
		return warnCheck(groupSystem, nameBearRunning,
			"Bear is running; concurrent edits may race apply/daemon writes",
			"quit Bear before a large apply to avoid sync races")
	}
	return okCheck(groupSystem, nameBearRunning, "Bear not running")
}

// bearDBChecks runs the Bear DB group: directory presence then
// read-only database readability.
func bearDBChecks(opts Options) []diag.Check {
	return []diag.Check{
		checkBearDBDir(opts),
		checkBearDBReadable(opts),
	}
}

// checkBearDBDir stats the resolved Bear DB directory. Missing → error
// (blocking: nowhere to read the vault from).
func checkBearDBDir(opts Options) diag.Check {
	info, err := opts.StatFn(opts.BearDBDir)
	if err != nil {
		return errorCheckWithFix(groupBearDB, nameDBDir,
			fmt.Sprintf("Bear DB directory not found at %s: %v", opts.BearDBDir, err),
			"point --bear-db at your Bear group container, or check BEAR_DB_DIR")
	}
	if !info.IsDir() {
		return errorCheckWithFix(groupBearDB, nameDBDir,
			fmt.Sprintf("Bear DB path %s is not a directory", opts.BearDBDir),
			"point --bear-db at your Bear group container, or check BEAR_DB_DIR")
	}
	return okCheck(groupBearDB, nameDBDir, "Bear DB directory present")
}

// checkBearDBReadable opens database.sqlite read-only and immediately
// closes it — a readability proof with zero reads of contents and zero
// writes. Open failure → error (blocking).
func checkBearDBReadable(opts Options) diag.Check {
	dbPath := filepath.Join(opts.BearDBDir, bearDatabaseFile)
	file, err := opts.OpenFn(dbPath)
	if err != nil {
		return errorCheckWithFix(groupBearDB, nameDBReadable,
			fmt.Sprintf("cannot open %s read-only: %v", dbPath, err),
			"check filesystem permissions on the Bear DB directory")
	}
	_ = file.Close()
	return okCheck(groupBearDB, nameDBReadable, "Bear database readable")
}

// configChecks runs the Config group: file presence then validity
// (delegated to config.Load).
func configChecks(opts Options) []diag.Check {
	return []diag.Check{
		checkConfigFound(opts),
		checkConfigValid(opts),
	}
}

// checkConfigFound stats the resolved config path. Missing → error
// (blocking: no catalog, nothing to apply).
func checkConfigFound(opts Options) diag.Check {
	if _, err := opts.StatFn(opts.ConfigPath); err != nil {
		return errorCheckWithFix(groupConfig, nameConfigFound,
			fmt.Sprintf("config not found at %s: %v", opts.ConfigPath, err),
			"create noxctl.toml or pass --config")
	}
	return okCheck(groupConfig, nameConfigFound, fmt.Sprintf("config found at %s", opts.ConfigPath))
}

// checkConfigValid delegates validity entirely to config.Load +
// config.FormatLoadError. doctor does NOT re-implement schema
// validation. A load error → error (blocking); the formatted message
// carries the uniform path:line:col: kind: message shape.
func checkConfigValid(opts Options) diag.Check {
	if _, _, err := config.Load(opts.ConfigPath); err != nil {
		return errorCheckWithFix(groupConfig, nameConfigValid,
			config.FormatLoadError(err, opts.ConfigPath),
			"fix the reported config error and re-run `noxctl validate`")
	}
	return okCheck(groupConfig, nameConfigValid, "config valid")
}

// stateChecks runs the State group: state.json presence then freshness.
func stateChecks(opts Options) []diag.Check {
	return []diag.Check{
		checkStatePresent(opts),
		checkStateFreshness(opts),
	}
}

// checkStatePresent reads state.json READ-ONLY via state.Peek — doctor
// MUTATES NOTHING, so it must never route through state.Load (which
// renames a corrupt file). A missing file → warn "first run"; a corrupt
// file → warn "investigate" (NOT renamed); a present prior apply → pass.
// Never errors — doctor reports state, it does not block on it.
func checkStatePresent(opts Options) diag.Check {
	lastApply, present, corrupt, err := state.Peek(opts.StatePath)
	if err != nil {
		return warnCheck(groupState, nameStatePresent,
			fmt.Sprintf("could not read state at %s: %v", opts.StatePath, err),
			"check filesystem permissions on .noxctl/")
	}
	if !present {
		return warnCheck(groupState, nameStatePresent,
			"no prior apply recorded (first run)",
			"run `noxctl apply` to establish baseline state")
	}
	if corrupt {
		return warnCheck(groupState, nameStatePresent,
			fmt.Sprintf("state at %s is unreadable (corrupt); investigate", opts.StatePath),
			"inspect the file, then re-run `noxctl apply` to rebuild it")
	}
	if lastApply.IsZero() {
		return warnCheck(groupState, nameStatePresent,
			"no prior apply recorded (first run)",
			"run `noxctl apply` to establish baseline state")
	}
	return okCheck(groupState, nameStatePresent, "state.json present")
}

// checkStateFreshness warns when the last apply is older than
// StaleThreshold. Reads READ-ONLY via state.Peek (same MUTATES-NOTHING
// invariant). A missing/corrupt file or a zero LastApply is the
// first-run/unreadable case already surfaced by state.present, so
// freshness reports skipped there. Never errors.
func checkStateFreshness(opts Options) diag.Check {
	lastApply, present, corrupt, err := state.Peek(opts.StatePath)
	if err != nil || !present || corrupt || lastApply.IsZero() {
		return diag.Check{
			Group: groupState, Name: nameStateFreshness, Status: diag.StatusSkipped,
			Message: "no prior apply to age-check (see state.present)",
		}
	}
	// Clamp a future LastApply (clock skew, restored-from-backup state)
	// to "just now" so the human-facing day count is never negative; the
	// threshold comparison below then treats it as fresh.
	age := max(time.Since(lastApply), 0)
	if age > StaleThreshold {
		return warnCheck(groupState, nameStateFreshness,
			fmt.Sprintf("last apply was %d day(s) ago", int(age.Hours()/24)),
			"re-run `noxctl apply` to refresh the vault")
	}
	return okCheck(groupState, nameStateFreshness, "state recently applied")
}

// daemonChecks runs the Daemon group: launchd service status then log
// freshness.
func daemonChecks(opts Options) []diag.Check {
	return []diag.Check{
		checkDaemonService(opts),
		checkDaemonLog(opts),
	}
}

// checkDaemonService inspects the launchd service read-only via
// LaunchctlPrintFn(engine.LaunchdServiceLabel). A non-nil error means
// the job is not loaded → warn with a remediation hint. Never errors
// (the daemon is optional infra). doctor only ever inspects the service
// — it never loads, starts, or unloads it.
func checkDaemonService(opts Options) diag.Check {
	if err := opts.LaunchctlPrintFn(engine.LaunchdServiceLabel); err != nil {
		return warnCheck(groupDaemon, nameDaemonService,
			fmt.Sprintf("launchd service %s not loaded", engine.LaunchdServiceLabel),
			"load the daemon plist with launchctl if you want the background watcher")
	}
	return okCheck(groupDaemon, nameDaemonService, "daemon service loaded")
}

// checkDaemonLog stats the resolved daemon log path. Absent → warn,
// stale (mtime older than StaleThreshold) → warn, fresh → pass.
// Presence/mtime ONLY — no log content scan. Never errors.
func checkDaemonLog(opts Options) diag.Check {
	path := opts.LogPath
	if path == "" {
		resolved, err := defaultDaemonLogPath()
		if err != nil {
			return warnCheck(groupDaemon, nameDaemonLog,
				fmt.Sprintf("could not resolve daemon log path: %v", err),
				"set --log-path or check $HOME")
		}
		path = resolved
	}
	info, err := opts.StatFn(path)
	if err != nil {
		return warnCheck(groupDaemon, nameDaemonLog,
			fmt.Sprintf("daemon log absent at %s (daemon may never have run)", path),
			"start the daemon to begin logging")
	}
	if time.Since(info.ModTime()) > StaleThreshold {
		return warnCheck(groupDaemon, nameDaemonLog,
			fmt.Sprintf("daemon log at %s is stale (no writes in over %d days)",
				path, int(StaleThreshold.Hours()/24)),
			"the daemon may be stopped; check `launchctl print`")
	}
	return okCheck(groupDaemon, nameDaemonLog, "daemon log fresh")
}

// defaultDaemonLogPath resolves ~/.cache/regen-watchd.log — the same
// location verify uses, so doctor and verify agree on where the daemon
// writes.
func defaultDaemonLogPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("UserHomeDir: %w", err)
	}
	return filepath.Join(home, ".cache", "regen-watchd.log"), nil
}
