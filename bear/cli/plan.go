package cli

// plan.go implements the noxctl plan subcommand business logic.
//
// cmd/noxctl/plan.go reduces to cobra-wiring + flag parsing; this
// file handles ParseColorMode validation, the TOML loader, domain
// scoping, and result rendering.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cliutil"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

// ErrDriftDetected is returned by Run when engine.Plan reports at
// least one domain with Status=="drift". Callers (cmd/noxctl) map it
// to exit code 2 per the Terraform -detailed-exitcode convention.
var ErrDriftDetected = errors.New("noxctl plan: drift detected")

// ErrInterrupted is returned when the plan was interrupted by SIGINT
// or SIGTERM mid-execution. Callers map this to exit 130 (POSIX 128
// + SIGINT).
var ErrInterrupted = errors.New("noxctl plan: interrupted")

// PlanOptions carries the resolved CLI inputs for RunPlan. Callers parse the
// raw string flags (--color, --output) once in their cobra layer and
// pass the validated values here.
type PlanOptions struct {
	Color     engine.ColorMode
	Output    string   // "text" or "json"; validated by ValidateOutput
	Args      []string // positional args from cobra (0 or 1 tag)
	CfgPath   string   // path to noxctl.toml
	PinLegacy string   // ~/.cache/regen-watchd-pins.json
	PinTarget string   // ./.noxctl/pins.json
	Verbose   bool
	Stdout    io.Writer
	Stderr    io.Writer
}

// ValidateOutput reports an error if output is not "text" or "json".
// Hoisted to a public helper so cmd/noxctl can validate the flag
// before constructing Options.
func ValidateOutput(output string) error {
	if output != "text" && output != "json" {
		return fmt.Errorf("invalid -o value %q (expected text|json)", output)
	}
	return nil
}

// RunPlan is the plan orchestrator. Loads the domain slice from the TOML
// catalog, dispatches to engine.Plan, renders the result, and returns
// one of (nil, ErrDriftDetected, ErrInterrupted, error).
//
// Initializes the global bearcli semaphore at entry via the shared
// `engine.DefaultBearcliConcurrency` ceiling — `engine.Plan`'s
// `SnapshotDomainRenderInputs` calls `listNotes` which requires the
// pool to be live. Apply and daemon paths set this themselves;
// plan's path was missed, surfacing as "bearcli pool not initialized"
// errors for every domain when `noxctl plan` ran standalone.
func RunPlan(ctx context.Context, opts PlanOptions) error {
	bearcli.SetConcurrency(engine.DefaultBearcliConcurrency)

	domains, catalog, err := LoadDomainsAndCatalog(opts.Args,
		opts.CfgPath, opts.PinLegacy, opts.PinTarget, opts.Stderr)
	if err != nil {
		return err
	}
	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, planErr := engine.Plan(sigCtx, engine.PlanOpts{
		Domains:  domains,
		Verbose:  opts.Verbose,
		Stderr:   opts.Stderr,
		Features: new(cliutil.FeaturesFromCatalog(catalog)),
	})
	if planErr != nil {
		return planErr
	}

	if renderErr := DispatchRender(opts.Stdout, result, opts.Output, opts.Color, opts.Verbose); renderErr != nil {
		return renderErr
	}

	if result.Interrupted {
		return ErrInterrupted
	}
	if len(result.Errors) > 0 {
		return fmt.Errorf("noxctl plan: %d error(s)", len(result.Errors))
	}
	if result.HasDrift() {
		return ErrDriftDetected
	}
	return nil
}

// LoadDomains resolves the domain slice from the TOML catalog. Pin
// migration runs once at the top — the migration is idempotent and
// should not be skipped from any CLI path.
//
// When the caller supplied a positional tag arg, the scope filter
// narrows the slice to that single domain (or reports an unknown-tag
// error so the operator gets a loud rejection on a typo).
func LoadDomains(args []string,
	configPath, pinLegacy, pinTarget string, stderr io.Writer,
) ([]*domain.Domain, error) {
	domains, _, err := LoadDomainsAndCatalog(args, configPath, pinLegacy, pinTarget, stderr)
	return domains, err
}

// LoadDomainsAndCatalog loads the scoped plan domain set plus the source catalog.
func LoadDomainsAndCatalog(args []string,
	configPath, pinLegacy, pinTarget string, stderr io.Writer,
) ([]*domain.Domain, *config.Catalog, error) {
	if migrationErr := state.MigratePins(pinLegacy, pinTarget); migrationErr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: pin migration failed: %v\n", migrationErr)
	}

	loaded, cat, loadErr := config.Load(configPath)
	if loadErr != nil {
		return nil, nil, errors.New(config.FormatLoadError(loadErr, configPath))
	}
	if cat != nil && cat.Meta.Locale != "" {
		domain.SetLocale(cat.Meta.Locale)
	}

	if len(args) == 1 {
		scoped, err := ScopeDomains(loaded, args[0])
		return scoped, cat, err
	}
	return loaded, cat, nil
}

// DispatchRender chooses RenderJSON vs RenderText based on output.
// Centralizes the format dispatch so callers stay under gocognit ≤ 15.
func DispatchRender(
	stdout io.Writer,
	result *engine.PlanResult,
	output string,
	color engine.ColorMode,
	verbose bool,
) error {
	if output == "json" {
		return engine.RenderJSON(stdout, result)
	}
	return engine.RenderText(stdout, result, color, verbose)
}

// ScopeDomains filters the loaded catalog to the single requested
// tag. Rejects unknown tags with a friendly error — the catalog is
// closed, so a tag missing from the slice is almost always a typo,
// and a loud rejection beats a silent zero-result run.
func ScopeDomains(domains []*domain.Domain, wanted string) ([]*domain.Domain, error) {
	for _, d := range domains {
		if d.Tag == wanted {
			return []*domain.Domain{d}, nil
		}
	}
	return nil, fmt.Errorf("noxctl plan: unknown tag %q (not in noxctl.toml)", wanted)
}
