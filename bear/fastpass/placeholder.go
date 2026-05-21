package fastpass

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

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// ApplyPlaceholderRefresh scans notes whose H1 still carries the
// placeholder marker for any domain in the catalog and rewrites
// the H1 with a fresh datetime stamp. Self-cleaning: once H1 is
// rewritten, the marker is gone, so the next tick skips the same
// note. Returns the number of notes actually rewritten.
func ApplyPlaceholderRefresh(ctx context.Context, domainsByTag map[string]*domain.Domain) (int, error) {
	placeholderToDomains := buildPlaceholderIndex(domainsByTag)
	if len(placeholderToDomains) == 0 {
		return 0, nil
	}

	out, err := bearcli.Run(
		ctx,
		[]string{
			"list",
			"--sort", "created:desc",
			"--limit", "20",
			bearcli.FlagFormat, bearcli.FormatJSON,
			bearcli.FlagFields, bearcli.FieldsAutoTag,
		},
		"",
	)
	if err != nil {
		return 0, fmt.Errorf("ApplyPlaceholderRefresh list: %w", err)
	}
	var notes []domain.AutoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return 0, fmt.Errorf("ApplyPlaceholderRefresh parse: %w", err)
	}

	refreshed := 0
	stamp := domain.NowForNewNoteLink().Format(domain.H1DatetimeFormat)
	//nolint:dupl // mirrors sibling fastpass loop; shared scan pattern
	for _, note := range notes {
		if err = domain.CheckCtx(ctx); err != nil {
			return refreshed, err
		}
		if refreshOnePlaceholder(ctx, note, placeholderToDomains, stamp) {
			refreshed++
		}
	}
	return refreshed, nil
}

// buildPlaceholderIndex inverts domainsByTag into placeholder→[]*domain.Domain.
// Multiple domains may share the same placeholder (e.g. all default to
// domain.DefaultQuickPlaceholderH1); the per-note tag check in the loop
// disambiguates which one a given Title=="<placeholder>" note belongs to.
func buildPlaceholderIndex(domainsByTag map[string]*domain.Domain) map[string][]*domain.Domain {
	out := make(map[string][]*domain.Domain, len(domainsByTag))
	for _, d := range domainsByTag {
		if d == nil {
			continue
		}
		key := d.EffectiveQuickPlaceholderH1()
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
	note domain.AutoTagNote,
	placeholderToDomains map[string][]*domain.Domain,
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
	newContent, didRefresh := refreshPlaceholderH1(note.Content, d.EffectiveQuickPlaceholderH1(), stamp)
	if !didRefresh {
		return false
	}
	if err := bearcli.OverwriteWithRetry(ctx, note.ID, newContent); err != nil {
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
func ApplyQuicknotePlaceholderRefresh(ctx context.Context, dailyDomain *domain.Domain) (int, error) {
	if dailyDomain == nil {
		return 0, fmt.Errorf("ApplyQuicknotePlaceholderRefresh: dailyDomain is nil")
	}
	return ApplyPlaceholderRefresh(ctx, map[string]*domain.Domain{dailyDomain.Tag: dailyDomain})
}

// refreshPlaceholderH1 rewrites the literal "# <placeholder>\n" H1
// line at the start of content to "# <stamp>\n". Returns
// (content, false) when the marker is absent — caller skips the
// bearcli write in that case. Body below H1 is preserved
// byte-for-byte. Trailing `\n` on the marker is intentional:
// matched against full lines, not bare strings, so we don't
// accidentally match "# Quicknote " or "# Quicknote-X".
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
