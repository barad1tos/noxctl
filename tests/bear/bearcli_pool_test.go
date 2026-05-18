// Package bear_test — bearcli subprocess pool tests.
//
// Validates the global bearcli concurrency semaphore (PAR-01)
// and the calls-by-kind / hash-conflict metrics counters (PAR-06).
// External package per project convention ("Naming Patterns") —
// tests/bear/ is the canonical home for bear_test files.
//
// GREEN: drives the production API landed in
// (bear.SetBearcliConcurrency, bear.AcquireBearcliForTest,
// bear.BearcliMetricsSnapshot, bear.ResetBearcliPoolForTest). Tests use
// the standard-library testing/synctest bubble so goroutine scheduling
// is deterministic — no real wall-clock sleeps, no flaky timing.
package bear_test

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// startHolder spawns a goroutine that acquires one bearcli slot and
// holds it until release is signaled, then closes done. Extracted from
// the per-test bodies to keep dupl + gocognit under the project caps.
func startHolder(t *testing.T, ctx context.Context, kind string, release <-chan struct{}) <-chan struct{} {
	t.Helper()
	done := make(chan struct{})
	go func() {
		rel, err := bear.AcquireBearcliForTest(ctx, kind)
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
func assertChanBlocked(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
		t.Fatal(msg)
	default:
	}
}

// assertChanFired fails the test if ch has not fired by the time the
// assertion runs. Used after synctest.Wait to assert "this goroutine
// has progressed past the blocking point".
func assertChanFired(t *testing.T, ch <-chan struct{}, msg string) {
	t.Helper()
	select {
	case <-ch:
	default:
		t.Fatal(msg)
	}
}

// TestBearcliSemaphore proves the global semaphore bounds concurrent
// acquires at the configured cap. With capacity 2 and three concurrent
// acquires, exactly two succeed pre-release and the third blocks until
// one slot is returned.
func TestBearcliSemaphore(t *testing.T) {
	t.Cleanup(func() { bear.ResetBearcliPoolForTest(1) })

	synctest.Test(
		t, func(t *testing.T) {
			bear.ResetBearcliPoolForTest(2)

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
				rel, err := bear.AcquireBearcliForTest(ctx, "list")
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

			snap := bear.BearcliMetricsSnapshot()
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
	t.Cleanup(func() { bear.ResetBearcliPoolForTest(1) })

	synctest.Test(
		t, func(t *testing.T) {
			bear.ResetBearcliPoolForTest(1)

			// Saturate the single slot.
			rel, err := bear.AcquireBearcliForTest(t.Context(), "list")
			if err != nil {
				t.Fatalf("primary acquire failed: %v", err)
			}

			ctx, cancel := context.WithCancel(t.Context())
			blockedDone := make(chan error, 1)
			go func() {
				_, acquireErr := bear.AcquireBearcliForTest(ctx, "list")
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
			rel2, err := bear.AcquireBearcliForTest(ctx2, "list")
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
	if got := bear.BearcliMetricsSnapshot().CallsByKind[kind]; got != want {
		t.Errorf("CallsByKind[%s] = %d, want %d", kind, got, want)
	}
}

// TestBearcliMetrics_CallsByKind proves the per-kind counter increments
// once per acquire (segregated by bearcli sub-command argument) and
// that ResetBearcliPoolForTest zeroes the counters. Also covers the
// SetBearcliConcurrency sync.Once contract — a second call is a no-op.
func TestBearcliMetrics_CallsByKind(t *testing.T) {
	t.Cleanup(func() { bear.ResetBearcliPoolForTest(1) })

	bear.ResetBearcliPoolForTest(4)
	ctx := context.Background()
	for _, k := range []string{"list", "list", "list", "cat", "cat", "overwrite"} {
		rel, err := bear.AcquireBearcliForTest(ctx, k)
		if err != nil {
			t.Fatalf("acquire %q failed: %v", k, err)
		}
		rel()
	}

	snap := bear.BearcliMetricsSnapshot()
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
	bear.ResetBearcliPoolForTest(2)
	snap = bear.BearcliMetricsSnapshot()
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
	bear.SetBearcliConcurrency(5)
	bear.SetBearcliConcurrency(99)
	if got := bear.BearcliMetricsSnapshot().Capacity; got != 5 {
		t.Errorf("after second SetBearcliConcurrency: Capacity = %d, want 5 (sync.Once silent on second call)", got)
	}

	// ResetBearcliMetrics zeroes counters without replacing the channel.
	rel, err := bear.AcquireBearcliForTest(ctx, "find")
	if err != nil {
		t.Fatalf("post-sync.Once acquire failed: %v", err)
	}
	rel()
	assertKindCount(t, "find", 1)
	bear.ResetBearcliMetrics()
	assertKindCount(t, "find", 0)
	if got := bear.BearcliMetricsSnapshot().Capacity; got != 5 {
		t.Errorf("after ResetBearcliMetrics: Capacity = %d, want 5 (untouched)", got)
	}
}
