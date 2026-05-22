package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/fastpass"
	"github.com/barad1tos/noxctl/bear/state"
)

// pinPaths returns the canonical legacy and target pin-registry
// paths. Legacy at ~/.cache/regen-watchd-pins.json (kept for
// auto-migration); target at per-project ./.noxctl/pins.json.
//
// Subcommands that need pin-registry access call this directly. Stub
// subcommands deliberately skip pin migration — when a stub gets a
// real Phase-N implementation, that implementation owns its own
// preflight wiring.
func pinPaths() (legacy, target string) {
	home, _ := os.UserHomeDir()
	legacy = filepath.Join(home, ".cache", "regen-watchd-pins.json")
	target = filepath.Join(".noxctl", "pins.json")
	return
}

// runWithSignalContext wraps a RunE body in the standard
// SIGINT/SIGTERM-aware context dance: install signal.NotifyContext
// against cmd.Context() (which Cobra cancels on its own lifecycle),
// hand the derived ctx to the body, and on body return inspect
// ctx.Err — context.Canceled / context.DeadlineExceeded map to the
// shared errInterrupted sentinel that main.go translates into POSIX
// exit 130.
//
// Audit and lint share this exact shape; without the helper each
// subcommand would re-implement the signal wiring and the post-run
// ctx-error check, drifting whenever one side adds a new failure
// mode the other does not.
//
// The helper is intentionally minimal: it does not introduce a
// timeout, does not own the ctx mutation, does not retry — just
// SIGINT → 130. Adding more behavior here would obscure the
// per-subcommand control flow.
func runWithSignalContext(cmd *cobra.Command, fn func(ctx context.Context) error) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := fn(ctx); err != nil {
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return errInterrupted
	}
	return nil
}

// runLintSweep is the shared body for `noxctl audit` (apply=false)
// and `noxctl lint` (apply=lintApply). Splits the preflight error
// path from the sweep itself so the two CLI shims stay trivial.
// Lives in preflight.go alongside the other audit/lint helpers
// (domainsWithPreflight, runWithSignalContext) so a reader following
// the CLI dispatch chain finds the body where the rest of the
// shared plumbing lives.
func runLintSweep(cmd *cobra.Command, apply bool) error {
	domains, _, _, loadErr := domainsWithPreflight()
	if loadErr != nil {
		return loadErr
	}
	return runWithSignalContext(cmd, func(ctx context.Context) error {
		cli.RunLint(ctx, os.Stdout, domains, apply)
		return nil
	})
}

// domainsWithPreflight runs the standard pre-load chore (pin
// migration, catalog load, catalog-declared locale apply) and
// returns everything the calling subcommand needs to proceed.
// Load errors get wrapped in formattedLoadError so the stderr
// trace stays uniform across every subcommand.
//
// The four returns let callers ignore what they don't need:
//   - audit / lint / recap need only domains
//   - apply / daemon need domains, catalog, AND the pin-target path
//     (passed to domain.LoadPinRegistry without re-deriving)
//
// validate keeps an inline preflight because its config path comes
// from a positional argument, not the package-level configPath.
func domainsWithPreflight() ([]*domain.Domain, *config.Catalog, string, error) {
	legacyPath, target := pinPaths()
	if migrationErr := state.MigratePins(legacyPath, target); migrationErr != nil {
		_, _ = fmt.Fprintf(os.Stderr, "warning: pin migration failed: %v\n", migrationErr)
	}
	domains, catalog, loadErr := config.Load(configPath)
	if loadErr != nil {
		return nil, nil, "", &formattedLoadError{
			inner: loadErr,
			msg:   config.FormatLoadError(loadErr, configPath),
		}
	}
	// Apply catalog-declared locale before any T()-driven render.
	// Empty meta.locale leaves the default in place.
	if catalog != nil && catalog.Meta.Locale != "" {
		domain.SetLocale(catalog.Meta.Locale)
	}
	return domains, catalog, target, nil
}

