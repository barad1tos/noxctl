// Package engine_test pins the cycle-telemetry contract: exactly one
// structured key=value summary line emitted at REGEN cycle completion (both
// `noxctl apply --once` and the daemon's FSEvent/poll-triggered cycleOnce),
// and ZERO lines across the ~2s auto-tag fast-pass tick.
//
// Task 1 exercises the pure formatter via the EmitCycleTelemetryForTest export
// seam (mirroring the ComputeContentHash directory-gap convention — external
// tests live under tests/bear/engine/ and cannot reach unexported engine
// symbols). It pins the field set, the avg_queue_ms math, the top-N ordering,
// and the security invariant that no note title/body/hash ever reaches the line.
// Integration emit-count + no-emit-on-tick assertions follow below.
package engine_test

import (
	"bytes"
	"context"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// countTelemetryLines counts the `regen cycle:` summary lines in the
// captured log. Distinct from countCycles, which counts `regen trigger:` —
// cycleOnce emits one trigger line at entry and engine.Apply emits one
// telemetry line at completion, so the two counts coincide for completed
// cycles but the telemetry count is the one this contract pins.
func countTelemetryLines(buf *bytes.Buffer) int {
	return strings.Count(buf.String(), "regen cycle:")
}

// telemetryMetricsFixture returns a bearcli.Metrics snapshot with distinct,
// recognizable values for every field the telemetry line surfaces. Every
// numeric field is set to a unique value so a missing/mis-keyed field shows up
// as an assertion miss rather than a coincidental match.
func telemetryMetricsFixture() bearcli.Metrics {
	return bearcli.Metrics{
		Capacity:       8,
		PeakConcurrent: 6,
		AcquireCount:   10,
		// 250ms total wait across 10 acquires => avg 25ms.
		WaitNanosSum: (250 * time.Millisecond).Nanoseconds(),
		CallsByKind: map[string]int64{
			"list":      3,
			"cat":       7,
			"overwrite": 2,
			"create":    1,
			"show":      4,
		},
		HashConflictsTotal: 5,
		RetriesSucceeded:   2,
		RetriesFailed:      1,
	}
}

// timingSlice builds the per-domain timing input the telemetry formatter
// accepts via the test seam (tag + elapsed pairs). Returned in arbitrary order
// so the formatter's DESC-by-elapsed sort is exercised.
func timingSlice() []engine.DomainTimingForTest {
	return []engine.DomainTimingForTest{
		{Tag: "library/poetry", Elapsed: 30 * time.Millisecond},
		{Tag: "library/lyrics", Elapsed: 120 * time.Millisecond},
		{Tag: "llm/agents", Elapsed: 75 * time.Millisecond},
		{Tag: "it/vendors", Elapsed: 10 * time.Millisecond},
		{Tag: "library/prose", Elapsed: 90 * time.Millisecond},
		{Tag: "library/quotes", Elapsed: 5 * time.Millisecond},
	}
}

// TestCycleTelemetry_FieldSet asserts the emitted line carries every required
// key=value substring and that the avg_queue_ms math is correct.
func TestCycleTelemetry_FieldSet(t *testing.T) {
	var buf bytes.Buffer
	engine.EmitCycleTelemetryForTest(&buf, telemetryMetricsFixture(), timingSlice(), 500*time.Millisecond)
	line := buf.String()

	if got := strings.Count(strings.TrimRight(line, "\n"), "\n"); got != 0 {
		t.Fatalf("telemetry emitted %d extra newlines; want exactly one line\nline:\n%s", got, line)
	}

	wantSubstrings := []string{
		"regen cycle:",
		"wall=500ms",
		"calls_list=3",
		"calls_cat=7",
		"calls_overwrite=2",
		"calls_create=1",
		"peak_concurrency=6",
		"avg_queue_ms=25.0",
		"hash_conflicts=5",
		"retries_ok=2",
		"retries_fail=1",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(line, want) {
			t.Errorf("telemetry line missing %q\nline:\n%s", want, line)
		}
	}

	// Memory fields are present and keyed (values are runtime-dependent, so
	// assert the keys exist, not a fixed magnitude).
	for _, key := range []string{"heap_alloc_mb=", "sys_mb="} {
		if !strings.Contains(line, key) {
			t.Errorf("telemetry line missing memory key %q\nline:\n%s", key, line)
		}
	}
}

// TestCycleTelemetry_AvgQueueZeroOnNoAcquires pins the divide-by-zero guard:
// AcquireCount==0 must yield avg_queue_ms=0.0, never NaN/panic.
func TestCycleTelemetry_AvgQueueZeroOnNoAcquires(t *testing.T) {
	var buf bytes.Buffer
	m := bearcli.Metrics{CallsByKind: map[string]int64{}}
	engine.EmitCycleTelemetryForTest(&buf, m, nil, 0)
	if line := buf.String(); !strings.Contains(line, "avg_queue_ms=0.0") {
		t.Errorf("avg_queue_ms with zero acquires = %q, want 0.0", line)
	}
}

// TestCycleTelemetry_SlowestTopNDescByTag asserts slowest renders the top-5
// per-domain timings DESC by elapsed, keyed on d.Tag, space-joined, and that
// the 6th-slowest (library/quotes, 5ms) is dropped.
func TestCycleTelemetry_SlowestTopNDescByTag(t *testing.T) {
	var buf bytes.Buffer
	engine.EmitCycleTelemetryForTest(&buf, telemetryMetricsFixture(), timingSlice(), 500*time.Millisecond)
	line := buf.String()

	_, after, found := strings.Cut(line, "slowest=")
	if !found {
		t.Fatalf("telemetry line missing slowest= field\nline:\n%s", line)
	}
	slowest := strings.TrimRight(after, "\n")

	// Top-5 DESC by elapsed: lyrics(120) prose(90) agents(75) poetry(30) vendors(10).
	wantOrder := []string{
		"library/lyrics=120ms",
		"library/prose=90ms",
		"llm/agents=75ms",
		"library/poetry=30ms",
		"it/vendors=10ms",
	}
	prev := -1
	for _, tok := range wantOrder {
		at := strings.Index(slowest, tok)
		if at < 0 {
			t.Errorf("slowest missing token %q\nslowest:\n%s", tok, slowest)
			continue
		}
		if at <= prev {
			t.Errorf("slowest token %q out of DESC order (at %d, prev %d)\nslowest:\n%s", tok, at, prev, slowest)
		}
		prev = at
	}

	// 6th-slowest must be dropped (top-5 only).
	if strings.Contains(slowest, "library/quotes") {
		t.Errorf("slowest included the 6th domain (library/quotes); want top-5 only\nslowest:\n%s", slowest)
	}
}

// TestCycleTelemetry_FewerThanNAndEmpty pins the edge cases: a slice shorter
// than N lists all of them; an empty slice yields an empty slowest with no panic.
func TestCycleTelemetry_FewerThanNAndEmpty(t *testing.T) {
	var buf bytes.Buffer
	short := []engine.DomainTimingForTest{
		{Tag: "library/poetry", Elapsed: 20 * time.Millisecond},
		{Tag: "llm/agents", Elapsed: 40 * time.Millisecond},
	}
	engine.EmitCycleTelemetryForTest(&buf, telemetryMetricsFixture(), short, 100*time.Millisecond)
	line := buf.String()
	if !strings.Contains(line, "llm/agents=40ms") || !strings.Contains(line, "library/poetry=20ms") {
		t.Errorf("short slice should list all entries\nline:\n%s", line)
	}

	buf.Reset()
	engine.EmitCycleTelemetryForTest(&buf, telemetryMetricsFixture(), nil, 100*time.Millisecond)
	if empty := buf.String(); !strings.Contains(empty, "slowest=") {
		t.Errorf("empty timings must still emit a (possibly empty) slowest= field\nline:\n%s", empty)
	}
}

// TestCycleTelemetry_NoVaultContentLeak is the security pin: given a
// metrics snapshot and per-domain timings keyed only on catalog tags, the
// emitted line must contain NO note title, body, or hash substring. We feed
// recognizable sentinel strings as note-shaped data NOWHERE in the telemetry
// inputs and assert their absence — the formatter only ever sees counts,
// timings, memory, and d.Tag, so a leak would require the formatter to invent
// content it never received.
func TestCycleTelemetry_NoVaultContentLeak(t *testing.T) {
	var buf bytes.Buffer
	engine.EmitCycleTelemetryForTest(&buf, telemetryMetricsFixture(), timingSlice(), 500*time.Millisecond)
	line := buf.String()

	// These are the shapes a leak would take: a markdown H1 title, a body
	// fragment, or a 64-hex content hash. None are inputs to the formatter.
	forbidden := []string{
		"# ",       // markdown H1 (note title)
		"---",      // canonical separator (note body)
		"bear://",  // x-callback URL (note body)
		"deadbeef", // hash-shaped sentinel
		"[[",       // wikilink (note body)
	}
	for _, frag := range forbidden {
		if strings.Contains(line, frag) {
			t.Errorf("telemetry line leaked vault-content shape %q\nline:\n%s", frag, line)
		}
	}
}

// TestApply_EmitsCycleTelemetry pins the integration emit: ONE engine.Apply run
// over a fixture domain set emits EXACTLY ONE `regen cycle:` line at cycle
// completion, with WithMetrics=false — proving the emit is UNCONDITIONAL and not
// gated on the bench flag (the production daemon leaves WithMetrics
// false, so a gated emit would make it telemetry-blind). The line's calls_*
// fields reflect the cycle's real bearcli traffic; slowest reflects the
// per-domain timings accumulated via the DomainTimingHook seam.
func TestApply_EmitsCycleTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeBackend(0)
		ctx := bearcli.ContextWithBackend(t.Context(), fake)
		buf := captureLog(t)

		umbrella := umbrellaStub("library", "Library Index", "library/a")
		leafA := stubDomain("library/a", "Library A", umbrella.IndexTitle)
		leafB := stubDomain("library/b", "Library B", umbrella.IndexTitle)

		opts := applyOptsFor(t, []*domain.Domain{leafA, leafB, umbrella})
		opts.SkipFlock = true    // synctest bubble + flock would block on real syscalls
		opts.WithMetrics = false // the production-daemon default — assert emit fires anyway

		result, err := engine.Apply(ctx, opts)
		if err != nil {
			t.Fatalf("Apply: %v", err)
		}
		if result.Interrupted {
			t.Fatal("expected Interrupted=false")
		}

		if got := countTelemetryLines(buf); got != 1 {
			t.Errorf("telemetry line count = %d, want exactly 1 per Apply (WithMetrics=false → emit must be unconditional)\nlog:\n%s",
				got, buf.String())
		}
		// slowest must carry the real per-domain tags accumulated via the hook —
		// proof the accumulator is wired, not an empty placeholder.
		if !strings.Contains(buf.String(), "library/a") || !strings.Contains(buf.String(), "library/b") {
			t.Errorf("telemetry slowest field missing accumulated per-domain tags\nlog:\n%s", buf.String())
		}
	})
}

