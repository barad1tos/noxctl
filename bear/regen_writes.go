package bear

// Per-domain regen sub-steps: hub upserts, master upserts, and the
// atomics pass that walks every atom and rewrites its canonical
// line. Split from core.go so the I/O-heavy mutation paths sit
// next to each other and away from pure parsing/grouping helpers.

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
)

func (d *Domain) upsertHub(ctx context.Context, bucket string, notes []Note) (string, error) {
	if d.RenderHub == nil {
		return bucket + ": skipped (no Tier-2)", nil
	}
	hubTitle := d.hubTitleFor(bucket)
	hubID, err := d.findHubID(ctx, hubTitle)
	if err != nil {
		return "", fmt.Errorf("upsertHub %q: %w", hubTitle, err)
	}

	if hubID == "" {
		// Fresh hub — no existing order, render alphabetical.
		newAuto := d.RenderHub(d, bucket, notes, nil)
		_, err = runBearcli(ctx,
			[]string{"create", hubTitle, flagFormat, formatJSON, flagFields, fieldsIDTitle},
			newAuto,
		)
		if err != nil {
			return "", fmt.Errorf("upsertHub %q create: %w", hubTitle, err)
		}
		return fmt.Sprintf("%s: created", hubTitle), nil
	}

	out, err := runBearcli(ctx, []string{"cat", hubID, flagFormat, formatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("upsertHub %q cat: %w", hubTitle, err)
	}
	var existing Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", fmt.Errorf("upsertHub %q parse: %w", hubTitle, err)
	}

	autoZone, manual := SplitMarker(existing.Content)
	existingOrder := parseHubOrder(autoZone)
	newAuto := d.RenderHub(d, bucket, notes, existingOrder)

	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if equalIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		return fmt.Sprintf("%s: unchanged", hubTitle), nil
	}
	if err = overwriteWithRetry(ctx, hubID, newBody); err != nil {
		return "", fmt.Errorf("upsertHub %q write: %w", hubTitle, err)
	}
	return fmt.Sprintf("%s: updated", hubTitle), nil
}

// upsertMasterIndex creates or updates the domain's master index note.
// Preserves the curator zone (below "## ✱ Куратор") on update. Returns a
// human-readable summary; an err signals the caller to aggregate failures.
func (d *Domain) upsertMasterIndex(ctx context.Context, groups map[string][]Note) (string, error) {
	newAuto := d.RenderMaster(d, groups)
	idxID, err := d.findIndexID(ctx)
	if err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s): %w", d.IndexTitle, err)
	}

	if idxID == "" {
		_, err = runBearcli(ctx,
			[]string{"create", d.IndexTitle, flagFormat, formatJSON, flagFields, fieldsIDTitle},
			newAuto,
		)
		if err != nil {
			return "", fmt.Errorf("upsertMasterIndex(%s) create: %w", d.IndexTitle, err)
		}
		return "index: created", nil
	}

	out, err := runBearcli(ctx, []string{"cat", idxID, flagFormat, formatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s) cat: %w", d.IndexTitle, err)
	}
	var existing Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s) parse: %w", d.IndexTitle, err)
	}

	_, manual := SplitMarker(existing.Content)
	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if equalIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		return "index: unchanged", nil
	}
	if err = overwriteWithRetry(ctx, idxID, newBody); err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s) write: %w", d.IndexTitle, err)
	}
	return "index: updated", nil
}

// atomicsPilotBucket returns the bucket filter for the atomics pass, or "" for
// "process all". Per-domain `REGEN_ATOMICS_PILOT_<TAG>` takes precedence over
// the global `REGEN_ATOMICS_PILOT`.
func (d *Domain) atomicsPilotBucket() string {
	if pilot := os.Getenv("REGEN_ATOMICS_PILOT_" + strings.ToUpper(d.tagSuffix())); pilot != "" {
		return pilot
	}
	return os.Getenv("REGEN_ATOMICS_PILOT")
}

// processAtomic upserts one atomic note's canonical header and logs the
// outcome. Returns 1/0 in (touched, failed) so the caller can sum.
//
// Tag-membership guard (canonical-pingpong fix, 2026-05-14): a domain
// refuses to canonicalize an atom whose current Tags array does not
// contain d.CanonicalTag. bearcli returns tags with leading `#` (e.g.
// "#quicknote/daily"), so we compare against d.CanonicalTag, not d.Tag.
// Without this, drag-to-tag in Bear can leave transient tag-index
// residue that lets a non-owning domain (e.g. quicknote/daily) stamp a
// note that already belongs to development/noxctl, flipping the
// canonical body to the wrong domain across multiple FSEvent bursts.
func (d *Domain) processAtomic(ctx context.Context, n Note, bucket string) (touched, failed int) {
	if !slices.Contains(n.Tags, d.CanonicalTag) {
		return 0, 0
	}
	result, err := d.upsertAtomicBacklink(ctx, n.ID, n.Title, bucket, n.Content)
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

// ProcessAtomicForTest exposes processAtomic for external tests in tests/bear/.
// Test seam — production callers MUST use RunRegen. Same precedent as
// ComputeContentHash on bear/engine/apply.go.
func (d *Domain) ProcessAtomicForTest(ctx context.Context, n Note, bucket string) (touched, failed int) {
	return d.processAtomic(ctx, n, bucket)
}

// runAtomicsPass rewrites each atomic note's header to canonical shape.
// Honors REGEN_ATOMICS_PILOT=<bucket> (or REGEN_ATOMICS_PILOT_<TAG>=<bucket>
// for per-domain limited-scope runs). Returns counts of touched/failed atomics
// so RunRegen can summarize the cycle.
func (d *Domain) runAtomicsPass(ctx context.Context, groups map[string][]Note) (touched, failed int) {
	pilot := d.atomicsPilotBucket()
	for bucket, items := range groups {
		if pilot != "" && bucket != pilot {
			continue
		}
		for _, note := range items {
			if CheckCtx(ctx) != nil {
				return
			}
			passTouched, passFailed := d.processAtomic(ctx, note, bucket)
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
// without Tier-2 hubs (d.RenderHub == nil). Returns count of failed hubs.
func (d *Domain) runHubsPass(ctx context.Context, groups map[string][]Note) (failed int) {
	if d.RenderHub == nil {
		return 0
	}
	for bucket, items := range groups {
		summary, err := d.upsertHub(ctx, bucket, items)
		if err != nil {
			d.Logf("ERROR: %v", err)
			failed++
			continue
		}
		d.Logf("%s", summary)
	}
	return failed
}
