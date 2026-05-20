// Package bear hosts the fast-pass canonicalization pipeline that the
// daemon's `handleAutoTagTick` (and `engine.applyPrePasses` for
// `noxctl apply --once`) runs every poll interval, in this fixed order:
//
// 1. [ApplyForeignTagEscape] — release notes that carry both
// `#quicknote/*` and a foreign tag from the quicknote auto-flow,
// substituting the foreign tag in body.
// 2. [ApplyDailyDefaultTag] — stamp untagged notes (e.g. Bear's
// compose-from-Notes-view) with `#quicknote/daily`.
// 3. [ApplyDomainBootstrap] — canonicalize any note whose tags match a
// managed leaf domain (most-specific-leaf wins; bare umbrella →
// `DefaultChild`). Wired in as the fourth pass.
// 4. [ApplyPlaceholderRefresh] — rewrite the H1 placeholder marker
// left by Bear's x-callback bootstrap with a fresh timestamp on
// opted-in domains.
//
// Order is load-bearing: foreign-tag escape first so a freshly-stamped
// daily note cannot be misclassified on the same tick;
// `ApplyDomainBootstrap` third so notes the daily/escape passes just
// stamped get their destination-canonical body written in the same
// tick; placeholder refresh last so it can rewrite H1 markers on notes
// the daily pass just produced via x-callback bootstrap.
//
// Each pass is idempotent and individually feature-gated
// (`Features.DomainBootstrap` etc.) so operators can disable any single
// pass without breaking the rest of the chain.
package bear

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
)

// ApplyDailyDefaultTag scans every note in the `notes` location, finds the
// ones with no tags at all, and stamps each with `#quicknote/daily` so the
// user's capture-without-thinking-about-tags flow (Bear's compose-from-
// Notes-view) lands in the daily journal automatically.
//
// Idempotency contract: only acts on notes whose `tags` array is empty.
// Once a note carries any tag — the daily tag from this pass, or any
// user-supplied tag like `#claude/sessions` — the predicate fails and the
// note is skipped. Moving a note out of `#quicknote/daily` to another tag
// will not be silently undone next regen.
//
// Canonical-form bootstrap: the pre-pass writes the
// FULL canonical body for `dailyDomain` in one bearcli call —
// `# <H1>` + `#quicknote/daily | [[✱ Daily]] | [Нова нотатка](bear://…)`
// + `\n---\n\n` + body. The subsequent regen cycle's
// upsertAtomicBacklink no-ops via equalIgnoringNewNoteLink, so the
// user sees the canonical form within one fast-pass tick (~2s) instead
// of waiting for the FSEvent-driven cycle to restructure. Body that
// the user typed before this pass moves below the `---` separator
// (RenderCanonicalForBootstrap re-classifies preamble as body).
//
// Failures per note are logged and skipped; the pass continues so one
// flaky note can't stall the rest of the regen pipeline.
//
// Returns the number of notes actually stamped (zero when every note is
// already tagged). Callers use this to decide whether the pre-pass
// produced bearcli writes that need to be self-gated downstream (Phase
// 09 fast-pass gate fix).
func ApplyDailyDefaultTag(ctx context.Context, dailyDomain *Domain) (int, error) {
	if dailyDomain == nil {
		return 0, fmt.Errorf("ApplyDailyDefaultTag: dailyDomain is nil")
	}
	out, err := runBearcli(
		ctx,
		[]string{"list", "--location", "notes", flagFormat, formatJSON, flagFields, fieldsAutoTag},
		"",
	)
	if err != nil {
		return 0, fmt.Errorf("ApplyDailyDefaultTag list: %w", err)
	}
	var notes []autoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return 0, fmt.Errorf("ApplyDailyDefaultTag parse: %w", err)
	}
	stamped := 0
	for _, note := range notes {
		if err = CheckCtx(ctx); err != nil {
			return stamped, err
		}
		if len(note.Tags) > 0 {
			continue
		}
		newContent := dailyDomain.RenderCanonicalForBootstrap(note.Content)
		if err = overwriteWithRetry(ctx, note.ID, newContent); err != nil {
			log.Printf("auto-tag %q failed: %v", note.Title, err)
			continue
		}
		log.Printf("auto-tag: %s → #quicknote/daily", note.Title)
		stamped++
	}
	if stamped > 0 {
		log.Printf("auto-tag: %d untagged note(s) stamped with #quicknote/daily", stamped)
	}
	return stamped, nil
}

