package fastpass

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// PromotionRule binds a source tag to a target tag, gated by a
// calendar boundary the atom's creation date must predate. The
// operator declares one rule per `[[promotion]]` block in the TOML
// catalog; `bear/config` ferries the structs onto `engine.ApplyOpts.
// PromotionRules`.
//
// Boundary is one of "day", "week", "month", "year". An empty
// boundary defaults to "day" so the most common case (daily inbox
// rolling forward) needs no explicit value. Unknown boundaries make
// the rule a no-op; load-time validation catches typos before they
// reach the promoter.
type PromotionRule struct {
	From     string
	To       string
	Boundary string
}

// PromoteByCalendar is the public testing seam for the rules-driven
// promoter. Returns (newTag, true) when an atom currently tagged
// `currentTag` should be promoted given its creation date, the
// current wall-clock `now`, and the operator-declared rule chain;
// returns (currentTag, false) when no rule applies or the rules
// slice is empty. Chains the table top-to-bottom so one call moves
// an atom across every applicable tier (aggressive catch-up).
//
// Production hot paths use promoteByCalendarIndexed directly with a
// pre-built rules map so per-atom work stays allocation-free; this
// wrapper exists so test cases can spell their rule tables as a flat
// slice.
func PromoteByCalendar(currentTag string, created, now time.Time, rules []PromotionRule) (string, bool) {
	if len(rules) == 0 {
		return currentTag, false
	}
	return promoteByCalendarIndexed(currentTag, created, now, indexPromotionRules(rules))
}

// promoteByCalendarIndexed is the hot-path body of PromoteByCalendar
// driven by a pre-built rules map. ApplyTimeBasedPromotion builds the
// map once per sweep and threads it through every atom; without this
// extraction the same map would be rebuilt N times per cycle.
func promoteByCalendarIndexed(
	currentTag string,
	created, now time.Time,
	index map[string]PromotionRule,
) (string, bool) {
	tag := currentTag
	moved := false
	for {
		next, advanced := stepPromoteByCalendar(tag, created, now, index)
		if !advanced || next == tag {
			// next == tag is the defense-in-depth guard against a
			// `From == To` self-loop sneaking past validation —
			// without it PromoteByCalendar hangs forever on a
			// malformed rule. validatePromotions rejects self-loops
			// at load time, but defensive coding keeps the runtime
			// safe if a programmatic catalog ever bypasses the
			// validator.
			return tag, moved
		}
		tag = next
		moved = true
	}
}

// stepPromoteByCalendar applies a single rule step against the
// already-indexed rules map. Returns (newTag, true) if the atom
// advances one tier, (currentTag, false) otherwise.
//
// Promotion fires when `created` predates the START of the current
// period for the rule's boundary — strict thresholds, no grace
// window. PromoteByCalendar's outer loop chains moves so a single
// tick can advance an atom across multiple tiers.
func stepPromoteByCalendar(currentTag string, created, now time.Time, rules map[string]PromotionRule) (string, bool) {
	rule, ok := rules[currentTag]
	if !ok {
		return currentTag, false
	}
	if created.Before(boundaryStart(rule.Boundary, now)) {
		return rule.To, true
	}
	return currentTag, false
}

// indexPromotionRules turns the operator-declared rule slice into a
// from-tag lookup map. Duplicate `from` keys are not validated here
// — the catalog loader catches them at load time. The map is built
// per PromoteByCalendar invocation; the cost is negligible against
// the bearcli I/O the surrounding loop performs.
func indexPromotionRules(rules []PromotionRule) map[string]PromotionRule {
	out := make(map[string]PromotionRule, len(rules))
	for _, r := range rules {
		out[r.From] = r
	}
	return out
}

