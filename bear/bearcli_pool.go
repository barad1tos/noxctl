package bear

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// errBearcliPoolNotInit is returned by acquireBearcli when the package-level
// semaphore has not been initialized via SetBearcliConcurrency yet. In
// production the daemon's startup path calls SetBearcliConcurrency
// before any bearcli traffic, so this branch is reachable only when tests
// link the bear package without bootstrapping the pool. Surfaces as an
// actionable error rather than a nil-channel panic.
var errBearcliPoolNotInit = errors.New("bearcli pool not initialized; call SetBearcliConcurrency first")

// BearcliMetrics is a read-only snapshot of the bearcli pool counters.
// Returned by BearcliMetricsSnapshot for the audit reporter.
// All fields are plain values safe for JSON encoding; no atomics escape.
type BearcliMetrics struct {
	// Capacity is the current semaphore slot count (the configured
	// bearcli_concurrency knob).
	Capacity int
	// PeakConcurrent is the highest observed inFlight value since the
	// last reset.
	PeakConcurrent int64
	// AcquireCount is the total number of acquireBearcli calls since
	// the last reset (one per bearcli invocation).
	AcquireCount int64
	// WaitNanosSum is the cumulative wait time across all acquires;
	// callers render Average = WaitNanosSum / AcquireCount.
	WaitNanosSum int64
	// CallsByKind segregates AcquireCount by the bearcli sub-command
	// argument ("list", "cat", "show", "overwrite", "create", "find").
	CallsByKind map[string]int64
	// HashConflictsTotal counts the ErrHashConflict events observed
	// by overwriteWithRetry (regardless of retry outcome).
	HashConflictsTotal int64
	// RetriesSucceeded counts hash-conflict retries that succeeded on
	// the second attempt.
	RetriesSucceeded int64
	// RetriesFailed counts hash-conflict retries that still failed
	// after the second attempt (or could not fetch a fresh hash).
	RetriesFailed int64
}

// BearcliBackend abstracts the bearcli subprocess boundary so tests can
// inject a fake. Production code never implements this interface —
// runBearcli falls through to a real exec.Command when no backend is
// stamped on the context. fixtures use this seam to record
// per-call (kind, args, timestamp) and sleep deterministically inside
// synctest bubbles.
type BearcliBackend interface {
	// Run mirrors the boundary of runBearcli's cmd.Run: receive args
	// plus optional stdin, return stdout bytes plus error. Backends
	// decide their own latency and error semantics.
	Run(ctx context.Context, args []string, stdin string) ([]byte, error)
}

// bearcliBackendKey is the unexported context key for the BearcliBackend
// test seam. Using a private type prevents collisions with other packages'
// context values.
type bearcliBackendKey struct{}

// poolMetrics holds the atomic counter state for the bearcli pool.
// Every field except capacity is incremented from concurrent goroutines —
// hence atomic.Int64. capacity is written under bearcliPoolMu's WLock and
// read under RLock; it is stored as atomic.Int64 anyway for cheap reads
// from BearcliMetricsSnapshot without taking the mutex.
type poolMetrics struct {
	capacity       atomic.Int64
	inFlight       atomic.Int64
	peak           atomic.Int64
	waitNanosSum   atomic.Int64
	acquireCount   atomic.Int64
	callsList      atomic.Int64
	callsCat       atomic.Int64
	callsShow      atomic.Int64
	callsOverwrite atomic.Int64
	callsCreate    atomic.Int64
	callsFind      atomic.Int64
	hashConflicts  atomic.Int64
	retriesOK      atomic.Int64
	retriesFail    atomic.Int64
}

// Package-level state.
//
// bearcliPoolOnce gates daemon-path initialisation — SetBearcliConcurrency
// is a no-op after the first call.
//
// bearcliPoolMu protects the bearcliSema field (the channel pointer), not
// the channel slots: acquire/release take an RLock and snapshot the pointer
// locally before operating on it. Reset takes a WLock and swaps the channel
// atomically.
//
// bearcliMetrics is the single instance of poolMetrics; its fields are
// updated via the atomic methods directly (no lock needed for counters).
var (
	bearcliPoolOnce sync.Once
	bearcliPoolMu   sync.RWMutex
	bearcliSema     chan struct{}
	bearcliMetrics  poolMetrics
)

