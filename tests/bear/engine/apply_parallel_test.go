// Package engine_test — parallel applyPerDomain orchestration tests.
//
// Validates the per-umbrella errgroup orchestration of
// engine.applyPerDomain: siblings concurrent / umbrella waits on
// family / families concurrent; state.Save serialization under
// parallel writers; idempotency across cycles.
//
// Production code uses errgroup.WithContext-based applyPerDomain over
// a per-umbrella dependency graph. These
// tests drive the real orchestrator with a fake BearcliBackend injected
// via bear.ContextWithBackend, recording per-call timestamps inside a
// testing/synctest bubble for deterministic ordering. The bearcli pool
// is the back-pressure target; tests reset it to a known
// capacity via bear.ResetBearcliPoolForTest before each Apply.
package engine_test

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

// fakeBackend is a deterministic bear.BearcliBackend used by parallel
// orchestrator tests. Every Run call:
//
// 1. increments inflight (and updates peak); a snapshot is exposed via
// PeakInflight so siblings-concurrent and families-concurrent
// tests can assert observed fan-out independent of the pool's
// own counters.
// 2. records (kind, tag, virtualEndTime) inside a slice so the
// umbrella-waits-on-family test can assert happens-before ordering
// via virtual-clock timestamps.
// 3. sleeps perCallSleep on the synctest virtual clock — concurrent
// callers wake up in the same virtual epoch.
// 4. returns a minimal valid JSON payload for the bearcli sub-command
// dispatched in args[0], chosen so RunRegen + FetchMasterContent +
// FetchHubContents all complete without producing real I/O.
type fakeBackend struct {
	perCallSleep time.Duration

	mu       sync.Mutex
	records  []fakeCall
	inflight atomic.Int64
	peak     atomic.Int64
}

type fakeCall struct {
	Kind string
	Tag  string
	End  time.Time
}

func newFakeBackend(sleep time.Duration) *fakeBackend {
	return &fakeBackend{perCallSleep: sleep}
}

// Run satisfies bear.BearcliBackend. Records inflight/peak/timestamps,
// sleeps perCallSleep, and returns a per-kind JSON payload.
func (f *fakeBackend) Run(ctx context.Context, args []string, _ string) ([]byte, error) {
	now := f.inflight.Add(1)
	for {
		prev := f.peak.Load()
		if now <= prev || f.peak.CompareAndSwap(prev, now) {
			break
		}
	}
	defer f.inflight.Add(-1)

	if f.perCallSleep > 0 {
		select {
		case <-time.After(f.perCallSleep):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	kind := bearcliKindFromArgs(args)
	tag := tagFromArgs(args)
	f.mu.Lock()
	f.records = append(f.records, fakeCall{Kind: kind, Tag: tag, End: time.Now()})
	f.mu.Unlock()
	return fakePayload(kind), nil
}

// PeakInflight returns the highest observed concurrent Run count.
func (f *fakeBackend) PeakInflight() int64 {
	return f.peak.Load()
}

// CallsByTag returns the slice of fakeCall entries whose Tag matches.
// The slice is sorted by End ascending (insertion order under append).
func (f *fakeBackend) CallsByTag(tag string) []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, 0, len(f.records))
	for _, c := range f.records {
		if c.Tag == tag {
			out = append(out, c)
		}
	}
	return out
}

// Records returns a copy of the full call log.
func (f *fakeBackend) Records() []fakeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]fakeCall, len(f.records))
	copy(out, f.records)
	return out
}

// bearcliKindFromArgs mirrors the unexported classifier in
// bear/bearcli/pool.go so test assertions can speak the same vocabulary
// as the production metrics counters.
func bearcliKindFromArgs(args []string) string {
	if len(args) == 0 {
		return "other"
	}
	switch args[0] {
	case "list", "cat", "show", "overwrite", "create", "find":
		return args[0]
	default:
		return "other"
	}
}

