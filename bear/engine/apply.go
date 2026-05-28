// Package engine apply orchestrator — one-shot wrapper around the
// pre-pass + per-domain regen pipeline that previously lived in the
// legacy daemon's runAllRegens routine. Same pre-pass order, same
// log-and-continue policy, same self-write-gate discipline (the gate
// itself lives on engine.Daemon; Apply is one-shot and needs no gate).
//
// Layering: stdlib + bear + bear/state + golang.org/x/sys/unix.
// engine never imports bear/config — CLI shims map
// *config.Catalog.Features into engine.Features at the CLI boundary.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
	"github.com/barad1tos/noxctl/bear/state"
)

// ApplyOpts configures one Apply invocation. Plain struct, no methods,
// no defaults; caller supplies every field per "Accept interfaces,
// return structs".
type ApplyOpts struct {
	Domains   []*domain.Domain    // REQUIRED — engine iterates and calls regen.Run
	Pins      *domain.PinRegistry // REQUIRED — may be empty registry; nil-safe per bear/pins.go
	StatePath string              // REQUIRED — "./.noxctl/state.json"
	LockPath  string              // REQUIRED — "./.noxctl/.lock" (used by AcquireApply)
	Features  Features            // REQUIRED — flat pre-pass toggles
	NoWait    bool                // optional; --no-wait fail-fast on lock contention
	Stderr    io.Writer           // optional; default os.Stderr — used by lock-acquire wait advisory
	// AuditEnabled, when true, runs the Scan pre-pass. Mirrors
	// the legacy daemon's REGEN_AUDIT != "off" gate. Default false.
	AuditEnabled bool
	// SkipFlock — optional; when true, Apply skips both the AcquireApply
	// flock acquire AND the `.apply-pending` sentinel write. Used by
	// engine.Daemon.cycleOnce, which already holds the daemon flock and
	// must not nest-acquire (would deadlock on macOS BSD flock semantics
	// — flock is not ctx-aware, so a nested LOCK_EX on an independent fd
	// blocks indefinitely). Daemon sets SkipFlock=true on the inner
	// ApplyOpts inside cycleOnce.
	SkipFlock bool

	// BearcliConcurrency is the operator-tuned cap for concurrent bearcli
	// subprocesses. Zero or negative => engine default
	// DefaultBearcliConcurrency. When > 0, Apply calls bearcli.SetConcurrency(n)
	// once at entry before any pre-pass fires. The sync.Once gate inside
	// SetConcurrency means subsequent calls are silent no-ops; bench mode uses
	// bearcli.ResetPoolForTest to re-arm between sweep cycles.
	BearcliConcurrency int

	// WithMetrics, when true, copies the bearcli pool snapshot into
	// ApplyResult.Metrics at Apply completion. Zero cost when false. Set
	// by --bench mode; production daemon leaves false.
	WithMetrics bool

	// DomainTimingHook, when non-nil, is called inside runDomainAndSave
	// after each regen.Run returns. tag = domain.Tag, elapsed = wall-clock duration.
	// Used by --bench mode to accumulate
	// per_domain[] in the JSON envelope; nil in production. Hook fires
	// from worker goroutines — implementations MUST be concurrency-safe
	// (atomic/mutex).
	DomainTimingHook func(tag string, elapsed time.Duration)

	// DailyDefaultTag binds the operator-chosen tag for untagged-on-
	// create notes (e.g. Bear's compose-from-Notes-view). Empty disables
	// the auto-tag fast-pass entirely so a fork with no notion of a
	// "daily" tag stays silent. Wired from [meta].daily_default_tag in
	// the TOML catalog.
	DailyDefaultTag string

	// PromotionRules drives the time-based promotion fast-pass. Empty
	// disables time-promotion entirely. Each rule names a source tag,
	// a target tag, and the calendar boundary that triggers the move.
	// Wired from [[promotion]] blocks in the TOML catalog.
	PromotionRules []fastpass.PromotionRule
}

