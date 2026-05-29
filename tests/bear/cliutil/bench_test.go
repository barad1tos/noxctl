package cliutil_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/cliutil"
)

type benchCase struct {
	name        string
	bench       bool
	concurrency int
	wantMetrics bool
	wantCap     int
	wantErr     bool
}

// TestBenchOptsFromFlags locks the boundary-mapper cases: --bench sets
// WithMetrics, --concurrency threads through, 0 means "engine default" (passed
// through untouched), and a negative value is rejected. This is the unit half
// of the Pattern-A guard — the CLI-boundary integration test (bench_wiring_test)
// proves the SAME values actually reach engine.ApplyOpts.
func TestBenchOptsFromFlags(t *testing.T) {
	cases := []benchCase{
		{name: "bench enables metrics", bench: true, concurrency: 0, wantMetrics: true, wantCap: 0},
		{name: "concurrency threads through", bench: true, concurrency: 4, wantMetrics: true, wantCap: 4},
		{name: "zero concurrency is engine default", bench: false, concurrency: 0, wantMetrics: false, wantCap: 0},
		{name: "no bench leaves metrics off", bench: false, concurrency: 8, wantMetrics: false, wantCap: 8},
		{name: "negative concurrency is rejected", bench: true, concurrency: -1, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) { assertBenchCase(t, tc) })
	}
}

func assertBenchCase(t *testing.T, tc benchCase) {
	t.Helper()
	got, err := cliutil.BenchOptsFromFlags(tc.bench, tc.concurrency)
	if tc.wantErr {
		if err == nil {
			t.Fatalf("BenchOptsFromFlags(%v, %d) err = nil, want error", tc.bench, tc.concurrency)
		}
		return
	}
	if err != nil {
		t.Fatalf("BenchOptsFromFlags(%v, %d): %v", tc.bench, tc.concurrency, err)
	}
	if got.WithMetrics != tc.wantMetrics {
		t.Errorf("WithMetrics = %v, want %v", got.WithMetrics, tc.wantMetrics)
	}
	if got.BearcliConcurrency != tc.wantCap {
		t.Errorf("BearcliConcurrency = %d, want %d", got.BearcliConcurrency, tc.wantCap)
	}
}
