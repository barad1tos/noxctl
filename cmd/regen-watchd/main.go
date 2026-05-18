// regen-watchd: native macOS daemon that watches Bear's SQLite database for
// writes and triggers per-domain Hub regeneration after a debounce period of
// inactivity.
//
// Architecture: domain-driven. Each managed Bear tag is one `*bear.Domain`
// config registered in the `domains` slice below. Domains are organized by
// top-level tag family into sibling packages — `github.com/barad1tos/noxctl/library`
// for `library/*`, `github.com/barad1tos/noxctl/llm` for `llm/*`. Adding support
// for a new tag is one new file under the matching package + one append here.
// Shared abstractions (Domain struct, helpers, default callbacks) live in
// `github.com/barad1tos/noxctl/bear`.
//
// FSEvents → debounce → orchestrator iterates domains → bearcli list+overwrite
// → idempotent regeneration of master + Tier-2 hubs (where applicable).
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"sync"
	"syscall"

	"strings"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/cmd/regen-watchd/bench"
	"github.com/barad1tos/noxctl/registry"
)

// defaultBearDBDir is the historical fallback when neither daemon.toml
// nor BEAR_DB_DIR specifies a Bear database directory. The other
// per-path defaults live inside config.LoadDaemon.
const defaultBearDBDir = "$HOME/Library/Group Containers/9K33E3U3T4.net.shinyfrog.bear/Application Data"

// pinRegistry holds the cross-domain move pins loaded at startup. Promotion
// pre-passes consult it to honor short-lived user overrides; the registry is
// saved after every regen cycle so pins survive daemon restarts.
var pinRegistry *bear.PinRegistry

// daemonCfg + daemonCfgOnce cache the resolved daemon config so
// buildApplyOpts, runDaemon, loadPinRegistry, setupFileLogging, and
// resolveBearDBDir share one resolution per process.
var (
	daemonCfgOnce sync.Once
	daemonCfg     config.DaemonConfig
)

// loadDaemonConfigOrFatal resolves ~/.noxctl/daemon.toml plus env-vars
// into a fully-populated DaemonConfig. Errors (file present but
// unparseable, bad duration, etc.) are fatal at startup per the
// spec's fail-loud contract. Cached so multiple callers share one
// resolution per process.
func loadDaemonConfigOrFatal() config.DaemonConfig {
	daemonCfgOnce.Do(func() {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "."
		}
		path := filepath.Join(home, ".noxctl", "daemon.toml")
		cfg, loadErr := config.LoadDaemon(path)
		if loadErr != nil {
			log.Fatalf("regen-watchd: %v", loadErr)
		}
		if _, statErr := os.Stat(path); errors.Is(statErr, fs.ErrNotExist) {
			log.Printf("config: %s not found; using defaults", path)
		}
		daemonCfg = cfg
	})
	return daemonCfg
}

// domains lists every Bear tag the daemon regenerates on each cycle.
// Order matters: it determines log line ordering and serial regen sequence.
// The slice is owned by registry.All during the bridge —
// cmd/regen-watchd and tests/bear/config consume one canonical source
// so adding/removing domains stays a single-edit operation.
var domains = registry.All()

// runOnce executes one engine.Apply cycle. Engine-level orchestration
// (audit + pre-passes + per-domain RunRegen + state.json incremental
// save + LastApply finalize) lives in bear/engine.Apply since Plan
// 02-02. cmd/regen-watchd hardcodes all features on per CONTEXT D-03
// — production launchd parity is the contract; cmd/noxctl reads
// features from noxctl.toml in.
//
// Self-write-gate envelope removed in: the gate now lives
// only on the daemon path inside engine.Daemon. Apply is one-shot and
// needs no gate — by the time Apply returns, no follow-up FSEvents
// can re-trigger anything (no event loop in --once mode).
func runOnce(ctx context.Context) {
	opts := buildApplyOpts()
	if _, err := engine.Apply(ctx, opts); err != nil {
		log.Printf("regen-watchd: engine.Apply: %v", err)
	}
}

