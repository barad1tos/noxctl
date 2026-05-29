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
