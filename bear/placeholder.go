package bear

// Placeholder-refresh fast-pass: scans notes whose H1 still carries the
// Bear-stamped `# Quicknote` placeholder (or domain-specific override)
// and rewrites it with a fresh datetime. Two-shaped: per-domain via
// ApplyPlaceholderRefresh (looks up the catalog by tag), single-domain
// via ApplyQuicknotePlaceholderRefresh (used by Daily bootstrap path).

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
)

// ApplyPlaceholderRefresh scans notes whose H1 still carries the
// placeholder marker for any domain in the catalog and rewrites
// the H1 with a fresh datetime stamp. Self-cleaning: once H1 is
// rewritten, the marker is gone, so the next tick skips the same
// note. Returns the number of notes actually rewritten.
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