// ApplyPlaceholderRefresh scans the most recently created notes for
// ones whose Title equals any opted-in domain's effective placeholder
// (Domain.QuickPlaceholderH1 override, falling back to
// bear.DefaultQuickPlaceholderH1). For each match whose tags include
// the source domain's Tag, rewrites the H1 line with a fresh
// nowForNewNoteLink timestamp. Body below H1 is preserved
// byte-for-byte so the user's caret position survives the silent
// overwrite.
//
// Scope is narrowed at the bearcli level: --sort created:desc
// --limit 20 — placeholder notes are by definition freshly clicked,
// so the newest slice catches them within ~2 s tick latency. No
// --tag filter because placeholders are shared across domains; the
// per-note tag check inside the loop ensures we only act on notes
// that genuinely belong to an opted-in domain.
//
// Two-signal filter (Title==placeholder AND content starts with that
// H1 marker, via refreshPlaceholderH1) guards against false-positives
// on notes a user manually titled the placeholder string.
//
// Self-cleaning idempotency: once H1 is rewritten, the marker is
// gone, so the next tick skips the same note.
//
// Returns the number of notes actually rewritten.
func ApplyPlaceholderRefresh(ctx context.Context, domainsByTag map[string]*Domain) (int, error) {
	placeholderToDomains := buildPlaceholderIndex(domainsByTag)
	if len(placeholderToDomains) == 0 {
		return 0, nil
	}

	out, err := runBearcli(
		ctx,
		[]string{
			"list",
			"--sort", "created:desc",
			"--limit", "20",
			flagFormat, formatJSON,
			flagFields, fieldsAutoTag,
		},
		"",
	)
	if err != nil {
		return 0, fmt.Errorf("ApplyPlaceholderRefresh list: %w", err)
	}
	var notes []autoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return 0, fmt.Errorf("ApplyPlaceholderRefresh parse: %w", err)
	}

	refreshed := 0
	stamp := nowForNewNoteLink().Format(h1DatetimeFormat)
	for _, note := range notes {
		if err = CheckCtx(ctx); err != nil {
			return refreshed, err
		}
		if refreshOnePlaceholder(ctx, note, placeholderToDomains, stamp) {
			refreshed++
		}
	}
	return refreshed, nil
}

// buildPlaceholderIndex inverts domainsByTag into placeholder→[]*Domain.
// Multiple domains may share the same placeholder (e.g. all default to
// DefaultQuickPlaceholderH1); the per-note tag check in the loop
// disambiguates which one a given Title=="<placeholder>" note belongs to.
func buildPlaceholderIndex(domainsByTag map[string]*Domain) map[string][]*Domain {
	out := make(map[string][]*Domain, len(domainsByTag))
	for _, d := range domainsByTag {
		if d == nil {
			continue
		}
		key := d.effectiveQuickPlaceholderH1()
		out[key] = append(out[key], d)
	}
	return out
}

// refreshOnePlaceholder applies the dispatch + rewrite for a single
// bearcli list entry: lookup-by-Title, tag-set verification,
// H1-marker rewrite, bearcli overwrite. Returns true iff the note
// was actually rewritten. Extracted from the loop in
// ApplyPlaceholderRefresh to keep the parent function under the
// gocognit ≤15 budget.
func refreshOnePlaceholder(
	ctx context.Context,
	note autoTagNote,
	placeholderToDomains map[string][]*Domain,
	stamp string,
) bool {
	candidates, ok := placeholderToDomains[note.Title]
	if !ok {
		return false
	}
	d := matchDomainByTag(note.Tags, candidates)
	if d == nil {
		return false
	}
	newContent, didRefresh := refreshPlaceholderH1(note.Content, d.effectiveQuickPlaceholderH1(), stamp)
	if !didRefresh {
		return false
	}
	if err := overwriteWithRetry(ctx, note.ID, newContent); err != nil {
		log.Printf("placeholder refresh %q failed: %v", note.Title, err)
		return false
	}
	log.Printf("placeholder refresh: %s → # %s", note.Title, stamp)
	return true
}

