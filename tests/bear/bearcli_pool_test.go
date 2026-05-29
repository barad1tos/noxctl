// Package bear_test — bearcli subprocess pool tests.
//
// Validates the global bearcli concurrency semaphore and the
// calls-by-kind / hash-conflict metrics counters. External package
// per project convention — tests/bear/ is the canonical home for
// bear_test files.
//
// Drives the production API (bearcli.SetConcurrency,
// bearcli.AcquireForTest, bearcli.MetricsSnapshot,
// bearcli.ResetPoolForTest). Tests use the standard-library
// testing/synctest bubble so goroutine scheduling is deterministic
// — no real wall-clock sleeps, no flaky timing.
package bear_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// startHolder spawns a goroutine that acquires one bearcli slot and
// holds it until release is signaled, then closes done. Extracted from
// the per-test bodies to keep dupl + gocognit under the project caps.
func startHolder(t *testing.T, ctx context.Context, kind string, release <-chan struct{}) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		rel, err := bearcli.AcquireForTest(ctx, kind)
		if err != nil {
			t.Errorf("holder %q acquire failed: %v", kind, err)
			close(done)
			return
		}
		<-release
		rel()
		close(done)
	}()
	return done
}

// assertChanBlocked fails the test if ch has already fired by the time
// the assertion runs. Used after synctest.Wait to assert "this goroutine
// is durably blocked".
func assertChanBlocked(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(message)
	default:
	}
}

// assertChanFired fails the test if ch has not fired by the time the
// assertion runs. Used after synctest.Wait to assert "this goroutine
// has progressed past the blocking point".
func assertChanFired(t *testing.T, ch <-chan struct{}, message string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal(message)
	}
}

// TestBearcliSemaphore proves the global semaphore bounds concurrent
// acquires at the configured cap. With capacity 2 and three concurrent
// acquires, exactly two succeed pre-release and the third blocks until
// one slot is returned.
func TestBearcliSemaphore(t *testing.T) {
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	synctest.Test(
		t, func(t *testing.T) {
			bearcli.ResetPoolForTest(2)

			ctx := t.Context()
			holdA, holdB := make(chan struct{}), make(chan struct{})
			doneA := startHolder(t, ctx, "list", holdA)
			doneB := startHolder(t, ctx, "list", holdB)

			// After Wait the two holders are durably blocked on holdA/holdB,
			// so the semaphore is saturated.
			synctest.Wait()

			// Third acquire should block — start it on a goroutine and
			// observe via synctest.Wait that it does NOT progress.
			thirdDone := make(chan struct{})
			var thirdRelease func()
			go func() {
				rel, err := bearcli.AcquireForTest(ctx, "list")
				if err != nil {
					t.Errorf("acquire C failed: %v", err)
					close(thirdDone)
					return
				}
				thirdRelease = rel
				close(thirdDone)
			}()

			synctest.Wait()
			assertChanBlocked(t, thirdDone, "third acquire returned before slot was released — semaphore did not bound at 2")

			// Release one slot — third acquire should now proceed.
			close(holdA)
			<-doneA
			synctest.Wait()
			assertChanFired(t, thirdDone, "third acquire did not proceed after a slot was released")

			// Cleanup.
			thirdRelease()
			close(holdB)
			<-doneB

			snap := bearcli.MetricsSnapshot()
			if snap.Capacity != 2 {
				t.Errorf("Capacity = %d, want 2", snap.Capacity)
			}
			if snap.AcquireCount != 3 {
				t.Errorf("AcquireCount = %d, want 3", snap.AcquireCount)
			}
			if snap.PeakConcurrent < 1 || snap.PeakConcurrent > 2 {
				t.Errorf("PeakConcurrent = %d, want 1..2 (cap)", snap.PeakConcurrent)
			}
		},
	)
}