// TestApply_TelemetryIsPerCycleNotCumulative is the per-cycle telemetry guard:
// TWO engine.Apply cycles run in ONE process WITHOUT a pool reset between them
// (modeling the long-lived daemon, where SetConcurrency is sync.Once-gated and
// the pool counters accumulate across cycles). Each cycle issues identical
// bearcli traffic over the same steady no-op corpus, so a correct per-cycle
// telemetry line reports the SAME calls_list for cycle 2 as cycle 1. Before the
// per-cycle delta, the emit passed the raw lifetime MetricsSnapshot, so cycle 2's calls_list was
// DOUBLE cycle 1's (90->180 in the live-daemon evidence). peak_concurrency must
// also be the per-cycle peak (>=1), not a lifetime CAS-max artifact.
//
// The existing single-process telemetry tests cannot catch this — only a
// second cycle in the same process exposes the cumulative leak.
func TestApply_TelemetryIsPerCycleNotCumulative(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	d := noOpHubRoutedDomain()
	backend := testutil.NewRecordingBackend(noOpHubRoutedCorpus(d))
	ctx := bearcli.ContextWithBackend(context.Background(), backend)
	buf := captureLog(t)

	dir := t.TempDir()
	opts := engine.ApplyOpts{
		Domains:   []*domain.Domain{d},
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Features:  engine.Features{},
		SkipFlock: true,
	}

	if _, err := engine.Apply(ctx, opts); err != nil {
		t.Fatalf("cycle 1 Apply: %v", err)
	}
	if _, err := engine.Apply(ctx, opts); err != nil {
		t.Fatalf("cycle 2 Apply: %v", err)
	}

	lines := telemetryCycleLines(buf)
	if len(lines) != 2 {
		t.Fatalf("expected 2 `regen cycle:` lines, got %d\nlog:\n%s", len(lines), buf.String())
	}
	c1List := telemetryField(t, lines[0], "calls_list")
	c2List := telemetryField(t, lines[1], "calls_list")
	if c1List <= 0 {
		t.Fatalf("cycle 1 calls_list = %d, want > 0 (the no-op cycle still lists once)\nline:\n%s", c1List, lines[0])
	}
	if c2List != c1List {
		t.Errorf("cycle 2 calls_list = %d, want %d (per-cycle, NOT cumulative); a cumulative leak doubles it\n"+
			"cycle1: %s\ncycle2: %s", c2List, c1List, lines[0], lines[1])
	}
	// Per-cycle peak scoping: the pool capacity is 1, so each isolated cycle's
	// peak is at most 1 — and identical across both cycles. A LIFETIME CAS-max
	// would still be >= 1 here, so the >= 1 form cannot distinguish per-cycle
	// from lifetime; pinning cycle-2 peak == cycle-1 peak (and bounded by the
	// pool capacity) is what actually proves the per-cycle reset fires.
	c1Peak := telemetryField(t, lines[0], "peak_concurrency")
	c2Peak := telemetryField(t, lines[1], "peak_concurrency")
	if c1Peak <= 0 || c1Peak > 1 {
		t.Errorf("cycle 1 peak_concurrency = %d, want 1 (pool capacity is 1)\nline:\n%s", c1Peak, lines[0])
	}
	if c2Peak != c1Peak {
		t.Errorf("cycle 2 peak_concurrency = %d, want %d (per-cycle scoped, equal across cycles); "+
			"a lifetime CAS-max would drift\ncycle1: %s\ncycle2: %s", c2Peak, c1Peak, lines[0], lines[1])
	}
}

