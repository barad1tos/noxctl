package regen

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// FetchMasterContent returns the current body of d's master note,
// pre-stripped of the trailing [Нова нотатка] new-note-link, so the
// returned bytes are idempotency-stable for content hashing. Returns
// ("", err) on bearcli failure or when the master note doesn't
// exist yet (engine.Apply caller treats this as "skip the hash
// update for this domain", preserving last-known-good).
func FetchMasterContent(ctx context.Context, d *domain.Domain) (string, error) {
	idxID, err := FindIndexID(ctx, d)
	if err != nil {
		return "", fmt.Errorf("FetchMasterContent(%s) findIndex: %w", d.Tag, err)
	}
	if idxID == "" {
		return "", nil
	}
	out, err := bearcli.Run(ctx, []string{"cat", idxID, bearcli.FlagFormat, bearcli.FormatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("FetchMasterContent(%s) cat: %w", d.Tag, err)
	}
	var n domain.Note
	if err = json.Unmarshal(out, &n); err != nil {
		return "", fmt.Errorf("FetchMasterContent(%s) parse: %w", d.Tag, err)
	}
	return domain.StripNewNoteURLsFromBody(n.Content), nil
}

// FetchHubContents returns title→stripped-body for every Tier-2 hub
// owned by d. The returned map is keyed by hub note title (which is
// unique-by-domain by daemon construction). Uses listNotes + isHubNote
// + the same StripNewNoteURLsFromBody treatment as the master.
// Returns an empty map (no error) for domains without a Tier-2 hub
// layer (flat-list, grouped-vertical).
func FetchHubContents(ctx context.Context, d *domain.Domain) (map[string]string, error) {
	notes, err := listNotes(ctx, d)
	if err != nil {
		return nil, fmt.Errorf("FetchHubContents(%s) list: %w", d.Tag, err)
	}
	hubs := make(map[string]string)
	for _, n := range notes {
		if !d.IsManagedHubNote(n) {
			continue
		}
		hubs[n.Title] = domain.StripNewNoteURLsFromBody(n.Content)
	}
	return hubs, nil
}

// RenderInputs bundles the read-only inputs the engine needs to
// render a domain's master + hubs without writing. Returned by
// SnapshotDomainRenderInputs as a single value to keep the engine
// public-API surface narrow — facade pattern over bulk-exporting
// listNotes/computeMasterOverrides/computeHubOverrides/computeTagOverrides/groupAtomics.
//
//nolint:revive // public API surface; rename is breaking change for callers
type RenderInputs struct {
	Notes  []domain.Note            // every atom + hub + master under d.Tag
	Groups map[string][]domain.Note // bucket → atoms, post-override-merge
}

// SnapshotDomainRenderInputs fetches d's note list and computes the
// post-override grouping that d.RenderMaster expects as input. Pure
// read — calls bearcli list once, then runs the in-process
// override+grouping pipeline. Never writes.
//
// merge order matches Run: master > hub > tag, first claimant wins
// (see domain.RouteAtomics for the byte-equivalent invariant). Plan engine
// (bear/engine/plan.go) calls this and feeds.Groups straight into
// d.RenderMaster(d, groups) — same call shape as Apply.
//
// Returns Groups as an initialized empty map (not nil) when d has zero
// atoms; downstream consumers can range over the result safely without
// nil-checks. Errors propagate from listNotes verbatim.
func SnapshotDomainRenderInputs(ctx context.Context, d *domain.Domain) (RenderInputs, error) {
	notes, err := listNotes(ctx, d)
	if err != nil {
		return RenderInputs{}, fmt.Errorf("SnapshotDomainRenderInputs(%s): %w", d.Tag, err)
	}
	// RouteAtomics keeps plan and apply byte-equivalent. The only WARN we
	// suppress here is the higher-layer suppression notice (via nil onSkip).
	// Inner whitelist failures and tag conflicts still surface through d.Logf:
	// they represent configuration drift the planner needs to see; rebucket
	// counts surface through the plan-diff renderer instead.
	routing := d.RouteAtomics(notes, nil)
	if routing.TagConflicts > 0 {
		d.Logf("tag conflicts: %d (no override applied)", routing.TagConflicts)
	}
	return RenderInputs{Notes: notes, Groups: routing.Groups}, nil
}
