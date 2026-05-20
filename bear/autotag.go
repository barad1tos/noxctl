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
