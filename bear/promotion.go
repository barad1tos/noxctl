package bear

import (
	"context"
	"fmt"
	"log"
	"time"
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

// PromoteByCalendar returns (newTag, true) when an atom currently
// tagged `currentTag` should be promoted given its creation date,
// the current wall-clock `now`, and the operator-declared rule
// chain. Loops the rule table top-to-bottom so a single call chains
// every applicable rule in one shot (aggressive catch-up).
//
// Tags that don't appear as any rule's From pass through unchanged.
// An empty rules slice disables promotion entirely — the function
// returns (currentTag, false) immediately.
// PromoteByCalendar is the public testing seam for the rules-driven
// promoter. Production hot paths use promoteByCalendarIndexed
// directly with a pre-built map so the per-atom work stays
// allocation-free; this wrapper exists so test cases can spell their
// rule tables as a flat slice.
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
func promoteByCalendarIndexed(currentTag string, created, now time.Time, index map[string]PromotionRule) (string, bool) {
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
		return CalendarStartOfDay(now)
	case "week":
		return CalendarStartOfWeek(now)
	case "month":
		return CalendarStartOfMonth(now)
	case "year":
		return CalendarStartOfYear(now)
	}
	return time.Time{}
}

// ApplyTimeBasedPromotion walks every quicknote/* atom across `domains`,
// computes the calendar-correct destination via PromoteByCalendar, and
// rewrites canonical tag-lines for atoms that need to move. Skips atoms
// with a valid pin in `pins`.
//
// Two-phase design: snapshot all quicknote atoms once across every
// domain (deduped by ID), then process each atom exactly once with
// PromoteByCalendar's chained destination. Without the snapshot, a
// daily→weekly rewrite mid-sweep re-surfaces the moved atom under
// weekly's listNotes call, then monthly's, etc. — yielding up to four
// writes for an atom that crosses all five tiers. PromoteByCalendar
// already returns the FINAL destination via internal loop, so a single
// rewrite per atom suffices.
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
func ApplyTimeBasedPromotion(ctx context.Context, domains []*Domain, pins *PinRegistry, rules []PromotionRule) error {
	if len(rules) == 0 {
		return nil
	}
	now := time.Now()
	domainByTag := indexPromotionDomains(domains, rules)
	// Build the from-tag → rule lookup once per sweep; ApplyTimeBased
	// Promotion fans the same set across every atom, so rebuilding it
	// in each PromoteByCalendar call would be pure waste.
	ruleIndex := indexPromotionRules(rules)

	type atomToProcess struct {
		atom   Note
		source *Domain
	}

	atoms := make([]atomToProcess, 0)
	seen := make(map[string]struct{})
	for _, source := range domainByTag {
		notes, err := source.listNotes(ctx)
		if err != nil {
			log.Printf("time-promotion: list %s failed: %v", source.Tag, err)
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
		if err := CheckCtx(ctx); err != nil {
			return err
		}
		processAtomForPromotion(ctx, item.atom, item.source, domainByTag, pins, now, ruleIndex)
	}
	return nil
}

// processAtomForPromotion handles a single atom in the time-promotion
// sweep: skip-note gate, pin gate, calendar rule, target lookup, and
// rewrite. All failure paths log-and-continue so one bad atom doesn't
// halt the sweep.
func processAtomForPromotion(
	ctx context.Context,
	atom Note,
	source *Domain,
	domainByTag map[string]*Domain,
	pins *PinRegistry,
	now time.Time,
	ruleIndex map[string]PromotionRule,
) {
	if source.skipNote(atom) {
		return
	}
	if atom.Created.IsZero() {
		log.Printf("time-promotion: %q has no creation date; skipping", atom.Title)
		return
	}
	if pins.IsPinned(atom.ID, now) {
		return
	}
	newTag, shouldMove := promoteByCalendarIndexed(source.Tag, atom.Created, now, ruleIndex)
	if !shouldMove {
		return
	}
	target := domainByTag[newTag]
	if target == nil {
		log.Printf("time-promotion: %q would move to %q but no domain registered", atom.Title, newTag)
		return
	}
	if err := promoteAtomToDomain(ctx, atom, source, target); err != nil {
		log.Printf("time-promotion: %q failed: %v", atom.Title, err)
	}
}

// indexPromotionDomains returns a map keyed by Tag of every domain
// that appears as either the From or To of a promotion rule. Skip-
// atomics-pass domains (umbrellas) are excluded so listNotes doesn't
// pull child atoms via Bear's hierarchical tag query. The set is
// catalog-driven now — no hardcoded "quicknote/" prefix — so an
// operator with their own ladder ("fleeting/inbox" → "fleeting/week"
// → …) gets the same indexing for free.
func indexPromotionDomains(all []*Domain, rules []PromotionRule) map[string]*Domain {
	wanted := make(map[string]struct{}, len(rules)*2)
	for _, r := range rules {
		wanted[r.From] = struct{}{}
		wanted[r.To] = struct{}{}
	}
	out := make(map[string]*Domain, len(wanted))
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
// for the no-op gate. R2 asymmetry preserved: time-promotion is a soft
// move, so the non-strict predicate is used here (rewriteAtomTag uses
// the strict variant).
func promoteAtomToDomain(ctx context.Context, atom Note, source, target *Domain) error {
	newContent, rewrote := rewriteCanonicalTag(atom.Content, source.CanonicalTag, target)
	if !rewrote || equalIgnoringNewNoteLink(newContent, atom.Content) {
		return nil
	}
	err := overwriteWithRetry(ctx, atom.ID, newContent)
	if err != nil {
		return fmt.Errorf("time-promotion(%s→%s) %q: %w", source.Tag, target.Tag, atom.Title, err)
	}
	target.Logf("time-promoted: %s ← %s", atom.Title, source.Tag)
	return nil
}