// TestCycleDelta_MissingBaselineKeyYieldsFullEndValue pins the per-cycle delta
// math for the first-cycle / fresh-pool case: when the baseline snapshot has no
// entry for a call kind (nil-safe map read returns 0), the delta for that kind
// is the full end value — never a panic, never a negative. This is the daemon's
// very first cycle, where the baseline is the zeroed pool: the emitted line must
// report the cycle's real counts, not garbage.
func TestCycleDelta_MissingBaselineKeyYieldsFullEndValue(t *testing.T) {
	baseline := bearcli.Metrics{
		// Baseline observed only "list" traffic; "cat"/"overwrite" keys absent.
		CallsByKind: map[string]int64{"list": 2},
	}
	end := bearcli.Metrics{
		CallsByKind: map[string]int64{"list": 5, "cat": 9, "overwrite": 3},
	}

	delta := engine.CycleDeltaForTest(baseline, end)

	// "list" present in both: 5 - 2 == 3.
	if got := delta.CallsByKind["list"]; got != 3 {
		t.Errorf("delta calls_list = %d, want 3 (5 end - 2 baseline)", got)
	}
	// "cat" missing from baseline (reads as 0): full end value 9.
	if got := delta.CallsByKind["cat"]; got != 9 {
		t.Errorf("delta calls_cat = %d, want 9 (full end value; missing baseline key must read 0, not panic)", got)
	}
	if got := delta.CallsByKind["overwrite"]; got != 3 {
		t.Errorf("delta calls_overwrite = %d, want 3 (full end value)", got)
	}
}

