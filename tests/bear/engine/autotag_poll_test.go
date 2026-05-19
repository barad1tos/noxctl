// Package engine_test — auto-tag fast-pass tests for the daemon.
//
// Validates the 7th-case fast-pass body, the skip-while-regen-in-progress
// behavior, the BearcliBackend semaphore (respected via bear.runBearcli,
// no bypass), and the disabled-poll path (AutoTagPollInterval == 0 → no
// ticker, no work). Drives the real Daemon.Run select loop with a fake
// FsWatcher and a fake bear.BearcliBackend stamped on ctx via
// bear.ContextWithBackend. All tests wrap the body in
// testing/synctest.Test so time.NewTicker and the virtual clock advance
// deterministically.
//
// Test seam: bear.BearcliBackend is the SAME seam used by
// tests/bear/engine/apply_parallel_test.go. Both ApplyForeignTagEscape
// and ApplyDailyDefaultTag route their bearcli list/overwrite calls
// through bear.runBearcli, which consults BackendFromContext(ctx) and
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
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// fakeAutoTagBackend records every bear.runBearcli call routed through
// the BearcliBackend seam. Returns canned JSON for "list" (one
// untagged note unless overridden), and {"id":..."ok":true} stub for
// "overwrite". Test scenarios pre-populate listPayload + assert via
// Calls snapshot. Mirrors apply_parallel_test.go::fakeBackend in
// spirit; trimmed for this test's needs (no inflight peak counting —
// the BearcliPool semaphore itself is exercised by TestApplyParallel_*).
type fakeAutoTagBackend struct {
	listPayload []byte // canned response for "list"

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

// Run satisfies bear.BearcliBackend.
func (f *fakeAutoTagBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	f.count.Add(1)
	kind := "other"
	if len(args) > 0 {
		kind = args[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeAutoTagCall{Kind: kind, Args: append([]string(nil), args...), Body: stdin})
	f.mu.Unlock()
	switch kind {
	case "list":
		return f.listPayload, nil
	case "show":
		// overwriteWithRetry calls showHash first to obtain the
		// optimistic-concurrency hash; empty-hash is treated as fault
		// (bear/core.go:120). Return a stable non-empty hash so the
		// downstream "overwrite" call proceeds and the test can observe it.
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
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
// untagged quicknote. Matches autoTagNote shape in bear/auto_tag.go.
func untaggedListPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{
		"id":      "abc123",
		"title":   "Нова нотатка",
		"tags":    []string{}, // empty → ApplyDailyDefaultTag stamps
		"content": "Body of a brand new quicknote\n",
	}})
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
	return autoTagOptsForDomains(t, pollInterval, features, []*bear.Domain{testutil.Domain(t, "quicknote/daily")})
}

// countAutoTagStamps counts "auto-tag:" prefix lines emitted by
// bear.ApplyDailyDefaultTag (see bear/auto_tag.go:56). Each occurrence
// equals one note stamped with #quicknote/daily.
func countAutoTagStamps(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "auto-tag:")
}

// countForeignEscapes counts "foreign-tag escape:" prefix lines from
// bear.ApplyForeignTagEscape (see bear/foreign_tag.go). Each = one
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
	ctx = bear.ContextWithBackend(ctx, fake)
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
// seam — but NEVER invokes cycleOnce. With one untagged note injected
// via the fake list payload, the daily-default pass stamps it (one
// "overwrite" call), and no "regen trigger:" log line is emitted.
func TestDaemonAutoTagPoll_TickFires(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(untaggedListPayload(t))
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(500 * time.Millisecond) // ~4 ticks of the 100ms ticker
		buf := run.Buf

		// At least one overwrite must fire (the daily-default pass stamps
		// the untagged note). Use >=1 rather than ==1 because the fake
		// list payload does NOT simulate idempotency (real
		// ApplyDailyDefaultTag skips notes whose Tags is already
		// populated; the fake keeps returning the same untagged shape
		// each tick). The contract is "fast-pass invokes the pre-passes",
		// not "exactly one stamp" — exactness comes from production
		// behavior, not the fake.
		if got := fake.CountKind("overwrite"); got < 1 {
			t.Errorf("overwrite count = %d, want >= 1 (one note stamped #quicknote/daily per tick)\nlog:\n%s",
				got, buf.String())
		}
		if cycles := countCycles(buf); cycles != 0 {
			t.Errorf("cycle count = %d, want 0 (fast-pass MUST NOT invoke cycleOnce)\nlog:\n%s",
				cycles, buf.String())
		}
		if listN := fake.CountKind("list"); listN < 1 {
			t.Errorf("list call count = %d, want >= 1 (fast-pass must list notes each tick)", listN)
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
func autoTagOptsForDomains(t *testing.T, pollInterval time.Duration, features engine.Features, domains []*bear.Domain) engine.DaemonOpts {
	t.Helper()
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
		[]*bear.Domain{testutil.Domain(t, "quicknote/daily"), testutil.Domain(t, "library/aphorisms")})
}

// TestDaemonAutoTagPoll_FourPassesInOrder asserts:
// when `Features.DomainBootstrap=true`, the per-tick `passes` slice in
// `handleAutoTagTick` runs all FOUR pre-passes — foreign-tag escape,
// daily-default, domain-bootstrap, placeholder-refresh — each issuing
// one `bearcli list` per tick. Drives a single virtual tick under
// `synctest` and asserts:
// - `list` count == 4 (one per pass; equality pins the ordinal slot)
// - `overwrite` count >= 1 (the aphorism note got canonicalized by
// the 4th pass via the `#library/aphorisms` leaf).
func TestDaemonAutoTagPoll_FourPassesInOrder(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend(aphorismListPayload(t))
		// Pin a single tick: ticker=200ms + sleep=300ms under synctest
		// virtual time advances past exactly one fire at the 200ms mark.
		opts := fourPassDaemonOpts(t, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		run.WaitFor(300 * time.Millisecond) // one virtual tick at 200ms
		buf := run.Buf

		if got := fake.CountKind("list"); got != 4 {
			t.Errorf("list call count = %d, want 4 "+
				"(foreign-tag + daily-default + domain-bootstrap + placeholder-refresh, "+
				"one each per tick)\nlog:\n%s", got, buf.String())
		}
		if got := fake.CountKind("overwrite"); got < 1 {
			t.Errorf("overwrite count = %d, want >= 1 (domain-bootstrap must canonicalize the aphorism note)\nlog:\n%s",
				got, buf.String())
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
		fake := newFakeAutoTagBackend(aphorismListPayload(t))
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
