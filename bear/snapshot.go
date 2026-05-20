package bear

import (
	"context"
	"encoding/json"
	"fmt"
)

// FetchMasterContent returns the current body of d's master note,
// pre-stripped of the trailing [Нова нотатка] new-note-link, so the
// returned bytes are idempotency-stable for content hashing. Returns
// ("", err) on bearcli failure or when the master note doesn't
// exist yet (engine.Apply caller treats this as "skip the hash
// update for this domain", preserving last-known-good).
func FetchMasterContent(ctx context.Context, d *Domain) (string, error) {
	idxID, err := d.findIndexID(ctx)
	if err != nil {
		return "", fmt.Errorf("FetchMasterContent(%s) findIndex: %w", d.Tag, err)
	}
	out, err := runBearcli(ctx, []string{"cat", idxID, flagFormat, formatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("FetchMasterContent(%s) cat: %w", d.Tag, err)
	}
	var n Note
	if err = json.Unmarshal(out, &n); err != nil {
		return "", fmt.Errorf("FetchMasterContent(%s) parse: %w", d.Tag, err)
	}
	return StripNewNoteURLsFromBody(n.Content), nil
}

// FetchHubContents returns title→stripped-body for every Tier-2 hub
// owned by d. The returned map is keyed by hub note title (which is
// unique-by-domain by daemon construction). Uses listNotes + isHubNote
// + the same StripNewNoteURLsFromBody treatment as the master.
// Returns an empty map (no error) for domains without a Tier-2 hub
// layer (flat-list, flat-table, grouped-vertical).
func FetchHubContents(ctx context.Context, d *Domain) (map[string]string, error) {
	notes, err := d.listNotes(ctx)
	if err != nil {
		return nil, fmt.Errorf("FetchHubContents(%s) list: %w", d.Tag, err)
	}
	hubs := make(map[string]string)
	for _, n := range notes {
		if !d.isHubNote(n) {
			continue
		}
		hubs[n.Title] = StripNewNoteURLsFromBody(n.Content)
	}
	return hubs, nil
}

// DomainRenderInputs bundles the read-only inputs the engine needs to
// render a domain's master + hubs without writing. Returned by
// SnapshotDomainRenderInputs as a single value to keep the engine
// public-API surface narrow — facade pattern over bulk-exporting
// listNotes/computeMasterOverrides/computeHubOverrides/groupAtomics.
type DomainRenderInputs struct {
	Notes  []Note            // every atom + hub + master under d.Tag
	Groups map[string][]Note // bucket → atoms, post-override-merge
}

// SnapshotDomainRenderInputs fetches d's note list and computes the
// post-override grouping that d.RenderMaster expects as input. Pure
// read — calls bearcli list once, then runs the in-process
// override+grouping pipeline. Never writes.
//
// The merge order matches engine.Apply's RunRegen (per_domain_regen.go):
// master overrides override hub overrides on collision. Plan engine
// (bear/engine/plan.go) calls this and feeds.Groups straight into
// d.RenderMaster(d, groups) — same call shape as Apply.
//
// Returns Groups as an initialized empty map (not nil) when d has zero
// atoms; downstream consumers can range over the result safely without
// nil-checks. Errors propagate from listNotes verbatim.
func SnapshotDomainRenderInputs(ctx context.Context, d *Domain) (DomainRenderInputs, error) {
	notes, err := d.listNotes(ctx)
	if err != nil {
		return DomainRenderInputs{}, fmt.Errorf("SnapshotDomainRenderInputs(%s): %w", d.Tag, err)
	}
	masterOverrides := d.computeMasterOverrides(notes)
	hubOverrides := d.computeHubOverrides(notes)
	// Master wins on collision — exact mirror of per_domain_regen.go
	// "master overrides win on collision". computeMasterOverrides may
	// return nil when ParseMasterTable is unset; lazily initialize before
	// merging so we never write into a nil map.
	for atomID, bucket := range hubOverrides {
		if _, alreadySet := masterOverrides[atomID]; alreadySet {
			continue
		}
		if masterOverrides == nil {
			masterOverrides = make(map[string]string)
		}
		masterOverrides[atomID] = bucket
	}
	groups := d.groupAtomics(notes, masterOverrides)
	if groups == nil {
		groups = map[string][]Note{}
	}
	return DomainRenderInputs{Notes: notes, Groups: groups}, nil
}
