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
	"errors"
	"io/fs"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

func TestSQLiteNoteChangeToken_ReadOnlyAndSemantic(t *testing.T) {
	if _, err := exec.LookPath("sqlite3"); err != nil {
		t.Fatalf("sqlite3 not installed: %v", err)
	}
	dbPath := filepath.Join(t.TempDir(), "database.sqlite")
	runSQLiteFixtureForTokenTest(t, dbPath, "sqlite_note_token_setup.fixture")

	beforeInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat before token: %v", err)
	}
	token1, err := engine.SQLiteNoteChangeToken(dbPath, beforeInfo)
	if err != nil {
		t.Fatalf("SQLiteNoteChangeToken: %v", err)
	}
	afterInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat after token: %v", err)
	}
	if !afterInfo.ModTime().Equal(beforeInfo.ModTime()) {
		t.Fatalf("read-only token changed database mtime: before=%s after=%s",
			beforeInfo.ModTime(), afterInfo.ModTime())
	}

	runSQLiteFixtureForTokenTest(t, dbPath, "sqlite_note_token_note_update.fixture")
	updatedInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat after update: %v", err)
	}
	token2, err := engine.SQLiteNoteChangeToken(dbPath, updatedInfo)
	if err != nil {
		t.Fatalf("SQLiteNoteChangeToken after update: %v", err)
	}
	if token1 == token2 {
		t.Fatalf("token did not change after semantic note update: %q", token1)
	}

	runSQLiteFixtureForTokenTest(t, dbPath, "sqlite_note_token_tag_update.fixture")
	tagInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat after tag update: %v", err)
	}
	token3, err := engine.SQLiteNoteChangeToken(dbPath, tagInfo)
	if err != nil {
		t.Fatalf("SQLiteNoteChangeToken after tag update: %v", err)
	}
	if token2 == token3 {
		t.Fatalf("token did not change after semantic tag update: %q", token2)
	}

	runSQLiteFixtureForTokenTest(t, dbPath, "sqlite_note_token_link_insert.fixture")
	linkInfo, err := os.Stat(dbPath)
	if err != nil {
		t.Fatalf("stat after link update: %v", err)
	}
	token4, err := engine.SQLiteNoteChangeToken(dbPath, linkInfo)
	if err != nil {
		t.Fatalf("SQLiteNoteChangeToken after link update: %v", err)
	}
	if token3 == token4 {
		t.Fatalf("token did not change after semantic note-tag link update: %q", token3)
	}
}

func runSQLiteForTokenTest(t *testing.T, dbPath, sql string) {
	t.Helper()
	cmd := exec.Command("sqlite3", dbPath, sql)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 failed: %v: %s", err, strings.TrimSpace(string(output)))
	}
}

func runSQLiteFixtureForTokenTest(t *testing.T, dbPath, fixture string) {
	t.Helper()
	sql, err := os.ReadFile(filepath.Join("testdata", fixture))
	if err != nil {
		t.Fatalf("read sqlite fixture %s: %v", fixture, err)
	}
	runSQLiteForTokenTest(t, dbPath, string(sql))
}

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
// (suppression WARNs, conflict summaries). Prefix save/restore is
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

type pollDaemonRun struct {
	Daemon  *engine.Daemon
	Watcher *fakeWatcher
	Buf     *bytes.Buffer
	cancel  context.CancelFunc
	errCh   <-chan error
}

func startPollDaemonRun(
	t *testing.T,
	opts engine.DaemonOpts,
	contextFn func(context.Context) context.Context,
) *pollDaemonRun {
	t.Helper()
	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	t.Cleanup(func() { _ = d.Close() })

	buf := captureLog(t)
	ctx, cancel := context.WithCancel(t.Context())
	if contextFn != nil {
		ctx = contextFn(ctx)
	}
	t.Cleanup(cancel)

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	return &pollDaemonRun{Daemon: d, Watcher: fw, Buf: buf, cancel: cancel, errCh: errCh}
}

func (r *pollDaemonRun) WaitFor(dur time.Duration) {
	time.Sleep(dur)
	r.cancel()
	<-r.errCh
}

