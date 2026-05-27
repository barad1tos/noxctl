// Package engine_test — mtime-poll fallback tests for the daemon.
//
// Validates the stat-and-trigger primitive, the zero-disables-poll-loop
// behavior, and the self-write gate honored on poll-triggered events.
// Drives the real Daemon.Run select loop with a fake FsWatcher (no
// FSEvents arrive unless the test injects them) and a fake StatFn
// (scripted ModTime sequence per scenario). All tests wrap the body in
// testing/synctest.Test so time.NewTicker, time.NewTimer, and the
// virtual clock advance deterministically.
package engine_test

import (
	"bytes"
	"context"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/engine"
)

// fakeStat returns scripted os.FileInfo values per call. The mtimes
// slice is exhausted in order; once the test consumes all scripted
// values, the last one repeats indefinitely (so a runaway poll loop
// safely converges instead of panicking on index out of range).
//
// Mirrors apply_parallel_test.go::fakeBackend in spirit: synchronously
// records each call via an atomic counter, exposes a snapshot accessor
// the test asserts on.
type fakeStat struct {
	mu     sync.Mutex
	mtimes []time.Time
	idx    int
	calls  atomic.Int64
}

// Stat satisfies the DaemonOpts.StatFn signature.
func (f *fakeStat) Stat(_ string) (os.FileInfo, error) {
	f.calls.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	mt := f.mtimes[f.idx]
	if f.idx+1 < len(f.mtimes) {
		f.idx++
	}
	return fakeFileInfo{mt: mt}, nil
}

// fakeFileInfo is a minimal os.FileInfo for the poll path. Only
// ModTime carries meaningful data — the rest exists to satisfy the
// interface contract.
type fakeFileInfo struct{ mt time.Time }

func (fakeFileInfo) Name() string         { return "database.sqlite" }
func (fakeFileInfo) Size() int64          { return 0 }
func (fakeFileInfo) Mode() fs.FileMode    { return 0o644 }
func (f fakeFileInfo) ModTime() time.Time { return f.mt }
func (fakeFileInfo) IsDir() bool          { return false }
func (fakeFileInfo) Sys() any             { return nil }

// pollOptsFor builds a DaemonOpts wired for poll synctest scenarios.
// SkipFlock=true mirrors apply_parallel_test.go — flock isn't ctx-aware
// and synctest cannot virtualize the syscall. Reuses applyOptsFor from
// apply_parallel_test.go (same package) so the inner ApplyOpts shape
// stays in one place (dupl ≥30 tokens trips on duplicated literals
// otherwise).
func pollOptsFor(t *testing.T, stat *fakeStat, pollInterval, debounce time.Duration) engine.DaemonOpts {
	t.Helper()
	inner := applyOptsFor(t, nil)
	inner.SkipFlock = true
	return engine.DaemonOpts{
		ApplyOpts:         inner,
		BearDBDir:         t.TempDir(),
		DebouncePause:     debounce,
		MaxBurstWindow:    10 * time.Second,
		SelfWriteEpsilon:  2 * time.Second,
		MtimePollInterval: pollInterval,
		StatFn:            stat.Stat,
	}
}

// captureLog redirects the package log to a buffer and registers a
// Cleanup that restores the prior writer + flags + prefix. Mirrors the
// pattern in tests/bear/config/daemon_test.go::
// TestLoadDaemon_BearcliConcurrency_SoftCap. Also serves the
// tag-override integration tests that assert on `d.Logf` content
// (suppression WARNs, conflict rollups). Prefix save/restore is
// defensive — production never calls log.SetPrefix, but a sibling
// test in the same binary might, and an unrestored prefix would leak
// into the captured buffer of the next test that uses this helper.
//
// Mutates package-global stdlib log state — tests using this helper MUST
// NOT call t.Parallel(); otherwise siblings' log emissions race into the
// captured buffer.
func captureLog(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	prevPrefix := log.Prefix()
	log.SetOutput(&buf)
	log.SetFlags(0)
	log.SetPrefix("")
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
		log.SetPrefix(prevPrefix)
	})
	return &buf
}

// countCycles returns the number of "regen trigger:" lines emitted by
// cycleOnce. Each entry corresponds to exactly one cycleOnce invocation.
func countCycles(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "regen trigger:")
}

// TestDaemonPoll_StatAndTrigger asserts: a poll-detected mtime change
// resets the debounce timer and the loop fires at least the
// initial-record + change-detect stat sequence under virtual time.
func TestDaemonPoll_StatAndTrigger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{base, base.Add(1 * time.Second)}}
		opts := pollOptsFor(t, stat, 100*time.Millisecond, 50*time.Millisecond)
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		// Virtual-time advance: tick 1 records the initial mtime; tick 2
		// observes the advanced mtime and routes through handleEvent
		// (debounce armed); a subsequent quietTimer fire would invoke
		// cycleOnce. 500ms of virtual time covers all three timers
		// (poll 100ms x N + debounce 50ms) generously.
		time.Sleep(500 * time.Millisecond)
		cancel()
		<-errCh

		if got := stat.calls.Load(); got < 2 {
			t.Errorf("fakeStat.calls = %d, want >= 2 (at least one initial-record + one change-detect)", got)
		}
	})
}

