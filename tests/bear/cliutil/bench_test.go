package cliutil_test

import (
	"slices"
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

// TestParseSweep pins the --sweep flag parser an operator hits directly: an
// empty flag is the single-run path (nil), a comma list parses to its values,
// surrounding whitespace is trimmed, and a non-integer or non-positive entry is
// a clear CLI error (never a panic). These are the exact strings a shell user
// types after `noxctl apply --sweep=...`.
func TestParseSweep(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		want    []int
		wantErr bool
	}{
		{name: "empty is single-run", raw: "", want: nil},
		{name: "two values", raw: "4,8", want: []int{4, 8}},
		{name: "whitespace trimmed", raw: "4, 8 ", want: []int{4, 8}},
		{name: "single value", raw: "12", want: []int{12}},
		{name: "non-integer rejected", raw: "x", wantErr: true},
		{name: "non-integer among valid rejected", raw: "4,x,8", wantErr: true},
		{name: "zero rejected", raw: "0", wantErr: true},
		{name: "negative rejected", raw: "-1", wantErr: true},
		{name: "negative among valid rejected", raw: "4,-1", wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cliutil.ParseSweep(tc.raw)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseSweep(%q) err = nil, want a CLI error", tc.raw)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseSweep(%q): %v", tc.raw, err)
			}
			if !slices.Equal(got, tc.want) {
				t.Errorf("ParseSweep(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
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