// tagFromArgs extracts the --tag value when present (list / find paths)
// or returns "" for tag-less calls (cat / show / overwrite / create by
// ID). Used by the umbrella-waits test to filter records by domain.
func tagFromArgs(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tag" {
			return args[i+1]
		}
	}
	return ""
}

// fakePayload returns the minimal valid JSON the production code parses
// for each sub-command. For list: empty array (RunRegen handles zero
// notes cleanly). For single-note calls: a stub note with empty
// content. The empty-list path drives RunRegen straight to
// upsertMasterIndex → create → done; FetchMasterContent then bottoms
// out because findIndexID returns "" — the snapshot fails softly and
// computeDomainHash returns "" (preserves prior hash). That is the
// exact contract the tests want: no real subprocesses, no Bear sqlite
// I/O, only the orchestration shape under observation.
func fakePayload(kind string) []byte {
	switch kind {
	case "list":
		return []byte(`[]`)
	case "cat", "show", "create":
		// Single-note JSON object. Hash empty because the fake never
		// drives a real overwrite-with-retry path.
		return []byte(`{"id":"fake","title":"fake","content":"","hash":"","tags":[],"created":"0001-01-01T00:00:00Z"}`)
	case "overwrite":
		return []byte(`{}`)
	default:
		return []byte(`{}`)
	}
}

// stubDomain builds a minimal *bear.Domain that satisfies Validate and
// drives RunRegen straight through the empty-notes happy path. The
// ParseMeta and RenderMaster callbacks return zero values — never
// called because list returns [] under the fake backend.
func stubDomain(tag, indexTitle, parentMaster string) *bear.Domain {
	return &bear.Domain{
		Tag:          tag,
		CanonicalTag: "#" + tag,
		IndexTitle:   indexTitle,
		ParentMaster: parentMaster,
		ParseMeta: func(_ *bear.Domain, _ string) bear.AtomicMeta {
			return bear.AtomicMeta{}
		},
		RenderMaster: func(_ *bear.Domain, _ map[string][]bear.Note) string {
			return ""
		},
	}
}

// umbrellaStub builds a stub umbrella (SkipAtomicsPass=true) that
// satisfies Validate by setting DefaultChild to one of its leaves' tags.
func umbrellaStub(tag, indexTitle, defaultChild string) *bear.Domain {
	d := stubDomain(tag, indexTitle, "")
	d.SkipAtomicsPass = true
	d.DefaultChild = defaultChild
	return d
}

// poolCapacityForParallelTests is the pool slot count every parallel
// orchestration test resets to. Eight is the ship default and gives
// every test fixture enough headroom to fan out without the semaphore
// ever becoming the back-pressure source under observation.
const poolCapacityForParallelTests = 8

// resetPoolForApply resets the bearcli pool capacity to the test-wide
// constant and registers a Cleanup that restores capacity 1 (the global
// default the rest of the test corpus assumes).
func resetPoolForApply(t *testing.T) {
	t.Helper()
	bear.ResetBearcliPoolForTest(poolCapacityForParallelTests)
	t.Cleanup(func() { bear.ResetBearcliPoolForTest(1) })
}

// applyOptsFor builds an ApplyOpts that disables every pre-pass (so
// the tests observe only applyPerDomain) and points state/lock paths
// at the per-test temp dir.
func applyOptsFor(t *testing.T, domains []*bear.Domain) engine.ApplyOpts {
	t.Helper()
	dir := t.TempDir()
	return engine.ApplyOpts{
		Domains:            domains,
		Pins:               nil,
		StatePath:          filepath.Join(dir, "state.json"),
		LockPath:           filepath.Join(dir, ".lock"),
		Features:           engine.Features{}, // all pre-passes off
		BearcliConcurrency: poolCapacityForParallelTests,
		// Auto-tag fast-pass tests resolve dailyDomain via
		// domainsByTag[opts.DailyDefaultTag]; without this hint the
		// fast-pass logs `dailyDomain is nil` and skips. Tests that
		// don't care still get a harmless zero-effect default.
		DailyDefaultTag: "quicknote/daily",
	}
}

