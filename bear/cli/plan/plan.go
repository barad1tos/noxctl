// Package plan implements the noxctl plan subcommand business logic.
//
// cmd/noxctl/plan.go reduces to cobra-wiring + flag parsing; this
// package handles ParseColorMode/ParseConfigSource validation, the
// dual-source loader (TOML + hardcoded), domain scoping, parity
// dispatch, and result rendering.
//
// Note on layering: this package sits in the CLI helper layer
// (bear/cli/*), which BY DESIGN crosses the D-01 boundary —
// bear/ core code never imports bear/config or registry, but the CLI
// helpers MUST (they are the boundary translator). bear/cli/plan
// imports bear/config + registry + bear/state for exactly this reason.
package plan

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
	"github.com/barad1tos/noxctl/registry"
)

// ErrDriftDetected is returned by Run when engine.Plan reports at
// least one domain with Status=="drift" OR Status=="parity-mismatch".
// Callers (cmd/noxctl) map it to ExitDiffPresent (=2) per the
// Terraform -detailed-exitcode convention (PLAN-04).
var ErrDriftDetected = errors.New("noxctl plan: drift detected")

// ErrInterrupted is returned when the plan was interrupted by SIGINT
// or SIGTERM mid-execution. Callers map this to exit 130 (POSIX 128
// + SIGINT).
var ErrInterrupted = errors.New("noxctl plan: interrupted")