// ValidPromotionBoundaries is the single source of truth for the
// boundary strings the time-promotion fast-pass understands. The
// catalog validator (bear/config/validate.go) and the runtime
// promoter (boundaryStart below) both consult this set so the two
// can't drift — adding a fifth boundary requires exactly one edit
// here and one new arm in boundaryStart.
var ValidPromotionBoundaries = map[string]struct{}{
	"":      {},
	"day":   {},
	"week":  {},
	"month": {},
	"year":  {},
}

// boundaryStart maps the rule's Boundary string to the matching
// calendar-start helper. Unknown boundaries return a zero Time so
// the comparison in stepPromoteByCalendar never fires — load-time
// validation (against ValidPromotionBoundaries) catches typos before
// they reach this path.
func boundaryStart(boundary string, now time.Time) time.Time {
	switch boundary {
	case "", "day":
		return domain.CalendarStartOfDay(now)
	case "week":
		return domain.CalendarStartOfWeek(now)
	case "month":
		return domain.CalendarStartOfMonth(now)
	case "year":
		return domain.CalendarStartOfYear(now)
	}
	return time.Time{}
}

// ApplyTimeBasedPromotion walks every atom whose source tag appears
// as a rule's `From` across `domains`, computes the calendar-correct
// destination via the rule chain, and rewrites canonical tag-lines
// for atoms that need to move. Skips atoms with a valid pin in
// `pins`.
//
// Two-phase design: snapshot all promotion-eligible atoms once
// across every domain (deduped by ID), then process each atom
// exactly once with the chained destination. Without the snapshot,
// a tier-1→tier-2 rewrite mid-sweep re-surfaces the moved atom
// under tier-2's listNotes call, then tier-3's, etc. — yielding one
// extra write per tier crossed. The chained promoter already
// returns the FINAL destination, so a single rewrite per atom
// suffices.
//
// Idempotency comparison strips the trailing new-note link before
// equality (same helper used by cross-domain) so the embedded title
// timestamp doesn't trigger a no-op write.
//
// Failures per atom are logged and skipped so one bad note doesn't stall
// the whole sweep. Pins observed during the pass are NOT recorded — only
// user-driven cross-domain moves create pins.
//
// An empty `rules` slice short-circuits — no rules declared in the
// catalog means time-promotion is disabled.
//
//nolint:lll
func ApplyTimeBasedPromotion(
	ctx context.Context,
	domains []*domain.Domain,
	pins *domain.PinRegistry,
	rules []PromotionRule,
) error {
	_, err := ApplyTimeBasedPromotionResult(ctx, domains, pins, rules)
	return err
}

// ApplyTimeBasedPromotionResult is ApplyTimeBasedPromotion with per-note
// failure counts for apply recap and exit-status decisions.
func ApplyTimeBasedPromotionResult(
	ctx context.Context,
	domains []*domain.Domain,
	pins *domain.PinRegistry,
	rules []PromotionRule,
) (PassResult, error) {
	if len(rules) == 0 {
		return PassResult{}, nil
	}
	now := time.Now()
	domainByTag := indexPromotionDomains(domains, rules)
	// Build the from-tag → rule lookup once per sweep; ApplyTimeBased
	// Promotion fans the same set across every atom, so rebuilding it
	// in each PromoteByCalendar call would be pure waste.
	ruleIndex := indexPromotionRules(rules)

	type atomToProcess struct {
		atom   domain.Note
		source *domain.Domain
	}

	atoms := make([]atomToProcess, 0)
	seen := make(map[string]struct{})
	result := PassResult{}
	for _, source := range domainByTag {
		notes, err := bearcli.ListNotesForTag(ctx, source.Tag)
		if err != nil {
			log.Printf("time-promotion: list %s failed: %v", source.Tag, err)
			result.Failed++
			continue
		}
		for _, atom := range notes {
			if _, dup := seen[atom.ID]; dup {
				continue
			}
			seen[atom.ID] = struct{}{}
			atoms = append(atoms, atomToProcess{atom: atom, source: source})
		}
	}

	for _, item := range atoms {
		if err := domain.CheckCtx(ctx); err != nil {
			return result, err
		}
		switch processAtomForPromotion(ctx, item.atom, item.source, domainByTag, pins, now, ruleIndex) {
		case passSkipped:
		case passChanged:
			result.Changed++
		case passFailed:
			result.Failed++
		}
	}
	return result, nil
}