// ApplyQuicknotePlaceholderRefresh is the legacy entry point retained
// for binary compatibility. Forwards to ApplyPlaceholderRefresh with
// a one-element domain map. Slated for removal once external callers
// migrate.
func ApplyQuicknotePlaceholderRefresh(ctx context.Context, dailyDomain *Domain) (int, error) {
	if dailyDomain == nil {
		return 0, fmt.Errorf("ApplyQuicknotePlaceholderRefresh: dailyDomain is nil")
	}
	return ApplyPlaceholderRefresh(ctx, map[string]*Domain{dailyDomain.Tag: dailyDomain})
}

// ApplyDomainBootstrap canonicalizes any note whose `Tags` array
// matches a managed leaf domain. Fourth fast-pass; wired into
// `handleAutoTagTick` and `applyPrePasses`.
//
// Routing:
// 1. Most-specific leaf wins — note with `#llm/agents` (possibly
// alongside `#llm`) routes to leaf `llm/agents`.
// 2. Bare umbrella → DefaultChild — note with ONLY `#llm` routes to
// the umbrella's DefaultChild (e.g. `llm/agents`); the canonical
// rewrite replaces the umbrella tag with the leaf tag in body.
// 3. No managed tag — skip silently (not our note).
// 4. Multi-leaf tie of unrelated families — log WARN once per
// note-ID, skip (per user direction: do not guess).
//
// Idempotency: existing canonical notes detected by
// `equalIgnoringNewNoteLinkStrict` — zero bearcli writes when
// already canonical, ensuring `≤3-pass unchanged` convergence.
//
// Self-write safety: relies on the daemon's self-write gate
// around `handleAutoTagTick` plus `effectiveSelfWriteEpsilon`
// to suppress FSEvent feedback on our own writes.
//
// Returns the number of notes actually rewritten (zero when every
// candidate is already canonical or no notes match).
func ApplyDomainBootstrap(ctx context.Context, domainsByTag map[string]*Domain) (int, error) {
	if len(domainsByTag) == 0 {
		return 0, nil
	}
	if bootstrapLoop.disabledSnapshot() {
		return 0, nil
	}
	out, err := runBearcli(
		ctx,
		[]string{"list", "--location", "notes", flagFormat, formatJSON, flagFields, fieldsAutoTag},
		"",
	)
	if err != nil {
		return 0, fmt.Errorf("ApplyDomainBootstrap list: %w", err)
	}
	var notes []autoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return 0, fmt.Errorf("ApplyDomainBootstrap parse: %w", err)
	}
	rewritten := 0
	warned := make(map[string]struct{}) // : log-once-per-tick per note ID.
	for _, note := range notes {
		if err = CheckCtx(ctx); err != nil {
			return rewritten, err
		}
		if applyDomainBootstrapOne(ctx, note, domainsByTag, warned) {
			rewritten++
		}
	}
	if rewritten > 0 {
		log.Printf("domain-bootstrap: %d note(s) canonicalized", rewritten)
	}
	return rewritten, nil
}

