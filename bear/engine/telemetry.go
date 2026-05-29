package engine

// Cycle telemetry — one structured key=value summary line per REGEN cycle
// (D-03). Surfaces the per-cycle metrics the bearcli pool already computes
// (calls-by-kind, peak concurrency, queue wait, hash conflicts/retries) plus a
// once-per-cycle process-memory snapshot, so an operator can confirm the
// read-amplification win and inform the concurrency decision without pprof or
// per-hub log parsing.
//
// Security (T-14-08): the line emits ONLY numeric counts/timings/memory and
// domain TAGS (catalog config, not vault content). It never formats a note
// title, body, or content hash — a leak would require formatting an input the
// function never receives.

import (
	"fmt"
	"io"
	"log"
	"runtime"
	"sort"
	"sync"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// slowestTopN caps how many per-domain timings the `slowest=` field lists,
// sorted DESC by elapsed wall time. Five is enough to spot the dominant cost
// (e.g. lyrics' 161-bucket no-op) without flooding the single line.
const slowestTopN = 5

// domainTiming is one per-domain wall-clock sample fed to the telemetry
// formatter. Tag is the catalog domain tag (never a note title); Elapsed is the
// regen.Run wall duration accumulated via the DomainTimingHook seam.
type domainTiming struct {
	Tag     string
	Elapsed time.Duration
}

// timingAccumulator collects per-domain timings from the worker goroutines that
// fire DomainTimingHook. The hook fires concurrently from runDomainAndSave
// (T-14-10), so every append is mutex-guarded.
type timingAccumulator struct {
	mu      sync.Mutex
	samples []domainTiming
}

// add records one per-domain sample. Safe for concurrent callers.
func (a *timingAccumulator) add(tag string, elapsed time.Duration) {
	a.mu.Lock()
	a.samples = append(a.samples, domainTiming{Tag: tag, Elapsed: elapsed})
	a.mu.Unlock()
}

// snapshot returns a copy of the accumulated samples under the lock, safe to
// hand to the formatter after all workers have returned.
func (a *timingAccumulator) snapshot() []domainTiming {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]domainTiming, len(a.samples))
	copy(out, a.samples)
	return out
}

// installTimingAccumulator returns a fresh accumulator and rewrites
// opts.DomainTimingHook so runDomainAndSave feeds it. A caller-supplied hook
// (bench mode) is preserved by wrapping: both the bench hook and the telemetry
// accumulator receive every sample. Production leaves the hook nil, so the
// accumulator becomes the sole consumer.
func installTimingAccumulator(opts *ApplyOpts) *timingAccumulator {
	acc := &timingAccumulator{}
	prior := opts.DomainTimingHook
	opts.DomainTimingHook = func(tag string, elapsed time.Duration) {
		if prior != nil {
			prior(tag, elapsed)
		}
		acc.add(tag, elapsed)
	}
	return acc
}

// cycleDelta returns a per-cycle Metrics view: the ADDITIVE counters
// (AcquireCount, WaitNanosSum, CallsByKind, HashConflictsTotal, Retries*) are
// the end-snapshot minus the cycle-start baseline, so on the long-lived daemon
// each emitted line reflects ONLY the just-completed cycle, not lifetime totals
// (FIX-3). PeakConcurrent is NOT a delta — it is a CAS-max that engine.Apply
// scopes per cycle via bearcli.ScopePeakToCurrentInFlight at cycle start, so the
// end-snapshot value is already the cycle peak; subtracting two maxes would be
// wrong. Capacity is config, copied straight through. For apply --once / each
// --sweep value the baseline is zero (fresh/reset pool), so the delta equals the
// full-cycle snapshot — same numbers as before, still correct.
func cycleDelta(baseline, end bearcli.Metrics) bearcli.Metrics {
	// Snapshots are per-cycle-monotonic (counters only grow within a cycle), so
	// end >= baseline always holds in the normal case. The max(0, ...) clamp
	// defends against an out-of-band metrics reset mid-cycle (ResetMetrics from a
	// concurrent caller), which would otherwise yield a silently negative int64.
	calls := make(map[string]int64, len(end.CallsByKind))
	for kind, n := range end.CallsByKind {
		calls[kind] = max(0, n-baseline.CallsByKind[kind])
	}
	return bearcli.Metrics{
		Capacity:           end.Capacity,
		PeakConcurrent:     end.PeakConcurrent, // already cycle-scoped, not a delta
		AcquireCount:       max(0, end.AcquireCount-baseline.AcquireCount),
		WaitNanosSum:       max(0, end.WaitNanosSum-baseline.WaitNanosSum),
		CallsByKind:        calls,
		HashConflictsTotal: max(0, end.HashConflictsTotal-baseline.HashConflictsTotal),
		RetriesSucceeded:   max(0, end.RetriesSucceeded-baseline.RetriesSucceeded),
		RetriesFailed:      max(0, end.RetriesFailed-baseline.RetriesFailed),
	}
}

