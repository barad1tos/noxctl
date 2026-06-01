package fastpass

// domain.Domain-bootstrap fast-pass — canonicalize any note whose tag set
// matches a managed leaf domain (most-specific-leaf wins; bare
// umbrella tag → DefaultChild). Includes the loop guard that
// prevents bootstrap from re-stamping a note already rewritten on
// the same daemon process (catches a regression class observed
// during early universal-canonicalization rollout).

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// ApplyDomainBootstrap canonicalizes any note whose tag set matches
// a managed leaf domain (most-specific-leaf wins; bare umbrella tag
// → DefaultChild). Runs as the fourth fast-pass entry in the
// daemon's handleAutoTagTick loop. The self-write gate around the
// pass plus effectiveSelfWriteEpsilon suppresses FSEvent feedback
// on our own writes. Returns the number of notes actually rewritten
// (zero when every candidate is already canonical or no notes match).
func ApplyDomainBootstrap(ctx context.Context, domainsByTag map[string]*domain.Domain) (int, error) {
	result, runErr := ApplyDomainBootstrapResult(ctx, domainsByTag)
	changed := result.Changed
	return changed, runErr
}

// ApplyDomainBootstrapResult is ApplyDomainBootstrap with per-note failure
// counts for apply recap and exit-status decisions.
func ApplyDomainBootstrapResult(ctx context.Context, domainsByTag map[string]*domain.Domain) (PassResult, error) {
	if len(domainsByTag) == 0 {
		return PassResult{}, nil
	}
	if bootstrapLoop.disabledSnapshot() {
		return PassResult{}, nil
	}
	out, err := bearcli.Run(
		ctx,
		[]string{"list", "--location", "notes", bearcli.FlagFormat, bearcli.FormatJSON, bearcli.FlagFields, bearcli.FieldsAutoTag}, //nolint:lll
		"",
	)
	if err != nil {
		return PassResult{}, fmt.Errorf("ApplyDomainBootstrap list: %w", err)
	}
	var notes []domain.AutoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return PassResult{}, fmt.Errorf("ApplyDomainBootstrap parse: %w", err)
	}
	result := PassResult{}
	warned := make(map[string]struct{}) // log-once-per-tick per note ID
	//nolint:dupl // mirrors sibling fastpass loop; shared scan pattern
	for _, note := range notes {
		if err = domain.CheckCtx(ctx); err != nil {
			return result, err
		}
		switch applyDomainBootstrapOne(ctx, note, domainsByTag, warned) {
		case passSkipped:
		case passChanged:
			result.Changed++
		case passFailed:
			result.Failed++
		}
	}
	if result.Changed > 0 {
		logf(ctx, "domain-bootstrap: %d note(s) canonicalized", result.Changed)
	}
	return result, nil
}

// applyDomainBootstrapOne handles one note's match → guard → render →
// idempotency → overwrite chain. Returns true iff a bearcli overwrite
// fired. Extracted from `ApplyDomainBootstrap` to keep the parent loop
// under the gocognit ≤15 budget.
func applyDomainBootstrapOne(
	ctx context.Context,
	note domain.AutoTagNote,
	domainsByTag map[string]*domain.Domain,
	warned map[string]struct{},
) passOutcome {
	// Structural-note guard: titles prefixed with `✱ ` mark hub/master/
	// umbrella index notes (project convention). They're owned end-to-
	// end by the per-domain regen path (`WriteMasterHeader`, hub-tier
	// builders) — bootstrap pass MUST NOT stamp them as if they were
	// leaf atomics. Incident #3 (2026-05-17 18:32) had bootstrap mangle
	// `✱ Library`, `✱ LLM`, `✱ IT`, `✱ Quicknote` by prepending the
	// `DefaultChild` leaf canonical to their bodies (and triggering
	// Bear to auto-tag them with the leaf sub-tag).
	if strings.HasPrefix(note.Title, "✱ ") {
		return passSkipped
	}
	d := matchOwningDomain(ctx, note.Tags, domainsByTag, note.ID, warned)
	if d == nil {
		return passSkipped
	}
	// Defensive guard: `matchOwningDomain` MUST have resolved
	// umbrella → DefaultChild via `ResolveURLDomain`. This branch is
	// unreachable through that path today; it locks against future
	// refactors that might drop the resolution OR cases where
	// `domainsByTag` carries a bare umbrella without its child wiring.
	if d.SkipAtomicsPass {
		logf(ctx, "domain-bootstrap: BUG — umbrella %q leaked past matchOwningDomain "+
			"for note %q; skipping", d.Tag, note.ID)
		return passSkipped
	}
	// Loop-prevention guard: when the note's body ALREADY carries a
	// canonical tag-line for the leaf, drift fix-up (bucket shifts, URL
	// shape evolution) belongs to the per-domain `processAtomic` path —
	// NOT to the bootstrap pass. `domain.RenderCanonicalForBootstrap` always
	// re-stamps with `d.UnknownBucket`, so rewriting a per-domain-bucketed
	// note here would ping-pong with the next per-domain regen tick.
	// Bug surfaced 2026-05-17: 19,040 rewrites of the same notes
	// across a 50 min window before the kill-switch fired.
	// The bootstrap pass MUST only stamp truly fresh notes.
	if hasCanonicalLineForLeaf(note.Content, d.Tag) {
		return passSkipped
	}
	// Defense-in-depth: even with the hasCanonicalLineForLeaf guard,
	// a future render bug could re-introduce a loop. The `bootstrapLoop`
	// tracker is a process-lifetime circuit-breaker that skips notes
	// the pass has already rewritten ≥ `bootstrapNoteRewriteCap` times
	// and emergency-disables the whole pass once
	// `bootstrapStuckEmergencyCap` distinct notes hit that cap.
	if bootstrapLoop.shouldSkipNote(note.ID) {
		return passSkipped
	}
	canonical := d.RenderCanonicalForBootstrap(note.Content)
	if domain.EqualIgnoringNewNoteLinkStrict(note.Content, canonical) {
		return passSkipped
	}
	if err := bearcli.OverwriteWithRetry(ctx, note.ID, canonical); err != nil {
		logf(ctx, "domain-bootstrap %q failed: %v", note.Title, err)
		return passFailed
	}
	bootstrapLoop.recordRewrite(ctx, note.ID, note.Title)
	logf(ctx, "domain-bootstrap: %s → canonical %s", note.Title, d.Tag)
	return passChanged
}