// applyDomainBootstrapOne handles one note's match → guard → render →
// idempotency → overwrite chain. Returns true iff a bearcli overwrite
// fired. Extracted from `ApplyDomainBootstrap` to keep the parent loop
// under the gocognit ≤15 budget.
func applyDomainBootstrapOne(
	ctx context.Context,
	note autoTagNote,
	domainsByTag map[string]*Domain,
	warned map[string]struct{},
) bool {
	// Structural-note guard: titles prefixed with `✱ ` mark hub/master/
	// umbrella index notes (project convention). They're owned end-to-
	// end by the per-domain regen path (`WriteMasterHeader`, hub-tier
	// builders) — bootstrap pass MUST NOT stamp them as if they were
	// leaf atomics. Incident #3 (2026-05-17 18:32) had bootstrap mangle
	// `✱ Library`, `✱ LLM`, `✱ IT`, `✱ Quicknote` by prepending the
	// `DefaultChild` leaf canonical to their bodies (and triggering
	// Bear to auto-tag them with the leaf sub-tag).
	if strings.HasPrefix(note.Title, "✱ ") {
		return false
	}
	d := matchOwningDomain(note.Tags, domainsByTag, note.ID, warned)
	if d == nil {
		return false
	}
	// Defensive guard: `matchOwningDomain` MUST have resolved
	// umbrella → DefaultChild via `ResolveURLDomain`. This branch is
	// unreachable through that path today; it locks against future
	// refactors that might drop the resolution OR cases where
	// `domainsByTag` carries a bare umbrella without its child wiring.
	if d.SkipAtomicsPass {
		log.Printf("domain-bootstrap: BUG — umbrella %q leaked past matchOwningDomain "+
			"for note %q; skipping", d.Tag, note.ID)
		return false
	}
	// Loop-prevention guard: when the note's body ALREADY carries a
	// canonical tag-line for the leaf, drift fix-up (bucket shifts, URL
	// shape evolution) belongs to the per-domain `processAtomic` path —
	// NOT to the bootstrap pass. `RenderCanonicalForBootstrap` always
	// re-stamps with `d.UnknownBucket`, so rewriting a per-domain-bucketed
	// note here would ping-pong with the next per-domain regen tick.
	// Bug surfaced 2026-05-17: 19,040 rewrites of the same notes (Я: 2544,
	// В себя: 2208, …) across a 50 min window before the kill-switch
	// fired. The bootstrap pass MUST only stamp truly fresh notes.
	if hasCanonicalLineForLeaf(note.Content, d.Tag) {
		return false
	}
	// Defense-in-depth: even with the hasCanonicalLineForLeaf guard,
	// a future render bug could re-introduce a loop. The `bootstrapLoop`
	// tracker is a process-lifetime circuit-breaker that skips notes
	// the pass has already rewritten ≥ `bootstrapNoteRewriteCap` times
	// and emergency-disables the whole pass once
	// `bootstrapStuckEmergencyCap` distinct notes hit that cap.
	if bootstrapLoop.shouldSkipNote(note.ID) {
		return false
	}
	canonical := d.RenderCanonicalForBootstrap(note.Content)
	if equalIgnoringNewNoteLinkStrict(note.Content, canonical) {
		return false
	}
	if err := overwriteWithRetry(ctx, note.ID, canonical); err != nil {
		log.Printf("domain-bootstrap %q: %v", note.Title, err)
		return false
	}
	bootstrapLoop.recordRewrite(note.ID, note.Title)
	log.Printf("domain-bootstrap: %s → canonical %s", note.Title, d.Tag)
	return true
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
func (g *bootstrapLoopGuard) recordRewrite(noteID, title string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.counts[noteID]++
	if g.counts[noteID] < bootstrapNoteRewriteCap {
		return
	}
	if _, already := g.stuck[noteID]; !already {
		g.stuck[noteID] = struct{}{}
		log.Printf("domain-bootstrap: LOOP detected for note %q (%s); "+
			"rewrite_count=%d ≥ cap=%d; suppressing on future ticks",
			title, noteID, g.counts[noteID], bootstrapNoteRewriteCap)
	}
	if !g.disabled && len(g.stuck) >= bootstrapStuckEmergencyCap {
		g.disabled = true
		log.Printf(
			"domain-bootstrap: EMERGENCY DISABLE — %d distinct notes hit rewrite-loop cap; "+
				"pass disabled until daemon restart. Set REGEN_DOMAIN_BOOTSTRAP=off and "+
				"investigate render idempotency.",
			len(g.stuck),
		)
	}
}

// resetBootstrapLoopForTest zeroes the singleton guard. Test-only
// seam — production code never resets the counters (stuck notes stay
// suppressed for the daemon's lifetime by design).
func resetBootstrapLoopForTest() {
	bootstrapLoop.mu.Lock()
	defer bootstrapLoop.mu.Unlock()
	bootstrapLoop.counts = make(map[string]int)
	bootstrapLoop.stuck = make(map[string]struct{})
	bootstrapLoop.disabled = false
}

// ResetBootstrapLoopForTest exports the test-only reset seam to the
// integration tests in `tests/bear/engine`. Production callers MUST
// NOT use it.
func ResetBootstrapLoopForTest() { resetBootstrapLoopForTest() }

// hasCanonicalLineForLeaf reports whether `content` already carries a
// canonical tag-line for the given leaf `tag`. Accepts two shapes:
// - leaf form `#<tag> | …` (hub-routed, flat-list, flat-table)
// - sub-tag bucket form `#<tag>/<sub> | …` (grouped-vertical, where
// Bear materializes the bucket as a sibling sub-tag)
//
// Used by `applyDomainBootstrapOne` as the loop-prevention guard
// against `RenderCanonicalForBootstrap`'s `UnknownBucket` reset. The
// sub-tag form caught us on 2026-05-17 incident #2 — health-domain
// notes with bucket-as-subtag pattern (`#health/інше | …`) slipped
// past a strict `#health | ` prefix check and looped against per-
// domain regen until the circuit-breaker fired.
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
	noteTags []string,
	domainsByTag map[string]*Domain,
	noteID string,
	warned map[string]struct{},
) *Domain {
	var leaves []*Domain
	var umbrella *Domain
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
		return mostSpecificOrSkip(leaves, noteID, warned)
	}
}

