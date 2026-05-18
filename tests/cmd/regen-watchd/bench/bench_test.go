// Package bench_test — external tests for the regen-watchd --bench
// mode harness landed in (PAR-04).
//
// Lives under tests/ per the project test-placement rule (
// "Naming Patterns"). Drives the exported surface of
// github.com/barad1tos/noxctl/cmd/regen-watchd/bench:
//
// - ParseSweep — table-driven coverage of the --sweep argument
// parser (empty, single, multi, whitespace, non-numeric, zero,
// negative).
// - BuildBenchEnvelope — schema-shape assertions on the per-cycle
// JSON envelope (D-05): every required top-level key present,
// per_domain sorted descending by elapsed_ms, calls_by_kind
// populated with all six known kinds, JSON marshal/unmarshal
// round-trip clean.
//
// No engine.Apply or bearcli traffic — these are pure-function unit
// tests on the parser and envelope builder. The integration smoke
// (real --bench --sweep on Roman's vault) is deferred to.
package bench_test

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/cmd/regen-watchd/bench"
)

// parseSweepCase is one row of the TestParseSweep table. Extracted as
// a named type so the per-row checker can live in a helper (assertion
// shape is uniform) and the parent test stays under gocognit ≤ 15.
type parseSweepCase struct {
	name        string
	input       string
	want        []int
	wantErr     bool
	errContains string
}

// TestParseSweep covers the seven contract cases from the plan's
// Behavior section: empty (single-cycle sentinel), single, multi,
// whitespace-tolerant multi, non-numeric (parse error), zero (>= 0
// rejected), and negative (>= 0 rejected).
//
// Per-row assertion is delegated to checkParseSweepCase so the parent
// stays a trivial loop dispatcher; helper extraction keeps the function
// under the project's gocognit ≤ 15 budget.
func TestParseSweep(t *testing.T) {
	t.Parallel()
	cases := []parseSweepCase{
		{name: "empty returns nil", input: "", want: nil},
		{name: "single value", input: "8", want: []int{8}},
		{name: "multi value", input: "1,2,4,8", want: []int{1, 2, 4, 8}},
		{name: "multi value with whitespace", input: "1, 2,  4, 8", want: []int{1, 2, 4, 8}},
		{name: "non-numeric mid-list", input: "8,abc,4", wantErr: true, errContains: "abc"},
		{name: "zero rejected", input: "0,8", wantErr: true, errContains: "must be > 0"},
		{name: "negative rejected", input: "-1", wantErr: true, errContains: "must be > 0"},
		{name: "single non-numeric", input: "xyz", wantErr: true, errContains: "xyz"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			checkParseSweepCase(t, tc)
		})
	}
}

// checkParseSweepCase runs one row of the ParseSweep table. Either an
// error branch (wantErr true — assert presence + token mention) or a
// success branch (compare result vs want via the nil-aware comparator).
func checkParseSweepCase(t *testing.T, tc parseSweepCase) {
	t.Helper()
	got, err := bench.ParseSweep(tc.input)
	if tc.wantErr {
		assertParseSweepError(t, tc, err, got)
		return
	}
	if err != nil {
		t.Fatalf("ParseSweep(%q): unexpected error %v", tc.input, err)
	}
	if !intSliceEqual(got, tc.want) {
		t.Fatalf("ParseSweep(%q): got %v, want %v", tc.input, got, tc.want)
	}
}

// assertParseSweepError validates the error branch of one row.
// Separated from checkParseSweepCase so neither helper exceeds the
// gocognit budget — the error path has its own substring check.
func assertParseSweepError(t *testing.T, tc parseSweepCase, err error, got []int) {
	t.Helper()
	if err == nil {
		t.Fatalf("ParseSweep(%q): expected error, got nil (result=%v)", tc.input, got)
	}
	if tc.errContains != "" && !strings.Contains(err.Error(), tc.errContains) {
		t.Fatalf("ParseSweep(%q): error %q does not contain %q", tc.input, err.Error(), tc.errContains)
	}
}

// synthMetrics is the BearcliMetrics fixture used by TestBenchJSON_Schema.
// 6.2s wait across 500 acquires => 12.4 ms average queue depth, plus
// every known kind populated so the calls_by_kind assertion is meaningful.
func synthMetrics() bear.BearcliMetrics {
	return bear.BearcliMetrics{
		Capacity:       8,
		PeakConcurrent: 7,
		AcquireCount:   500,
		WaitNanosSum:   6_200_000_000,
		CallsByKind: map[string]int64{
			"list":      31,
			"cat":       124,
			"show":      218,
			"overwrite": 86,
			"find":      0,
			"create":    2,
		},
		HashConflictsTotal: 2,
		RetriesSucceeded:   2,
		RetriesFailed:      0,
	}
}

