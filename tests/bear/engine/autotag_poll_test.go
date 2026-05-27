// Package engine_test — auto-tag fast-pass tests for the daemon.
//
// Validates the 7th-case fast-pass body, the skip-while-regen-in-progress
// behavior, the BearcliBackend semaphore (respected via domain.runBearcli,
// no bypass), and the disabled-poll path (AutoTagPollInterval == 0 → no
// ticker, no work). Drives the real Daemon.Run select loop with a fake
// FsWatcher and a fake domain.BearcliBackend stamped on ctx via
// domain.ContextWithBackend. All tests wrap the body in
// testing/synctest.Test so time.NewTicker and the virtual clock advance
// deterministically.
//
// Test seam: domain.BearcliBackend is the SAME seam used by
// tests/bear/engine/apply_parallel_test.go. Both ApplyForeignTagEscape
// and ApplyDailyDefaultTag route their bearcli list/overwrite calls
// through domain.runBearcli, which consults BackendFromContext(ctx) and
// dispatches to the fake. DaemonOpts gains ZERO new test fields — the
// seam is one layer deeper than the daemon, so the daemon never has to
// know about test fakes.
//
// Regen-in-progress test seam: Daemon.SetRegenInProgressForTest mirrors
// the SetBearcliConcurrency precedent — a tiny exported helper that
// lets the test flip d.regenInProgress directly. This avoids
// orchestrating a blocking cycle to hold the flag mid-flight.

//goland:noinspection SpellCheckingInspection
package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// fakeAutoTagBackend records every domain.runBearcli call routed through
// the BearcliBackend seam. Returns canned JSON for "list" (one
// untagged note unless overridden), and {"id":..."ok":true} stub for
// "overwrite". Test scenarios pre-populate listPayload + assert via
// Calls snapshot. Mirrors apply_parallel_test.go::fakeBackend in
// spirit; trimmed for this test's needs (no inflight peak counting —
// the BearcliPool semaphore itself is exercised by TestApplyParallel_*).
type fakeAutoTagBackend struct {
	listPayload    []byte // canned response for "list"
	onRun          func([]string)
	failList       atomic.Int64
	failDomainList atomic.Int64
	failWrite      atomic.Int64

	mu    sync.Mutex
	calls []fakeAutoTagCall
	count atomic.Int64
}

type fakeAutoTagCall struct {
	Kind string // "list" | "overwrite" | other
	Args []string
	Body string // stdin payload — bearcli overwrite carries the note body here
}

func newFakeAutoTagBackend(notes []byte) *fakeAutoTagBackend {
	return &fakeAutoTagBackend{listPayload: notes}
}

