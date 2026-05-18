package bear

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
)

// PromoteByCalendar returns (newTag, true) when an atom currently tagged
// `currentTag` should be promoted given its creation date and the current
// wall-clock `now`. Loops the rule table top-to-bottom so a single call
// chains daily → weekly → monthly → yearly → decadal in one shot
// (aggressive catch-up).
//
// Non-quicknote tags pass through unchanged — the function only knows
// about the five quicknote children. Decadal is terminal.
func PromoteByCalendar(currentTag string, created, now time.Time) (string, bool) {
	if !strings.HasPrefix(currentTag, "quicknote/") {
		return currentTag, false
	}
	tag := currentTag
	moved := false
	for {
		next, advanced := stepPromoteByCalendar(tag, created, now)
		if !advanced {
			return tag, moved
		}
		tag = next
		moved = true
	}
}

// stepPromoteByCalendar applies a single rule step. Returns (newTag, true)
// if the atom advances one tier, (currentTag, false) otherwise.
//
// Each tier promotes when `created` predates the START of the current
// period for that tier — strict thresholds, no grace window. Top-down
// loop in PromoteByCalendar chains moves so a single tick can advance
// an atom multiple tiers (aggressive catch-up).
func stepPromoteByCalendar(currentTag string, created, now time.Time) (string, bool) {
	switch currentTag {
	case "quicknote/daily":
		if created.Before(CalendarStartOfDay(now)) {
			return "quicknote/weekly", true
		}
	case "quicknote/weekly":
		if created.Before(CalendarStartOfWeek(now)) {
			return "quicknote/monthly", true
		}
	case "quicknote/monthly":
		if created.Before(CalendarStartOfMonth(now)) {
			return "quicknote/yearly", true
		}
	case "quicknote/yearly":
		if created.Before(CalendarStartOfYear(now)) {
			return "quicknote/decadal", true
		}
	}
	return currentTag, false
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
func ApplyTimeBasedPromotion(ctx context.Context, domains []*Domain, pins *PinRegistry) error {
	now := time.Now()
	domainByTag := indexQuicknoteDomains(domains)

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
		processAtomForPromotion(ctx, item.atom, item.source, domainByTag, pins, now)
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
	newTag, shouldMove := PromoteByCalendar(source.Tag, atom.Created, now)
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

// indexQuicknoteDomains returns a map keyed by Tag of every quicknote/*
// domain in `all`. Skip-atomics-pass domains (umbrellas) are excluded so
// listNotes doesn't pull child atoms via Bear's hierarchical tag query.
func indexQuicknoteDomains(all []*Domain) map[string]*Domain {
	out := make(map[string]*Domain)
	for _, d := range all {
		if d.SkipAtomicsPass {
			continue
		}
		if !strings.HasPrefix(d.Tag, "quicknote/") {
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