// SetBearcliConcurrency installs the global bearcli semaphore at capacity n.
// MUST be called exactly once during daemon startup, before any goroutine
// invokes runBearcli. sync.Once-gated — subsequent calls are silent no-ops
// (the daemon resilience contract; second-call would surface as a startup
// race rather than recoverable state). Panics when n <= 0 to fail fast on
// misconfiguration; config-layer validation should reject the bad value
// before we reach this point.
func SetBearcliConcurrency(n int) {
	bearcliPoolOnce.Do(func() {
		if n <= 0 {
			panic("bear.SetBearcliConcurrency: capacity must be positive")
		}
		bearcliPoolMu.Lock()
		defer bearcliPoolMu.Unlock()
		bearcliSema = make(chan struct{}, n)
		bearcliMetrics.capacity.Store(int64(n))
	})
}

// ResetBearcliPoolForTest replaces the semaphore channel, zeroes every
// counter, and re-arms the sync.Once gate so a subsequent
// SetBearcliConcurrency call takes effect again.
//
// ONLY for tests and the regen-watchd --bench --sweep mode that measures
// throughput across multiple concurrency values in one run. The daemon
// hot path must never call this — a concurrent acquireBearcli during reset
// would block on the WLock and resume on the new channel, which is correct
// but only meaningful for benchmark-style reuse, not steady-state operation.
//
// Caller's responsibility: ensure no bearcli traffic is in flight before
// invoking. The function does NOT wait for outstanding releases.
//
// Silently returns when n <= 0 (defensive — leaves state untouched).
func ResetBearcliPoolForTest(n int) {
	if n <= 0 {
		return
	}
	bearcliPoolMu.Lock()
	defer bearcliPoolMu.Unlock()
	bearcliPoolOnce = sync.Once{}
	bearcliSema = make(chan struct{}, n)
	bearcliMetrics = poolMetrics{}
	bearcliMetrics.capacity.Store(int64(n))
}

// ResetBearcliMetrics zeroes every counter while leaving the semaphore
// channel intact. Used by bench mode between --sweep cycles when the
// operator wants throughput numbers segregated by capacity setting but
// shares the same channel across cycles.
func ResetBearcliMetrics() {
	bearcliPoolMu.Lock()
	defer bearcliPoolMu.Unlock()
	cap := bearcliMetrics.capacity.Load()
	bearcliMetrics = poolMetrics{}
	bearcliMetrics.capacity.Store(cap)
}

// AcquireBearcliForTest is a test-only export of the unexported
// acquireBearcli. Tests in tests/bear/ live in an external package and
// cannot reach the package-private function directly; promoting it
// behind an explicit ForTest suffix matches the precedent at
// bear/engine/daemon.go (NewDaemonWithWatcher).
//
// Production callers MUST use the unexported acquireBearcli via
// runBearcli — calling AcquireBearcliForTest from production is a bug
// the name suffix is designed to surface in code review.
func AcquireBearcliForTest(ctx context.Context, kind string) (func(), error) {
	return acquireBearcli(ctx, kind)
}