// DefaultBearcliConcurrency is the ship-default capacity for
// `bearcli.SetConcurrency` across every entry point — apply,
// daemon, plan. Exported so the read-only `noxctl plan` path can
// install the same ceiling without redeclaring the constant —
// silently drifting defaults are a maintenance trap.
const DefaultBearcliConcurrency = 8

const (
	applyInProgressVerb       = "apply"
	daemonApplyInProgressVerb = "daemon"
)

func applyInProgressVerbFor(opts ApplyOpts) string {
	if opts.SkipFlock {
		return daemonApplyInProgressVerb
	}
	return applyInProgressVerb
}

// Apply runs the orchestrator one-shot: acquires flock,
// runs pre-passes (gated by opts.Features), iterates opts.Domains
// calling regen.Run, persists state.json incrementally per-domain,
// releases flock.
//
// State.LastApply is set ONLY on successful completion; SIGINT or
// per-domain failure leaves it at the prior value. State.InProgress
// is set on entry, cleared on success — the plan engine uses both
// markers to discriminate completed-vs-interrupted runs.
//
// Stateless: owns no gate. Daemon (bear/engine/daemon.go) carries the
// regenMu/regenInProgress/regenEndTime state for FSEvents loops.
//
// This orchestrator preserves the per-DOMAIN ctx.Err check mirrored
// from the legacy daemon.
func Apply(ctx context.Context, opts ApplyOpts) (*ApplyResult, error) {
	result := &ApplyResult{
		PrePasses: make(map[string]PrePassCounts),
		Domains:   make(map[string]DomainCounts),
		StartedAt: time.Now().UTC(),
	}
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}

	// Step -1: initialize the global bearcli subprocess
	// pool. SetConcurrency is sync.Once-gated inside package bearcli,
	// so a second Apply in the same process is a no-op. Bench-mode
	// (--sweep) drives ResetPoolForTest between cycles to re-arm
	// at a new capacity; production daemon path is one-shot per process.
	if opts.BearcliConcurrency <= 0 {
		opts.BearcliConcurrency = DefaultBearcliConcurrency
	}
	bearcli.SetConcurrency(opts.BearcliConcurrency)

	// Step 0: acquire flock + write apply-pending sentinel. Gated on
	// !opts.SkipFlock so engine.Daemon.cycleOnce can call engine.Apply
	// without nesting flock — macOS BSD flock is not ctx-aware, so a
	// nested LOCK_EX on an independent fd from the same process blocks
	// indefinitely. When SkipFlock=true: skip BOTH the flock acquire
	// AND the sentinel write (semantic correctness — the daemon's
	// internal Apply is not an external apply requesting priority).
	release := func() { /* no-op default; reassigned when !SkipFlock acquires lock */ }
	if !opts.SkipFlock {
		lk, lockErr := AcquireApply(ctx, opts.LockPath, opts.NoWait, opts.Stderr)
		if lockErr != nil {
			return result, fmt.Errorf("engine.Apply lock: %w", lockErr)
		}
		release = lk
	}
	defer func() { release() }()

	// Step 1: load existing state (or zero State if file absent).
	st, err := state.Load(opts.StatePath)
	if err != nil {
		return result, fmt.Errorf("engine.Apply state.Load: %w", err)
	}
	if st.Version == "" {
		st.Version = "1"
	}
	if st.Domains == nil {
		st.Domains = make(map[string]state.DomainState)
	}

	// Step 2: write InProgress marker before any pre-pass mutation.
	st.InProgress = state.InProgress{Verb: applyInProgressVerbFor(opts), StartedAt: result.StartedAt}
	if err = st.Save(opts.StatePath); err != nil {
		return result, fmt.Errorf("engine.Apply state.Save(InProgress): %w", err)
	}

	// Step 3: pre-passes (gated by opts.Features).
	applyPrePasses(ctx, opts, result)

	// Step 4: per-domain loop.
	applyPerDomain(ctx, opts, st, result)

	// Step 5: finalize — set LastApply + clear InProgress IFF success.
	return applyFinalize(ctx, opts, st, result)
}
