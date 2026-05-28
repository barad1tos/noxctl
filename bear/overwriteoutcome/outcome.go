// Package overwriteoutcome captures the three counter-relevant exit
// paths of domain.overwriteWithRetry and exposes a pure Record routine
// for incrementing hash-conflict / retry-succeeded / retry-failed
// metrics counters.
//
// Lives as a sub-package so its arithmetic can be unit-tested from
// tests/bear/overwriteoutcome/ without exposing internal bearcli-pool
// counters in the bear public API and without dropping a *_test.go
// file inside bear/.
package overwriteoutcome

import "sync/atomic"

// Outcome enumerates the three counter-relevant exit paths of
// overwriteWithRetry.
type Outcome int

const (
	// NoConflict — first overwrite succeeded; no counter touched.
	NoConflict Outcome = iota
	// RetrySucceed — first overwrite hit ErrHashConflict, the single
	// retry succeeded. hashConflicts + retriesOK both incremented.
	RetrySucceed
	// RetryFail — first overwrite hit ErrHashConflict, the retry also
	// failed (or could not even fetch a fresh hash). hashConflicts +
	// retriesFail both incremented.
	RetryFail
)

// Record updates the bearcli pool's hash-conflict counters according
// to outcome. Counters are passed as pointers so this routine has no
// global state and stays trivially testable from an external package.
//
// Wired from bearcli.OverwriteWithRetry's three counter-increment branches.
// The audit reporter reads the resulting metrics via bearcli.MetricsSnapshot.
func Record(outcome Outcome, hashConflicts, retriesOK, retriesFail *atomic.Int64) {
	switch outcome {
	case NoConflict:
		// happy path — no counter change
	case RetrySucceed:
		hashConflicts.Add(1)
		retriesOK.Add(1)
	case RetryFail:
		hashConflicts.Add(1)
		retriesFail.Add(1)
	}
}