// Run satisfies domain.BearcliBackend.
func (f *fakeAutoTagBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	f.count.Add(1)
	kind := "other"
	if len(args) > 0 {
		kind = args[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeAutoTagCall{Kind: kind, Args: append([]string(nil), args...), Body: stdin})
	onRun := f.onRun
	f.mu.Unlock()
	if onRun != nil {
		onRun(append([]string(nil), args...))
	}
	switch kind {
	case "list":
		if f.failList.Load() > 0 && f.failList.Add(-1) >= 0 {
			return nil, errors.New("list failed")
		}
		if valueAfter(args, "--tag") != "" && f.failDomainList.Load() > 0 && f.failDomainList.Add(-1) >= 0 {
			return nil, errors.New("domain list failed")
		}
		f.mu.Lock()
		payload := append([]byte(nil), f.listPayload...)
		f.mu.Unlock()
		return payload, nil
	case "show":
		// overwriteWithRetry calls ShowHash first to obtain the
		// optimistic-concurrency hash; empty-hash is treated as fault
		// (bear/bearcli/overwrite.go::ShowHash). Return a stable
		// non-empty hash so the
		// downstream "overwrite" call proceeds and the test can observe it.
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		if f.failWrite.Load() > 0 && f.failWrite.Add(-1) >= 0 {
			return nil, errors.New("overwrite failed")
		}
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

func (f *fakeAutoTagBackend) SetListPayload(notes []byte) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listPayload = append([]byte(nil), notes...)
}

func (f *fakeAutoTagBackend) FailNextList() {
	f.failList.Add(1)
}

func (f *fakeAutoTagBackend) FailNextDomainList() {
	f.failDomainList.Add(1)
}

func (f *fakeAutoTagBackend) FailNextOverwrite() {
	f.failWrite.Add(1)
}

func (f *fakeAutoTagBackend) TotalCalls() int64 {
	return f.count.Load()
}

func (f *fakeAutoTagBackend) CountKind(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// untaggedListPayload returns the JSON bearcli list emits for one
// untagged quicknote. Matches autoTagNote shape in bear/autotag.go.
func untaggedListPayload(t *testing.T) []byte {
	t.Helper()
	return untaggedListPayloadWithIDs(t, "abc123")
}

func untaggedListPayloadWithIDs(t *testing.T, ids ...string) []byte {
	t.Helper()
	notes := make([]map[string]any, 0, len(ids))
	for _, id := range ids {
		notes = append(notes, map[string]any{
			"id":      id,
			"title":   "Нова нотатка",
			"tags":    []string{}, // empty → ApplyDailyDefaultTag stamps
			"content": "Body of a brand new quicknote\n",
		})
	}
	raw, err := json.Marshal(notes)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// autoTagOptsFor builds a DaemonOpts wired for auto-tag synctest
// scenarios. SkipFlock=true mirrors apply_parallel_test.go/pollOptsFor —
// flock is not ctx-aware and synctest cannot virtualize the syscall.
// MtimePollInterval=0 disables the mtime poll loop so cycle counts are
// driven exclusively by the fast-pass under test.
func autoTagOptsFor(t *testing.T, pollInterval time.Duration, features engine.Features) engine.DaemonOpts {
	t.Helper()
	// canonical-bootstrap requires the daily-default fast-pass
	// to receive a non-nil *Domain matching `quicknote/daily`. Register
	// the real DailyDomain here so domainsByTag["quicknote/daily"]
	// resolves correctly inside handleAutoTagTick.
	return autoTagOptsForDomains(t, pollInterval, features, []*domain.Domain{testutil.Domain(t, "quicknote/daily")})
}

// countAutoTagStamps counts "auto-tag:" prefix lines emitted by
// fastpass.ApplyDailyDefaultTag (see bear/autotag.go:56). Each occurrence
// equals one note stamped with #quicknote/daily.
func countAutoTagStamps(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "auto-tag:")
}

// countForeignEscapes counts "foreign-tag escape:" prefix lines from
// fastpass.ApplyForeignTagEscape (see bear/foreigntag.go). Each = one
// foreign-tag escape applied.
func countForeignEscapes(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "foreign-tag escape:")
}

// daemonRun bundles every handle a synctest scenario needs after the
// daemon goroutine is live. Construct via startDaemonRun; advance
// virtual time via WaitFor; access Daemon for mid-flight state writes
// (e.g. SetRegenInProgressForTest), Buf for log assertions.
type daemonRun struct {
	Daemon *engine.Daemon
	Buf    *bytes.Buffer
	cancel context.CancelFunc
	errCh  <-chan error
}

// startDaemonRun centralizes the synctest-based daemon test scaffold:
// reset bearcli pool, build a fake watcher + daemon, capture log
// output, attach the supplied BearcliBackend onto ctx, then start
// d.Run in a goroutine. Returned daemonRun must have WaitFor called
// to cancel ctx and drain the goroutine. Optional `before` callback
// fires AFTER daemon construction but BEFORE the goroutine starts,
// so tests can prime mid-flight state (regenInProgress) without a
// race against the first tick.
func startDaemonRun(t *testing.T, fake *fakeAutoTagBackend, opts engine.DaemonOpts, before func(d *engine.Daemon)) *daemonRun {
	t.Helper()
	resetPoolForApply(t)
	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	t.Cleanup(func() { _ = d.Close() })

	buf := captureLog(t)
	ctx, cancel := context.WithCancel(t.Context())
	ctx = domain.ContextWithBackend(ctx, fake)
	t.Cleanup(cancel)

	if before != nil {
		before(d)
	}

	errCh := make(chan error, 1)
	go func() { errCh <- d.Run(ctx) }()
	return &daemonRun{Daemon: d, Buf: buf, cancel: cancel, errCh: errCh}
}

// WaitFor advances virtual time by `dur`, then cancels ctx and waits
// for the daemon goroutine to return. Idempotent under repeated
// cancel (context.WithCancel allows it).
func (r *daemonRun) WaitFor(dur time.Duration) {
	time.Sleep(dur)
	r.cancel()
	<-r.errCh
}

// TestDaemonAutoTagPoll_TickFires asserts: a fast-pass tick invokes
// ApplyForeignTagEscape + ApplyDailyDefaultTag via the BearcliBackend
// seam. With one untagged note injected via the fake list payload, the
// daily-default pass stamps it (one "overwrite" call), then the daemon
// runs one follow-up apply.
func TestDaemonAutoTagPoll_TickFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		fake.onRun = func(args []string) {
			if len(args) > 0 && args[0] == "overwrite" {
				fake.SetListPayload([]byte("[]"))
			}
		}
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(500 * time.Millisecond)
		buf := run.Buf

		if got := fake.CountKind("overwrite"); got != 1 {
			t.Errorf("overwrite count = %d, want 1 (one note stamped #quicknote/daily once)\nlog:\n%s",
				got, buf.String())
		}
		if cycles := countCycles(buf); cycles != 1 {
			t.Errorf("cycle count = %d, want 1 (one follow-up apply after fast-pass write)\nlog:\n%s",
				cycles, buf.String())
		}
		if listN := fake.CountKind("list"); listN < 1 {
			t.Errorf("list call count = %d, want >= 1 (fast-pass must list notes on DB mtime advance)", listN)
		}
	})
}

