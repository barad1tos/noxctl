// Package engine_test guards the D-02 idempotency landmine: the per-domain
// state.json ContentHash MUST stay byte-identical across two consecutive no-op
// applies. If the changed-branch read-back ever regressed to hashing the
// in-memory rendered bytes instead of Bear's stored-normalized form, a
// NORMALIZING backend (one that collapses whitespace on read-back) would
// produce a different hash on cycle 2 — flipping the domain to "changed"
// forever and breaking the <=3-pass contract. Both backend flavors must yield
// the same hash across cycles.
package engine_test

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestApply_HashStableAcrossNoOpCycles runs engine.Apply twice over the same
// steady-state hub-routed domain and asserts the persisted ContentHash is
// identical across both cycles, for an echo-back backend (Bear returns exactly
// what was written) AND a normalizing backend (read-back collapses trailing
// whitespace). The normalizing variant is the real landmine guard: it only
// stays stable because the changed-branch read-back captures the STORED form.
func TestApply_HashStableAcrossNoOpCycles(t *testing.T) {
	cases := []struct {
		name      string
		normalize bool
	}{
		{"echo-back", false},
		{"normalizing", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bearcli.ResetPoolForTest(1)
			t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

			d := noOpHubRoutedDomain()
			corpus := noOpHubRoutedCorpus(d)
			backend := testutil.NewRecordingBackend(corpus)
			if tc.normalize {
				backend.NormalizeReadBack = true
			}
			ctx := bearcli.ContextWithBackend(context.Background(), backend)

			dir := t.TempDir()
			statePath := filepath.Join(dir, "state.json")
			opts := engine.ApplyOpts{
				Domains:   []*domain.Domain{d},
				StatePath: statePath,
				LockPath:  filepath.Join(dir, ".lock"),
				Features:  engine.Features{},
				SkipFlock: true,
			}

			hash1 := applyAndReadHash(t, ctx, opts, d.Tag)
			hash2 := applyAndReadHash(t, ctx, opts, d.Tag)

			if hash1 == "" {
				t.Fatalf("cycle 1 produced empty ContentHash for %q", d.Tag)
			}
			if hash1 != hash2 {
				t.Errorf("ContentHash drifted across no-op cycles (%s backend): cycle1=%q cycle2=%q",
					tc.name, hash1, hash2)
			}
		})
	}
}

// TestApply_HubCatFailurePreservesPriorHash guards the partial-hash landmine: a
// hub-routed domain with TWO buckets runs a cycle in which ONE hub's diff-check
// `cat` fails outright (a genuine read/parse failure, NOT a post-write read-back
// soft-fail), while the OTHER hub and the master upsert succeed. The failing hub
// drops out of the snapshot, so hashing the surviving hub + master would persist
// a content hash over a PARTIAL hub set — wrong, and it would silently flip the
// domain "changed" on the next clean cycle. The contract: a genuine hub failure
// marks the snapshot incomplete, so the prior ContentHash is preserved (the
// master's success cannot persist a hash computed from an incomplete corpus).
//
// Mirrors TestApply_ReadBackFailureAfterWriteIsNonFatal — that pins the soft
// post-write read-back path; this pins the hard diff-check failure path.
func TestApply_HubCatFailurePreservesPriorHash(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	// Two buckets, two hubs, one master — all in the steady no-op state so the
	// only failure injected is the diff-check cat of one hub.
	corpus := twoBucketNoOpCorpus(d)
	backend := testutil.NewRecordingBackend(corpus)
	// Fail the diff-check cat of the "Biko" hub specifically: a genuine read
	// failure for an existing hub. The "Achebe" hub and the master read fine.
	backend.FailCatForID = map[string]bool{"hub-biko": true}
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	priorHash := "previous-stable-content-hash"
	if err := (&state.State{
		Version: state.SchemaVersion,
		Domains: map[string]state.DomainState{d.Tag: {ContentHash: priorHash}},
	}).Save(statePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	}

	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}

	// The hub cat failure is a genuine failure — surfaced in the recap.
	if counts := result.Domains[d.Tag]; counts.Failed == 0 {
		t.Errorf("domain counts = %+v, want Failed>0 (a hub cat failure must be reported)", counts)
	}

	// The prior hash MUST survive: a failing cycle that dropped a hub from the
	// snapshot may not persist a hash computed from the surviving hub + master.
	after, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if got := after.Domains[d.Tag].ContentHash; got != priorHash {
		t.Errorf("ContentHash = %q, want prior %q preserved "+
			"(a dropped hub must mark the snapshot incomplete, not persist a partial hash)", got, priorHash)
	}
}

