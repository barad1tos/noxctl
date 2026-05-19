// Package overwriteoutcome_test — external tests for the
// overwriteoutcome.Record routine. Lives under tests/ per project test
// placement rule ("Naming Patterns"). Mirrors the source
// layout: bear/overwriteoutcome/record.go ⇄ tests/bear/overwriteoutcome/record_test.go.
//
// Validates the counter arithmetic: hashConflictsTotal increments
// exactly once per ErrHashConflict observed; retriesSucceeded /
// retriesFailed track the outcome of the single retry.
package overwriteoutcome_test

import (
	"sync/atomic"
	"testing"

	"github.com/barad1tos/noxctl/bear/overwriteoutcome"
)

// TestRecord exercises every branch of overwriteoutcome.Record via a
// table-driven matrix. The three single-outcome rows pin the counter
// deltas per branch; the cumulative row proves repeated calls accumulate
// against the same counters.
func TestRecord(t *testing.T) {
	cases := []struct {
		name              string
		outcomes          []overwriteoutcome.Outcome
		wantHashConflicts int64
		wantRetriesOK     int64
		wantRetriesFail   int64
	}{
		{"noConflict", []overwriteoutcome.Outcome{overwriteoutcome.NoConflict}, 0, 0, 0},
		{"retrySucceed", []overwriteoutcome.Outcome{overwriteoutcome.RetrySucceed}, 1, 1, 0},
		{"retryFail", []overwriteoutcome.Outcome{overwriteoutcome.RetryFail}, 1, 0, 1},
		{
			"cumulative",
			[]overwriteoutcome.Outcome{overwriteoutcome.RetrySucceed, overwriteoutcome.RetryFail},
			2, 1, 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var hashConflicts, retriesOK, retriesFail atomic.Int64
			for _, outcome := range tc.outcomes {
				overwriteoutcome.Record(outcome, &hashConflicts, &retriesOK, &retriesFail)
			}
			if got := hashConflicts.Load(); got != tc.wantHashConflicts {
				t.Errorf("hashConflicts = %d, want %d", got, tc.wantHashConflicts)
			}
			if got := retriesOK.Load(); got != tc.wantRetriesOK {
				t.Errorf("retriesOK = %d, want %d", got, tc.wantRetriesOK)
			}
			if got := retriesFail.Load(); got != tc.wantRetriesFail {
				t.Errorf("retriesFail = %d, want %d", got, tc.wantRetriesFail)
			}
		})
	}
}
