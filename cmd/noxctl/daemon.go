package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

var daemonBearDBFlag string // --bear-db override

// errFmtNoxctlDaemon prefixes every error returned by `noxctl daemon`
// so cobra's RunE handler renders a consistent `noxctl daemon: ...`
// prefix to stderr. Extracted to a const so the literal is defined
// once instead of repeated at every return site.
const errFmtNoxctlDaemon = "noxctl daemon: %w"

// daemonCmd is the real `noxctl daemon` subcommand. Replaces the
// stub. Loads noxctl.toml, constructs `engine.NewDaemon`, and runs the
// FSEvents-driven watcher until SIGINT/SIGTERM triggers graceful
// shutdown (exit 0; SIGINT-as-error is apply-only).
var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Run the FSEvents-driven background watcher",
	Long: `Daemon runs the long-running watcher that triggers a regen cycle whenever
Bear's SQLite database changes. Uses fsnotify's kqueue backend on macOS.

Per-cycle flock at ./.noxctl/.lock serializes with manual ` + "`noxctl apply`" + `
invocations; if a noxctl apply touches ./.noxctl/.apply-pending the daemon
yields its current cycle and lets apply proceed. Self-write epsilon (2s)
prevents the daemon from looping on its own bearcli writes.

Graceful shutdown on SIGINT/SIGTERM: drains the in-flight regen cycle,
releases the flock, exits 0.

Exit codes: 0=graceful shutdown or clean exit, 1=startup or runtime error.`,
	RunE: runDaemon,
}

// runDaemon is the daemon RunE. Extracted to a named function so the
// command literal stays small (mirrors apply.go::runApply).
func runDaemon(cmd *cobra.Command, _ []string) error {
	// Microsecond-precision timestamps match the format the legacy
	// daemon binary emitted, so log diff tooling and operator's eye
	// keep working across the rename.
	log.SetFlags(log.LstdFlags | log.Lmicroseconds)

	domains, cat, target, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}

	// Load daemon-toml runtime config (poll intervals, debounce, audit
	// gate, bearcli concurrency). LoadDaemon tolerates a missing file
	// (returns defaults) so an operator with only a catalog still gets
	// a working daemon.
	dcPath, dcPathErr := daemonConfigPath()
	if dcPathErr != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, dcPathErr)
	}
	dc, daemonErr := config.LoadDaemon(dcPath)
	if daemonErr != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, daemonErr)
	}

	pins, _ := domain.LoadPinRegistry(target)

	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bearDBDir, bearDBErr := resolveBearDB(cat, daemonBearDBFlag)
	if bearDBErr != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, bearDBErr)
	}
	features := featuresFromCatalog(cat)
	// Daemon-toml / env override of DomainBootstrap takes precedence
	// over the catalog setting — operator kill-switch
	// (`REGEN_DOMAIN_BOOTSTRAP=off` or `daemon.toml [daemon].domain_bootstrap`)
	// must win even if the in-vault catalog defaults the feature on.
	// Provenance check (Sources["DomainBootstrap"] != SourceDefault)
	// detects explicit operator override vs uninitialized default.
	if dc.Sources["DomainBootstrap"] != config.SourceDefault {
		features.DomainBootstrap = dc.DomainBootstrap
	}

	opts := engine.DaemonOpts{
		ApplyOpts: engine.ApplyOpts{
			Domains:            domains,
			Pins:               pins,
			StatePath:          dc.StatePath,
			LockPath:           dc.LockPath,
			Features:           features,
			AuditEnabled:       dc.AuditEnabled,
			BearcliConcurrency: dc.BearcliConcurrency,
			Stderr:             os.Stderr,
			DailyDefaultTag:    dailyDefaultTagFromCatalog(cat),
			PromotionRules:     promotionRulesFromCatalog(cat),
		},
		BearDBDir:           bearDBDir,
		DebouncePause:       dc.DebouncePause,
		MaxBurstWindow:      dc.MaxBurstWindow,
		MtimePollInterval:   dc.MtimePollInterval,
		AutoTagPollInterval: dc.AutoTagPollInterval,
	}

	warnSilentFastPassGates(opts)

	// Emit the startup marker `noxctl verify --check daemon-log` rewinds
	// to. Sourced from engine.DaemonStartupLogMarker so this emit and
	// the verify-side scanner share one source of truth — rename the
	// marker in bear/engine/daemon.go and both sides follow.
	log.Printf("%s; watching dir %s, domains=%d",
		engine.DaemonStartupLogMarker, opts.BearDBDir, len(domains))

	d, err := engine.NewDaemon(opts)
	if err != nil {
		return fmt.Errorf(errFmtNoxctlDaemon, err)
	}
	defer func() {
		if closeErr := d.Close(); closeErr != nil {
			log.Printf("noxctl daemon: close failed: %v", closeErr)
		}
	}()

	// Daemon SIGINT is graceful shutdown, NOT an error — the
	// SIGINT-as-error policy applies only to `apply`. Run returns
	// ctx.Err on cancel; squash to nil for exit 0.
	if runErr := d.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
		return fmt.Errorf(errFmtNoxctlDaemon, runErr)
	}
	return nil
}