// twoBucketNoOpCorpus builds a steady-state hub-routed corpus with two buckets
// ("Biko" and "Achebe"), each with one atom and a matching hub, plus the master.
// Every structural note already carries its rendered body so the cycle is no-op
// absent any injected failure.
func twoBucketNoOpCorpus(d *domain.Domain) map[string][]domain.Note {
	atomBiko := domain.Note{
		ID:      "atom-biko",
		Title:   "Poem Biko",
		Content: "# Poem Biko\n#library/poetry | [[Biko]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
	atomAchebe := domain.Note{
		ID:      "atom-achebe",
		Title:   "Poem Achebe",
		Content: "# Poem Achebe\n#library/poetry | [[Achebe]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
	groups := map[string][]domain.Note{
		"Biko":   {atomBiko},
		"Achebe": {atomAchebe},
	}
	tags := []string{"#library/poetry"}
	corpus := []domain.Note{
		atomBiko,
		atomAchebe,
		{ID: "hub-biko", Title: d.HubTitle("Biko"), Content: d.RenderHub(d, "Biko", groups["Biko"], nil), Tags: tags},
		{ID: "hub-achebe", Title: d.HubTitle("Achebe"), Content: d.RenderHub(d, "Achebe", groups["Achebe"], nil), Tags: tags},
		{ID: "master", Title: d.IndexTitle, Content: d.RenderMaster(d, groups), Tags: tags},
	}
	return map[string][]domain.Note{d.Tag: corpus}
}

// applyAndReadHash runs one engine.Apply and returns the persisted ContentHash
// for tag. Fails the test on Apply error or unreadable state.
func applyAndReadHash(t *testing.T, ctx context.Context, opts engine.ApplyOpts, tag string) string {
	t.Helper()
	if _, err := engine.Apply(ctx, opts); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	st, err := state.Load(opts.StatePath)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	return st.Domains[tag].ContentHash
}

// TestApply_CreateConvergesToStableHash is the FIX-1 guard: a hub-routed domain
// whose hub+master DO NOT YET EXIST is created on cycle 1, then a steady no-op
// cycle 2 follows. Under a NORMALIZING backend (read-back strips the EOF
// newline Bear collapses on store), the create branch must capture the STORED
// form for hashing — not the in-memory rendered bytes. Otherwise cycle 1 hashes
// the rendered create body while cycle 2 re-derives the hash from the normalized
// stored body, the two diverge, and the domain flips to "changed" forever,
// breaking the <=3-pass idempotency contract. The convergence assertion: the
// persisted ContentHash is byte-identical across cycle 1 (create) and cycle 2
// (no-op), proving the created note's hash is already the stored form.
func TestApply_CreateConvergesToStableHash(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	// Corpus carries ONLY the atom — no hub, no master — forcing the create path.
	atom := domain.Note{
		ID:      "atom-1",
		Title:   "Poem One",
		Content: "# Poem One\n#library/poetry | [[Biko]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{d.Tag: {atom}})
	backend.NormalizeReadBack = true
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	}

	hashCreate := applyAndReadHash(t, ctx, opts, d.Tag) // cycle 1: creates hub+master
	hashNoOp := applyAndReadHash(t, ctx, opts, d.Tag)   // cycle 2: steady no-op

	if hashCreate == "" {
		t.Fatalf("create cycle produced empty ContentHash for %q", d.Tag)
	}
	if hashCreate != hashNoOp {
		t.Errorf("ContentHash flipped after create under normalizing backend: create=%q no-op=%q "+
			"(create branch must hash Bear's STORED form, not rendered bytes)", hashCreate, hashNoOp)
	}
}

// TestApply_ReadBackFailureAfterWriteIsNonFatal is the FIX-2 guard: a hub-routed
// domain whose hub+master have STALE bodies (forcing an overwrite) runs under a
// backend whose post-write read-back `cat` fails transiently. The write to the
// vault already succeeded, so the failure MUST be non-fatal: the domain reports
// changed (not failed), AnyFailed stays false, and the prior ContentHash is
// PRESERVED (never overwritten with a partial/empty value). Pre-FIX-2 the
// read-back error bubbled out of upsertHub/upsertMasterIndex and flipped the
// upsert to failed, skewing recap counts after a durable write.
func TestApply_ReadBackFailureAfterWriteIsNonFatal(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	// Hub + master exist but carry STALE content, so the diff-check forces an
	// overwrite on this cycle (the changed branch, where the read-back fires).
	atom := domain.Note{
		ID:      "atom-1",
		Title:   "Poem One",
		Content: "# Poem One\n#library/poetry | [[Biko]]\n---\nbody\n",
		Tags:    []string{"#library/poetry"},
	}
	corpus := []domain.Note{
		atom,
		{ID: "hub-biko", Title: d.HubTitle("Biko"), Content: "# stale hub\n", Tags: []string{"#library/poetry"}},
		{ID: "master", Title: d.IndexTitle, Content: "# stale master\n", Tags: []string{"#library/poetry"}},
	}
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{d.Tag: corpus})
	backend.FailReadBackAfterWrite = true
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	priorHash := "previous-content-hash"
	if err := (&state.State{
		Version: state.SchemaVersion,
		Domains: map[string]state.DomainState{d.Tag: {ContentHash: priorHash}},
	}).Save(statePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	}

	result, err := engine.Apply(ctx, opts)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if result.AnyFailed() {
		t.Errorf("AnyFailed = true after a successful write whose read-back failed; "+
			"want false (write is durable, read-back failure is non-fatal): %#v", result.Domains[d.Tag])
	}
	if counts := result.Domains[d.Tag]; counts.Failed != 0 || counts.Changed == 0 {
		t.Errorf("domain counts = %+v, want changed>0 failed=0 (overwrite succeeded, hash unavailable)", counts)
	}
	after, err := state.Load(statePath)
	if err != nil {
		t.Fatalf("state.Load: %v", err)
	}
	if got := after.Domains[d.Tag].ContentHash; got != priorHash {
		t.Errorf("ContentHash = %q, want prior %q preserved (incomplete snapshot must not overwrite the hash)", got, priorHash)
	}
}