// featuresFromCatalog copies `*config.Catalog.Features` (whose fields
// are `*bool`, distinguishing "omitted in TOML" from "set to false")
// into the flat `engine.Features` struct (plain bool fields, defaults
// applied at this CLI boundary). This is the only legitimate place
// to bridge `bear/` and `bear/config/` — `cmd/noxctl` is the boundary;
// `bear/` and `bear/engine/` never import `bear/config/`.
//
// Defaults: every pre-pass ON. Operators get every fast-pass running
// out of the box unless they explicitly opt out via the catalog.
//
// `config.Catalog.Features` is a VALUE TYPE (not `*Features`); fields
// are `*bool` pointers. We start with all-true defaults, then
// per-pointer overwrite where the user explicitly set a value.
func featuresFromCatalog(cat *config.Catalog) engine.Features {
	f := engine.Features{
		AutoTagDefault:    true,
		CrossDomainMoves:  true,
		TimePromotion:     true,
		ForeignTagEscape:  true,
		DuplicateRegistry: true,
		DomainBootstrap:   true,
	}
	if cat == nil {
		return f
	}
	if cat.Features.AutoTagDefault != nil {
		f.AutoTagDefault = *cat.Features.AutoTagDefault
	}
	if cat.Features.CrossDomainMoves != nil {
		f.CrossDomainMoves = *cat.Features.CrossDomainMoves
	}
	if cat.Features.TimePromotion != nil {
		f.TimePromotion = *cat.Features.TimePromotion
	}
	if cat.Features.ForeignTagEscape != nil {
		f.ForeignTagEscape = *cat.Features.ForeignTagEscape
	}
	if cat.Features.DuplicateRegistry != nil {
		f.DuplicateRegistry = *cat.Features.DuplicateRegistry
	}
	if cat.Features.DomainBootstrap != nil {
		f.DomainBootstrap = *cat.Features.DomainBootstrap
	}
	return f
}

// dailyDefaultTagFromCatalog returns the operator-chosen "untagged-on-
// create" tag bound by `[meta].daily_default_tag`. Empty string when
// the operator omitted the field — engine.Apply treats empty as
// "auto-tag fast-pass disabled".
func dailyDefaultTagFromCatalog(cat *config.Catalog) string {
	if cat == nil {
		return ""
	}
	return cat.Meta.DailyDefaultTag
}

// promotionRulesFromCatalog maps `[[promotion]]` stanzas onto the
// engine-side `fastpass.PromotionRule` slice. Empty input or nil catalog
// yields a nil slice — time-promotion fast-pass treats nil as disabled.
//
// CLI boundary helper: `bear/` never imports `bear/config/`, so the
// TOML-to-Domain bridge lives in `cmd/noxctl/` alongside the rest
// of the catalog wiring.
func promotionRulesFromCatalog(cat *config.Catalog) []fastpass.PromotionRule {
	if cat == nil || len(cat.Promotions) == 0 {
		return nil
	}
	out := make([]fastpass.PromotionRule, 0, len(cat.Promotions))
	for _, p := range cat.Promotions {
		out = append(out, fastpass.PromotionRule{From: p.From, To: p.To, Boundary: p.Boundary})
	}
	return out
}

// resolveBearDB picks the Bear DB watch directory for the daemon.
// Precedence (RESEARCH Open Q 5 RESOLVED): `--bear-db` flag (highest) →
// `BEAR_DB_DIR` env → `[meta].bear_db` TOML → fsnotify default location.
//
// Empty cliFlag means "not set"; daemon CLI plumbs the value via
// its own `--bear-db` flag declaration in `cmd/noxctl/daemon.go`.
//
// B4 (checker fix): `config.Catalog.Meta` is a VALUE TYPE (not `*Meta`);
// check `cat.Meta.BearDB != ""` directly, NOT `cat.Meta != nil`.
func resolveBearDB(cat *config.Catalog, cliFlag string) (string, error) {
	if cliFlag != "" {
		return cliFlag, nil
	}
	if env := os.Getenv("BEAR_DB_DIR"); env != "" {
		return env, nil
	}
	if cat != nil && cat.Meta.BearDB != "" {
		return cat.Meta.BearDB, nil
	}
	// fsnotify default: Bear's macOS Group Container Application Data dir.
	// HOME resolution must succeed — silently joining with empty home yields
	// `/Library/Group Containers/...` (root-relative, on-disk nonexistent),
	// which then prints into the startup marker and tricks the verify gate
	// into a green PASS against a daemon watching nothing.
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolveBearDB: UserHomeDir: %w", err)
	}
	return filepath.Join(home, "Library", "Group Containers", "9K33E3U3T4.net.shinyfrog.bear", "Application Data"), nil
}