// TestApplyParallel_SiblingsConcurrent proves that within a single
// umbrella family, sibling leaf domains run concurrently — observed
// via the fake backend's peak-inflight counter. With 3 leaves issuing
// bearcli calls under a pool capacity of 8, the peak must reach at
// least 3 (sequential execution would peak at 1).
func TestApplyParallel_SiblingsConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeBackend(50 * time.Millisecond)
		ctx := bear.ContextWithBackend(t.Context(), fake)

		umbrella := umbrellaStub("library", "✱ Бібліотека", "library/a")
		leafA := stubDomain("library/a", "[Бібліотека · A]", umbrella.IndexTitle)
		leafB := stubDomain("library/b", "[Бібліотека · B]", umbrella.IndexTitle)
		leafC := stubDomain("library/c", "[Бібліотека · C]", umbrella.IndexTitle)

		opts := applyOptsFor(t, []*bear.Domain{leafA, leafB, leafC, umbrella})
		opts.SkipFlock = true // synctest bubble + flock would block on real syscalls

		result, err := engine.Apply(ctx, opts)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if result.Interrupted {
			t.Fatal("expected Interrupted=false")
		}
		if peak := fake.PeakInflight(); peak < 3 {
			t.Errorf("siblings did not run concurrently: peak inflight = %d, want >= 3", peak)
		}
		for _, leaf := range []*bear.Domain{leafA, leafB, leafC} {
			if _, ok := result.Domains[leaf.Tag]; !ok {
				t.Errorf("leaf %q missing from result.Domains", leaf.Tag)
			}
		}
		if _, ok := result.Domains[umbrella.Tag]; !ok {
			t.Errorf("umbrella %q missing from result.Domains", umbrella.Tag)
		}
	})
}

// TestApplyParallel_UmbrellaWaitsOnFamily proves that an umbrella's
// RunRegen executes only after every leaf in its family has completed.
// Verified via virtual-clock timestamps recorded by the fake backend:
// the umbrella's first recorded call must come strictly after each
// leaf's last recorded call.
func TestApplyParallel_UmbrellaWaitsOnFamily(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeBackend(20 * time.Millisecond)
		ctx := bear.ContextWithBackend(t.Context(), fake)

		umbrella := umbrellaStub("it", "✱ IT", "it/leaf1")
		leaf1 := stubDomain("it/leaf1", "[IT · Leaf 1]", umbrella.IndexTitle)
		leaf2 := stubDomain("it/leaf2", "[IT · Leaf 2]", umbrella.IndexTitle)

		opts := applyOptsFor(t, []*bear.Domain{leaf1, leaf2, umbrella})
		opts.SkipFlock = true

		if _, err := engine.Apply(ctx, opts); err != nil {
			t.Fatalf("Apply: %v", err)
		}

		umbCalls := fake.CallsByTag(umbrella.Tag)
		if len(umbCalls) == 0 {
			t.Fatalf("umbrella %q produced no bearcli calls", umbrella.Tag)
		}
		umbFirst := umbCalls[0].End
		for _, leaf := range []*bear.Domain{leaf1, leaf2} {
			calls := fake.CallsByTag(leaf.Tag)
			if len(calls) == 0 {
				t.Fatalf("leaf %q produced no bearcli calls", leaf.Tag)
			}
			leafLast := calls[len(calls)-1].End
			if !umbFirst.After(leafLast) {
				t.Errorf("ordering violation: umbrella %q first call at %v is not strictly after leaf %q last call at %v",
					umbrella.Tag, umbFirst, leaf.Tag, leafLast)
			}
		}
	})
}