// BearcliMetricsSnapshot reads every atomic counter into a plain-value
// struct safe for JSON encoding. Map allocation for CallsByKind happens
// here — callers may rely on a non-nil map even when no traffic has
// flowed through the pool. Reading capacity from the atomic (not the
// mutex-guarded channel field) keeps the snapshot allocation-free apart
// from the map.
func BearcliMetricsSnapshot() BearcliMetrics {
	return BearcliMetrics{
		Capacity:       int(bearcliMetrics.capacity.Load()),
		PeakConcurrent: bearcliMetrics.peak.Load(),
		AcquireCount:   bearcliMetrics.acquireCount.Load(),
		WaitNanosSum:   bearcliMetrics.waitNanosSum.Load(),
		CallsByKind: map[string]int64{
			"list":      bearcliMetrics.callsList.Load(),
			"cat":       bearcliMetrics.callsCat.Load(),
			"show":      bearcliMetrics.callsShow.Load(),
			"overwrite": bearcliMetrics.callsOverwrite.Load(),
			"create":    bearcliMetrics.callsCreate.Load(),
			"find":      bearcliMetrics.callsFind.Load(),
		},
		HashConflictsTotal: bearcliMetrics.hashConflicts.Load(),
		RetriesSucceeded:   bearcliMetrics.retriesOK.Load(),
		RetriesFailed:      bearcliMetrics.retriesFail.Load(),
	}
}

// ContextWithBackend returns ctx with backend stamped under a private
// context key. runBearcli checks the context first and dispatches to
// backend.Run when present, falling through to real exec.Command otherwise.
// Production code never calls this — only test fixtures inject backends.
func ContextWithBackend(parent context.Context, backend BearcliBackend) context.Context {
	return context.WithValue(parent, bearcliBackendKey{}, backend)
}

// BackendFromContext returns the BearcliBackend stamped on ctx, or nil if
// none was injected. Used internally by runBearcli; exported so external
// tests can verify the context-stamping contract.
func BackendFromContext(ctx context.Context) BearcliBackend {
	v, _ := ctx.Value(bearcliBackendKey{}).(BearcliBackend)
	return v
}

// acquireBearcli reserves one semaphore slot and increments the per-kind
// counter. Returns a release closure callers MUST invoke (typically via
// `defer`) so the slot is returned even on panic.
//
// The closure captures the channel pointer it acquired on so a concurrent
// ResetBearcliPoolForTest swap can't deadlock: release always happens on
// the same channel the acquire took.
//
// The ctx.Done branch propagates SIGINT promptly — a goroutine blocked on
// the semaphore returns ctx.Err and DOES NOT consume a slot, so the
// caller does not need to release in the error path.
func acquireBearcli(ctx context.Context, kind string) (func(), error) {
	bearcliPoolMu.RLock()
	sema := bearcliSema
	bearcliPoolMu.RUnlock()
	if sema == nil {
		return nil, errBearcliPoolNotInit
	}

	start := time.Now()
	select {
	case sema <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	bearcliMetrics.waitNanosSum.Add(time.Since(start).Nanoseconds())
	bearcliMetrics.acquireCount.Add(1)
	inflight := bearcliMetrics.inFlight.Add(1)
	updatePeak(inflight)
	incCallKind(kind)

	release := func() {
		bearcliMetrics.inFlight.Add(-1)
		<-sema
	}
	return release, nil
}

// updatePeak atomically lifts bearcliMetrics.peak to inflight when inflight
// is higher. A simple CAS loop avoids the lost-update window between
// Load and Store under contention.
func updatePeak(inflight int64) {
	for {
		current := bearcliMetrics.peak.Load()
		if inflight <= current {
			return
		}
		if bearcliMetrics.peak.CompareAndSwap(current, inflight) {
			return
		}
	}
}

// incCallKind bumps the per-kind counter for kind. Unknown kinds are a
// silent no-op (defensive — caller is bearcliKindFromArgs which already
// folds unrecognized args into "other", but a stray test-only call with
// an unknown kind should not panic).
func incCallKind(kind string) {
	switch kind {
	case "list":
		bearcliMetrics.callsList.Add(1)
	case "cat":
		bearcliMetrics.callsCat.Add(1)
	case "show":
		bearcliMetrics.callsShow.Add(1)
	case "overwrite":
		bearcliMetrics.callsOverwrite.Add(1)
	case "create":
		bearcliMetrics.callsCreate.Add(1)
	case "find":
		bearcliMetrics.callsFind.Add(1)
	}
}