// Loop-detection limits — defense-in-depth against future render
// bugs that could re-introduce a per-note rewrite cycle. The
// thresholds are tuned so a legitimate startup catch-up (one rewrite
// per note) never trips them, while pathological patterns (the
// 2026-05-17 incident hit 2544 rewrites for one note) are caught
// after ≤ N attempts.
const (
	// bootstrapNoteRewriteCap is the per-note cumulative rewrite
	// count beyond which the note is treated as "stuck" — further
	// bootstrap attempts on it within this process are suppressed.
	bootstrapNoteRewriteCap = 5
	// bootstrapStuckEmergencyCap is the distinct-stuck-note count
	// beyond which the entire bootstrap pass is disabled until
	// daemon restart. Hitting this signals a systemic render bug
	// (or a Bear-side write race), not isolated content edge cases.
	bootstrapStuckEmergencyCap = 20
)

// bootstrapLoopGuard tracks per-note rewrite counts across ticks and
// emergency-disables the bootstrap pass when too many notes get
// stuck. Process-lifetime state — counters reset only on daemon
// restart, intentionally so a stuck note stays suppressed until the
// operator intervenes.
type bootstrapLoopGuard struct {
	mu       sync.Mutex
	counts   map[string]int
	stuck    map[string]struct{}
	disabled bool
}

var bootstrapLoop = &bootstrapLoopGuard{
	counts: make(map[string]int),
	stuck:  make(map[string]struct{}),
}

// disabledSnapshot returns the current emergency-disabled flag.
func (g *bootstrapLoopGuard) disabledSnapshot() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.disabled
}

// shouldSkipNote reports whether the given note has been marked
// "stuck" and should be skipped on this tick.
func (g *bootstrapLoopGuard) shouldSkipNote(noteID string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	_, stuck := g.stuck[noteID]
	return stuck
}

// recordRewrite increments the per-note counter after a successful
// overwrite. Marks the note "stuck" at `bootstrapNoteRewriteCap` and
// emergency-disables the pass at `bootstrapStuckEmergencyCap` stuck
// notes. Both transitions are logged once (no log spam on subsequent
// ticks).
func (g *bootstrapLoopGuard) recordRewrite(ctx context.Context, noteID, title string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[noteID]++
	if g.counts[noteID] < bootstrapNoteRewriteCap {
		return
	}
	if _, already := g.stuck[noteID]; !already {
		g.stuck[noteID] = struct{}{}
		logf(ctx, "WARN: domain-bootstrap LOOP detected for note %q (%s); "+
			"rewrite_count=%d ≥ cap=%d; suppressing on future ticks",
			title, noteID, g.counts[noteID], bootstrapNoteRewriteCap)
	}
	if !g.disabled && len(g.stuck) >= bootstrapStuckEmergencyCap {
		g.disabled = true
		logf(ctx,
			"WARN: domain-bootstrap EMERGENCY DISABLE — %d distinct notes hit rewrite-loop cap; "+
				"pass disabled until daemon restart. Set REGEN_DOMAIN_BOOTSTRAP=off and "+
				"investigate render idempotency.",
			len(g.stuck),
		)
	}
}