func TestDaemonAutoTagPoll_IdleMtimeSkipsBearcli(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		idleMtime := time.Unix(1, 0)
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		opts.StatFn = func(string) (os.FileInfo, error) { return fakeFileInfo{mt: idleMtime}, nil }
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(500 * time.Millisecond)

		if got := fake.TotalCalls(); got != 0 {
			t.Errorf("backend calls while DB mtime is idle = %d, want 0\nlog:\n%s", got, run.Buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_MtimeNoiseWithoutTokenChangeSkipsBearcli(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		base := time.Now()
		stat := &fakeStat{mtimes: []time.Time{
			base,
			base.Add(1 * time.Second),
			base.Add(2 * time.Second),
		}}
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		opts.StatFn = stat.Stat
		opts.DatabaseChangeTokenFn = func(string, os.FileInfo) (string, error) {
			return "stable", nil
		}
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(500 * time.Millisecond)

		if got := fake.TotalCalls(); got != 0 {
			t.Errorf("backend calls while only DB mtime changes = %d, want 0\nlog:\n%s", got, run.Buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_FastPassFailureRetriesSameToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend([]byte("[]"))
		fake.FailNextList()
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(350 * time.Millisecond)

		if got := fake.CountKind("list"); got != 8 {
			t.Errorf("list call count = %d, want 8 (failed tick + one successful retry, then pending clears)\nlog:\n%s",
				got, run.Buf.String())
		}
		if cycles := countCycles(run.Buf); cycles != 0 {
			t.Errorf("cycle count = %d, want 0 (no-write retry should not trigger follow-up apply)\nlog:\n%s",
				cycles, run.Buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_PerNoteFailureRetriesSameToken(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		fake.FailNextOverwrite()
		fake.onRun = func(args []string) {
			if len(args) > 0 && args[0] == "overwrite" && fake.CountKind("overwrite") >= 2 {
				fake.SetListPayload([]byte("[]"))
			}
		}
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(350 * time.Millisecond)

		if got := fake.CountKind("overwrite"); got != 2 {
			t.Errorf("overwrite count = %d, want 2 (failed write + one successful retry, then pending clears)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_MixedWriteFailureRetriesFailedNote(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayloadWithIDs(t, "failed-note", "ok-note"))
		fake.FailNextOverwrite()
		fake.onRun = func(args []string) {
			if len(args) == 0 || args[0] != "overwrite" {
				return
			}
			switch fake.CountKind("overwrite") {
			case 2:
				fake.SetListPayload(untaggedListPayloadWithIDs(t, "failed-note"))
			case 3:
				fake.SetListPayload([]byte("[]"))
			}
		}
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(450 * time.Millisecond)

		if got := fake.CountKind("overwrite"); got != 3 {
			t.Errorf("overwrite count = %d, want 3 (failed note must retry after sibling write succeeds)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_FollowUpApplyFailureRetriesCycle(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		fake.FailNextDomainList()
		fake.onRun = func(args []string) {
			if len(args) > 0 && args[0] == "overwrite" {
				fake.SetListPayload([]byte("[]"))
			}
		}
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(350 * time.Millisecond)

		if cycles := countCycles(run.Buf); cycles != 2 {
			t.Errorf("cycle count = %d, want 2 (failed follow-up apply must retry before baseline commit)\nlog:\n%s",
				cycles, run.Buf.String())
		}
		if got := fake.CountKind("overwrite"); got != 1 {
			t.Errorf("overwrite count = %d, want 1 (retry should rerun apply, not the already-successful fast-pass)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_PreservesReadOnlyDBEvents(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend([]byte("[]"))
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		fw := newFakeWatcher()
		dbPath := filepath.Join(opts.BearDBDir, "database.sqlite")
		var injected atomic.Bool
		fake.onRun = func(args []string) {
			if len(args) == 0 || args[0] != "list" || !injected.CompareAndSwap(false, true) {
				return
			}
			fw.events <- fsnotify.Event{Op: fsnotify.Write, Name: dbPath}
		}

		resetPoolForApply(t)
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })
		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		ctx = domain.ContextWithBackend(ctx, fake)
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		time.Sleep(500 * time.Millisecond)
		cancel()
		<-errCh

		if listN := fake.CountKind("list"); listN < 1 {
			t.Fatalf("list call count = %d, want >= 1", listN)
		}
		if cycles := countCycles(buf); cycles != 1 {
			t.Errorf("cycle count = %d, want 1 (queued DB write must stay on the normal debounce path)\nlog:\n%s",
				cycles, buf.String())
		}
	})
}

func TestDaemonAutoTagPoll_SkipsWhileBurstActive(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend([]byte("[]"))
		opts := autoTagOptsFor(t, 50*time.Millisecond, engine.AllFeaturesOn())
		opts.DebouncePause = 200 * time.Millisecond
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })
		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		ctx = domain.ContextWithBackend(ctx, fake)
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		fw.events <- fsnotify.Event{
			Op:   fsnotify.Write,
			Name: filepath.Join(opts.BearDBDir, "database.sqlite"),
		}
		time.Sleep(150 * time.Millisecond)
		cancel()
		<-errCh

		if got := fake.TotalCalls(); got != 0 {
			t.Errorf("backend calls while burstActive=true = %d, want 0 (full apply is already pending)\nlog:\n%s",
				got, buf.String())
		}
	})
}

// TestDaemonAutoTagPoll_SkippedWhenRegenInProgress asserts:
// while d.regenInProgress is true, fast-pass ticks return silently
// without invoking the BearcliBackend at all. Clearing the flag must
// allow the next tick to fire normally. Uses the
// SetRegenInProgressForTest seam — mirrors the
// SetBearcliConcurrency precedent.
func TestDaemonAutoTagPoll_SkippedWhenRegenInProgress(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		opts := autoTagOptsFor(t, 50*time.Millisecond, engine.AllFeaturesOn())
		// Flip regenInProgress=true BEFORE the goroutine starts so the
		// very first tick observes it as set — same technique as
		// SetBearcliConcurrency: a tiny test-only setter on Daemon.
		run := startDaemonRun(t, fake, opts, func(d *engine.Daemon) {
			d.SetRegenInProgressForTest(true)
		})
		buf := run.Buf

		// Advance through ~4 fast-pass ticks while flag is true.
		time.Sleep(250 * time.Millisecond)

		if got := fake.TotalCalls(); got != 0 {
			t.Errorf("backend total calls while regenInProgress=true = %d, want 0 "+
				"(fast-pass must skip silently)\nlog:\n%s",
				got, buf.String())
		}

		// Clear the flag; next tick must fire normally.
		run.Daemon.SetRegenInProgressForTest(false)
		run.WaitFor(200 * time.Millisecond)

		if listN := fake.CountKind("list"); listN < 1 {
			t.Errorf("list count after clearing regenInProgress = %d, want >= 1 "+
				"(fast-pass must resume on next tick)\nlog:\n%s",
				listN, buf.String())
		}
	})
}

// TestDaemonAutoTagPoll_FeatureToggleRespected asserts feature
// gating: opts.Features.AutoTagDefault and opts.Features.ForeignTagEscape
// are honored independently. Sub-case (a): only foreign-tag escape runs.
// Sub-case (b): only daily-default runs.
func TestDaemonAutoTagPoll_FeatureToggleRespected(t *testing.T) {
	t.Run("only_foreign_escape", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := newFakeAutoTagBackend(untaggedListPayload(t))
			feats := engine.AllFeaturesOn()
			feats.AutoTagDefault = false // OFF — only foreign-tag escape should run
			opts := autoTagOptsFor(t, 100*time.Millisecond, feats)
			run := startDaemonRun(t, fake, opts, nil)
			run.WaitFor(300 * time.Millisecond)
			buf := run.Buf

			if stamps := countAutoTagStamps(buf); stamps != 0 {
				t.Errorf("auto-tag stamp count = %d, want 0 (Features.AutoTagDefault=false)\nlog:\n%s",
					stamps, buf.String())
			}
			// Foreign-tag escape may or may not log depending on note shape;
			// assert it was at least attempted via a list call.
			if fake.CountKind("list") < 1 {
				t.Errorf("list count = %d, want >= 1 (foreign-tag escape must still run)", fake.CountKind("list"))
			}
		})
	})

	t.Run("only_daily_default", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			fake := newFakeAutoTagBackend(untaggedListPayload(t))
			feats := engine.AllFeaturesOn()
			feats.ForeignTagEscape = false // OFF — only daily-default should run
			opts := autoTagOptsFor(t, 100*time.Millisecond, feats)
			run := startDaemonRun(t, fake, opts, nil)
			run.WaitFor(300 * time.Millisecond)
			buf := run.Buf

			if escapes := countForeignEscapes(buf); escapes != 0 {
				t.Errorf("foreign-tag escape count = %d, want 0 (Features.ForeignTagEscape=false)\nlog:\n%s",
					escapes, buf.String())
			}
			if stamps := countAutoTagStamps(buf); stamps < 1 {
				t.Errorf("auto-tag stamp count = %d, want >= 1 (daily-default must run on the untagged note)\nlog:\n%s",
					stamps, buf.String())
			}
		})
	})
}