// TestDaemonPoll_ZeroDisables asserts: when MtimePollInterval == 0,
// no ticker is created and StatFn is never invoked from the poll path.
func TestDaemonPoll_ZeroDisables(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		stat := &fakeStat{mtimes: []time.Time{time.Now()}}
		opts := pollOptsFor(t, stat, 0 /* disabled */, 50*time.Millisecond)
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })

		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)

		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		// 5 virtual seconds is enough that a 1s ticker would have fired
		// 5 times. With MtimePollInterval=0 the nil-channel idiom keeps
		// `case <-pollTick:` unreachable and the poll loop never runs.
		time.Sleep(5 * time.Second)
		cancel()
		<-errCh

		if got := stat.calls.Load(); got != 0 {
			t.Errorf("fakeStat.calls = %d, want 0 (MtimePollInterval=0 must not start a ticker)", got)
		}
	})
}

// TestDaemonPoll_SelfWriteGate asserts: a poll that catches a
// fresh mtime advance within the EFFECTIVE self-write epsilon of the
// last cycle end does NOT trigger a redundant cycle. After the gate
// closes the next mtime change DOES trigger a cycle. The contract gates
// poll-triggered synthetic events through the same isSelfTriggered
// path that FSEvent-triggered events traverse — zero new gate logic.
//
// Effective epsilon when polling is on:
//
//	max(SelfWriteEpsilon, MtimePollInterval + DebouncePause + 3s).
//
// The 3s safety margin covers Bear's async-flush trail after bearcli
// writes return — empirically 3-5s before the SQLite commit settles
// on disk.
//
// Virtual timeline (DebouncePause=50ms, MtimePollInterval=1s,
// SelfWriteEpsilon=100ms, so effective = 1s + 50ms + 3s = 4050ms):
// - T=+1000ms (tick 1, stat call 1 → base): lastMtime=zero, advance,
// handleEvent → quietTimer.Reset(50ms).
// - T=+1050ms (cycle 1 fires): updatePollBaseline stat call 2 →
// base+10ms, lastMtime=base+10ms. Gate closes at +5100ms.
// - Ticks 2-5 (T=2-5s, stat calls 3-6 → base+500ms..base+10s): each
// advances lastMtime, then handleEvent → isSelfTriggered ⇒ true ⇒
// suppressed, NO cycle.
// - Tick 6 (T=6s, stat call 7 → base+20s): advanced vs lastMtime,
// T=6s > 5.1s ⇒ NOT suppressed ⇒ quietTimer.Reset(50ms) → cycle 2
// at ~T=6050ms.
//
// Final countCycles == 2.
func TestDaemonPoll_SelfWriteGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                             // tick 1 (T=1s)
			base.Add(10 * time.Millisecond),  // updatePollBaseline after cycle 1
			base.Add(500 * time.Millisecond), // tick 2 (T=2s) — gated, slides
			base.Add(1 * time.Second),        // tick 3 (T=3s) — gated, slides
			base.Add(10 * time.Second),       // tick 4 (T=4s) — gated, slides
			base.Add(10 * time.Second),       // tick 5 (T=5s) — no advance
			base.Add(10 * time.Second),       // tick 6 (T=6s) — no advance
			base.Add(10 * time.Second),       // tick 7 (T=7s) — no advance
			base.Add(10 * time.Second),       // tick 8 (T=8s) — no advance
			base.Add(20 * time.Second),       // tick 9 (T=9s) — past slid gate, fires
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })

		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		// 10s covers all 9 poll ticks plus cycle 2's debounce settle.
		time.Sleep(10 * time.Second)
		cancel()
		<-errCh

		if got := countCycles(buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (suppressed ticks slide the gate;"+
				" a new mtime after the quiet gap must still fire cycle 2)\nlog:\n%s", got, buf.String())
		}
	})
}