// TestCycleDelta_PeakConcurrentPassedThroughNotSubtracted pins that
// PeakConcurrent is carried straight from the end snapshot, NOT differenced
// against the baseline. It is a per-cycle CAS-max (scoped at cycle start), so
// subtracting two maxes would report a meaningless value. With a baseline peak
// HIGHER than the end peak, a naive subtraction would go negative; the
// pass-through must report the end peak verbatim.
func TestCycleDelta_PeakConcurrentPassedThroughNotSubtracted(t *testing.T) {
	baseline := bearcli.Metrics{PeakConcurrent: 7, CallsByKind: map[string]int64{}}
	end := bearcli.Metrics{PeakConcurrent: 4, Capacity: 8, CallsByKind: map[string]int64{}}

	delta := engine.CycleDeltaForTest(baseline, end)

	if got := delta.PeakConcurrent; got != 4 {
		t.Errorf("delta peak_concurrency = %d, want 4 (end value passed through, not baseline-subtracted)", got)
	}
	if got := delta.Capacity; got != 8 {
		t.Errorf("delta capacity = %d, want 8 (config copied straight through)", got)
	}
}

// TestCycleDelta_ClampsNegativeOnOutOfBandReset pins the defensive clamp: if an
// out-of-band metrics reset drops the end snapshot BELOW the cycle baseline
// (concurrent ResetMetrics), the additive deltas must clamp to 0 — never emit a
// silently negative int64 that would corrupt the telemetry line.
func TestCycleDelta_ClampsNegativeOnOutOfBandReset(t *testing.T) {
	baseline := bearcli.Metrics{
		AcquireCount:       100,
		WaitNanosSum:       500,
		HashConflictsTotal: 9,
		RetriesSucceeded:   4,
		RetriesFailed:      2,
		CallsByKind:        map[string]int64{"cat": 50},
	}
	// A mid-cycle reset zeroed the pool, so end is below baseline everywhere.
	end := bearcli.Metrics{CallsByKind: map[string]int64{"cat": 0}}

	delta := engine.CycleDeltaForTest(baseline, end)

	checks := map[string]int64{
		"AcquireCount":       delta.AcquireCount,
		"WaitNanosSum":       delta.WaitNanosSum,
		"HashConflictsTotal": delta.HashConflictsTotal,
		"RetriesSucceeded":   delta.RetriesSucceeded,
		"RetriesFailed":      delta.RetriesFailed,
		"CallsByKind[cat]":   delta.CallsByKind["cat"],
	}
	for field, got := range checks {
		if got < 0 {
			t.Errorf("delta %s = %d, want clamped to >= 0 (out-of-band reset must not yield a negative counter)", field, got)
		}
	}
}

