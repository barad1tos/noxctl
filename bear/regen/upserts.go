package regen

// Per-domain regen sub-steps: hub upserts, master upserts, and the
// atomics pass that walks every atom and rewrites its canonical
// line. Concentrates the I/O-heavy mutation paths so they sit next
// to each other and away from pure parsing/grouping helpers.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

type upsertOutcome int

const (
	upsertSkipped upsertOutcome = iota
	upsertCreated
	upsertChanged
	upsertUnchanged
)

func incrementOutcome(outcome upsertOutcome, created, changed, unchanged *int) {
	switch outcome {
	case upsertCreated:
		(*created)++
	case upsertChanged:
		(*changed)++
	case upsertUnchanged:
		(*unchanged)++
	}
}

func upsertHub(
	ctx context.Context,
	d *domain.Domain,
	bucket string,
	notes []domain.Note,
) (string, upsertOutcome, error) {
	if d.RenderHub == nil {
		return bucket + ": skipped (no Tier-2)", upsertSkipped, nil
	}
	hubTitle := d.HubTitle(bucket)
	hubID, err := findHubID(ctx, d, hubTitle)
	if err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertHub %q: %w", hubTitle, err)
	}

	if hubID == "" {
		// Fresh hub — no existing order, render alphabetical.
		newAuto := d.RenderHub(d, bucket, notes, nil)
		_, err = bearcli.Run(ctx,
			[]string{
				"create", hubTitle,
				bearcli.FlagFormat, bearcli.FormatJSON,
				bearcli.FlagFields, bearcli.FieldsIDTitle,
			},
			newAuto,
		)
		if err != nil {
			return "", upsertSkipped, fmt.Errorf("upsertHub %q create: %w", hubTitle, err)
		}
		return fmt.Sprintf("%s: created", hubTitle), upsertCreated, nil
	}

	out, err := bearcli.Run(ctx, []string{"cat", hubID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertHub %q cat: %w", hubTitle, err)
	}
	var existing domain.Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertHub %q parse: %w", hubTitle, err)
	}

	autoZone, manual := domain.SplitMarker(existing.Content)
	existingOrder := domain.ParseHubOrder(autoZone)
	newAuto := d.RenderHub(d, bucket, notes, existingOrder)

	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if domain.EqualIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		return fmt.Sprintf("%s: unchanged", hubTitle), upsertUnchanged, nil
	}
	if err = bearcli.OverwriteWithRetry(ctx, hubID, newBody); err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertHub %q write: %w", hubTitle, err)
	}
	return fmt.Sprintf("%s: updated", hubTitle), upsertChanged, nil
}

// upsertMasterIndex creates or updates the domain's master index note.
// Preserves the curator zone (below "## ✱ Куратор") on update. Returns a
// human-readable summary; an err signals the caller to aggregate failures.
func upsertMasterIndex(
	ctx context.Context,
	d *domain.Domain,
	groups map[string][]domain.Note,
) (string, upsertOutcome, error) {
	newAuto := d.RenderMaster(d, groups)
	idxID, err := FindIndexID(ctx, d)
	if err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertMasterIndex(%s): %w", d.IndexTitle, err)
	}

	if idxID == "" {
		_, err = bearcli.Run(ctx,
			[]string{
				"create", d.IndexTitle,
				bearcli.FlagFormat, bearcli.FormatJSON,
				bearcli.FlagFields, bearcli.FieldsIDTitle,
			},
			newAuto,
		)
		if err != nil {
			return "", upsertSkipped, fmt.Errorf("upsertMasterIndex(%s) create: %w", d.IndexTitle, err)
		}
		return "index: created", upsertCreated, nil
	}

	out, err := bearcli.Run(ctx, []string{"cat", idxID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertMasterIndex(%s) cat: %w", d.IndexTitle, err)
	}
	var existing domain.Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertMasterIndex(%s) parse: %w", d.IndexTitle, err)
	}

	_, manual := domain.SplitMarker(existing.Content)
	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if domain.EqualIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		return "index: unchanged", upsertUnchanged, nil
	}
	if err = bearcli.OverwriteWithRetry(ctx, idxID, newBody); err != nil {
		return "", upsertSkipped, fmt.Errorf("upsertMasterIndex(%s) write: %w", d.IndexTitle, err)
	}
	return "index: updated", upsertChanged, nil
}