// TestApplyParallel_FamiliesConcurrent proves independent umbrella
// families (no shared parent) run concurrently. Observed via virtual-
// clock total elapsed: with 2 families × 2 leaves each + a 20ms per-
// call sleep, serial execution would scale linearly in domain count;
// parallel execution stays bounded by the deepest single family.
func TestApplyParallel_FamiliesConcurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeBackend(20 * time.Millisecond)
		ctx := bear.ContextWithBackend(t.Context(), fake)

		umbA := umbrellaStub("library", "✱ Бібліотека", "library/a1")
		leafA1 := stubDomain("library/a1", "[Бібліотека · A1]", umbA.IndexTitle)
		leafA2 := stubDomain("library/a2", "[Бібліотека · A2]", umbA.IndexTitle)

		umbB := umbrellaStub("llm", "✱ LLM", "llm/b1")
		leafB1 := stubDomain("llm/b1", "[LLM · B1]", umbB.IndexTitle)
		leafB2 := stubDomain("llm/b2", "[LLM · B2]", umbB.IndexTitle)

		opts := applyOptsFor(t, []*bear.Domain{leafA1, leafA2, leafB1, leafB2, umbA, umbB})
		opts.SkipFlock = true

		start := time.Now()
		if _, err := engine.Apply(ctx, opts); err != nil {
			t.Fatalf("Apply: %v", err)
		}
		elapsed := time.Since(start)

		// Peak inflight must reflect at least the 4 leaves running
		// concurrently (2 from each family) on top of any pool
		// contention. Sequential execution would peak at 1.
		if peak := fake.PeakInflight(); peak < 4 {
			t.Errorf("families did not run concurrently: peak inflight = %d, want >= 4", peak)
		}
		// Serial estimate: each domain issues ~3 bearcli calls inside
		// RunRegen + computeDomainHash (list, list-for-findIndexID,
		// list-for-FetchHubContents). 6 domains × 3 calls × 20ms =
		// 360ms serial. Parallel families should finish in well under
		// half that — the deepest family runs ~3 calls × 20ms = 60ms,
		// plus the umbrella's own ~3 calls = ~120ms. We assert <200ms
		// to leave generous slack for scheduling jitter inside the
		// virtual clock.
		if elapsed >= 250*time.Millisecond {
			t.Errorf("families serialized: elapsed = %v, want < 250ms", elapsed)
		}
	})
}

// buildStateSaveFixture returns an 8-leaf single-family domain set and
// the leaf-tag slice the assertions check after Apply returns. Extracted
// from TestApply_StateSave_Concurrent so the test body stays under the
// gocognit budget — the fixture's branching is now isolated here.
func buildStateSaveFixture() (umbrella *bear.Domain, domains []*bear.Domain, leafTags []string) {
	umbrella = umbrellaStub("library", "✱ Бібліотека", "library/leaf0")
	for i := range 8 {
		tag := "library/leaf" + string(rune('0'+i))
		leafTags = append(leafTags, tag)
		domains = append(domains, stubDomain(tag, "[Бібліотека · "+tag+"]", umbrella.IndexTitle))
	}
	domains = append(domains, umbrella)
	return umbrella, domains, leafTags
}

// assertEveryDomainStamped fails the test for each tag missing from
// result.Domains — surfaces both a "leaf goroutine never ran" defect
// and a "stateMu lost-entry race" defect with the same assertion.
func assertEveryDomainStamped(t *testing.T, result *engine.ApplyResult, tags []string) {
	t.Helper()
	for _, tag := range tags {
		if _, ok := result.Domains[tag]; !ok {
			t.Errorf("tag %q missing from result.Domains (state-save race?)", tag)
		}
	}
}