// TestDaemonAutoTagPoll_Disabled asserts the disabled sentinel:
// AutoTagPollInterval == 0 → no ticker → no list → no overwrite.
// Mirrors mtime_poll_test.go::TestDaemonPoll_ZeroDisables shape.
func TestDaemonAutoTagPoll_Disabled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		opts := autoTagOptsFor(t, 0, engine.AllFeaturesOn()) // AutoTagPollInterval == 0 → disabled
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(5 * time.Second) // far longer than the default 2s ticker would fire

		if got := fake.TotalCalls(); got != 0 {
			t.Errorf("backend total calls = %d, want 0 (AutoTagPollInterval=0 must disable fast-pass entirely)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

// autoTagOptsForDomains is a variant of autoTagOptsFor that
// accepts a caller-supplied domain set. The 4th `domain-bootstrap`
// fast-pass needs at least one non-`quicknote/daily` leaf domain in
// scope (e.g. `#library/aphorisms`) so the canonicalization path
// actually rewrites a note. Keeps the rest of the DaemonOpts shape
// identical to `autoTagOptsFor` so existing wiring (SkipFlock,
// MtimePollInterval=0, etc.) carries through.
func autoTagOptsForDomains(t *testing.T, pollInterval time.Duration, features engine.Features, domains []*domain.Domain) engine.DaemonOpts {
	t.Helper()
	base := time.Now()
	stat := &fakeStat{mtimes: []time.Time{base, base.Add(1 * time.Second)}}
	inner := applyOptsFor(t, domains)
	inner.SkipFlock = true
	inner.Features = features
	return engine.DaemonOpts{
		ApplyOpts:           inner,
		BearDBDir:           t.TempDir(),
		DebouncePause:       50 * time.Millisecond,
		MaxBurstWindow:      10 * time.Second,
		SelfWriteEpsilon:    2 * time.Second,
		MtimePollInterval:   0,
		AutoTagPollInterval: pollInterval,
		StatFn:              stat.Stat,
	}
}

// aphorismListPayload builds the bearcli list JSON for one note tagged
// `#library/aphorisms` with a non-canonical body — exactly the shape
// `ApplyDomainBootstrap` rewrites via the `#library/aphorisms` leaf's
// `RenderCanonicalForBootstrap`. Mirrors the `aph-1` fixture in
// `bootstrap_pass_test.go::TestApplyDomainBootstrap_LeafDomain_GroupedVerticalFlat`.
func aphorismListPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{
		"id":      "aph-1",
		"title":   "Орвелл",
		"tags":    []string{"#library/aphorisms"},
		"content": "War is peace.\n",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// fourPassDaemonOpts returns the canonical DaemonOpts shape used by
// every four-pass autotag-tick test in this file: one virtual tick
// every 200ms, all features on, with the daily + aphorisms leaves
// in scope. Extracted so per-test variants (gate-split asserts,
// flag-off asserts) can tweak a single field without duplicating
// the full constructor — that duplication tripped the dupl linter
// when sibling tests differed only in one DaemonOpts field.
func fourPassDaemonOpts(t *testing.T, features engine.Features) engine.DaemonOpts {
	t.Helper()
	return autoTagOptsForDomains(t, 200*time.Millisecond, features,
		[]*domain.Domain{testutil.Domain(t, "quicknote/daily"), testutil.Domain(t, "library/aphorisms")})
}

// TestDaemonAutoTagPoll_FourPassesInOrder asserts:
// when `Features.DomainBootstrap=true`, the per-tick `passes` slice in
// `handleAutoTagTick` runs all FOUR pre-passes — foreign-tag escape,
// daily-default, domain-bootstrap, placeholder-refresh — each issuing
// one `bearcli list` per tick. The empty payload keeps this focused on
// pass order: no pass has a note to rewrite, so no follow-up apply runs.
func TestDaemonAutoTagPoll_FourPassesInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend([]byte("[]"))
		// Pin a single tick: ticker=200ms + sleep=300ms under synctest
		// virtual time advances past exactly one fire at the 200ms mark.
		opts := fourPassDaemonOpts(t, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(300 * time.Millisecond) // one virtual tick at 200ms
		buf := run.Buf

		if got := fake.CountKind("list"); got != 4 {
			t.Errorf("list call count = %d, want 4 "+
				"(foreign-tag + daily-default + domain-bootstrap + placeholder-refresh)\nlog:\n%s", got, buf.String())
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 (no-write payload keeps this test focused on pass order)\nlog:\n%s",
				got, buf.String())
		}
		if cycles := countCycles(buf); cycles != 0 {
			t.Errorf("cycle count = %d, want 0 (no-write fast-pass should not trigger follow-up apply)\nlog:\n%s",
				cycles, buf.String())
		}
	})
}

