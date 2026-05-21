package config

import (
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/barad1tos/noxctl/bear/fastpass"
)

// tagFormatRE allows lowercase / uppercase letters, digits, slash,
// underscore and hyphen. Anything else is rejected as a tag-injection
// surface (e.g. `tag = "foo;rm -rf /"`). The regex anchors to the
// whole string so partial matches don't slip through.
var tagFormatRE = regexp.MustCompile(`^[a-zA-Z0-9_/-]+$`)

// bucketRejectREs lists patterns that must NOT appear in user-
// supplied bucket names. Blocks bear://x-callback-url injection that
// would otherwise produce arbitrary callback wikilinks when a reader
// clicks the master.
var bucketRejectREs = []*regexp.Regexp{
	regexp.MustCompile(`://`),
	regexp.MustCompile(`(?i)^bear:`),
}

// supportedLocales locks v1 to Ukrainian. Future locales must add
// both their key here AND ship a `bear/locales/<key>.toml` catalog.
// Empty locale is allowed — caller treats as "use default".
var supportedLocales = map[string]struct{}{
	"en": {},
	"uk": {},
}

// ValidateCatalog runs catalog-level invariants AFTER successful TOML
// decode and BEFORE per-stanza dispatch. Aggregates every problem
// into one errors.Join return — never short-circuits on first
// failure.
//
// path is included in every nested error so multi-file callers
// (rare; the loader currently calls with one path) get
// disambiguation for free.
func ValidateCatalog(catalog *Catalog, path string) error {
	var errs []error
	errs = append(errs, validateMeta(catalog, path)...)
	errs = append(errs, validateDomains(catalog, path)...)
	errs = append(errs, validatePromotions(catalog, path)...)
	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

// validatePromotions checks the [[promotion]] block for regressions the
// rules-driven promoter can't recover from at runtime:
//
//   - unknown `boundary` (typos silently produce a no-op rule),
//   - duplicate `from` (the index map keeps only one rule, the rest
//     of the operator's ladder is silently dropped),
//   - self-loop (`From == To`) — PromoteByCalendar's chain loop
//     advances `tag = next` and re-enters with the same rule, the
//     boundary check stays satisfied, and the function hangs,
//   - unreachable `To` — a typo'd target points at a tag no domain
//     owns and no other rule chains from, so promoted notes land
//     under a tag with no master/hub and silently disappear from the
//     regen pipeline.
//
// Aggregated like every other catalog-level check.
func validatePromotions(catalog *Catalog, path string) []error {
	if len(catalog.Promotions) == 0 {
		return nil
	}
	var errs []error
	seen := make(map[string]int, len(catalog.Promotions))
	knownTargets := promotionTargetSet(catalog)
	for i, promotion := range catalog.Promotions {
		errs = append(errs, validatePromotionShape(promotion, i, path, knownTargets)...)
		if promotion.From == "" {
			continue
		}
		if prev, dup := seen[promotion.From]; dup {
			errs = append(errs, fmt.Errorf(
				"%s: promotion[%d] from=%q: duplicate; first declared at promotion[%d]",
				path, i, promotion.From, prev,
			))
		} else {
			seen[promotion.From] = i
		}
	}
	return errs
}

// validatePromotionShape collects every per-rule defect: missing
// from/to, unknown boundary, self-loop, unknown destination tag.
// Extracted from validatePromotions to keep the outer loop under the
// gocognit budget.
func validatePromotionShape(promotion Promotion, i int, path string, knownTargets map[string]struct{}) []error {
	var errs []error
	if promotion.From == "" {
		errs = append(errs, fmt.Errorf("%s: promotion[%d]: from is required", path, i))
		// from-less rule can't meaningfully report the rest with a
		// useful identifier — return early so error stream stays
		// readable.
		return errs
	}
	if promotion.To == "" {
		errs = append(errs, fmt.Errorf("%s: promotion[%d] from=%q: to is required", path, i, promotion.From))
	}
	if _, ok := fastpass.ValidPromotionBoundaries[promotion.Boundary]; !ok {
		errs = append(errs, fmt.Errorf(
			"%s: promotion[%d] from=%q: unknown boundary %q (valid: day|week|month|year, empty = day)",
			path, i, promotion.From, promotion.Boundary,
		))
	}
	if promotion.To != "" && promotion.From == promotion.To {
		errs = append(errs, fmt.Errorf(
			"%s: promotion[%d] from=%q: self-loop (from == to); time-promotion would advance forever",
			path, i, promotion.From,
		))
	}
	if promotion.To != "" && promotion.From != promotion.To {
		if _, ok := knownTargets[promotion.To]; !ok {
			errs = append(errs, fmt.Errorf(
				"%s: promotion[%d] from=%q: to=%q does not match any declared domain tag or another promotion's from",
				path, i, promotion.From, promotion.To,
			))
		}
	}
	return errs
}

// promotionTargetSet returns the universe of tags a `[[promotion]]
// .to` field may legitimately reference: every declared domain Tag
// plus every promotion's From (chained ladders are valid even if the
// intermediate hop isn't a top-level domain — the engine simply
// keeps rewriting the canonical tag-line). Used by validation to
// catch typos in `to` at load time, not at first sweep.
func promotionTargetSet(catalog *Catalog) map[string]struct{} {
	out := make(map[string]struct{}, len(catalog.Domains)+len(catalog.Promotions))
	for _, stanza := range catalog.Domains {
		if stanza.Tag != "" {
			out[stanza.Tag] = struct{}{}
		}
	}
	for _, promotion := range catalog.Promotions {
		if promotion.From != "" {
			out[promotion.From] = struct{}{}
		}
	}
	return out
}

// validateMeta covers [meta].version + [meta].locale. Extracted to
// keep ValidateCatalog under the gocognit budget.
func validateMeta(catalog *Catalog, path string) []error {
	var errs []error
	if catalog.Meta.Version != "1" {
		errs = append(errs, fmt.Errorf("%s: %w: meta.version must be %q (got %q)",
			path, ErrSchemaVersion, "1", catalog.Meta.Version))
	}
	if catalog.Meta.Locale != "" {
		if _, ok := supportedLocales[catalog.Meta.Locale]; !ok {
			errs = append(errs, fmt.Errorf(
				"%s: meta.locale %q unsupported (v1 supports: en, uk)",
				path, catalog.Meta.Locale,
			))
		}
	}
	return errs
}

// validateDomains walks every stanza, applying the per-stanza
// invariants and tracking duplicates. Returns the full list of
// problems; never short-circuits.
func validateDomains(catalog *Catalog, path string) []error {
	var errs []error
	seen := map[string]int{}
	for i, stanza := range catalog.Domains {
		errs = append(errs, validateStanzaInvariants(stanza, i, path)...)
		if stanza.Tag == "" {
			continue
		}
		if prev, dup := seen[stanza.Tag]; dup {
			errs = append(errs, fmt.Errorf("%s: %w: tag %q appears at domain[%d] and domain[%d]",
				path, ErrDuplicateTag, stanza.Tag, prev, i))
		} else {
			seen[stanza.Tag] = i
		}
	}
	return errs
}

// validateStanzaInvariants runs the per-stanza invariants that don't
// require neighbor-context (duplicate detection lives in
// validateDomains because it needs the full slice).
func validateStanzaInvariants(stanza Stanza, i int, path string) []error {
	var errs []error
	if stanza.Tag == "" {
		errs = append(errs, fmt.Errorf("%s: domain[%d]: tag is required", path, i))
	} else {
		if !tagFormatRE.MatchString(stanza.Tag) {
			errs = append(errs, fmt.Errorf(
				"%s: domain[%d] tag=%q: must match [a-zA-Z0-9_/-]+ (security: tag-injection guard)",
				path, i, stanza.Tag,
			))
		}
		if strings.Count(stanza.Tag, "/") > 1 {
			errs = append(errs, fmt.Errorf(
				"%s: domain[%d] tag=%q: tag tree depth limit exceeded (max 2 segments)",
				path, i, stanza.Tag,
			))
		}
	}
	if stanza.IndexTitle == "" {
		errs = append(errs, fmt.Errorf("%s: domain[%d] tag=%q: index_title is required", path, i, stanza.Tag))
	}
	if stanza.Blueprint == "" {
		errs = append(errs, fmt.Errorf("%s: domain[%d] tag=%q: blueprint is required", path, i, stanza.Tag))
	}
	if stanza.UnknownBucket != nil {
		for _, re := range bucketRejectREs {
			if re.MatchString(*stanza.UnknownBucket) {
				errs = append(errs, fmt.Errorf(
					"%s: domain[%d] tag=%q unknown_bucket=%q: contains forbidden pattern (security)",
					path, i, stanza.Tag, *stanza.UnknownBucket,
				))
			}
		}
	}
	return errs
}