// TestDaemonPoll_DebounceReset asserts the debounce-uniformity
// contract: an FSEvent followed by a poll-detected mtime change INSIDE
// the debounce window resets the same quietTimer (idempotent reset),
// so exactly one cycle fires after the LATER reset's DebouncePause.
//
// Virtual timeline (DebouncePause=200ms, MtimePollInterval=100ms):
// - T=0: inject an FSEvent for database.sqlite → quietTimer.Reset(200)
// → would fire at T=200ms.
// - T=+100ms (tick 1): mtime=base, lastMtime=zero → handleEvent →
// quietTimer.Reset(200) → would fire at T=300ms.
// - T=+200ms (tick 2): mtime=base+1s → handleEvent →
// quietTimer.Reset(200) → would fire at T=400ms.
// - T=+300/400/...ms (subsequent ticks): mtime unchanged (last value
// repeats), no further handleEvent invocation.
// - quietTimer eventually fires at ~T=400ms → cycle 1 runs.
//
// Final countCycles == 1: exactly one cycle, AFTER both early triggers
// rolled into the same debounce window.
func TestDaemonPoll_DebounceReset(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{base, base.Add(1 * time.Second)}}
		opts := pollOptsFor(t, stat, 100*time.Millisecond, 200*time.Millisecond)
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })

		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		// Inject the seed FSEvent. The fakeWatcher in daemon_test.go
		// exposes the events channel as `events` (buffered 16); we
		// reuse the same fake to keep the test surface small.
		fw.events <- fsnotify.Event{
			Op:   fsnotify.Write,
			Name: filepath.Join(opts.BearDBDir, "database.sqlite"),
		}

		// 1 second of virtual time covers FSEvent (T=0) + 2 poll-ticks
		// (T=100, T=200) + the final debounce settle (T=400) + slack.
		time.Sleep(1 * time.Second)
		cancel()
		<-errCh

		if got := countCycles(buf); got != 1 {
			t.Errorf("cycle count = %d, want 1 (FSEvent + 2 poll ticks should all reset the SAME quietTimer)\nlog:\n%s",
				got, buf.String())
		}
	})
}

// TestDaemonPoll_SelfWriteGateSlidesOnSuppressedEvents asserts:
// if a poll-detected daemon-originated mtime advance lands inside the
// effective self-write gate, a later FSEvent for the same delayed SQLite
// activity is still suppressed even after the original post-cycle window.
func TestDaemonPoll_SelfWriteGateSlidesOnSuppressedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,
			base.Add(1 * time.Second),
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })

		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		dbPath := filepath.Join(opts.BearDBDir, "database.sqlite")
		fw.events <- fsnotify.Event{Op: fsnotify.Write, Name: dbPath}

		time.Sleep(4500 * time.Millisecond)
		fw.events <- fsnotify.Event{Op: fsnotify.Write, Name: dbPath}

		time.Sleep(1 * time.Second)
		cancel()
		<-errCh

		if got := countCycles(buf); got != 1 {
			t.Errorf("cycle count = %d, want 1 (suppressed delayed write must extend self-write gate)\nlog:\n%s",
				got, buf.String())
		}
	})
}

// TestDaemonPoll_CycleEndResetBaseline asserts the cycle-storm fix:
// after cycleOnce completes, updatePollBaseline locks lastMtime to the
// post-cycle database.sqlite ModTime. The next poll tick then sees
// "no advance" (because the daemon's OWN cycle-end writes are baked
// into the new baseline) and does NOT fire a redundant cycle.
//
// Empirically, without this reset the daemon ran 7 cycles in 6 idle
// minutes — each cycle's bearcli mutations bumped the db mtime ~4s
// past the cycle's quiet-timer trigger, and SelfWriteEpsilon=2s
// closed before the next 5s poll tick could be gated.
//
// Virtual timeline (DebouncePause=50ms, MtimePollInterval=200ms,
// SelfWriteEpsilon=50ms — intentionally tiny to ensure the self-write
// gate is NOT the mechanism under test; only updatePollBaseline can
// prevent cycle 2):
// - T=+200ms (tick 1, stat call 1 → base): lastMtime=zero → advance,
// handleEvent → quietTimer.Reset(50ms).
// - T=+250ms (quietTimer fires): cycle 1 runs. updatePollBaseline
// stat call 2 → base+1s, lastMtime=base+1s. Self-write window
// closes at ~T=300ms.
// - T=+400ms (tick 2, stat call 3 → base+1s [capped]): no advance
// vs lastMtime, no event, NO cycle.
// - T=+600ms+ (subsequent ticks): same, no advance, no cycle.
//
// Final countCycles == 1. WITHOUT updatePollBaseline this would be
// 2: tick 2 would advance from base→base+1s and fire (self-write
// window already closed at T=300, well before T=400 tick).
func TestDaemonPoll_CycleEndResetBaseline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                      // tick 1
			base.Add(1 * time.Second), // updatePollBaseline after cycle 1
		}}
		opts := pollOptsFor(t, stat, 200*time.Millisecond, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 50 * time.Millisecond
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })

		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		// 1.5s = 7 poll ticks + plenty of debounce headroom. If the
		// baseline reset is broken (storm), cycle count will be 7+.
		time.Sleep(1500 * time.Millisecond)
		cancel()
		<-errCh

		if got := countCycles(buf); got != 1 {
			t.Errorf("cycle count = %d, want 1 (cycle 1 should reset baseline;"+
				" subsequent poll ticks should see no advance and skip)\nlog:\n%s",
				got, buf.String())
		}
	})
}