// TestDaemonAutoTagPoll_DailyDefaultTagEmptyGate pins the daemon-side
// half of the catalog-driven silent-disable contract: when an
// operator omits [meta].daily_default_tag (DailyDefaultTag == ""),
// the daily-default fast-pass is skipped while placeholder-refresh
// keeps running. Without this guard, the daemon would log a
// `daily-default failed: dailyDomain is nil` line every poll tick,
// AND a regression that folded both gates back together would
// silently disable placeholder refresh for OSS catalogs that set
// `quick_placeholder_h1` on a domain without declaring the daily
// tag.
//
// Asserts on the daemon analog to TestApply_AutoTagGatedOnDailyDefault
// Tag (one-shot path): list count drops from 4 to 3 (daily-default
// dropped; foreign-tag + domain-bootstrap + placeholder-refresh
// remain), and no `auto-tag:` stamp lines appear in the log.
func TestDaemonAutoTagPoll_DailyDefaultTagEmptyGate(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend([]byte("[]"))
		opts := fourPassDaemonOpts(t, engine.AllFeaturesOn())
		// Override the default DailyDefaultTag seeded by applyOptsFor
		// to assert the empty-tag silent-disable contract directly.
		opts.DailyDefaultTag = ""
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(300 * time.Millisecond) // one virtual tick at 200ms
		buf := run.Buf

		if got := fake.CountKind("list"); got != 3 {
			t.Errorf("list call count = %d, want 3 (DailyDefaultTag=\"\" → daily-default skipped; "+
				"foreign-tag + domain-bootstrap + placeholder-refresh must still run)\nlog:\n%s",
				got, buf.String())
		}
		if stamps := countAutoTagStamps(buf); stamps != 0 {
			t.Errorf("auto-tag stamp count = %d, want 0 (empty DailyDefaultTag must silently disable daily-default)\nlog:\n%s",
				stamps, buf.String())
		}
		if strings.Contains(buf.String(), "dailyDomain is nil") {
			t.Errorf("log must not mention 'dailyDomain is nil' under the empty-tag gate; got:\n%s", buf.String())
		}
	})
}

