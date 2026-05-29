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
	case upsertSkipped:
		return
	case upsertCreated:
		*created++
	case upsertChanged:
		*changed++
	case upsertUnchanged:
		*unchanged++
	}
}

// upsertResult bundles one hub/master upsert's reportable outcome with the
// stripped body that feeds the per-domain content hash (D-02). Body is "" for
// the skipped (no-Tier-2) and create branches: a freshly-created note carries
// the rendered body, but D-02 only needs hash input for the steady-state path
// (the create cycle reports `created`, never `unchanged`, so the hash is
// recomputed next cycle from the persisted note anyway). Surfacing it keeps the
// snapshot complete without an extra read on create.
type upsertResult struct {
	Summary string
	Title   string // hub note title (or master IndexTitle); key for Snapshot.Hubs
	Body    string // stripped body for content hashing; "" when not produced
	Outcome upsertOutcome
}

func upsertHub(
	ctx context.Context,
	d *domain.Domain,
	idx noteIndex,
	bucket string,
	notes []domain.Note,
) (upsertResult, error) {
	if d.RenderHub == nil {
		return upsertResult{Summary: bucket + ": skipped (no Tier-2)", Outcome: upsertSkipped}, nil
	}
	hubTitle := d.HubTitle(bucket)
	// Index lookup replaces the per-bucket findHubID list. Parity with
	// findNoteByTitle: "" on miss, first-match-wins (see note_index.go).
	hubID := idx.lookup(hubTitle)

	if hubID == "" {
		// Fresh hub — no existing order, render alphabetical.
		newAuto := d.RenderHub(d, bucket, notes, nil)
		out, err := bearcli.Run(ctx,
			[]string{
				"create", hubTitle,
				bearcli.FlagFormat, bearcli.FormatJSON,
				bearcli.FlagFields, bearcli.FieldsIDTitle,
			},
			newAuto,
		)
		if err != nil {
			return upsertResult{}, fmt.Errorf("upsertHub %q create: %w", hubTitle, err)
		}
		// Patch the index so a master/subsequent lookup for this title
		// resolves the freshly-created ID without a re-list.
		patchIndexFromCreate(idx, hubTitle, out)
		return upsertResult{
			Summary: fmt.Sprintf("%s: created", hubTitle),
			Title:   hubTitle,
			Body:    domain.StripNewNoteURLsFromBody(newAuto),
			Outcome: upsertCreated,
		}, nil
	}

	out, err := bearcli.Run(ctx, []string{"cat", hubID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return upsertResult{}, fmt.Errorf("upsertHub %q cat: %w", hubTitle, err)
	}
	var existing domain.Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return upsertResult{}, fmt.Errorf("upsertHub %q parse: %w", hubTitle, err)
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
		// Unchanged: reuse the body already fetched by this diff-check cat —
		// NO extra read. Stripped to match the post-write read-back below and
		// FetchHubContents, so the hash input is byte-identical on no-op cycles.
		return upsertResult{
			Summary: fmt.Sprintf("%s: unchanged", hubTitle),
			Title:   hubTitle,
			Body:    domain.StripNewNoteURLsFromBody(existing.Content),
			Outcome: upsertUnchanged,
		}, nil
	}
	if err = bearcli.OverwriteWithRetry(ctx, hubID, newBody); err != nil {
		return upsertResult{}, fmt.Errorf("upsertHub %q write: %w", hubTitle, err)
	}
	// Changed: hash the STORED form, not the rendered newBody. Bear can
	// normalize markdown on overwrite (whitespace, link shape); hashing the
	// in-memory rendered bytes would diverge from next cycle's diff-check
	// read-back and flip the domain to "changed" forever, breaking the <=3-pass
	// idempotency contract (Pitfall 1 / T-14-05). One deliberate read-back.
	stored, readErr := readBackStripped(ctx, hubID)
	if readErr != nil {
		return upsertResult{}, fmt.Errorf("upsertHub %q read-back: %w", hubTitle, readErr)
	}
	return upsertResult{
		Summary: fmt.Sprintf("%s: updated", hubTitle),
		Title:   hubTitle,
		Body:    stored,
		Outcome: upsertChanged,
	}, nil
}