// telemetryCycleLines returns each `regen cycle:` line from the captured log.
func telemetryCycleLines(buf *bytes.Buffer) []string {
	var out []string
	for line := range strings.SplitSeq(buf.String(), "\n") {
		if strings.Contains(line, "regen cycle:") {
			out = append(out, line)
		}
	}
	return out
}

// telemetryField parses an integer `key=N` field out of a telemetry line.
func telemetryField(t *testing.T, line, key string) int {
	t.Helper()
	_, after, found := strings.Cut(line, key+"=")
	if !found {
		t.Fatalf("telemetry line missing %q field\nline:\n%s", key, line)
	}
	token, _, _ := strings.Cut(after, " ")
	n, err := strconv.Atoi(token)
	if err != nil {
		t.Fatalf("telemetry field %q value %q not an int: %v\nline:\n%s", key, token, err, line)
	}
	return n
}

// TestAutoTagTick_NoTelemetry pins half of the no-spam contract: N
// auto-tag fast-pass ticks (handleAutoTagTick, ~every 2s in production) emit
// ZERO `regen cycle:` lines — the telemetry belongs to the REGEN cycle, not the
// fast-pass tick. The buffer is read only AFTER the daemon goroutine is
// canceled and drained, so the read never races the logging goroutine
// (captureLog mutates package-global log state).
func TestAutoTagTick_NoTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		// Empty list payload → fast-pass passes find nothing to write, so no
		// follow-up cycle is triggered by the ticks themselves.
		fake := newFakeAutoTagBackend([]byte("[]"))
		opts := autoTagOptsFor(t, 100*time.Millisecond, engine.AllFeaturesOn())
		run := startDaemonRun(t, fake, opts, nil)
		// Advance through ~4 fast-pass ticks, then cancel + drain before reading.
		run.WaitFor(450 * time.Millisecond)

		if got := countTelemetryLines(run.Buf); got != 0 {
			t.Errorf("telemetry line count across fast-pass ticks = %d, want 0 (ticks must not emit a regen cycle line)\nlog:\n%s",
				got, run.Buf.String())
		}
	})
}