// processAtomForPromotion handles a single atom in the time-promotion
// sweep: skip-note gate, pin gate, calendar rule, target lookup, and
// rewrite. All failure paths log-and-continue so one bad atom doesn't
// halt the sweep.
func processAtomForPromotion(
	ctx context.Context,
	atom domain.Note,
	source *domain.Domain,
	domainByTag map[string]*domain.Domain,
	pins *domain.PinRegistry,
	now time.Time,
	ruleIndex map[string]PromotionRule,
) passOutcome {
	if domain.IsAuxNote(source, atom) {
		return passSkipped
	}
	if atom.Created.IsZero() {
		log.Printf("time-promotion: %q has no creation date; skipping", atom.Title)
		return passSkipped
	}
	if pins.IsPinned(atom.ID, now) {
		return passSkipped
	}
	newTag, shouldMove := promoteByCalendarIndexed(source.Tag, atom.Created, now, ruleIndex)
	if !shouldMove {
		return passSkipped
	}
	target := domainByTag[newTag]
	if target == nil {
		log.Printf("time-promotion: %q would move to %q but no domain registered", atom.Title, newTag)
		return passSkipped
	}
	changed, err := promoteAtomToDomain(ctx, atom, source, target)
	if err != nil {
		log.Printf("time-promotion: %q failed: %v", atom.Title, err)
		return passFailed
	}
	if !changed {
		return passSkipped
	}
	return passChanged
}

// indexPromotionDomains returns a map keyed by Tag of every domain
// that appears as either the From or To of a promotion rule. Skip-
// atomics-pass domains (umbrellas) are excluded so listNotes doesn't
// pull child atoms via Bear's hierarchical tag query. The set is
// purely catalog-driven — operators with an arbitrary ladder shape
// (e.g. "fleeting/inbox" → "fleeting/week" → …) get the same indexing
// without code changes.
func indexPromotionDomains(all []*domain.Domain, rules []PromotionRule) map[string]*domain.Domain {
	wanted := make(map[string]struct{}, len(rules)*2)
	for _, r := range rules {
		wanted[r.From] = struct{}{}
		wanted[r.To] = struct{}{}
	}
	out := make(map[string]*domain.Domain, len(wanted))
	for _, d := range all {
		if d.SkipAtomicsPass {
			continue
		}
		if _, ok := wanted[d.Tag]; !ok {
			continue
		}
		out[d.Tag] = d
	}
	return out
}

// promoteAtomToDomain rewrites atom's canonical tag-line from source to
// target and overwrites the note in Bear. Reuses rewriteCanonicalTag
// (cross-domain helper) for the line surgery and equalIgnoringNewNoteLink
// for the no-op gate. Asymmetry vs rewriteAtomTag: time-promotion is a
// soft move, so the non-strict predicate is used here (rewriteAtomTag
// uses the strict variant).
func promoteAtomToDomain(ctx context.Context, atom domain.Note, source, target *domain.Domain) (bool, error) {
	newContent, rewrote := rewriteCanonicalTag(atom.Content, source.CanonicalTag, target)
	if !rewrote || domain.EqualIgnoringNewNoteLink(newContent, atom.Content) {
		return false, nil
	}
	err := bearcli.OverwriteWithRetry(ctx, atom.ID, newContent)
	if err != nil {
		return false, fmt.Errorf("time-promotion(%s→%s) %q: %w", source.Tag, target.Tag, atom.Title, err)
	}
	target.Logf("time-promoted: %s ← %s", atom.Title, source.Tag)
	return true, nil
}
