// Package fastpass hosts the fast-pass canonicalization pipeline that the
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
package fastpass

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
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
// (domain.RenderCanonicalForBootstrap re-classifies preamble as body).
//
// Failures per note are logged and skipped; the pass continues so one
// flaky note can't stall the rest of the regen pipeline.
//
// Returns the number of notes actually stamped (zero when every note is
// already tagged). Callers use this to decide whether the pre-pass
// produced bearcli writes that need to be self-gated downstream by
// the daemon's self-write epsilon.
func ApplyDailyDefaultTag(ctx context.Context, dailyDomain *domain.Domain) (int, error) {
	if dailyDomain == nil {
		return 0, fmt.Errorf("ApplyDailyDefaultTag: dailyDomain is nil")
	}
	out, err := bearcli.Run(
		ctx,
		[]string{"list", "--location", "notes", bearcli.FlagFormat, bearcli.FormatJSON, bearcli.FlagFields, bearcli.FieldsAutoTag}, //nolint:lll
		"",
	)
	if err != nil {
		return 0, fmt.Errorf("ApplyDailyDefaultTag list: %w", err)
	}
	var notes []domain.AutoTagNote
	if err = json.Unmarshal(out, &notes); err != nil {
		return 0, fmt.Errorf("ApplyDailyDefaultTag parse: %w", err)
	}
	stamped := 0
	for _, note := range notes {
		if err = domain.CheckCtx(ctx); err != nil {
			return stamped, err
		}
		if len(note.Tags) > 0 {
			continue
		}
		newContent := dailyDomain.RenderCanonicalForBootstrap(note.Content)
		if err = bearcli.OverwriteWithRetry(ctx, note.ID, newContent); err != nil {
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
