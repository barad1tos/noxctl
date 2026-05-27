package engine

// Pre-pass dispatch — the fast-pass canonicalization passes that run
// before per-domain regen, sourced from the bear/ Apply* family
// (ForeignTagEscape, DailyDefaultTag, CrossDomainMoves,
// TimeBasedPromotion, DomainBootstrap, PlaceholderRefresh).

import (
	"context"
	"errors"
	"log"

	"github.com/barad1tos/noxctl/bear/audit"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
	"github.com/barad1tos/noxctl/bear/regen"
)

// prePassSpec describes one pre-pass: the enable gate, the metric
// key in ApplyResult.PrePasses, a human-readable label for log
// output, and the closure that runs the pass.
type prePassSpec struct {
	enabled bool
	name    string // PrePasses-map key, e.g. "foreign_tag"
	label   string // log prefix, e.g. "foreign-tag escape"
	fn      func() (PrePassCounts, error)
}

func applyPrePasses(ctx context.Context, opts ApplyOpts, result *ApplyResult) {
	if opts.AuditEnabled {
		findings := audit.Scan(ctx, opts.Domains)
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
			fn: func() (PrePassCounts, error) {
				passResult, err := fastpass.ApplyForeignTagEscapeResult(ctx, domainsByTag)
				return prePassCountsFromFastPass(passResult), err
			},
		},
		{
			enabled: dailyTagOn,
			name:    "auto_tag",
			label:   "auto-tag",
			fn: func() (PrePassCounts, error) {
				passResult, err := fastpass.ApplyDailyDefaultTagResult(ctx, domainsByTag[opts.DailyDefaultTag])
				return prePassCountsFromFastPass(passResult), err
			},
		},
		{
			enabled: opts.Features.DomainBootstrap,
			name:    "domain_bootstrap",
			label:   "domain-bootstrap canonicalize",
			fn: func() (PrePassCounts, error) {
				passResult, err := fastpass.ApplyDomainBootstrapResult(ctx, domainsByTag)
				return prePassCountsFromFastPass(passResult), err
			},
		},
		{
			enabled: opts.Features.AutoTagDefault,
			name:    "placeholder_refresh",
			label:   "placeholder refresh",
			fn: func() (PrePassCounts, error) {
				passResult, err := fastpass.ApplyPlaceholderRefreshResult(ctx, domainsByTag)
				return prePassCountsFromFastPass(passResult), err
			},
		},
		{
			enabled: opts.Features.CrossDomainMoves,
			name:    "cross_domain",
			label:   "cross-domain moves",
			fn: func() (PrePassCounts, error) {
				passResult, err := fastpass.ApplyCrossDomainMovesResult(ctx, opts.Domains, opts.Pins)
				return prePassCountsFromFastPass(passResult), err
			},
		},
		{
			enabled: opts.Features.TimePromotion,
			name:    "time_promotion",
			label:   "time-promotion",
			fn: func() (PrePassCounts, error) {
				passResult, err := fastpass.ApplyTimeBasedPromotionResult(
					ctx, opts.Domains, opts.Pins, opts.PromotionRules,
				)
				return prePassCountsFromFastPass(passResult), err
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
	clearDuplicateRegistries(opts.Domains)
	if opts.Features.DuplicateRegistry && len(opts.Domains) > 0 {
		registry, err := regen.BuildCorpusDuplicateRegistry(ctx)
		if err != nil {
			log.Printf("duplicates: registry build failed: %v (continuing with plain wikilinks)", err)
			result.PrePasses["duplicate_registry"] = PrePassCounts{Failed: 1}
		} else {
			for _, d := range opts.Domains {
				d.Duplicates = registry
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
	counts, err := spec.fn()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Interrupted = true
			result.PrePasses[spec.name] = counts
			return
		}
		log.Printf("%s failed: %v (continuing per-domain regen)", spec.label, err)
		if counts.Failed == 0 {
			counts.Failed = 1
		}
		result.PrePasses[spec.name] = counts
		return
	}
	if counts.OK == 0 && counts.Changed == 0 && counts.Failed == 0 {
		counts.OK = 1
	}
	result.PrePasses[spec.name] = counts
}

// RunPrePassForTest exposes pre-pass result classification to external tests.
func RunPrePassForTest(
	result *ApplyResult,
	enabled bool,
	name, label string,
	fn func() (PrePassCounts, error),
) {
	runPrePass(prePassSpec{
		enabled: enabled,
		name:    name,
		label:   label,
		fn:      fn,
	}, result)
}

func prePassCountsFromFastPass(result fastpass.PassResult) PrePassCounts {
	return PrePassCounts{
		Changed: result.Changed,
		Failed:  result.Failed,
	}
}