// synthDomainTimings mixes elapsed values out of order so the sort
// assertion in TestBenchJSON_Schema has a meaningful before/after.
func synthDomainTimings() []bench.DomainTiming {
	return []bench.DomainTiming{
		{Tag: "library/poetry", ElapsedMs: 1450},
		{Tag: "llm/agents", ElapsedMs: 980},
		{Tag: "it/vendors", ElapsedMs: 1820}, // largest — should sort to top
		{Tag: "personal/work", ElapsedMs: 312},
	}
}

// TestBenchJSON_Schema constructs a synthetic input, calls
// BuildBenchEnvelope, and asserts the resulting envelope matches the
// D-05 schema. Top-level checks are split across small sub-helpers so
// the parent stays under the project's gocognit budget.
func TestBenchJSON_Schema(t *testing.T) {
	t.Parallel()
	const concurrency = 8
	wallClock := 4321 * time.Millisecond
	perDomain := synthDomainTimings()
	metrics := synthMetrics()
	envelope := bench.BuildBenchEnvelope(concurrency, wallClock, perDomain, metrics)

	assertScalarFields(t, envelope, concurrency, wallClock)
	assertPerDomainSorted(t, envelope, len(perDomain))
	assertBearcliBlock(t, envelope, metrics)
	assertHashConflictsBlock(t, envelope, metrics)
	assertJSONRoundTrip(t, envelope)
}

// assertScalarFields covers bench_version, concurrency_setting, and
// wall_clock_ms — the three flat scalars at the envelope root.
func assertScalarFields(t *testing.T, env bench.Envelope, wantConcurrency int, wantWall time.Duration) {
	t.Helper()
	if env.BenchVersion != "1" {
		t.Fatalf("BenchVersion: got %q, want %q", env.BenchVersion, "1")
	}
	if env.ConcurrencySetting != wantConcurrency {
		t.Fatalf("ConcurrencySetting: got %d, want %d", env.ConcurrencySetting, wantConcurrency)
	}
	if env.WallClockMs != wantWall.Milliseconds() {
		t.Fatalf("WallClockMs: got %d, want %d", env.WallClockMs, wantWall.Milliseconds())
	}
}

// assertPerDomainSorted confirms the per_domain array is the right
// length and sorted descending by elapsed_ms, with the top entry being
// the critical-path domain (largest elapsed input).
func assertPerDomainSorted(t *testing.T, env bench.Envelope, wantLen int) {
	t.Helper()
	if len(env.PerDomain) != wantLen {
		t.Fatalf("PerDomain length: got %d, want %d", len(env.PerDomain), wantLen)
	}
	for i := 1; i < len(env.PerDomain); i++ {
		if env.PerDomain[i-1].ElapsedMs < env.PerDomain[i].ElapsedMs {
			t.Fatalf("PerDomain not sorted desc at index %d: %v", i, env.PerDomain)
		}
	}
	if env.PerDomain[0].Tag != "it/vendors" {
		t.Fatalf("PerDomain[0].Tag: got %q, want %q (largest elapsed)", env.PerDomain[0].Tag, "it/vendors")
	}
}

// assertBearcliBlock checks the three required keys of the bearcli
// sub-object: peak_concurrent (matches input), average_queue_depth_ms
// (computed correctly), calls_by_kind (all six known kinds present).
func assertBearcliBlock(t *testing.T, env bench.Envelope, metrics bear.BearcliMetrics) {
	t.Helper()
	if env.Bearcli.PeakConcurrent != metrics.PeakConcurrent {
		t.Fatalf("PeakConcurrent: got %d, want %d", env.Bearcli.PeakConcurrent, metrics.PeakConcurrent)
	}
	const wantAvg = 12.4 // 6_200_000_000 ns / 500 / 1e6
	gotAvg := env.Bearcli.AverageQueueDepthMs
	if gotAvg < wantAvg-0.001 || gotAvg > wantAvg+0.001 {
		t.Fatalf("AverageQueueDepthMs: got %v, want %v", gotAvg, wantAvg)
	}
	for _, k := range []string{"list", "cat", "show", "overwrite", "create", "find"} {
		if _, ok := env.Bearcli.CallsByKind[k]; !ok {
			t.Fatalf("CallsByKind missing key %q (got %v)", k, env.Bearcli.CallsByKind)
		}
	}
	if got := env.Bearcli.CallsByKind["overwrite"]; got != 86 {
		t.Fatalf("CallsByKind[overwrite]: got %d, want 86", got)
	}
}

// assertHashConflictsBlock checks total + retries_succeeded +
// retries_failed match the input metrics.
func assertHashConflictsBlock(t *testing.T, env bench.Envelope, metrics bear.BearcliMetrics) {
	t.Helper()
	if env.HashConflicts.Total != metrics.HashConflictsTotal {
		t.Fatalf("HashConflicts.Total: got %d, want %d", env.HashConflicts.Total, metrics.HashConflictsTotal)
	}
	if env.HashConflicts.RetriesSucceeded != metrics.RetriesSucceeded {
		t.Fatalf("RetriesSucceeded: got %d, want %d", env.HashConflicts.RetriesSucceeded, metrics.RetriesSucceeded)
	}
	if env.HashConflicts.RetriesFailed != metrics.RetriesFailed {
		t.Fatalf("RetriesFailed: got %d, want %d", env.HashConflicts.RetriesFailed, metrics.RetriesFailed)
	}
}