// buildApplyOpts is the single source of truth for engine.ApplyOpts
// shared by runOnce (--once mode) and runDaemon (default mode). Keeps
// the StatePath / LockPath / Features / AuditEnabled / Stderr defaults
// in one place.
//
// Path + audit resolution flows through config.LoadDaemon
// (~/.noxctl/daemon.toml → env vars → built-in defaults). State + lock
// paths anchor to $HOME/.noxctl/ rather than./.noxctl/ because
// regen-watchd is a single-user macOS daemon supervised by launchd,
// which starts agents with cwd=/ unless WorkingDirectory is set. The
// relative-cwd defaults silently failed for ~3 days in production
// with "mkdir.noxctl: read-only file system" before being caught —
// anchoring to UserHomeDir makes the daemon resilient to missing
// plist WorkingDirectory and any future invocation context.
//
// The noxctl CLI keeps the Terraform-style per-project./.noxctl/
// defaults (see cmd/noxctl/{apply,daemon}.go) because those are
// human-invoked from project roots, not launchd-supervised.
func buildApplyOpts() engine.ApplyOpts {
	cfg := loadDaemonConfigOrFatal()
	//: AllFeaturesOn ships all toggles on (production
	// launchd parity per D-03); the only knob the operator can flip is
	// DomainBootstrap, threaded here from cfg via env > file > default.
	features := engine.AllFeaturesOn()
	features.DomainBootstrap = cfg.DomainBootstrap
	return engine.ApplyOpts{
		Domains:            domains,
		Pins:               pinRegistry,
		StatePath:          cfg.StatePath,
		LockPath:           cfg.LockPath,
		Features:           features,
		AuditEnabled:       cfg.AuditEnabled,
		BearcliConcurrency: cfg.BearcliConcurrency, //  D-01: operator-tuned bearcli pool cap.
		Stderr:             os.Stderr,
	}
}

// runDaemon constructs an engine.Daemon, drives it until ctx is
// canceled, and emits the same final "regen-watchd stopped" line as
// the pre-Phase-2 cmd/regen-watchd path. The hardcoded `domains`
// slice + AllFeaturesOn preserves production launchd parity (D-03).
func runDaemon(ctx context.Context, bearDBDir string) {
	cfg := loadDaemonConfigOrFatal()
	opts := engine.DaemonOpts{
		ApplyOpts:           buildApplyOpts(),
		BearDBDir:           bearDBDir,
		DebouncePause:       cfg.DebouncePause,
		MaxBurstWindow:      cfg.MaxBurstWindow,
		MtimePollInterval:   cfg.MtimePollInterval,   //  POLL-01: routes through engine.DaemonOpts → Daemon.Run poll loop.
		AutoTagPollInterval: cfg.AutoTagPollInterval, //  TAG-01: routes through engine.DaemonOpts → Daemon.Run fast-pass loop.
	}
	d, err := engine.NewDaemon(opts)
	if err != nil {
		log.Fatalf("regen-watchd: NewDaemon: %v", err)
	}
	defer func() {
		if closeErr := d.Close(); closeErr != nil {
			log.Printf("regen-watchd: daemon close: %v", closeErr)
		}
	}()
	if runErr := d.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		log.Printf("regen-watchd: daemon exited: %v", runErr)
	}
}

// parseBenchArgs scans args for --bench [--sweep=N,M,...]. Returns
// isBench=false (no allocation) when --bench is absent. When --bench
// is present but --sweep parsing fails, returns isBench=true + err so
// main can log and exit 1 (D-07: sweep-parse failure is fatal at
// argv-time, parallel to a daemon config-load failure).
//
// --sweep is optional; absence => single cycle at the daemon config's
// effective bearcli_concurrency (bench.Deps.DefaultConcurrency).
func parseBenchArgs(args []string) (bool, []int, error) {
	isBench := false
	sweepRaw := ""
	for _, a := range args {
		switch {
		case a == "--bench":
			isBench = true
		case strings.HasPrefix(a, "--sweep="):
			sweepRaw = strings.TrimPrefix(a, "--sweep=")
		}
	}
	if !isBench {
		return false, nil, nil
	}
	sweep, err := bench.ParseSweep(sweepRaw)
	if err != nil {
		return true, nil, err
	}
	return true, sweep, nil
}

// runBenchMode wires the bench sub-package's Run to the host-process
// callbacks (pin-loading, ApplyOpts builder, effective default
// concurrency). Extracted from main so the dispatch site stays a
// trivial conditional and the bench package stays free of any direct
// dependency on cmd/regen-watchd package-level state.
func runBenchMode(ctx context.Context, sweep []int) {
	bench.Run(ctx, sweep, bench.Deps{
		LoadPins:           loadPinRegistry,
		BuildOpts:          buildApplyOpts,
		DefaultConcurrency: loadDaemonConfigOrFatal().BearcliConcurrency,
	})
}