// TestDaemonAutoTagPoll_DomainBootstrapFlagOff asserts:
// when `Features.DomainBootstrap=false`, the 4th `domain-bootstrap`
// pass is skipped entirely — no `bearcli list` charged, no overwrite
// fired against the aphorism note. The other three passes still run
// (foreign-tag escape + daily-default + placeholder-refresh) so the
// list count drops from 4 to 3.
func TestDaemonAutoTagPoll_DomainBootstrapFlagOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(aphorismListPayload(t))
		feats := engine.AllFeaturesOn()
		feats.DomainBootstrap = false
		opts := fourPassDaemonOpts(t, feats)
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(300 * time.Millisecond) // one virtual tick at 200ms
		buf := run.Buf

		if got := fake.CountKind("list"); got != 3 {
			t.Errorf("list call count = %d, want 3 (DomainBootstrap=false → bootstrap pass skipped, other 3 still run)\nlog:\n%s",
				got, buf.String())
		}
		if got := fake.CountKind("overwrite"); got != 0 {
			t.Errorf("overwrite count = %d, want 0 "+
				"(aphorism note has #library/aphorisms tag → daily-default skips it; "+
				"no other pass rewrites it when DomainBootstrap=false)\nlog:\n%s",
				got, buf.String())
		}
	})
}