// TestDaemonPoll_StatAndTrigger asserts: a poll-detected mtime change
// resets the debounce timer and the loop fires at least the
// initial-record + change-detect stat sequence under virtual time.
func TestDaemonPoll_StatAndTrigger(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{base, base.Add(1 * time.Second)}}
		opts := pollOptsFor(t, stat, 100*time.Millisecond, 50*time.Millisecond)
		run := startPollDaemonRun(t, opts, nil)

		// Virtual-time advance: tick 1 records the initial mtime; tick 2
		// observes the advanced mtime and routes through handleEvent
		// (debounce armed); a subsequent quietTimer fire would invoke
		// cycleOnce. 500ms of virtual time covers all three timers
		// (poll 100ms x N + debounce 50ms) generously.
		run.WaitFor(500 * time.Millisecond)

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
		run := startPollDaemonRun(t, opts, nil)

		// 5 virtual seconds is enough that a 1s ticker would have fired
		// 5 times. With MtimePollInterval=0 the nil-channel idiom keeps
		// `case <-pollTick:` unreachable and the poll loop never runs.
		run.WaitFor(5 * time.Second)

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
// The 3s safety margin covers one poll/debounce roundtrip without turning
// the gate into long-lived event suppression. Longer delayed SQLite
// housekeeping is filtered by the database content token, not by widening
// this time window.
//
// Virtual timeline (DebouncePause=50ms, MtimePollInterval=1s,
// SelfWriteEpsilon=100ms, so effective = 1s + 50ms + 3s = 4050ms):
// - T=+1000ms (tick 1, stat call 1 → base): baseline mtime=zero, advance,
// handleEvent → quietTimer.Reset(50ms).
// - T=+1050ms (cycle 1 fires): updateDatabaseBaseline stat call 2 →
// base+10ms, baseline mtime=base+10ms. Gate closes at roughly +5100ms.
// - Tick 2 (T=2s, stat call 3 → base+500ms): records a pending candidate,
// then handleEvent → isSelfTriggered ⇒ true ⇒ suppressed, NO cycle.
// - Ticks 3-5 (T=3-5s): retry the same pending candidate while the gate
// stays closed. StatFn may still observe newer mtimes, but the unchanged
// pending token must not reset debounce while the burst is active.
// - Tick 6 (T=6s): replays the pending candidate after the original gate
// closes ⇒ quietTimer.Reset(50ms) → cycle 2 at ~T=6050ms.
//
// Final countCycles == 2.
func TestDaemonPoll_SelfWriteGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                             // tick 1 (T=1s)
			base.Add(10 * time.Millisecond),  // updateDatabaseBaseline after cycle 1
			base.Add(500 * time.Millisecond), // tick 2 (T=2s) — gated
			base.Add(10 * time.Second),       // updateDatabaseBaseline after cycle 2
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		run := startPollDaemonRun(t, opts, nil)

		// 7s covers all 6 poll ticks plus cycle 2's debounce settle.
		run.WaitFor(7 * time.Second)

		if got := countCycles(run.Buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (cycle 1 from tick 1;"+
				" tick 2 within effective gate window must suppress;"+
				" tick 6 past gate must fire cycle 2)\nlog:\n%s", got, run.Buf.String())
		}
	})
}