func init() {
	daemonCmd.Flags().StringVar(&daemonBearDBFlag, "bear-db", "",
		"Bear DB watch directory (precedence: this flag > BEAR_DB_DIR env > [meta].bear_db > default)")
	rootCmd.AddCommand(daemonCmd)
}

// warnSilentFastPassGates emits a one-shot WARN at startup for each
// fast-pass gate that would silently no-op without telling the
// operator why. Two shapes are covered: (a) feature flag is ON but
// the catalog data the pass needs is empty (daily-default tag,
// promotion rules); (b) a periodic poll interval is zero while a
// fast-pass that depends on the ticker is enabled. Each WARN names
// the catalog/daemon-toml field the operator would edit to fix the
// silent disable.
func warnSilentFastPassGates(opts engine.DaemonOpts) {
	if opts.Features.AutoTagDefault && opts.DailyDefaultTag == "" {
		log.Printf("WARN: daily-default tag stamping inactive — features.auto_tag_default=true " +
			"but [meta].daily_default_tag is unset in the catalog; untagged notes will " +
			"NOT be stamped with a default tag until the catalog declares one " +
			"(example: [meta].daily_default_tag = \"quicknote/daily\")")
	}
	if opts.Features.TimePromotion && len(opts.PromotionRules) == 0 {
		log.Printf("WARN: time-promotion inactive — features.time_promotion=true " +
			"but no [[promotion]] stanzas declared in the catalog; date-rollover " +
			"of notes between domains will NOT run until at least one promotion " +
			"rule is added")
	}
	// Both fast-pass tickers gate every periodic pre-pass. Zero is a
	// legitimate operator opt-out, but operators frequently set it
	// unintentionally — emit a loud breadcrumb explaining what they
	// just turned off.
	if opts.AutoTagPollInterval == 0 &&
		(opts.Features.AutoTagDefault || opts.Features.ForeignTagEscape || opts.Features.DomainBootstrap) {
		log.Printf("WARN: auto-tag poll loop disabled (daemon.auto_tag_poll_interval=0); " +
			"foreign-tag escape, daily-default stamping, domain-bootstrap, and " +
			"placeholder-refresh will NOT run on the daemon path until " +
			"auto_tag_poll_interval is positive")
	}
	if opts.MtimePollInterval == 0 {
		log.Printf("WARN: database.sqlite mtime poll disabled (daemon.mtime_poll_interval=0); " +
			"the daemon will rely exclusively on FSEvents — Bear writes that " +
			"FSEvents drops (RAM-buffered 5-10s after note save) will be missed " +
			"until the next user-initiated event")
	}
	logFeaturesDisabled(opts.Features)
}

// logFeaturesDisabled emits a single INFO line listing any
// ship-default-ON feature flags that the resolved configuration has
// turned OFF. Covers the inverse silent-disable shape (default_should_be_on
// && catalog_overrides_off) — without this line, an operator who set
// `[features].domain_bootstrap = false` in the catalog gets zero
// breadcrumb in the daemon log. Stays INFO rather than WARN because
// turning a feature off is a legitimate operator choice that just
// needs to be visible.
func logFeaturesDisabled(f engine.Features) {
	type flag struct {
		name string
		on   bool
	}
	flags := []flag{
		{"auto_tag_default", f.AutoTagDefault},
		{"cross_domain_moves", f.CrossDomainMoves},
		{"time_promotion", f.TimePromotion},
		{"foreign_tag_escape", f.ForeignTagEscape},
		{"duplicate_registry", f.DuplicateRegistry},
		{"domain_bootstrap", f.DomainBootstrap},
	}
	var off []string
	for _, fl := range flags {
		if !fl.on {
			off = append(off, fl.name)
		}
	}
	if len(off) == 0 {
		return
	}
	log.Printf("INFO: features disabled by config: %s (default state is ON for all features; "+
		"override resolves env > daemon.toml > catalog [features].* > default)", strings.Join(off, ", "))
}
