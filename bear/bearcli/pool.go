// Package bearcli owns the bearcli subprocess boundary: a global
// concurrency semaphore, per-kind metrics, a test seam for fake
// backends, and the low-level Run primitive every bearcli call goes
// through. Lives below bear/ so every higher layer (render, fastpass,
// audit, engine, cli/*) can import it without forming an import
// cycle back through bear/.
//
// The package intentionally drops the `Bearcli` prefix that the
// original bear/-level API carried — exports read as
// `bearcli.Run`, `bearcli.Backend`, `bearcli.Metrics`, etc. Old names
// remain available as backward-compatible aliases in bear/, see
// bear/aliases.go.
package bearcli

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// errPoolNotInit is returned by Acquire when the package-level
// semaphore has not been initialized via SetConcurrency yet. In
// production the daemon's startup path calls SetConcurrency before
// any bearcli traffic, so this branch is reachable only when tests
// link the package without bootstrapping the pool. Surfaces as an
// actionable error rather than a nil-channel panic.
var errPoolNotInit = errors.New("bearcli pool not initialized; call SetConcurrency first")

// Metrics is a read-only snapshot of the pool counters. Returned by
// MetricsSnapshot for the audit reporter. All fields are plain values
// safe for JSON encoding; no atomics escape.
type Metrics struct {
	// Capacity is the current semaphore slot count (the configured
	// bearcli_concurrency knob).
	Capacity int
	// PeakConcurrent is the highest observed inFlight value since the
	// last reset.
	PeakConcurrent int64
	// AcquireCount is the total number of Acquire calls since the
	// last reset (one per bearcli invocation).
	AcquireCount int64
	// WaitNanosSum is the cumulative wait time across all acquires;
	// callers render Average = WaitNanosSum / AcquireCount.
	WaitNanosSum int64
	// CallsByKind segregates AcquireCount by the bearcli sub-command
	// argument ("list", "cat", "show", "overwrite", "create", "find",
	// "trash", "tag"). New verbs slot in here AND in incCallKind.
	CallsByKind map[string]int64
	// HashConflictsTotal counts the ErrHashConflict events observed
	// by OverwriteWithRetry (regardless of retry outcome).
	HashConflictsTotal int64
	// RetriesSucceeded counts hash-conflict retries that succeeded on
	// the second attempt.
	RetriesSucceeded int64
	// RetriesFailed counts hash-conflict retries that still failed
	// after the second attempt (or could not fetch a fresh hash).
	RetriesFailed int64
}

// Backend abstracts the bearcli subprocess boundary so tests can
// inject a fake. Production code never implements this interface —
// Run falls through to a real exec.Command when no backend is
// stamped on the context. Test fixtures use this seam to record
// per-call (kind, args, timestamp) and sleep deterministically inside
// synctest bubbles.
type Backend interface {
	// Run mirrors the boundary of Run's cmd.Run: receive args plus
	// optional stdin, return stdout bytes plus error. Backends decide
	// their own latency and error semantics.
	Run(ctx context.Context, args []string, stdin string) ([]byte, error)
}

// backendKey is the unexported context key for the Backend test
// seam. Using a private type prevents collisions with other packages'
// context values.
type backendKey struct{}

// poolMetrics holds the atomic counter state for the pool. Every
// field except capacity is incremented from concurrent goroutines —
// hence atomic.Int64. capacity is written under poolMu's WLock and
// read under RLock; it is stored as atomic.Int64 anyway for cheap
// reads from MetricsSnapshot without taking the mutex.
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
	callsTrash     atomic.Int64
	callsTag       atomic.Int64
	hashConflicts  atomic.Int64
	retriesOK      atomic.Int64
	retriesFail    atomic.Int64
}

// Package-level state.
//
// poolOnce gates daemon-path initialisation — SetConcurrency is a
// no-op after the first call.
//
// poolMu protects the sema field (the channel pointer), not the
// channel slots: acquire/release take an RLock and snapshot the
// pointer locally before operating on it. Reset takes a WLock and
// swaps the channel atomically.
//
// metrics is the single instance of poolMetrics; its fields are
// updated via the atomic methods directly (no lock needed for
// counters).
var (
	poolOnce sync.Once
	poolMu   sync.RWMutex
	sema     chan struct{}
	metrics  poolMetrics
)

// SetConcurrency installs the global semaphore at capacity n. MUST
// be called exactly once during daemon startup, before any goroutine
// invokes Run. sync.Once-gated — subsequent calls are silent no-ops
// (the daemon resilience contract; second-call would surface as a
// startup race rather than recoverable state). Panics when n <= 0
// to fail fast on misconfiguration; config-layer validation should
// reject the bad value before we reach this point.
func SetConcurrency(n int) {
	poolOnce.Do(func() {
		if n <= 0 {
			panic("bearcli.SetConcurrency: capacity must be positive")
		}
		poolMu.Lock()
		defer poolMu.Unlock()
		sema = make(chan struct{}, n)
		metrics.capacity.Store(int64(n))
	})
}

// ResetPoolForTest replaces the semaphore channel, zeroes every
// counter, and re-arms the sync.Once gate so a subsequent
// SetConcurrency call takes effect again.
//
// ONLY for tests and the bench --sweep mode that measures throughput
// across multiple concurrency values in one run. The daemon hot path
// must never call this — a concurrent Acquire during reset would
// block on the WLock and resume on the new channel, which is correct
// but only meaningful for benchmark-style reuse, not steady-state
// operation.
//
// Caller's responsibility: ensure no bearcli traffic is in flight
// before invoking. The function does NOT wait for outstanding
// releases.
//
// Silently returns when n <= 0 (defensive — leaves state untouched).
func ResetPoolForTest(n int) {
	if n <= 0 {
		return
	}
	poolMu.Lock()
	defer poolMu.Unlock()
	poolOnce = sync.Once{}
	sema = make(chan struct{}, n)
	metrics = poolMetrics{}
	metrics.capacity.Store(int64(n))
}