func TestDaemonPoll_MtimeNoiseWithoutTokenChangeSkipsCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                            // tick 1: startup catch-up
			base.Add(10 * time.Millisecond), // baseline after cycle 1
			base.Add(10 * time.Millisecond), // tick 2: no advance
			base.Add(10 * time.Millisecond), // tick 3: no advance
			base.Add(10 * time.Millisecond), // tick 4: no advance
			base.Add(10 * time.Millisecond), // tick 5: no advance
			base.Add(20 * time.Second),      // tick 6: mtime noise after gate
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			return "stable", nil
		}
		run := startPollDaemonRun(t, opts, nil)

		run.WaitFor(7 * time.Second)

		if got := countCycles(run.Buf); got != 1 {
			t.Errorf("cycle count = %d, want 1 (mtime-only noise must not trigger cycle 2)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonPoll_TokenChangeAfterGateTriggersCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                            // tick 1: startup catch-up
			base.Add(10 * time.Millisecond), // baseline after cycle 1
			base.Add(10 * time.Millisecond), // tick 2: no advance
			base.Add(10 * time.Millisecond), // tick 3: no advance
			base.Add(10 * time.Millisecond), // tick 4: no advance
			base.Add(10 * time.Millisecond), // tick 5: no advance
			base.Add(20 * time.Second),      // tick 6: real token change after gate
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		var tokenCalls atomic.Int64
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			if tokenCalls.Add(1) <= 2 {
				return "v1", nil
			}
			return "v2", nil
		}
		run := startPollDaemonRun(t, opts, nil)

		run.WaitFor(7 * time.Second)

		if got := countCycles(run.Buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (token change after gate must trigger cycle 2)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonPoll_TokenReadFailureRetriesSameMtime(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                            // tick 1: startup catch-up
			base.Add(10 * time.Millisecond), // baseline after cycle 1
			base.Add(20 * time.Second),      // tick 2: token read fails
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		var tokenCalls atomic.Int64
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			switch tokenCalls.Add(1) {
			case 1, 2:
				return "v1", nil
			case 3:
				return "", errors.New("sqlite busy")
			default:
				return "v2", nil
			}
		}
		run := startPollDaemonRun(t, opts, nil)

		run.WaitFor(7 * time.Second)

		if got := countCycles(run.Buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (failed token read must not consume the mtime advance)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonPoll_GatedTokenChangeRetriesAfterGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                            // tick 1: startup catch-up
			base.Add(10 * time.Millisecond), // baseline after cycle 1
			base.Add(20 * time.Second),      // tick 2: real token change while self-write gate is open
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		var tokenCalls atomic.Int64
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			if tokenCalls.Add(1) <= 2 {
				return "v1", nil
			}
			return "v2", nil
		}
		run := startPollDaemonRun(t, opts, nil)

		run.WaitFor(7 * time.Second)

		if got := countCycles(run.Buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (gated token change must stay pending until the gate closes)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonPoll_FailedCycleRetriesSameToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{base}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		opts.Domains = []*domain.Domain{minimalApplyDomain("test/failing", "Test Failing")}
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			return "v1", nil
		}
		resetPoolForApply(t)
		run := startPollDaemonRun(t, opts, func(ctx context.Context) context.Context {
			return bearcli.ContextWithBackend(ctx, failingApplyBackend{})
		})

		run.WaitFor(7 * time.Second)

		if got := countCycles(run.Buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (failed apply must leave the pending DB token retryable)\nlog:\n%s",
				got, run.Buf.String())
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
// - T=+100ms (tick 1): mtime=base, baseline mtime=zero → handleEvent →
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
		run := startPollDaemonRun(t, opts, nil)

		// Inject the seed FSEvent. The fakeWatcher in daemon_test.go
		// exposes the events channel as `events` (buffered 16); we
		// reuse the same fake to keep the test surface small.
		run.Watcher.events <- fsnotify.Event{
			Op:   fsnotify.Write,
			Name: filepath.Join(opts.BearDBDir, "database.sqlite"),
		}

		// 1 second of virtual time covers FSEvent (T=0) + 2 poll-ticks
		// (T=100, T=200) + the final debounce settle (T=400) + slack.
		run.WaitFor(1 * time.Second)

		if got := countCycles(run.Buf); got != 1 {
			t.Errorf("cycle count = %d, want 1 (FSEvent + 2 poll ticks should all reset the SAME quietTimer)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonPoll_PendingTokenChangeResetsActiveBurst(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                      // tick 1: first poll-only token change
			base.Add(1 * time.Second), // tick 2: second poll-only token change inside debounce
		}}
		opts := pollOptsFor(t, stat, 100*time.Millisecond, 300*time.Millisecond)
		var tokenCalls atomic.Int64
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			if tokenCalls.Add(1) == 1 {
				return "v1", nil
			}
			return "v2", nil
		}
		var cycles atomic.Int64
		opts.Domains = []*domain.Domain{minimalApplyDomain("test/pending", "Test Pending")}
		opts.DomainTimingHook = func(string, time.Duration) {
			cycles.Add(1)
		}
		resetPoolForApply(t)
		run := startPollDaemonRun(t, opts, func(ctx context.Context) context.Context {
			return bearcli.ContextWithBackend(ctx, emptyApplyBackend{})
		})

		time.Sleep(450 * time.Millisecond)
		if got := cycles.Load(); got != 0 {
			t.Fatalf("cycle count at 450ms = %d, want 0 (second poll token should reset debounce)\nlog:\n%s",
				got, run.Buf.String())
		}
		time.Sleep(150 * time.Millisecond)
		run.cancel()
		<-run.errCh

		if got := cycles.Load(); got != 1 {
			t.Errorf("cycle count after debounce settle = %d, want 1\nlog:\n%s", got, run.Buf.String())
		}
	})
}