// mostSpecificOrSkip ranks leaves by tag-string length (longest =
// most-specific). Ties (multiple distinct leaves with the same
// longest length) → log WARN once per note-ID and return nil. The
// `warned` set is allocated per-tick by `ApplyDomainBootstrap` so an
// ambiguous note re-warns on the next tick if it still carries
// ambiguous tags — the visibility we want for stuck-state debugging.
func mostSpecificOrSkip(leaves []*Domain, noteID string, warned map[string]struct{}) *Domain {
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
		log.Printf("domain-bootstrap: note %q has ambiguous managed tags %v — "+
			"skipping (resolve manually)", noteID, tags)
	}
	return nil
}

// matchDomainByTag returns the first candidate domain whose Tag
// appears in the note's tag set, or nil. Tags from bearcli carry
// the leading '#'; strip before comparison.
func matchDomainByTag(noteTags []string, candidates []*Domain) *Domain {
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

// autoTagNote is the bearcli JSON shape we need for the auto-tag pre-pass —
// a slim subset of bear.Note plus the `tags` array that the standard Note
// type doesn't carry.
type autoTagNote struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Content string   `json:"content"`
}

// refreshPlaceholderH1 rewrites the literal "# <placeholder>\n" H1
// line at the start of content to "# <stamp>\n". Returns
// (content, false) when the marker is absent — caller skips the
// bearcli write in that case. Body below H1 is preserved
// byte-for-byte. Generalizes the previous quicknote-specific
// refreshQuicknotePlaceholder by taking the placeholder string as
// a parameter so any opted-in domain can drive the refresh.
//
// Trailing `\n` on the marker is intentional: matched against full
// lines, not bare strings, so we don't accidentally match
// "# Quicknote " or "# Quicknote-X".
func refreshPlaceholderH1(content, placeholder, stamp string) (string, bool) {
	marker := "# " + placeholder + "\n"
	if !strings.HasPrefix(content, marker) {
		return content, false
	}
	return "# " + stamp + "\n" + strings.TrimPrefix(content, marker), true
}

// RefreshPlaceholderH1ForTest exposes refreshPlaceholderH1 to tests/bear.
func RefreshPlaceholderH1ForTest(content, placeholder, stamp string) (string, bool) {
	return refreshPlaceholderH1(content, placeholder, stamp)
}