// isOnceMode returns true if --once / -1 is among args.
func isOnceMode(args []string) bool {
	for _, arg := range args {
		if arg == "--once" || arg == "-1" {
			return true
		}
	}
	return false
}

// isAuditMode returns true if --audit is among args. Audit scans every
// domain's atomics, prints lint findings, and exits without writing.
func isAuditMode(args []string) bool {
	return slices.Contains(args, "--audit")
}

// isLintApplyMode returns true if both --lint and --apply are among args.
// Auto-fixes the unambiguous lint findings (multi-canonical, orphan-tag);
// broken-h1 and unsafe-title are reported but not auto-fixed.
func isLintApplyMode(args []string) bool {
	hasLint, hasApply := false, false
	for _, arg := range args {
		switch arg {
		case "--lint":
			hasLint = true
		case "--apply":
			hasApply = true
		}
	}
	return hasLint && hasApply
}

// loadPinRegistry loads the pin registry from the daemon-config
// PinsPath into the package-level pinRegistry. Errors are logged but
// non-fatal — pins are best-effort hints, an empty registry is a valid
// starting state.
func loadPinRegistry() {
	cfg := loadDaemonConfigOrFatal()
	pinsPath := cfg.PinsPath
	pins, err := bear.LoadPinRegistry(pinsPath)
	if err != nil {
		log.Printf("pins: load from %s failed: %v (continuing with empty registry)", pinsPath, err)
	}
	pinRegistry = pins
	log.Printf("pins: loaded registry from %s", pinsPath)
}

// resolveBearDBDir returns the Bear database directory, preferring
// the daemon.toml bear_db setting (which already absorbs the
// BEAR_DB_DIR env-var inside config.LoadDaemon), falling back to the
// hardcoded historical default path.
func resolveBearDBDir() string {
	cfg := loadDaemonConfigOrFatal()
	if cfg.BearDBDir != "" {
		return cfg.BearDBDir
	}
	return os.ExpandEnv(defaultBearDBDir)
}

// setupFileLogging redirects log output to logPath. Returns a cleanup func that
// closes the log file (no-op if open failed).
func setupFileLogging(logPath string) func() {
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		// log dir creation failed; logs stay on stderr — nothing was opened, so nothing to close.
		return func() { /* no log file opened */ }
	}
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		// log file open failed; logs stay on stderr — nothing to close.
		return func() { /* no log file opened */ }
	}
	log.SetOutput(f)
	return func() { _ = f.Close() }
}

func main() {
	for _, domain := range domains {
		if err := domain.Validate(); err != nil {
			log.Fatalf("domain misconfigured: %v", err)
		}
	}

	bearDBDir := resolveBearDBDir()

	// Bench mode (PAR-04) dispatches BEFORE --once because
	// it is conceptually one Apply cycle with metrics instrumentation,
	// but the operator must opt in explicitly. JSON streams to stdout
	// (operator pipes through jq); log lines stay on stderr — no
	// setupFileLogging here, otherwise diagnostics would vanish into
	// ~/.cache/regen-watchd.log instead of the operator's terminal.
	if isBench, sweep, err := parseBenchArgs(os.Args[1:]); isBench {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		if err != nil {
			log.Fatalf("regen-watchd: --bench: %v", err) // exit 1 per D-07
		}
		runBenchMode(context.Background(), sweep)
		return
	}

	if isAuditMode(os.Args[1:]) {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		findings := bear.AuditDomains(context.Background(), domains)
		bear.PrintFindings(os.Stdout, findings, len(domains))
		return
	}

	if isLintApplyMode(os.Args[1:]) {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		bear.LintApplyDomains(context.Background(), domains)
		return
	}

	if isOnceMode(os.Args[1:]) {
		log.SetFlags(log.LstdFlags | log.Lmicroseconds)
		loadPinRegistry()
		runOnce(context.Background())
		return
	}

	cleanup := setupFileLogging(loadDaemonConfigOrFatal().LogPath)
	defer cleanup()
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)
	log.Printf("regen-watchd starting; watching dir %s", bearDBDir)
	loadPinRegistry()

	// Graceful shutdown: SIGINT/SIGTERM cancels the context;
	// engine.Daemon.Run drains in-flight regen via its internal regenMu
	// before returning ctx.Err. Defer-cleanup chain (file logger
	// close, watcher close inside runDaemon) runs as main exits.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		sig := <-sigCh
		log.Printf("received signal %s, shutting down", sig)
		cancel()
	}()

	runDaemon(ctx, bearDBDir)
	log.Printf("regen-watchd stopped")
}