// assertJSONRoundTrip marshals the envelope into JSON, unmarshals into
// a generic map, and confirms every required top-level key plus the
// nested bearcli block's three required keys are present.
func assertJSONRoundTrip(t *testing.T, env bench.Envelope) {
	t.Helper()
	raw, marshalErr := json.Marshal(env)
	if marshalErr != nil {
		t.Fatalf("json.Marshal: %v", marshalErr)
	}
	var generic map[string]any
	if unmarshalErr := json.Unmarshal(raw, &generic); unmarshalErr != nil {
		t.Fatalf("json.Unmarshal into map[string]any: %v", unmarshalErr)
	}
	for _, k := range []string{
		"bench_version", "concurrency_setting", "wall_clock_ms",
		"per_domain", "bearcli", "hash_conflicts",
	} {
		if _, present := generic[k]; !present {
			t.Fatalf("JSON missing required top-level key %q (got keys %v)", k, mapKeys(generic))
		}
	}
	bearcli, isMap := generic["bearcli"].(map[string]any)
	if !isMap {
		t.Fatalf("bearcli not a map: %T", generic["bearcli"])
	}
	for _, k := range []string{"peak_concurrent", "average_queue_depth_ms", "calls_by_kind"} {
		if _, present := bearcli[k]; !present {
			t.Fatalf("bearcli missing required key %q (got %v)", k, mapKeys(bearcli))
		}
	}
}

// TestBenchJSON_AverageQueueDepth_ZeroDivision exercises the
// degenerate "no acquires happened" path. BuildBenchEnvelope must
// return 0.0 rather than NaN/Inf so the JSON envelope stays parseable.
func TestBenchJSON_AverageQueueDepth_ZeroDivision(t *testing.T) {
	t.Parallel()
	metrics := bear.BearcliMetrics{
		Capacity:     1,
		AcquireCount: 0,
		WaitNanosSum: 0,
		CallsByKind:  map[string]int64{},
	}
	envelope := bench.BuildBenchEnvelope(1, time.Millisecond, nil, metrics)
	if envelope.Bearcli.AverageQueueDepthMs != 0.0 {
		t.Fatalf("AverageQueueDepthMs with zero acquires: got %v, want 0.0", envelope.Bearcli.AverageQueueDepthMs)
	}
	// JSON serializability — Inf/NaN would error here.
	if _, err := json.Marshal(envelope); err != nil {
		t.Fatalf("json.Marshal on zero-acquire envelope: %v", err)
	}
}

// TestBenchJSON_CallsByKind_ZeroFilled confirms BuildBenchEnvelope
// populates every known kind even when the input metrics map omits
// some (or all) of them. The schema contract is "always six keys,
// never null" so jq consumers can rely on `.bearcli.calls_by_kind.find`
// being numeric (zero if no traffic) rather than null.
func TestBenchJSON_CallsByKind_ZeroFilled(t *testing.T) {
	t.Parallel()
	metrics := bear.BearcliMetrics{
		AcquireCount: 1,
		WaitNanosSum: 0,
		CallsByKind:  map[string]int64{"list": 1}, // only one key supplied
	}
	envelope := bench.BuildBenchEnvelope(1, time.Millisecond, nil, metrics)
	for _, k := range []string{"list", "cat", "show", "overwrite", "create", "find"} {
		v, ok := envelope.Bearcli.CallsByKind[k]
		if !ok {
			t.Fatalf("CallsByKind missing key %q (got %v)", k, envelope.Bearcli.CallsByKind)
		}
		if k != "list" && v != 0 {
			t.Fatalf("CallsByKind[%q]: got %d, want 0 (zero-filled default)", k, v)
		}
	}
	if envelope.Bearcli.CallsByKind["list"] != 1 {
		t.Fatalf("CallsByKind[list]: got %d, want 1", envelope.Bearcli.CallsByKind["list"])
	}
}

// intSliceEqual is a local nil-aware comparator. reflect.DeepEqual
// treats nil and empty as unequal which is exactly what ParseSweep's
// contract demands (empty input returns nil, not []int{}), so the
// helper preserves that distinction directly.
func intSliceEqual(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	if a == nil && b == nil {
		return true
	}
	if (a == nil) != (b == nil) {
		// Distinguish nil vs empty for the "empty input returns nil" contract.
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// mapKeys returns the keys of m as a slice. Used only for test failure
// messages so a missing-key assertion shows what keys WERE present.
func mapKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
