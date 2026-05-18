package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/state"
)

// fakeWatcher is the test seam injected via engine.NewDaemonWithWatcher.
// Implements engine.FsWatcher (exported per W8 fix).
type fakeWatcher struct {
	events chan fsnotify.Event
	errors chan error

	mu     sync.Mutex
	added  []string
	closed bool
}

func newFakeWatcher() *fakeWatcher {
	return &fakeWatcher{
		events: make(chan fsnotify.Event, 16),
		errors: make(chan error, 4),
	}
}

func (f *fakeWatcher) Events() <-chan fsnotify.Event { return f.events }
func (f *fakeWatcher) Errors() <-chan error          { return f.errors }

func (f *fakeWatcher) Add(path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.added = append(f.added, path)
	return nil
}

func (f *fakeWatcher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.events)
	close(f.errors)
	return nil
}

// Static type-check: fakeWatcher satisfies the exported interface.
var _ engine.FsWatcher = (*fakeWatcher)(nil)

// inProgressVerb reads state.json (if any) and returns the current
// InProgress.Verb. Returns "" if state.json absent or unmarshal fails.
// Extracted to dedupe the same decode block across SentinelYields and
// NonDBEventIgnored tests (dupl ≥30 tokens trips otherwise).
func inProgressVerb(t *testing.T, statePath string) string {
	t.Helper()
	raw, err := os.ReadFile(statePath)
	if err != nil {
		return ""
	}
	var st state.State
	_ = json.Unmarshal(raw, &st)
	return st.InProgress.Verb
}

func newTestDaemonOpts(t *testing.T) engine.DaemonOpts {
	t.Helper()
	dir := t.TempDir()
	return engine.DaemonOpts{
		ApplyOpts: engine.ApplyOpts{
			Domains:   nil,
			Pins:      nil,
			StatePath: filepath.Join(dir, "state.json"),
			LockPath:  filepath.Join(dir, ".lock"),
			Features:  engine.Features{}, // all false — no bearcli traffic
			Stderr:    os.Stderr,
		},
		BearDBDir:        dir,
		DebouncePause:    50 * time.Millisecond,
		MaxBurstWindow:   500 * time.Millisecond,
		SelfWriteEpsilon: 100 * time.Millisecond,
	}
}

func TestDaemon_GracefulShutdown(t *testing.T) {
	opts := newTestDaemonOpts(t)
	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Errorf("expected context canceled error, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("daemon did not shut down within BearcliTimeout (10s)")
	}
}

func TestDaemon_SentinelYieldsCycle(t *testing.T) {
	opts := newTestDaemonOpts(t)
	// Pre-create sentinel in the same dir as LockPath.
	if err := os.MkdirAll(filepath.Dir(opts.LockPath), 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	sentinel := filepath.Join(filepath.Dir(opts.LockPath), engine.SentinelName)
	if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	var logBuf bytes.Buffer
	log.SetOutput(&logBuf)
	defer log.SetOutput(os.Stderr)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Inject a watched DB event.
	fw.events <- fsnotify.Event{
		Name: filepath.Join(opts.BearDBDir, "database.sqlite-wal"),
		Op:   fsnotify.Write,
	}
	// Wait for debounce + cycleOnce to fire and yield.
	time.Sleep(opts.DebouncePause + 200*time.Millisecond)
	cancel()
	<-done

	if !strings.Contains(logBuf.String(), "apply-pending sentinel present") {
		t.Errorf("expected sentinel-skip log line; got: %s", logBuf.String())
	}
	if verb := inProgressVerb(t, opts.StatePath); verb != "" {
		t.Errorf("expected no InProgress write on yielded cycle; got %q", verb)
	}
}

func TestDaemon_NonDBEventIgnored(t *testing.T) {
	opts := newTestDaemonOpts(t)
	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Inject an event for an unrelated file.
	fw.events <- fsnotify.Event{
		Name: filepath.Join(opts.BearDBDir, "unrelated.txt"),
		Op:   fsnotify.Write,
	}
	time.Sleep(opts.DebouncePause + 100*time.Millisecond)
	cancel()
	<-done

	// state.json may not exist at all; if it does, InProgress must be clear.
	if verb := inProgressVerb(t, opts.StatePath); verb != "" {
		t.Errorf("non-DB event triggered Apply; state shows InProgress=%q", verb)
	}
}

// TestDaemon_FakeCycleProducesStateWrite is the smoke test that
// fakeWatcher injection + event coalescing actually drives cycleOnce.
// Asserts a watched DB event with NO sentinel triggers a state.json
// write within DebouncePause + 300ms.
func TestDaemon_FakeCycleProducesStateWrite(t *testing.T) {
	opts := newTestDaemonOpts(t)
	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	fw.events <- fsnotify.Event{
		Name: filepath.Join(opts.BearDBDir, "database.sqlite"),
		Op:   fsnotify.Write,
	}
	time.Sleep(opts.DebouncePause + 300*time.Millisecond)
	cancel()
	<-done

	if _, statErr := os.Stat(opts.StatePath); statErr != nil {
		t.Errorf("expected state.json after cycle; got %v", statErr)
	}
}

// TestDaemon_CycleOnce_DoesNotDeadlock is the explicit regression
// test for B-flock-deadlock (Iteration 2). If the executor drops
// `applyOpts.SkipFlock = true` from cycleOnce, the daemon→Apply path
// would call AcquireDaemon (LOCK_EX on fd1) → engine.Apply →
// AcquireApply (LOCK_EX on fd2 for the SAME lockPath). On macOS BSD
// flock semantics this deadlocks indefinitely; flock is not
// ctx-aware, so cancellation cannot break it. This test arms a
// 2-second wall-clock fail-safe and reports the deadlock-mode
// explicitly so the regression is unambiguous.
func TestDaemon_CycleOnce_DoesNotDeadlock(t *testing.T) {
	opts := newTestDaemonOpts(t)
	fw := newFakeWatcher()
	d := engine.NewDaemonWithWatcher(opts, fw)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()

	// Inject a watched DB event to drive cycleOnce.
	fw.events <- fsnotify.Event{
		Name: filepath.Join(opts.BearDBDir, "database.sqlite"),
		Op:   fsnotify.Write,
	}

	// Wait long enough for debounce + cycleOnce + Apply to complete.
	// If SkipFlock=true is intact, this completes well under 1s.
	// If SkipFlock is missing, cycleOnce deadlocks at AcquireApply
	// and we hit the 2s deadline below.
	deadline := time.After(2 * time.Second)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	cycleCompleted := false
pollLoop:
	for {
		select {
		case <-ticker.C:
			if _, err := os.Stat(opts.StatePath); err == nil {
				cycleCompleted = true
				break pollLoop
			}
		case <-deadline:
			t.Fatalf("cycleOnce deadlocked — likely nested flock acquire " +
				"(B-flock-deadlock regression: applyOpts.SkipFlock = true " +
				"is missing in daemon.go::cycleOnce)")
		}
	}
	if !cycleCompleted {
		t.Errorf("cycle did not complete within deadline")
	}
	cancel()
	<-done
}