// logCycleTelemetry is the PRODUCTION emit: it routes the one telemetry line
// through the standard logger so the line carries the daemon's timestamp prefix
// (loop.go house style) and lands in ~/.cache/regen-watchd.log alongside every
// other daemon line. The Apply finalize tail calls this UNCONDITIONALLY (never
// gated on WithMetrics — Pitfall C).
func logCycleTelemetry(m bearcli.Metrics, timings []domainTiming, totalWall time.Duration) {
	log.Printf("%s", formatCycleTelemetry(m, timings, totalWall))
}

// emitCycleTelemetry writes EXACTLY one `regen cycle:` key=value line to w. It
// is the writer-seam variant the external test exercises via
// EmitCycleTelemetryForTest; production uses logCycleTelemetry.
func emitCycleTelemetry(w io.Writer, m bearcli.Metrics, timings []domainTiming, totalWall time.Duration) {
	_, _ = fmt.Fprintln(w, formatCycleTelemetry(m, timings, totalWall))
}

// formatCycleTelemetry builds the single key=value summary string for one
// completed regen cycle. It takes the bearcli pool snapshot, the per-domain
// timings accumulated this cycle, and the total cycle wall time; it reads
// process memory itself via a single runtime.ReadMemStats call (cycle end,
// outside the per-domain goroutines — T-14-09 keeps the stop-the-world pause to
// once per cycle).
//
// avg_queue_ms = WaitNanosSum / AcquireCount / 1e6 when AcquireCount > 0, else
// 0.0. slowest lists the top-N timings DESC by elapsed, keyed on Tag only. The
// returned string carries no trailing newline — callers add their own.
func formatCycleTelemetry(m bearcli.Metrics, timings []domainTiming, totalWall time.Duration) string {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	calls := m.CallsByKind

	return fmt.Sprintf(
		"regen cycle: wall=%s calls_list=%d calls_cat=%d calls_overwrite=%d calls_create=%d "+
			"peak_concurrency=%d avg_queue_ms=%.1f hash_conflicts=%d retries_ok=%d retries_fail=%d "+
			"heap_alloc_mb=%.1f sys_mb=%.1f slowest=%s",
		totalWall,
		calls["list"], calls["cat"], calls["overwrite"], calls["create"],
		m.PeakConcurrent, avgQueueMs(m), m.HashConflictsTotal, m.RetriesSucceeded, m.RetriesFailed,
		bytesToMB(mem.HeapAlloc), bytesToMB(mem.Sys), formatSlowest(timings),
	)
}

// avgQueueMs converts the cumulative semaphore wait into a per-acquire average
// in milliseconds, guarding the zero-acquire case (no bearcli traffic) against
// a divide-by-zero.
func avgQueueMs(m bearcli.Metrics) float64 {
	if m.AcquireCount <= 0 {
		return 0.0
	}
	return float64(m.WaitNanosSum) / float64(m.AcquireCount) / float64(time.Millisecond)
}

// bytesToMB renders a byte count as mebibytes for the memory fields.
func bytesToMB(b uint64) float64 {
	return float64(b) / (1024 * 1024)
}

// formatSlowest sorts timings DESC by elapsed, keeps the top-N, and renders
// them as space-joined `tag=<dur>` tokens. A shorter-than-N slice lists all of
// its entries; an empty slice yields "" (no panic). Keys on Tag only — never a
// note title/body/hash.
func formatSlowest(timings []domainTiming) string {
	sorted := make([]domainTiming, len(timings))
	copy(sorted, timings)
	sort.Slice(sorted, func(i, j int) bool {
		return sorted[i].Elapsed > sorted[j].Elapsed
	})
	if len(sorted) > slowestTopN {
		sorted = sorted[:slowestTopN]
	}
	out := make([]byte, 0, len(sorted)*24)
	for i, t := range sorted {
		if i > 0 {
			out = append(out, ' ')
		}
		out = append(out, fmt.Sprintf("%s=%s", t.Tag, t.Elapsed)...)
	}
	return string(out)
}