// ResetBootstrapLoopForTest zeroes the singleton guard. Test-only
// seam — production code never resets the counters (stuck notes
// stay suppressed for the daemon's lifetime by design). Exported
// for the integration tests in `tests/bear/engine`; production
// callers MUST NOT use it.
func ResetBootstrapLoopForTest() {
	bootstrapLoop.mu.Lock()
	defer bootstrapLoop.mu.Unlock()
	bootstrapLoop.counts = make(map[string]int)
	bootstrapLoop.stuck = make(map[string]struct{})
	bootstrapLoop.disabled = false
}

// hasCanonicalLineForLeaf reports whether `content` already carries a
// canonical tag-line for the given leaf `tag`. Accepts two shapes:
// - leaf form `#<tag> | …` (hub-routed, flat-list, grouped-vertical)
// - sub-tag bucket form `#<tag>/<sub> | …` (grouped-vertical, where
// Bear materializes the bucket as a sibling sub-tag)
//
// Used by `applyDomainBootstrapOne` as the loop-prevention guard
// against `domain.RenderCanonicalForBootstrap`'s `UnknownBucket`
// reset. The sub-tag form (`#<top>/<unknown-bucket> | …`) slipped
// past a strict `#<top> | ` prefix check in an earlier incident and
// looped against per-domain regen until the circuit-breaker fired —
// hence this predicate accepts BOTH `#tag | ` and `#tag/<sub> | `.
func hasCanonicalLineForLeaf(content, tag string) bool {
	base := "#" + tag
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, base) {
			continue
		}
		rest := trimmed[len(base):]
		if strings.HasPrefix(rest, " | ") {
			return true
		}
		if strings.HasPrefix(rest, "/") && strings.Contains(rest, " | ") {
			return true
		}
	}
	return false
}

// matchOwningDomain picks the domain that should own a note based on
// its tag array. Returns nil when no managed tag matches OR when
// multiple unrelated leaf tags are present at equal max length
// (multi-leaf tie). The `warned` set suppresses repeated WARN logs
// for the same note ID within a single tick.
//
// Resolution order:
// 1. Collect every managed tag — separating umbrellas
// (`SkipAtomicsPass=true`) from leaves.
// 2. If ≥1 leaf present, return the most-specific (longest tag
// string) or skip on tie.
// 3. If no leaves but an umbrella matched, return
// `umbrella.ResolveURLDomain` — typically the umbrella's
// `DefaultChild` leaf.
// 4. Otherwise return nil.
func matchOwningDomain(
	ctx context.Context,
	noteTags []string,
	domainsByTag map[string]*domain.Domain,
	noteID string,
	warned map[string]struct{},
) *domain.Domain {
	var leaves []*domain.Domain
	var umbrella *domain.Domain
	for _, raw := range noteTags {
		clean := strings.TrimPrefix(raw, "#")
		d, ok := domainsByTag[clean]
		if !ok {
			continue
		}
		if d.SkipAtomicsPass {
			umbrella = d
			continue
		}
		leaves = append(leaves, d)
	}
	switch len(leaves) {
	case 0:
		if umbrella != nil {
			return umbrella.ResolveURLDomain() // bare-umbrella → DefaultChild leaf
		}
		return nil
	case 1:
		return leaves[0]
	default:
		return mostSpecificOrSkip(ctx, leaves, noteID, warned)
	}
}

// mostSpecificOrSkip ranks leaves by tag-string length (longest =
// most-specific). Ties (multiple distinct leaves with the same
// longest length) → log WARN once per note-ID and return nil. The
// `warned` set is allocated per-tick by `ApplyDomainBootstrap` so an
// ambiguous note re-warns on the next tick if it still carries
// ambiguous tags — the visibility we want for stuck-state debugging.
func mostSpecificOrSkip(
	ctx context.Context,
	leaves []*domain.Domain,
	noteID string,
	warned map[string]struct{},
) *domain.Domain {
	longest := leaves[0]
	tied := false
	for _, d := range leaves[1:] {
		switch {
		case len(d.Tag) > len(longest.Tag):
			longest = d
			tied = false
		case len(d.Tag) == len(longest.Tag) && d.Tag != longest.Tag:
			tied = true
		}
	}
	if !tied {
		return longest
	}
	if _, seen := warned[noteID]; !seen {
		warned[noteID] = struct{}{}
		tags := make([]string, 0, len(leaves))
		for _, d := range leaves {
			tags = append(tags, d.Tag)
		}
		logf(ctx, "domain-bootstrap: note %q has ambiguous managed tags %v — "+
			"skipping (resolve manually)", noteID, tags)
	}
	return nil
}

// matchDomainByTag returns the first candidate domain whose Tag
// appears in the note's tag set, or nil. Tags from bearcli carry
// the leading '#'; strip before comparison.
func matchDomainByTag(noteTags []string, candidates []*domain.Domain) *domain.Domain {
	for _, tag := range noteTags {
		clean := strings.TrimPrefix(tag, "#")
		for _, d := range candidates {
			if d.Tag == clean {
				return d
			}
		}
	}
	return nil
}