// Options carries the resolved CLI inputs for Run. Callers parse the
// raw string flags (--color, --output, --config-source) once in their
// cobra layer and pass the validated values here.
type Options struct {
	Color     engine.ColorMode
	Output    string // "text" or "json"; validated by ValidateOutput
	Source    engine.ConfigSource
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

// Run is the plan orchestrator. Loads the domain slice(s) per
// ConfigSource, dispatches to engine.Plan, renders the result, and
// returns one of (nil, ErrDriftDetected, ErrInterrupted, error).
func Run(ctx context.Context, opts Options) error {
	tomlDomains, hardDomains, err := LoadDomainsByConfigSource(opts.Source, opts.Args,
		opts.CfgPath, opts.PinLegacy, opts.PinTarget, opts.Stderr)
	if err != nil {
		return err
	}

	sigCtx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()

	result, planErr := engine.Plan(sigCtx, engine.PlanOpts{
		Domains:      PickPrimary(opts.Source, tomlDomains, hardDomains),
		HardcodedRef: PickRef(opts.Source, hardDomains),
		ConfigSource: opts.Source,
		Verbose:      opts.Verbose,
		Stderr:       opts.Stderr,
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
	if result.HasDrift() || HasParityMismatch(result) {
		return ErrDriftDetected
	}
	return nil
}

// PickPrimary selects the slice that plays the role of "domains under
// inspection" given the resolved ConfigSource:
//
// - ConfigSourceTOML — TOML slice (single-path drift)
// - ConfigSourceHardcoded — hardcoded slice (bridge-window opt-in)
// - ConfigSourceBoth — TOML slice (parity treats this as the
// canonical set; HardcodedRef carries the comparison reference)
func PickPrimary(src engine.ConfigSource, toml, hard []*bear.Domain) []*bear.Domain {
	if src == engine.ConfigSourceHardcoded {
		return hard
	}
	return toml
}

// PickRef returns the hardcoded slice when src == ConfigSourceBoth so
// engine.planParity can pair (TOML, hardcoded) by Tag. Returns nil for
// TOML and Hardcoded — those branches dispatch into planSinglePath
// which never reads HardcodedRef.
func PickRef(src engine.ConfigSource, hard []*bear.Domain) []*bear.Domain {
	if src == engine.ConfigSourceBoth {
		return hard
	}
	return nil
}

// HasParityMismatch reports whether any DomainPlan has Status ==
// StatusParityMismatch. Mirrors PlanResult.HasDrift but for parity
// rows — keeps the parity-check exit-2 contract enforced at the CLI
// boundary even when the inner Summary tally is zero (defensive).
func HasParityMismatch(r *engine.PlanResult) bool {
	if r == nil {
		return false
	}
	for _, d := range r.Domains {
		if d.Status == engine.StatusParityMismatch {
			return true
		}
	}
	return false
}

// LoadDomainsByConfigSource resolves the domain slice(s) for the
// requested ConfigSource. Returns (toml, hard, err) where one or both
// slices may be nil:
//
// - ConfigSourceTOML — toml populated, hard nil
// - ConfigSourceHardcoded — toml nil, hard populated
// - ConfigSourceBoth — both populated; planParity pairs by Tag
//
// Pin migration runs once at the top regardless of source — the
// migration is idempotent and the bridge-window opt-ins should not
// skip user-visible plumbing.
//
// When the caller supplied a positional tag arg, both slices get the
// scope filter applied symmetrically so the parity diff focuses on the
// same domain on both sides.
func LoadDomainsByConfigSource(src engine.ConfigSource, args []string,
	cfgPath, pinLegacy, pinTarget string, stderr io.Writer,
) (toml, hard []*bear.Domain, err error) {
	if migrationErr := state.MigratePins(pinLegacy, pinTarget); migrationErr != nil {
		_, _ = fmt.Fprintf(stderr, "warning: pin migration failed: %v\n", migrationErr)
	}

	if src == engine.ConfigSourceTOML || src == engine.ConfigSourceBoth {
		loaded, _, loadErr := config.Load(cfgPath)
		if loadErr != nil {
			return nil, nil, errors.New(config.FormatLoadError(loadErr, cfgPath))
		}
		toml = loaded
	}
	if src == engine.ConfigSourceHardcoded || src == engine.ConfigSourceBoth {
		hard = registry.All()
	}

	if len(args) == 1 {
		toml, hard, err = ScopeBothSlices(toml, hard, args[0], src)
		if err != nil {
			return nil, nil, err
		}
	}
	return toml, hard, nil
}

// ConfigSourceLabel maps the resolved ConfigSource to the
// human-readable catalog name surfaced in ScopeDomains' "unknown tag"
// error message. Diverges from engine.ConfigSource.String (the
// wire/flag form): the CLI surfaces "noxctl.toml" / "hardcoded
// registry" / "any catalog (toml, hardcoded)" so the operator
// immediately sees WHICH catalog rejected their --config-source
// choice instead of getting a generic "not in noxctl.toml" when they
// explicitly asked for hardcoded.
func ConfigSourceLabel(src engine.ConfigSource) string {
	switch src {
	case engine.ConfigSourceHardcoded:
		return "hardcoded registry"
	case engine.ConfigSourceBoth:
		return "any catalog (toml, hardcoded)"
	case engine.ConfigSourceTOML:
		fallthrough
	default:
		return "noxctl.toml"
	}
}

// ScopeBothSlices applies ScopeDomains to whichever slice(s) are
// populated. Extracted so LoadDomainsByConfigSource stays under
// gocognit even with the dual-slice scoping path. Returns the scoped
// slices verbatim — nil inputs stay nil; non-nil inputs flow through
// ScopeDomains and surface the same "unknown tag" error if the tag is
// missing from BOTH catalogs.
//
// The src arg threads through ConfigSourceLabel into ScopeDomains so
// the operator-facing "unknown tag" message names the catalog they
// actually requested via --config-source (WR-03). Both sides receive
// the SAME label — in =both mode the label says "any catalog" because
// the JOIN semantics mean the tag is missing from BOTH.
func ScopeBothSlices(toml, hard []*bear.Domain, wanted string, src engine.ConfigSource) ([]*bear.Domain, []*bear.Domain, error) {
	label := ConfigSourceLabel(src)
	var (
		scopedToml, scopedHard []*bear.Domain
		tomlErr, hardErr       error
	)
	if toml != nil {
		scopedToml, tomlErr = ScopeDomains(toml, wanted, label)
	}
	if hard != nil {
		scopedHard, hardErr = ScopeDomains(hard, wanted, label)
	}
	// In =both mode either side may legitimately lack the tag (one side
	// in flight, the other not yet migrated). Surface the error only if
	// every populated source rejected the tag — otherwise drop the side
	// that doesn't know about it; the parity loop will record it as a
	// missing-half row.
	switch {
	case toml != nil && hard != nil:
		if tomlErr != nil && hardErr != nil {
			return nil, nil, tomlErr
		}
		if tomlErr != nil {
			scopedToml = nil
		}
		if hardErr != nil {
			scopedHard = nil
		}
	case toml != nil && tomlErr != nil:
		return nil, nil, tomlErr
	case hard != nil && hardErr != nil:
		return nil, nil, hardErr
	}
	return scopedToml, scopedHard, nil
}

// DispatchRender chooses RenderJSON vs RenderText based on output.
// Centralizes the format dispatch so callers stay under gocognit ≤ 15.
func DispatchRender(stdout io.Writer, result *engine.PlanResult, output string, color engine.ColorMode, verbose bool) error {
	if output == "json" {
		return engine.RenderJSON(stdout, result)
	}
	return engine.RenderText(stdout, result, color, verbose)
}

// ScopeDomains filters the loaded catalog to the single requested
// tag. Rejects unknown tags with a friendly error (the user
// probably typo'd; surfacing the closed-catalog scope is the
// helpful response per CONTEXT D-04).
//
// sourceLabel names the catalog being searched ("noxctl.toml" /
// "hardcoded registry" / "any catalog (toml, hardcoded)") so the
// operator sees WHICH catalog rejected their --config-source choice.
// Computed by ConfigSourceLabel from the requested engine.ConfigSource.
func ScopeDomains(domains []*bear.Domain, wanted string, sourceLabel string) ([]*bear.Domain, error) {
	for _, d := range domains {
		if d.Tag == wanted {
			return []*bear.Domain{d}, nil
		}
	}
	return nil, fmt.Errorf("noxctl plan: unknown tag %q (not in %s)", wanted, sourceLabel)
}