// TestCycleOnce_EmitsTelemetry pins the other half: a real daemon cycle (driven
// via an FSEvent burst → debounce → cycleOnce → engine.Apply) emits EXACTLY ONE
// `regen cycle:` line. Together with TestAutoTagTick_NoTelemetry this is the
// daemon-path proof that the single Apply-tail emit covers cycleOnce while the
// fast-pass tick stays silent (daemon-only
// behavior invisible to the apply-only ship-gate). The buffer is read only
// after cancel + drain to avoid racing the daemon's logging goroutine.
func TestCycleOnce_EmitsTelemetry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		fake := newFakeAutoTagBackend([]byte("[]"))
		// AutoTagPollInterval=0 disables the fast-pass ticker so the ONLY
		// telemetry source under observation is the FSEvent-driven cycle.
		opts := autoTagOptsFor(t, 0, engine.Features{})

		resetPoolForApply(t)
		fw := newFakeWatcher()
		d := engine.NewDaemonWithWatcher(opts, fw)
		t.Cleanup(func() { _ = d.Close() })
		buf := captureLog(t)
		ctx, cancel := context.WithCancel(t.Context())
		ctx = bearcli.ContextWithBackend(ctx, fake)
		t.Cleanup(cancel)
		errCh := make(chan error, 1)
		go func() { errCh <- d.Run(ctx) }()

		// One FSEvent burst on the watched DB file → debounce (50ms) → cycleOnce.
		fw.events <- fsnotify.Event{
			Op:   fsnotify.Write,
			Name: filepath.Join(opts.BearDBDir, "database.sqlite"),
		}
		time.Sleep(300 * time.Millisecond)
		cancel()
		<-errCh

		if got := countTelemetryLines(buf); got != 1 {
			t.Errorf("telemetry line count after one real cycle = %d, want exactly 1 (cycleOnce → engine.Apply emits once)\nlog:\n%s",
				got, buf.String())
		}
		if cycles := countCycles(buf); cycles != 1 {
			t.Errorf("regen trigger count = %d, want exactly 1 (one cycleOnce)\nlog:\n%s", cycles, buf.String())
		}
	})
}