// ResetMetrics zeroes every counter while leaving the semaphore
// channel intact. Used by bench mode between --sweep cycles when the
// operator wants throughput numbers segregated by capacity setting
// but shares the same channel across cycles.
func ResetMetrics() {
	poolMu.Lock()
	defer poolMu.Unlock()
	current := metrics.capacity.Load()
	metrics = poolMetrics{}
	metrics.capacity.Store(current)
}

// AcquireForTest is a test-only export of the unexported Acquire.
// Tests in tests/bear/ live in an external package and cannot reach
// the package-private function directly; promoting it behind an
// explicit ForTest suffix matches the precedent at
// bear/engine/daemon.go (NewDaemonWithWatcher).
//
// Production callers MUST use the unexported Acquire via Run —
// calling AcquireForTest from production is a bug the name suffix is
// designed to surface in code review.
func AcquireForTest(ctx context.Context, kind string) (func(), error) {
	return acquire(ctx, kind)
}

// MetricsSnapshot reads every atomic counter into a plain-value
// struct safe for JSON encoding. Map allocation for CallsByKind
// happens here — callers may rely on a non-nil map even when no
// traffic has flowed through the pool. Reading capacity from the
// atomic (not the mutex-guarded channel field) keeps the snapshot
// allocation-free apart from the map.
func MetricsSnapshot() Metrics {
	return Metrics{
		Capacity:       int(metrics.capacity.Load()),
		PeakConcurrent: metrics.peak.Load(),
		AcquireCount:   metrics.acquireCount.Load(),
		WaitNanosSum:   metrics.waitNanosSum.Load(),
		CallsByKind: map[string]int64{
			"list":      metrics.callsList.Load(),
			"cat":       metrics.callsCat.Load(),
			"show":      metrics.callsShow.Load(),
			"overwrite": metrics.callsOverwrite.Load(),
			"create":    metrics.callsCreate.Load(),
			"find":      metrics.callsFind.Load(),
			"trash":     metrics.callsTrash.Load(),
			"tag":       metrics.callsTag.Load(),
		},
		HashConflictsTotal: metrics.hashConflicts.Load(),
		RetriesSucceeded:   metrics.retriesOK.Load(),
		RetriesFailed:      metrics.retriesFail.Load(),
	}
}

// ContextWithBackend returns ctx with backend stamped under a private
// context key. Run checks the context first and dispatches to
// backend.Run when present, falling through to real exec.Command
// otherwise. Production code never calls this — only test fixtures
// inject backends.
func ContextWithBackend(parent context.Context, backend Backend) context.Context {
	return context.WithValue(parent, backendKey{}, backend)
}

// BackendFromContext returns the Backend stamped on ctx, or nil if
// none was injected. Used internally by Run; exported so external
// tests can verify the context-stamping contract.
func BackendFromContext(ctx context.Context) Backend {
	v, _ := ctx.Value(backendKey{}).(Backend)
	return v
}

// acquire reserves one semaphore slot and increments the per-kind
// counter. Returns a release closure callers MUST invoke (typically
// via `defer`) so the slot is returned even on panic.
//
// The closure captures the channel pointer it acquired on so a
// concurrent ResetPoolForTest swap can't deadlock: release always
// happens on the same channel the acquire took.
//
// The ctx.Done branch propagates SIGINT promptly — a goroutine
// blocked on the semaphore returns ctx.Err and DOES NOT consume a
// slot, so the caller does not need to release in the error path.
func acquire(ctx context.Context, kind string) (func(), error) {
	poolMu.RLock()
	semaSnapshot := sema
	poolMu.RUnlock()
	if semaSnapshot == nil {
		return nil, errPoolNotInit
	}

	start := time.Now()
	select {
	case semaSnapshot <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	metrics.waitNanosSum.Add(time.Since(start).Nanoseconds())
	metrics.acquireCount.Add(1)
	inflight := metrics.inFlight.Add(1)
	updatePeak(inflight)
	incCallKind(kind)

	release := func() {
		metrics.inFlight.Add(-1)
		<-semaSnapshot
	}
	return release, nil
}

// updatePeak atomically lifts metrics.peak to inflight when inflight
// is higher. A simple CAS loop avoids the lost-update window between
// Load and Store under contention.
func updatePeak(inflight int64) {
	for {
		current := metrics.peak.Load()
		if inflight <= current {
			return
		}
		if metrics.peak.CompareAndSwap(current, inflight) {
			return
		}
	}
}

// incCallKind bumps the per-kind counter for kind. Unknown kinds are
// a silent no-op (defensive — caller is kindFromArgs which already
// folds unrecognized args into "other", but a stray test-only call
// with an unknown kind should not panic).
func incCallKind(kind string) {
	switch kind {
	case "list":
		metrics.callsList.Add(1)
	case "cat":
		metrics.callsCat.Add(1)
	case "show":
		metrics.callsShow.Add(1)
	case "overwrite":
		metrics.callsOverwrite.Add(1)
	case "create":
		metrics.callsCreate.Add(1)
	case "find":
		metrics.callsFind.Add(1)
	case "trash":
		metrics.callsTrash.Add(1)
	case "tag":
		metrics.callsTag.Add(1)
	}
}