// readBackStripped re-reads a just-written note and returns its stored body
// stripped of new-note URLs — the canonical hash input on the changed branch.
// A deliberate read (one cat) per overwritten note: it captures Bear's
// stored-normalized markdown so the next cycle's diff-check sees no drift.
func readBackStripped(ctx context.Context, noteID string) (string, error) {
	out, err := bearcli.Run(ctx, []string{"cat", noteID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("read-back cat: %w", err)
	}
	var n domain.Note
	if err = json.Unmarshal(out, &n); err != nil {
		return "", fmt.Errorf("read-back parse: %w", err)
	}
	return domain.StripNewNoteURLsFromBody(n.Content), nil
}

// upsertMasterIndex creates or updates the domain's master index note.
// Preserves the curator zone (below "## ✱ Куратор") on update. Returns a
// human-readable summary; an err signals the caller to aggregate failures.
func upsertMasterIndex(
	ctx context.Context,
	d *domain.Domain,
	idx noteIndex,
	groups map[string][]domain.Note,
) (upsertResult, error) {
	newAuto := d.RenderMaster(d, groups)
	// Index lookup replaces the master FindIndexID list. The exported
	// FindIndexID stays for external callers (fast-pass, snapshot) — only
	// this internal regen lookup path is index-backed.
	idxID := idx.lookup(d.IndexTitle)

	if idxID == "" {
		out, err := bearcli.Run(ctx,
			[]string{
				"create", d.IndexTitle,
				bearcli.FlagFormat, bearcli.FormatJSON,
				bearcli.FlagFields, bearcli.FieldsIDTitle,
			},
			newAuto,
		)
		if err != nil {
			return upsertResult{}, fmt.Errorf("upsertMasterIndex(%s) create: %w", d.IndexTitle, err)
		}
		patchIndexFromCreate(idx, d.IndexTitle, out)
		return upsertResult{
			Summary: "index: created",
			Title:   d.IndexTitle,
			Body:    domain.StripNewNoteURLsFromBody(newAuto),
			Outcome: upsertCreated,
		}, nil
	}

	out, err := bearcli.Run(ctx, []string{"cat", idxID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return upsertResult{}, fmt.Errorf("upsertMasterIndex(%s) cat: %w", d.IndexTitle, err)
	}
	var existing domain.Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return upsertResult{}, fmt.Errorf("upsertMasterIndex(%s) parse: %w", d.IndexTitle, err)
	}

	_, manual := domain.SplitMarker(existing.Content)
	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if domain.EqualIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		// Unchanged: reuse the diff-check cat's body — no extra read. Same
		// strip treatment as the changed branch and FetchMasterContent.
		return upsertResult{
			Summary: "index: unchanged",
			Title:   d.IndexTitle,
			Body:    domain.StripNewNoteURLsFromBody(existing.Content),
			Outcome: upsertUnchanged,
		}, nil
	}
	if err = bearcli.OverwriteWithRetry(ctx, idxID, newBody); err != nil {
		return upsertResult{}, fmt.Errorf("upsertMasterIndex(%s) write: %w", d.IndexTitle, err)
	}
	// Changed: read back the STORED master body for hashing (see upsertHub's
	// read-back rationale — Bear's normalized form, not the rendered bytes).
	stored, readErr := readBackStripped(ctx, idxID)
	if readErr != nil {
		return upsertResult{}, fmt.Errorf("upsertMasterIndex(%s) read-back: %w", d.IndexTitle, readErr)
	}
	return upsertResult{
		Summary: "index: updated",
		Title:   d.IndexTitle,
		Body:    stored,
		Outcome: upsertChanged,
	}, nil
}

// patchIndexFromCreate records a freshly-created note's ID into the index so a
// later same-cycle lookup (e.g. the master after its hubs were just created)
// resolves it WITHOUT a re-list. The create call requested --fields id,title,
// so the returned JSON carries the new ID. A parse miss or empty ID is
// non-fatal: the create already succeeded; the next regen rebuilds the index
// from a fresh listNotes, so a missed patch only forgoes the in-cycle reuse,
// never corrupts state.
func patchIndexFromCreate(idx noteIndex, title string, createOut []byte) {
	var created domain.Note
	if err := json.Unmarshal(createOut, &created); err != nil {
		return
	}
	if created.ID == "" {
		return
	}
	idx.patchCreated(title, created.ID)
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

// hubsPassResult bundles the hub outcome counts with the per-hub stripped
// bodies (title -> body) the content-hash pass reuses (D-02). Hubs is nil for
// domains without a Tier-2 layer (d.RenderHub == nil).
type hubsPassResult struct {
	Created   int
	Changed   int
	Unchanged int
	Failed    int
	Hubs      map[string]string
}

// runHubsPass upserts each per-bucket Tier-2 Hub note. No-op for domains
// without Tier-2 hubs (d.RenderHub == nil). Returns hub outcome counts plus the
// stripped hub bodies for the per-domain content hash.
func runHubsPass(
	ctx context.Context,
	d *domain.Domain,
	idx noteIndex,
	groups map[string][]domain.Note,
) hubsPassResult {
	if d.RenderHub == nil {
		return hubsPassResult{}
	}
	res := hubsPassResult{Hubs: make(map[string]string, len(groups))}
	for bucket, items := range groups {
		hub, err := upsertHub(ctx, d, idx, bucket, items)
		if err != nil {
			d.Logf("ERROR: %v", err)
			res.Failed++
			continue
		}
		incrementOutcome(hub.Outcome, &res.Created, &res.Changed, &res.Unchanged)
		if hub.Title != "" {
			res.Hubs[hub.Title] = hub.Body
		}
		d.Logf("%s", hub.Summary)
	}
	return res
}