// TestApply_StateSave_Concurrent proves that parallel writers serialize
// on the state.Save mutex inside runDomainAndSave. 8 leaves all
// completing near-simultaneously must produce a state.json with 8
// distinct domain entries; -race would catch concurrent-map-write
// regressions; missing entries would catch a last-writer-wins race
// where one goroutine's mutate-then-save races another's mutate-then-
// save and the final on-disk image loses an entry.
func TestApply_StateSave_Concurrent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeBackend(5 * time.Millisecond)
		ctx := bear.ContextWithBackend(t.Context(), fake)

		umbrella, domains, leafTags := buildStateSaveFixture()

		opts := applyOptsFor(t, domains)
		opts.SkipFlock = true

		result, err := engine.Apply(ctx, opts)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if result.Interrupted {
			t.Fatal("expected Interrupted=false")
		}

		// Every leaf + the umbrella must appear in result.Domains. A
		// torn Save (truncate without rewrite) or a lost-entry race
		// would surface here as a missing tag.
		assertEveryDomainStamped(t, result, append(leafTags, umbrella.Tag))

		// state.json on disk must parse cleanly — a torn write would
		// surface as json.Unmarshal returning an error inside Load.
		st, err := state.Load(opts.StatePath)
		if err != nil {
			t.Fatalf("state.Load after concurrent Save: %v", err)
		}
		if st.LastApply.IsZero() {
			t.Error("expected st.LastApply set after success, got zero")
		}
	})
}

// TestApplyParallel_Idempotent proves the parallel apply preserves the
// idempotency contract: two back-to-back Apply calls produce a
// stable result.Domains map (every entry stamped Unchanged) and an
// identical state.json image. Regardless of goroutine scheduling,
// cycle 2 must converge to the same on-disk state as cycle 1.
func TestApplyParallel_Idempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeBackend(5 * time.Millisecond)
		ctx := bear.ContextWithBackend(t.Context(), fake)

		umbrella := umbrellaStub("llm", "✱ LLM", "llm/a")
		leafA := stubDomain("llm/a", "[LLM · A]", umbrella.IndexTitle)
		leafB := stubDomain("llm/b", "[LLM · B]", umbrella.IndexTitle)

		domains := []*bear.Domain{leafA, leafB, umbrella}
		opts := applyOptsFor(t, domains)
		opts.SkipFlock = true

		// Cycle 1.
		result1, err := engine.Apply(ctx, opts)
		if err != nil {
			t.Fatalf("Apply cycle 1: %v", err)
		}
		state1 := readStateJSON(t, opts.StatePath)

		// Cycle 2 — same opts, same backend, same state path.
		result2, err := engine.Apply(ctx, opts)
		if err != nil {
			t.Fatalf("Apply cycle 2: %v", err)
		}
		state2 := readStateJSON(t, opts.StatePath)

		// Every domain present in cycle 1's result must reappear in
		// cycle 2's result with an equivalent Unchanged stamp.
		if len(result1.Domains) != len(result2.Domains) {
			t.Errorf("cycle 1 wrote %d domain entries; cycle 2 wrote %d (expected equal)",
				len(result1.Domains), len(result2.Domains))
		}
		for tag, c := range result2.Domains {
			if c.Changed > 0 {
				t.Errorf("cycle 2 reported Changed=%d for %q (idempotency violation)", c.Changed, tag)
			}
		}

		// Per-domain content-hash entries (the fingerprint of the
		// regenerated master+hubs) must match across cycles — the
		// idempotency contract.
		if !sameDomainHashes(state1.Domains, state2.Domains) {
			t.Errorf("state.json domains diverged between cycles 1 and 2:\n cycle1=%v\n cycle2=%v",
				state1.Domains, state2.Domains)
		}
	})
}

// readStateJSON loads state.json or fatals the test. Returns *state.State
// so tests can compare the.Domains map across cycles.
func readStateJSON(t *testing.T, path string) *state.State {
	t.Helper()
	st, err := state.Load(path)
	if err != nil {
		t.Fatalf("state.Load(%s): %v", path, err)
	}
	return st
}

// sameDomainHashes compares two state.DomainState maps key-set and
// per-key ContentHash. Returns true only when the maps are identical;
// used by the idempotency test to assert byte-level state convergence.
func sameDomainHashes(a, b map[string]state.DomainState) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok || vb.ContentHash != va.ContentHash {
			return false
		}
	}
	return true
}
