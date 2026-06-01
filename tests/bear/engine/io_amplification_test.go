// Package engine_test pins the read-amplification contract of the no-op regen
// pass. The goroutine-local note index replaces the per-bucket findNoteByTitle
// scan (each of which issued its own `bearcli list --fields id,title`) in the
// hub/master upsert path with a single index built from the initial listNotes
// result. The assertion below is the independently-verifiable proof of that
// change: a no-op regen of a hub-routed domain with B buckets must issue
// EXACTLY ONE `list` for its tag (the initial listNotes) — pre-phase it issued
// B+2 (initial list + one per-bucket hub list + one master list).
//
// The probe drives regen.Run directly through the shared testutil recording
// backend (mirroring tests/bear/hub_upsert_test.go, which also drives
// regen.Run with a fake bearcli.Backend). The engine.Apply hash-snapshot pass
// (computeDomainHash -> FetchMasterContent/FetchHubContents) issues its OWN
// reads and is a SEPARATE amplification surface that the snapshot-reuse pass
// eliminates by reusing regen.Result.Snapshot — so the one-list-per-no-op
// contract is pinned at the regen layer here, where the index makes the fix.
package engine_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/regen"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/bear/state"
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
// `cat` reads (B hubs + 1 master) are untouched — the index removes lists, not cats.
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
		t.Errorf("no-op hub pass issued %d `cat` calls, want 2 (1 hub + 1 master); the index removes lists, not cats", got)
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

// TestApply_NoOpCycle_NoRedundantSnapshotRead is the snapshot-reuse read-
// amplification proof: a no-op regen of a hub-routed domain (B buckets) issues
// exactly 1 `list` (the initial listNotes) AND no MORE than B+1 `cat` (the B hub
// diff-check cats + 1 master diff-check cat). The post-domain content hash is
// sourced from regen.Result.Snapshot — content already fetched during the
// diff-check — so the no-op cycle does ZERO extra reads for hashing (no second
// list, no extra cat). Before this reuse the engine hash pass re-read every hub via
// FetchHubContents (+1 list) and the master via FetchMasterContent (+1 list,
// +1 cat); this asserts those are gone.
//
// Golden parity: the hash persisted by engine.Apply must equal the hash the
// pre-phase snapshotDomainContent re-read path produced for the same steady-
// state fixture. Both hash sources see the SAME stripped bytes, so the
// state.json fingerprint is byte-identical across the optimization.
func TestApply_NoOpCycle_NoRedundantSnapshotRead(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	corpus := noOpHubRoutedCorpus(d)
	backend := testutil.NewRecordingBackend(corpus)
	ctx := bearcli.ContextWithBackend(context.Background(), backend)
	dir := t.TempDir()

	result, err := engine.Apply(ctx, engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	})
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	const buckets = 1 // B == 1 ("Biko")
	if got := backend.CountKind("list", d.Tag); got != 1 {
		t.Errorf("no-op cycle issued %d `list` calls for tag %q, want exactly 1 (initial listNotes only; no snapshot re-list)",
			got, d.Tag)
	}
	// B hub diff-check cats + 1 master diff-check cat == B+1. No extra cat for
	// the hash — it reuses the diff-check content via Result.Snapshot.
	if got := backend.CountKind("cat", ""); got != buckets+1 {
		t.Errorf("no-op cycle issued %d `cat` calls, want %d (B hub + 1 master diff-check cats; zero extra for hashing)",
			got, buckets+1)
	}
	hubTitle := d.HubTitle("Biko")
	if counts := result.Domains[d.Tag]; counts.Created != 0 || counts.Changed != 0 || counts.Failed != 0 {
		t.Errorf("domain counts = %+v, want no-op apply with created=0 changed=0 failed=0", counts)
	}

	// Golden parity: the persisted hash equals the hash the old re-read path
	// produced for the same fixture. The old path stripped the existing
	// master/hub bodies and hashed them — reproduce that here from the corpus to
	// pin byte-identity across the optimization.
	wantMaster := domain.StripNewNoteURLsFromBody(corpusNoteContent(corpus, d.Tag, d.IndexTitle))
	wantHubs := map[string]string{
		hubTitle: domain.StripNewNoteURLsFromBody(corpusNoteContent(corpus, d.Tag, hubTitle)),
	}
	wantHash := engine.ComputeContentHash(wantMaster, wantHubs)
	st, err := state.Load(filepath.Join(dir, "state.json"))
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if got := st.Domains[d.Tag].ContentHash; got != wantHash {
		t.Errorf("persisted ContentHash = %q, want golden hash %q (engine.Apply must persist snapshot-derived hash)",
			got, wantHash)
	}
}

// corpusNoteContent returns the body of the note with the given title from the
// recording-backend corpus for tag. Test helper for the golden-parity assertion.
func corpusNoteContent(corpus map[string][]domain.Note, tag, title string) string {
	for _, n := range corpus[tag] {
		if n.Title == title {
			return n.Content
		}
	}
	return ""
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
	// Pin the read-back cost: each created note is read back EXACTLY ONCE (to
	// hash Bear's stored form), and the create path issues no diff-check cat. So
	// 1 hub + 1 master create => exactly 2 cats. If a refactor ever re-read a
	// created note twice (or added a pre-create cat), this catches the silent
	// cost erosion of the "one read, only on create" contract.
	if got := backend.CountKind("cat", ""); got != 2 {
		t.Errorf("create cycle issued %d `cat` calls, want 2 (one read-back per created note: hub + master)", got)
	}
}