// TestBearcliSemaphore_CtxCancel proves AcquireBearcliForTest honors
// ctx.Done — a blocked acquire returns ctx.Err promptly when the
// context is canceled, and the canceled caller does NOT consume a slot.
func TestBearcliSemaphore_CtxCancel(t *testing.T) {
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	synctest.Test(
		t, func(t *testing.T) {
			bearcli.ResetPoolForTest(1)

			// Saturate the single slot.
			rel, err := bearcli.AcquireForTest(t.Context(), "list")
			if err != nil {
				t.Fatalf("primary acquire failed: %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			blockedDone := make(chan error, 1)
			go func() {
				_, acquireErr := bearcli.AcquireForTest(ctx, "list")
				blockedDone <- acquireErr
			}()

			// Confirm the second goroutine is durably blocked on the
			// semaphore send.
			synctest.Wait()
			select {
			case <-blockedDone:
				t.Fatal("blocked acquire returned before cancellation — slot was somehow free")
			default:
			}

			cancel()
			synctest.Wait()

			select {
			case cancelErr := <-blockedDone:
				if cancelErr == nil {
					t.Fatal("blocked acquire returned nil error after cancel — expected ctx.Err()")
				}
				if !errors.Is(cancelErr, context.Canceled) {
					t.Errorf("err = %v, want context.Canceled", cancelErr)
				}
			default:
				t.Fatal("blocked acquire did not return after cancel")
			}

			// The slot held by `rel` should still be the only outstanding
			// reservation — release and verify a fresh acquire succeeds.
			rel()
			synctest.Wait()

			ctx2, cancel2 := context.WithTimeout(t.Context(), 100*time.Millisecond)
			defer cancel2()
			rel2, err := bearcli.AcquireForTest(ctx2, "list")
			if err != nil {
				t.Fatalf("post-release acquire failed: %v", err)
			}
			rel2()
		},
	)
}

// assertKindCount fails the test if CallsByKind[kind] != want.
// Encapsulates the dupl-magnet "if got, want:=..." pattern so a row
// of kind assertions stays under the gocognit budget.
func assertKindCount(t *testing.T, kind string, want int64) {
	t.Helper()
	if got := bearcli.MetricsSnapshot().CallsByKind[kind]; got != want {
		t.Errorf("CallsByKind[%s] = %d, want %d", kind, got, want)
	}
}

// TestBearcliMetrics_CallsByKind proves the per-kind counter increments
// once per acquire (segregated by bearcli sub-command argument) and
// that ResetBearcliPoolForTest zeroes the counters. Also covers the
// SetBearcliConcurrency sync.Once contract — a second call is a no-op.
func TestBearcliMetrics_CallsByKind(t *testing.T) {
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	bearcli.ResetPoolForTest(4)
	ctx := context.Background()
	for _, k := range []string{"list", "list", "list", "cat", "cat", "overwrite"} {
		rel, err := bearcli.AcquireForTest(ctx, k)
		if err != nil {
			t.Fatalf("acquire %q failed: %v", k, err)
		}
		rel()
	}

	snap := bearcli.MetricsSnapshot()
	assertKindCount(t, "list", 3)
	assertKindCount(t, "cat", 2)
	assertKindCount(t, "overwrite", 1)
	assertKindCount(t, "show", 0)
	if snap.AcquireCount != 6 {
		t.Errorf("AcquireCount = %d, want 6", snap.AcquireCount)
	}
	if snap.PeakConcurrent < 1 || snap.PeakConcurrent > 4 {
		t.Errorf("PeakConcurrent = %d, want 1..4 (cap)", snap.PeakConcurrent)
	}

	// ResetBearcliPoolForTest zeroes counters and re-arms the sync.Once.
	bearcli.ResetPoolForTest(2)
	snap = bearcli.MetricsSnapshot()
	if snap.AcquireCount != 0 {
		t.Errorf("after reset: AcquireCount = %d, want 0", snap.AcquireCount)
	}
	for kind, n := range snap.CallsByKind {
		if n != 0 {
			t.Errorf("after reset: CallsByKind[%s] = %d, want 0", kind, n)
		}
	}
	if snap.Capacity != 2 {
		t.Errorf("after reset: Capacity = %d, want 2", snap.Capacity)
	}

	// SetBearcliConcurrency sync.Once contract — the first call after
	// reset installs cap 5; the second call is a silent no-op.
	bearcli.SetConcurrency(5)
	bearcli.SetConcurrency(99)
	if got := bearcli.MetricsSnapshot().Capacity; got != 5 {
		t.Errorf("after second SetBearcliConcurrency: Capacity = %d, want 5 (sync.Once silent on second call)", got)
	}

	// ResetBearcliMetrics zeroes counters without replacing the channel.
	rel, err := bearcli.AcquireForTest(ctx, "find")
	if err != nil {
		t.Fatalf("post-sync.Once acquire failed: %v", err)
	}
	rel()
	assertKindCount(t, "find", 1)
	bearcli.ResetMetrics()
	assertKindCount(t, "find", 0)
	if got := bearcli.MetricsSnapshot().Capacity; got != 5 {
		t.Errorf("after ResetBearcliMetrics: Capacity = %d, want 5 (untouched)", got)
	}
}

// TestScopePeakToCurrentInFlight_ResetsToInFlightFloorNotZero pins that
// ScopePeakToCurrentInFlight rescopes the peak high-water mark DOWN to the
// calls currently in flight — not to zero, and not leaving it at the prior
// cycle's higher max. The daemon calls this at each regen cycle start; if it
// zeroed the mark while a still-draining call was in flight, that cycle's
// peak_concurrency telemetry would under-report real concurrency to an operator
// tuning the pool. A capacity-1 drained cycle cannot tell the correct reset
// from a no-op or a zero-reset, so this drives the lifetime peak to 3, releases
// every slot, then holds ONE slot across the scope call.
func TestScopePeakToCurrentInFlight_ResetsToInFlightFloorNotZero(t *testing.T) {
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	bearcli.ResetPoolForTest(4)
	ctx := context.Background()
	acquire := func() func() {
		rel, err := bearcli.AcquireForTest(ctx, "list")
		if err != nil {
			t.Fatalf("AcquireForTest: %v", err)
		}
		return rel
	}

	// Drive the lifetime peak to 3, then release every slot — the CAS-max
	// stays at 3 while in-flight returns to 0.
	r1, r2, r3 := acquire(), acquire(), acquire()
	r1()
	r2()
	r3()
	if got := bearcli.MetricsSnapshot().PeakConcurrent; got != 3 {
		t.Fatalf("setup: PeakConcurrent = %d, want 3 after three concurrent acquires", got)
	}

	// One call is still in flight at the cycle boundary.
	hold := acquire()
	defer hold()

	bearcli.ScopePeakToCurrentInFlight()

	if got := bearcli.MetricsSnapshot().PeakConcurrent; got != 1 {
		t.Fatalf("PeakConcurrent after ScopePeakToCurrentInFlight with one call in flight = %d, "+
			"want 1 (the in-flight floor — not the prior cycle's max 3, not 0)", got)
	}
}

// alwaysOKBackend is a minimal bearcli.Backend that returns an empty
// JSON array for every Run call. Used by kind-classification tests that
// only care about the metrics side effect (kindFromArgs + incCallKind),
// not about the bearcli output payload.
type alwaysOKBackend struct{}

func (alwaysOKBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	return []byte("[]"), nil
}

// TestBearcliMetrics_KindClassification proves that kindFromArgs maps
// every supported bearcli sub-command to its dedicated CallsByKind
// bucket and that incCallKind increments the matching counter. Covers
// the eight first-class verbs (list/cat/show/overwrite/create/find/
// trash) plus the two-level `tags` family (`tags` bare vs `tags add`)
// and the defensive `other` fallback for empty/unknown args.
//
// The trash + tags + tags-add classifications were added in Phase 13
// alongside bear/bearcli/tag.go::AddTag — prior to this they collapsed
// into the `other` bucket and silently inflated the unknown-kind
// metric. This test pins their first-class status so a future refactor
// cannot regress them back into `other`.
func TestBearcliMetrics_KindClassification(t *testing.T) {
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })
	bearcli.ResetPoolForTest(4)

	ctx := bearcli.ContextWithBackend(context.Background(), alwaysOKBackend{})

	cases := []struct {
		name string
		args []string
		kind string
	}{
		{"list", []string{"list", "--tag", "x"}, "list"},
		{"cat", []string{"cat", "id"}, "cat"},
		{"show", []string{"show", "id"}, "show"},
		{"overwrite", []string{"overwrite", "id"}, "overwrite"},
		{"create", []string{"create"}, "create"},
		{"find", []string{"find", "--tag", "x"}, "find"},
		{"trash", []string{"trash", "id"}, "trash"},
		{"tags-bare", []string{"tags", "list"}, "tags"},
		{"tags-add", []string{"tags", "add", "id", "x"}, "tags-add"},
		{"unknown-verb", []string{"unknown-verb"}, "other"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bearcli.ResetMetrics()
			if _, err := bearcli.Run(ctx, tc.args, ""); err != nil {
				t.Fatalf("bearcli.Run(%v) failed: %v", tc.args, err)
			}
			assertKindCount(t, tc.kind, 1)
		})
	}
}
