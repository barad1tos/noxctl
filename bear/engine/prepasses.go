package engine

// Pre-pass dispatch — the fast-pass canonicalization passes that run
// before per-domain regen, sourced from the bear/ Apply* family
// (ForeignTagEscape, DailyDefaultTag, CrossDomainMoves,
// TimeBasedPromotion, DomainBootstrap, PlaceholderRefresh).

import (
	"context"
	"log"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
)

// tripped both gocognit and dupl thresholds.
type prePassSpec struct {
	enabled bool
	name    string // PrePasses-map key, e.g. "foreign_tag"
	label   string // log prefix, e.g. "foreign-tag escape"
	fn      func() error
}

func applyPrePasses(ctx context.Context, opts ApplyOpts, result *ApplyResult) {
	if opts.AuditEnabled {
		findings := audit.AuditDomains(ctx, opts.Domains)
		audit.LogAuditFindings(findings, log.Printf)
	}
	// canonical-bootstrap wiring: build the tag→*Domain lookup
	// once so both fast-pass paths can write destination-canonical form
	// in a single bearcli call instead of relying on the next regen
	// cycle to restructure.
	domainsByTag := domain.DomainsByTag(opts.Domains)
	// dailyTagOn folds the catalog-driven "operator declared a daily
	// default tag" gate into the feature toggle. With AutoTagDefault on
	// but DailyDefaultTag empty the daily-default fast-pass is treated
	// as disabled — otherwise the spec calls ApplyDailyDefaultTag with
	// a nil domain (lookup miss on empty key) and stamps a spurious
	// Failed=1 for a feature the operator never opted into.
	//
	// placeholder-refresh stays gated on AutoTagDefault alone: it
	// iterates every domain with a non-empty QuickPlaceholderH1 and
	// has no dependency on the daily tag. Folding DailyDefaultTag
	// into its gate would silently disable placeholder refresh for
	// any catalog that declares `quick_placeholder_h1` on a domain
	// without also setting `[meta].daily_default_tag`.
	dailyTagOn := opts.Features.AutoTagDefault && opts.DailyDefaultTag != ""
	// dupl: keyed-fields layout makes the 6 entries look similar by
	// shape, but each closure dispatches a distinct pre-pass with
	// different inputs. The repetition is the cost of the readable
	// vertical structure; collapsing into a helper hides the dispatch
	// list and makes the order harder to audit.
	//nolint:dupl
	specs := []prePassSpec{
		{
			enabled: opts.Features.ForeignTagEscape,
			name:    "foreign_tag",
			label:   "foreign-tag escape",
			fn: func() error {
				_, err := fastpass.ApplyForeignTagEscape(ctx, domainsByTag)
				return err
			},
		},
		{
			enabled: dailyTagOn,
			name:    "auto_tag",
			label:   "auto-tag",
			fn: func() error {
				_, err := fastpass.ApplyDailyDefaultTag(ctx, domainsByTag[opts.DailyDefaultTag])
				return err
			},
		},
		{
			enabled: opts.Features.DomainBootstrap,
			name:    "domain_bootstrap",
			label:   "domain-bootstrap canonicalize",
			fn: func() error {
				_, err := fastpass.ApplyDomainBootstrap(ctx, domainsByTag)
				return err
			},
		},
		{
			enabled: opts.Features.AutoTagDefault,
			name:    "placeholder_refresh",
			label:   "placeholder refresh",
			fn: func() error {
				_, err := fastpass.ApplyPlaceholderRefresh(ctx, domainsByTag)
				return err
			},
		},
		{
			enabled: opts.Features.CrossDomainMoves,
			name:    "cross_domain",
			label:   "cross-domain moves",
			fn: func() error {
				return fastpass.ApplyCrossDomainMoves(ctx, opts.Domains, opts.Pins)
			},
		},
		{
			enabled: opts.Features.TimePromotion,
			name:    "time_promotion",
			label:   "time-promotion",
			fn: func() error {
				return fastpass.ApplyTimeBasedPromotion(ctx, opts.Domains, opts.Pins, opts.PromotionRules)
			},
		},
	}
	for _, spec := range specs {
		runPrePass(spec, result)
	}
	if opts.Pins != nil {
		if err := opts.Pins.Save(); err != nil {
			log.Printf("pins: save failed: %v (in-memory state preserved)", err)
		}
	}
	if opts.Features.DuplicateRegistry {
		registry, err := domain.BuildDuplicateRegistry(ctx, opts.Domains)
		if err != nil {
			log.Printf("duplicates: registry build failed: %v (continuing with plain wikilinks)", err)
			result.PrePasses["duplicate_registry"] = PrePassCounts{Failed: 1}
		} else {
			for _, domain := range opts.Domains {
				domain.Duplicates = registry
			}
			result.PrePasses["duplicate_registry"] = PrePassCounts{OK: 1}
		}
	}
}

// runPrePass invokes one pre-pass when enabled, logs failure with the
// canonical "(continuing per-domain regen)" suffix, and stamps a
// PrePassCounts entry under spec.name.
func runPrePass(spec prePassSpec, result *ApplyResult) {
	if !spec.enabled {
		return
	}
	if err := spec.fn(); err != nil {
		log.Printf("%s failed: %v (continuing per-domain regen)", spec.label, err)
		result.PrePasses[spec.name] = PrePassCounts{Failed: 1}
		return
	}
	result.PrePasses[spec.name] = PrePassCounts{OK: 1}
}

// applyPerDomain orchestrates the per-domain RunRegen pipeline using a
// per-umbrella errgroup dependency graph.
//
// Structure:
// - Top-level errgroup.WithContext(ctx) — each of the N umbrella
// families (one entry per nil-keyed standalone-group plus one per
// real umbrella) becomes one outer goroutine.
// - Inside each family goroutine: an inner errgroup runs all leaves
// of that family concurrently; after inner.Wait returns, the
// umbrella domain's own RunRegen executes on the same goroutine.
// - Independent families fan out in parallel; back-pressure on actual
// bearcli subprocesses lives in the domain.SetBearcliConcurrency
// semaphore, NOT at the errgroup layer.
//
// State serialization: state.State map writes + st.Save calls happen
// under stateMu. The mutex is held strictly around the map mutation +
// Save call — NEVER during RunRegen or bearcli I/O.
//
// Per-domain failures inside RunRegen are log-and-continue (same
// contract as the sequential predecessor); only ctx cancellation
// propagates as a non-nil errgroup return, which flips
// result.Interrupted=true on Wait.
