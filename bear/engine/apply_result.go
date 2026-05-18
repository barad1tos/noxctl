package engine

import (
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// ApplyResult is the return payload from Apply. Counts per pre-pass +
// per-domain feed PLAY RECAP rendering in cmd/noxctl/recap.go.
type ApplyResult struct {
	PrePasses   map[string]PrePassCounts // pre-pass name → counts (e.g. "foreign_tag")
	Domains     map[string]DomainCounts  // domain.Tag → counts
	StartedAt   time.Time
	CompletedAt time.Time // zero if Interrupted
	Interrupted bool      // true if ctx canceled mid-cycle

	// Metrics is the bearcli pool snapshot taken at Apply completion.
	// Populated when ApplyOpts.WithMetrics == true; zero-value otherwise.
	// Bench mode reads this to emit per-cycle throughput
	// numbers; production daemon path leaves WithMetrics false so the
	// snapshot is a zero-cost no-op.
	Metrics bear.BearcliMetrics
}

// PrePassCounts captures per-pre-pass outcomes. OK = atoms processed
// without a state change; Changed = atoms that produced a write;
// Failed = per-atom log-and-continue failures (does NOT include
// ctx-cancel which surfaces via ApplyResult.Interrupted).
type PrePassCounts struct {
	OK, Changed, Failed int
}

// DomainCounts captures per-domain RunRegen outcomes for the PLAY RECAP
// table. Ansible-style: Created/Changed/Unchanged/Failed.
type DomainCounts struct {
	Created, Changed, Unchanged, Failed int
}

// AnyFailed reports whether any pre-pass or domain has Failed > 0.
// cmd/noxctl/apply.go uses this to decide between exit 0 and exit 1
// when the engine returned no error (i.e., partial-failure case).
func (r *ApplyResult) AnyFailed() bool {
	if r == nil {
		return false
	}
	for _, c := range r.PrePasses {
		if c.Failed > 0 {
			return true
		}
	}
	for _, c := range r.Domains {
		if c.Failed > 0 {
			return true
		}
	}
	return false
}