func upsertAtomicBacklink(
	ctx context.Context,
	d *domain.Domain,
	noteID, noteTitle, bucket, content string,
) (string, error) {
	desired := domain.RenderAtomicCanonical(d, bucket, content)
	if domain.EqualIgnoringNewNoteLinkStrict(desired, content) {
		return "", nil
	}
	if err := bearcli.OverwriteWithRetry(ctx, noteID, desired); err != nil {
		return "", fmt.Errorf("upsertAtomicBacklink %q: %w", noteTitle, err)
	}
	return fmt.Sprintf("%s → restructured", noteTitle), nil
}

// atomicsPilotBucket returns the bucket filter for the atomics pass, or "" for
// "process all". Per-domain `REGEN_ATOMICS_PILOT_<TAG>` takes precedence over
// the global `REGEN_ATOMICS_PILOT`.
func atomicsPilotBucket(d *domain.Domain) string {
	if pilot := os.Getenv("REGEN_ATOMICS_PILOT_" + strings.ToUpper(d.TagSuffix())); pilot != "" {
		return pilot
	}
	return os.Getenv("REGEN_ATOMICS_PILOT")
}

// ProcessAtomicForTest exposes processAtomic for external tests.
//
// Tag-membership guard (canonical-pingpong fix, 2026-05-14): a domain
// refuses to canonicalize an atom whose current Tags array does not
// contain d.CanonicalTag. bearcli returns tags with leading `#` (e.g.
// "#quicknote/daily"), so we compare against d.CanonicalTag, not d.Tag.
// Without this, drag-to-tag in Bear can leave transient tag-index
// residue that lets a non-owning domain (e.g. quicknote/daily) stamp a
// note that already belongs to development/noxctl, flipping the
// canonical body to the wrong domain across multiple FSEvent bursts.
func ProcessAtomicForTest(
	ctx context.Context,
	d *domain.Domain,
	n domain.Note,
	bucket string,
) (touched, failed int) {
	return processAtomic(ctx, d, n, bucket)
}

// processAtomic upserts one atomic note's canonical header and logs the
// outcome. Returns 1/0 in (touched, failed) so the caller can sum.
func processAtomic(ctx context.Context, d *domain.Domain, n domain.Note, bucket string) (touched, failed int) {
	if !slices.Contains(n.Tags, d.CanonicalTag) {
		return 0, 0
	}
	result, err := upsertAtomicBacklink(ctx, d, n.ID, n.Title, bucket, n.Content)
	if err != nil {
		d.Logf("atomic %q: ERROR: %v", n.Title, err)
		return 0, 1
	}
	if result != "" {
		d.Logf("atomic %s", result)
		return 1, 0
	}
	return 0, 0
}

// runAtomicsPass rewrites each atomic note's header to canonical shape.
// Honors REGEN_ATOMICS_PILOT=<bucket> (or REGEN_ATOMICS_PILOT_<TAG>=<bucket>
// for per-domain limited-scope runs). Returns counts of touched/failed atomics
// so Run can summarize the cycle.
func runAtomicsPass(
	ctx context.Context,
	d *domain.Domain,
	groups map[string][]domain.Note,
) (touched, failed int) {
	pilot := atomicsPilotBucket(d)
	for bucket, items := range groups {
		if pilot != "" && bucket != pilot {
			continue
		}
		for _, note := range items {
			if domain.CheckCtx(ctx) != nil {
				return
			}
			passTouched, passFailed := processAtomic(ctx, d, note, bucket)
			touched += passTouched
			failed += passFailed
		}
	}
	if pilot != "" {
		d.Logf("atomics pilot mode (bucket=%q), %d touched, %d failed", pilot, touched, failed)
	} else if touched > 0 || failed > 0 {
		d.Logf("atomics: %d touched, %d failed", touched, failed)
	}
	return touched, failed
}

// runHubsPass upserts each per-bucket Tier-2 Hub note. No-op for domains
// without Tier-2 hubs (d.RenderHub == nil). Returns hub outcome counts.
func runHubsPass(
	ctx context.Context,
	d *domain.Domain,
	groups map[string][]domain.Note,
) (created, changed, unchanged, failed int) {
	if d.RenderHub == nil {
		return 0, 0, 0, 0
	}
	for bucket, items := range groups {
		summary, outcome, err := upsertHub(ctx, d, bucket, items)
		if err != nil {
			d.Logf("ERROR: %v", err)
			failed++
			continue
		}
		incrementOutcome(outcome, &created, &changed, &unchanged)
		d.Logf("%s", summary)
	}
	return created, changed, unchanged, failed
}
