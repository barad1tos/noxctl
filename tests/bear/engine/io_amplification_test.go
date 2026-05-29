// Package engine_test pins the read-amplification contract of the no-op regen
// pass. D-01 (plan 14-01) replaces the per-bucket findNoteByTitle scan (each of
// which issued its own `bearcli list --fields id,title`) in the hub/master
// upsert path with a single goroutine-local note index built from the initial
// listNotes result. The assertion below is the independently-verifiable proof
// of that change: a no-op regen of a hub-routed domain with B buckets must
// issue EXACTLY ONE `list` for its tag (the initial listNotes) — pre-phase it
// issued B+2 (initial list + one per-bucket hub list + one master list).
//
// The probe drives regen.Run directly through the shared testutil recording
// backend (mirroring tests/bear/hub_upsert_test.go, which also drives
// regen.Run with a fake bearcli.Backend). The engine.Apply hash-snapshot pass
// (computeDomainHash -> FetchMasterContent/FetchHubContents) issues its OWN
// reads and is a SEPARATE amplification surface that plan 14-02 (D-02)
// eliminates by reusing regen.Result.Snapshot — so SC-1 ("a no-op REGEN issues
// exactly one list") is pinned at the regen layer here, where D-01 actually
// makes the fix.
package engine_test

import (
	"context"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/regen"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// noOpHubRoutedDomain builds a hub-routed domain in the steady (no-op) state:
// SkipAtomicsPass keeps the atomics pass out of the picture so the only Bear
// reads come from listNotes + the hub/master ID lookups under observation.
func noOpHubRoutedDomain() *domain.Domain {
	d := render.NewHubRoutedDomain(
		"library/poetry",
		"Poetry Index",
		"Unknown",
		"Poems",
		render.DefaultRenderMaster3Tier,
	)
	d.SkipAtomicsPass = true
	return d
}

// noOpHubRoutedCorpus renders the hub + master bodies via the domain's OWN
// renderers, then returns a corpus where the existing hub/master notes already
// carry those exact bodies. That guarantees a no-op cycle: upsertHub and
// upsertMasterIndex find the rendered body byte-equal to the existing content
// and report `unchanged` — no create, no overwrite. The atom drives a single
// bucket ("Biko") so B == 1: pre-phase that is B+2 == 3 list calls.
func noOpHubRoutedCorpus(d *domain.Domain) map[string][]domain.Note {
	atom := domain.Note{
		ID:      "atom-1",
		Title:   "Poem One",
		Content: "# Poem One\n#library/poetry | [[Biko]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
	groups := map[string][]domain.Note{"Biko": {atom}}

	hubBody := d.RenderHub(d, "Biko", groups["Biko"], nil)
	masterBody := d.RenderMaster(d, groups)

	corpus := []domain.Note{
		atom,
		{ID: "hub-biko", Title: d.HubTitle("Biko"), Content: hubBody, Tags: []string{"#library/poetry"}},
		{ID: "master", Title: d.IndexTitle, Content: masterBody, Tags: []string{"#library/poetry"}},
	}
	return map[string][]domain.Note{d.Tag: corpus}
}

// TestApply_NoOpCycle_ZeroPerBucketList proves a no-op regen of a hub-routed
// domain issues exactly ONE `list` for its tag (the initial listNotes) and
// zero per-bucket hub/master ID lists. It also pins the orthogonal contracts:
// the cycle is genuinely no-op (created == changed == 0), and the per-bucket
// `cat` reads (B hubs + 1 master) are untouched — D-01 removes lists, not cats.
func TestApply_NoOpCycle_ZeroPerBucketList(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	backend := testutil.NewRecordingBackend(noOpHubRoutedCorpus(d))
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	result := regen.Run(ctx, d)

	if got := backend.CountKind("list", d.Tag); got != 1 {
		t.Errorf("no-op hub pass issued %d `list` calls for tag %q, want exactly 1 (initial listNotes only)",
			got, d.Tag)
	}
	// B == 1 bucket -> B hub cats + 1 master cat == 2 cats. Cats are
	// tag-less (id-addressed), so they record under the empty-tag bucket.
	if got := backend.CountKind("cat", ""); got != 2 {
		t.Errorf("no-op hub pass issued %d `cat` calls, want 2 (1 hub + 1 master); D-01 removes lists, not cats", got)
	}
	if result.HubsCreated != 0 || result.MasterCreated != 0 {
		t.Errorf("no-op cycle created notes: hubsCreated=%d masterCreated=%d, want 0/0",
			result.HubsCreated, result.MasterCreated)
	}
	if result.Changed() != 0 {
		t.Errorf("no-op cycle reported Changed=%d, want 0 (Bear output unchanged)", result.Changed())
	}
	if result.HubsUnchanged != 1 || result.MasterUnchanged != 1 {
		t.Errorf("no-op cycle = hubsUnchanged:%d masterUnchanged:%d, want 1/1",
			result.HubsUnchanged, result.MasterUnchanged)
	}
}

// TestApply_HubCreate_PatchesIndexNoRelist proves a freshly-created hub/master
// is resolved later in the same cycle via the patched index — the create
// branch issues `create` calls but still NO extra `list` beyond the initial
// listNotes. The corpus carries only the atom (no hub, no master), forcing
// both creates.
func TestApply_HubCreate_PatchesIndexNoRelist(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	atom := domain.Note{
		ID:      "atom-1",
		Title:   "Poem One",
		Content: "# Poem One\n#library/poetry | [[Biko]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{
		d.Tag: {atom}, // hub + master absent -> create path
	})
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	result := regen.Run(ctx, d)

	if got := backend.CountKind("list", d.Tag); got != 1 {
		t.Errorf("create cycle issued %d `list` calls for tag %q, want exactly 1 (no re-list after create)",
			got, d.Tag)
	}
	if got := backend.CountKind("create", ""); got < 1 {
		t.Errorf("create cycle issued %d `create` calls, want >= 1 (hub + master created)", got)
	}
	if result.HubsCreated != 1 || result.MasterCreated != 1 {
		t.Errorf("create cycle = hubsCreated:%d masterCreated:%d, want 1/1",
			result.HubsCreated, result.MasterCreated)
	}
}
