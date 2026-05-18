// Package bench implements the regen-watchd --bench mode. It runs one
// engine.Apply cycle (or N sequential cycles when --sweep=N,M,... is
// supplied) with metrics instrumentation enabled, and emits a JSON
// document per cycle to stdout matching the D-05 schema (see
// ):
//
//	wall_clock_ms, per_domain (sorted desc), bearcli (peak / avg-queue /
//	calls-by-kind), hash_conflicts (total / retries_succeeded /
//	retries_failed), concurrency_setting.
//
// Bench is a measurement harness — it never propagates per-cycle Apply
// errors as process exit codes (D-07). Exit 1 is reserved for daemon
// config-load failures and --sweep argument parse failures, both of
// which happen before the first cycle starts.
//
// Symbols are exported so the external test package at
// tests/cmd/regen-watchd/bench/ can drive them directly. The package
// has no init and no package-level state — every call is self-
// contained, which keeps --bench --sweep cycles reproducible.
package bench

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/engine"
)

// benchVersion is the JSON envelope schema version. Bumping signals
// breaking changes for operators piping through `jq`. Stays "1" until
// the schema fields change.
const benchVersion = "1"

// DomainTiming carries one per-domain elapsed measurement. Bench mode
// installs a DomainTimingHook on ApplyOpts that appends one entry per
// completed RunRegen call; BuildBenchEnvelope sorts the slice
// descending by ElapsedMs before emit so the critical-path domain
// surfaces at the top of the JSON array.
type DomainTiming struct {
	Tag       string `json:"tag"`
	ElapsedMs int64  `json:"elapsed_ms"`
}

// Envelope is the top-level JSON document emitted once per sweep
// cycle. Field order matches the D-05 spec; struct tags fix the JSON
// keys so operator-facing tooling (`jq '.wall_clock_ms'`) is stable.
type Envelope struct {
	BenchVersion       string         `json:"bench_version"`
	ConcurrencySetting int            `json:"concurrency_setting"`
	WallClockMs        int64          `json:"wall_clock_ms"`
	PerDomain          []DomainTiming `json:"per_domain"`
	Bearcli            BearcliBlock   `json:"bearcli"`
	HashConflicts      ConflictsBlock `json:"hash_conflicts"`
}

// BearcliBlock captures the bearcli pool metrics for one cycle.
// AverageQueueDepthMs is rendered as a float so the JSON consumer can
// distinguish 0 (no waits) from a sub-millisecond average.
//
// CallsByKind always contains all six known bearcli sub-commands as
// keys (list, cat, show, overwrite, create, find), even when zero. A
// `jq '.bearcli.calls_by_kind.overwrite'` consumer never has to
// special-case `null`.
type BearcliBlock struct {
	PeakConcurrent      int64            `json:"peak_concurrent"`
	AverageQueueDepthMs float64          `json:"average_queue_depth_ms"`
	CallsByKind         map[string]int64 `json:"calls_by_kind"`
}

// ConflictsBlock carries the hash-conflict counters for the cycle.
// The retries_{succeeded,failed} pair sums to Total when every
// conflict was handled; a gap signals conflicts the retry path didn't
// reach (e.g. ctx cancellation before retry).
type ConflictsBlock struct {
	Total            int64 `json:"total"`
	RetriesSucceeded int64 `json:"retries_succeeded"`
	RetriesFailed    int64 `json:"retries_failed"`
}

// benchKnownKinds enumerates the six bearcli sub-commands the pool
// tracks. BuildBenchEnvelope uses this set to guarantee every key is
// present in CallsByKind even when no traffic flowed for a given kind.
var benchKnownKinds = []string{"list", "cat", "show", "overwrite", "create", "find"}

// ParseSweep parses the --sweep=N[,M,...] argument value into a slice
// of positive ints in submitted order. Whitespace around values is
// trimmed.
//
// Empty input returns (nil, nil) — the caller distinguishes "single
// cycle at effective default" from "1-element sweep [N]" by checking
// nil vs len > 0.
//
// Mirrors the validateConcurrency contract from: zero and
// negative are invalid. Non-numeric tokens (whitespace-trimmed) surface
// as a wrapped strconv.Atoi error mentioning the offending value so
// the operator immediately sees which token failed.
func ParseSweep(s string) ([]int, error) {
	if s == "" {
		return nil, nil
	}
	parts := strings.Split(s, ",")
	out := make([]int, 0, len(parts))
	for _, raw := range parts {
		v := strings.TrimSpace(raw)
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, fmt.Errorf("--sweep value %q: %w", v, err)
		}
		if n <= 0 {
			return nil, fmt.Errorf("--sweep value %q: must be > 0", v)
		}
		out = append(out, n)
	}
	return out, nil
}