// TestDaemonPoll_SelfWriteGateDoesNotSlideOnSuppressedEvents asserts:
// a suppressed mtime advance inside the effective self-write gate does not
// extend the blackout window. A later DB event after the original gate closes
// must still be able to start a new cycle.
func TestDaemonPoll_SelfWriteGateDoesNotSlideOnSuppressedEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,
			base.Add(1 * time.Second),
		}}
		opts := pollOptsFor(t, stat, 1*time.Second, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 100 * time.Millisecond
		run := startPollDaemonRun(t, opts, nil)

		dbPath := filepath.Join(opts.BearDBDir, "database.sqlite")
		run.Watcher.events <- fsnotify.Event{Op: fsnotify.Write, Name: dbPath}

		time.Sleep(4500 * time.Millisecond)
		run.Watcher.events <- fsnotify.Event{Op: fsnotify.Write, Name: dbPath}

		run.WaitFor(1 * time.Second)

		if got := countCycles(run.Buf); got != 2 {
			t.Errorf("cycle count = %d, want 2 (suppressed mtime must not extend self-write gate)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

// TestDaemonPoll_CycleEndResetBaseline asserts the cycle-storm fix:
// after cycleOnce completes, updateDatabaseBaseline locks the poll baseline
// to the post-cycle database.sqlite ModTime. The next poll tick then sees
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
// gate is NOT the mechanism under test; only updateDatabaseBaseline can
// prevent cycle 2):
// - T=+200ms (tick 1, stat call 1 → base): baseline mtime=zero → advance,
// handleEvent → quietTimer.Reset(50ms).
// - T=+250ms (quietTimer fires): cycle 1 runs. updateDatabaseBaseline
// stat call 2 → base+1s, baseline mtime=base+1s. Self-write window
// closes at ~T=300ms.
// - T=+400ms (tick 2, stat call 3 → base+1s [capped]): no advance
// vs baseline mtime, no event, NO cycle.
// - T=+600ms+ (subsequent ticks): same, no advance, no cycle.
//
// Final countCycles == 1. WITHOUT updateDatabaseBaseline this would be
// 2: tick 2 would advance from base→base+1s and fire (self-write
// window already closed at T=300, well before T=400 tick).
func TestDaemonPoll_CycleEndResetBaseline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,                      // tick 1
			base.Add(1 * time.Second), // updateDatabaseBaseline after cycle 1
		}}
		opts := pollOptsFor(t, stat, 200*time.Millisecond, 50*time.Millisecond)
		opts.SelfWriteEpsilon = 50 * time.Millisecond
		run := startPollDaemonRun(t, opts, nil)

		// 1.5s = 7 poll ticks + plenty of debounce headroom. If the
		// baseline reset is broken (storm), cycle count will be 7+.
		run.WaitFor(1500 * time.Millisecond)

		if got := countCycles(run.Buf); got != 1 {
			t.Errorf("cycle count = %d, want 1 (cycle 1 should reset baseline;"+
				" subsequent poll ticks should see no advance and skip)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}