// BuildBenchEnvelope assembles one cycle's Envelope from the raw
// measurements. PerDomain is sorted descending by ElapsedMs (stable —
// equal-elapsed ties preserve insertion order, which is the goroutine
// completion order from DomainTimingHook).
//
// AverageQueueDepthMs is derived as (WaitNanosSum / AcquireCount) / 1e6
// with a divide-by-zero guard for degenerate cycles where no bearcli
// call ran (e.g. a synthetic test envelope).
//
// CallsByKind is freshly allocated with all six known kinds explicitly
// keyed — even when zero — so jq consumers never observe `null` for a
// kind that simply had no traffic.
func BuildBenchEnvelope(
	concurrency int,
	wallClock time.Duration,
	perDomain []DomainTiming,
	metrics bear.BearcliMetrics,
) Envelope {
	sorted := make([]DomainTiming, len(perDomain))
	copy(sorted, perDomain)
	sort.SliceStable(sorted, func(i, j int) bool {
		return sorted[i].ElapsedMs > sorted[j].ElapsedMs
	})

	avgQueueMs := 0.0
	if metrics.AcquireCount > 0 {
		avgQueueMs = float64(metrics.WaitNanosSum) / float64(metrics.AcquireCount) / 1e6
	}

	callsByKind := make(map[string]int64, len(benchKnownKinds))
	for _, kind := range benchKnownKinds {
		callsByKind[kind] = metrics.CallsByKind[kind]
	}

	return Envelope{
		BenchVersion:       benchVersion,
		ConcurrencySetting: concurrency,
		WallClockMs:        wallClock.Milliseconds(),
		PerDomain:          sorted,
		Bearcli: BearcliBlock{
			PeakConcurrent:      metrics.PeakConcurrent,
			AverageQueueDepthMs: avgQueueMs,
			CallsByKind:         callsByKind,
		},
		HashConflicts: ConflictsBlock{
			Total:            metrics.HashConflictsTotal,
			RetriesSucceeded: metrics.RetriesSucceeded,
			RetriesFailed:    metrics.RetriesFailed,
		},
	}
}

// Deps bundles the host-process callbacks bench mode needs to drive
// engine.Apply. main supplies these from cmd/regen-watchd so the
// bench package never has to import the main package (Go forbids that)
// and stays test-callable in isolation.
//
// LoadPins is called once before any cycle so the pin registry mirrors
// production startup. BuildOpts is called per-cycle — bench mutates
// BearcliConcurrency / WithMetrics / DomainTimingHook on the returned
// opts before passing it to engine.Apply. DefaultConcurrency is the
// effective bearcli_concurrency for an empty --sweep (one cycle at the
// daemon config's resolved value).
type Deps struct {
	LoadPins           func()
	BuildOpts          func() engine.ApplyOpts
	DefaultConcurrency int
}

// Run drives one or more --bench cycles. When sweep is nil or empty,
// Run executes a single cycle at deps.DefaultConcurrency. Otherwise,
// each element of sweep produces one cycle in submitted order — bench
// resets the bearcli pool to the new capacity (ResetBearcliPoolForTest)
// AND zeroes the metric counters (ResetBearcliMetrics) between cycles
// so the JSON for cycle N reflects only that cycle's traffic. The
// repeat-N pattern (e.g. --sweep=1,1,1) doubles as an idempotency
// check: cycle 2+ should report zero overwrite calls.
//
// Per D-07, per-cycle engine.Apply errors are logged to stderr but do
// not propagate — bench is a measurement, not a test. The only way
// this function can short-circuit is via ctx cancellation between
// cycles; in-cycle ctx cancellation is propagated by engine.Apply
// directly.
//
// Cycle JSON is streamed to stdout via json.Encoder with indent set to
// two spaces — operators pipe through `jq -s '.'` to aggregate multi-
// cycle output into an array.
func Run(ctx context.Context, sweep []int, deps Deps) {
	if deps.LoadPins != nil {
		deps.LoadPins()
	}
	if len(sweep) == 0 {
		sweep = []int{deps.DefaultConcurrency}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	for _, n := range sweep {
		envelope := runOneCycle(ctx, n, deps)
		if err := enc.Encode(envelope); err != nil {
			log.Printf("bench: emit cycle n=%d: %v", n, err)
		}
	}
}

// runOneCycle executes a single sweep cycle and returns the resulting
// Envelope. Extracted from Run so the per-cycle setup (pool reset,
// hook install, Apply call, snapshot) stays under the gocognit budget
// — Run becomes the trivial sweep-loop driver.
//
// Pool reset before Apply: ResetBearcliPoolForTest re-arms the
// semaphore at the new capacity AND zeroes counters. Calling it on
// every cycle (including the first) keeps the semantics symmetric —
// the first cycle sees a freshly initialized pool too.
func runOneCycle(ctx context.Context, n int, deps Deps) Envelope {
	bear.ResetBearcliPoolForTest(n)

	opts := deps.BuildOpts()
	opts.BearcliConcurrency = n
	opts.WithMetrics = true

	var (
		timings   []DomainTiming
		timingsMu sync.Mutex
	)
	opts.DomainTimingHook = func(tag string, elapsed time.Duration) {
		timingsMu.Lock()
		defer timingsMu.Unlock()
		timings = append(timings, DomainTiming{
			Tag:       tag,
			ElapsedMs: elapsed.Milliseconds(),
		})
	}

	start := time.Now()
	if _, err := engine.Apply(ctx, opts); err != nil {
		// Per D-07: measurement, not test. Log and continue so the
		// envelope still emits — the operator wants to see WHICH
		// cycle failed alongside the others, not abort the sweep.
		log.Printf("bench: cycle n=%d: %v", n, err)
	}
	wallClock := time.Since(start)

	metrics := bear.BearcliMetricsSnapshot()
	timingsMu.Lock()
	snapshot := make([]DomainTiming, len(timings))
	copy(snapshot, timings)
	timingsMu.Unlock()

	return BuildBenchEnvelope(n, wallClock, snapshot, metrics)
}
