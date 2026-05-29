// Package bearcli_test pins the optimistic-concurrency overwrite gate at the
// bearcli boundary. The content-hash reuse changes only the state.json content-
// hash INPUT, never the write gate — OverwriteWithRetry reads a FRESH hash
// immediately before each write, and a hash conflict (the note changed between
// our read and the write) triggers exactly one retry with a re-fetched hash.
// This regression PIN proves the gate fires (conflict detected, not silently
// overwritten) and that the final written body is the caller's desired body.
package bearcli_test

import (
	"context"
	"sync"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// gateBackend models a note whose hash changes between the first read and the
// first write: the initial overwrite is rejected with ErrHashConflict, then a
// fresh ShowHash returns a new hash and the retry succeeds. It captures the
// body of the successful write so the test can assert the regen's desired body
// landed (the gate never drops or corrupts the payload).
type gateBackend struct {
	mu             sync.Mutex
	overwriteSeen  int
	finalBody      string
	finalCommitted bool
}

func (b *gateBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`{}`), nil
	}
	switch args[0] {
	case "show":
		// A fresh hash on every call — the gate re-reads before each write.
		return []byte(`{"hash":"h-fresh"}`), nil
	case "overwrite":
		return b.overwrite(stdin)
	default:
		return []byte(`{}`), nil
	}
}

func (b *gateBackend) overwrite(body string) ([]byte, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.overwriteSeen++
	if b.overwriteSeen == 1 {
		// First write loses the optimistic race: the note changed since our
		// hash read. bearcli surfaces this as ErrHashConflict.
		return nil, bearcli.ErrHashConflict
	}
	b.finalBody = body
	b.finalCommitted = true
	return []byte(`{"ok":true}`), nil
}

func (b *gateBackend) committedBody() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.finalBody, b.finalCommitted
}

// TestOverwriteGate_ConflictThenRetrySucceeds pins the read->conflict->retry
// sequence: RetriesSucceeded increments by exactly 1, the final committed body
// is the caller's desired body, and two overwrite attempts were made (the gate
// fired rather than silently overwriting).
func TestOverwriteGate_ConflictThenRetrySucceeds(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	before := bearcli.MetricsSnapshot()

	backend := &gateBackend{}
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	const desiredBody = "# Desired\n#test | [[Index]]\n---\n- [[atom]]\n"
	if err := bearcli.OverwriteWithRetry(ctx, "note-1", desiredBody); err != nil {
		t.Fatalf("OverwriteWithRetry: %v (gate should recover via one retry)", err)
	}

	after := bearcli.MetricsSnapshot()
	if got := after.RetriesSucceeded - before.RetriesSucceeded; got != 1 {
		t.Errorf("RetriesSucceeded delta = %d, want 1 (conflict-then-retry must record exactly one success)", got)
	}
	if got := after.HashConflictsTotal - before.HashConflictsTotal; got != 1 {
		t.Errorf("HashConflictsTotal delta = %d, want 1 (the gate must detect the conflict)", got)
	}
	if got := after.RetriesFailed - before.RetriesFailed; got != 0 {
		t.Errorf("RetriesFailed delta = %d, want 0 (retry succeeded)", got)
	}

	body, committed := backend.committedBody()
	if !committed {
		t.Fatal("no overwrite committed; the retry never wrote the body")
	}
	if body != desiredBody {
		t.Errorf("final committed body = %q, want the caller's desired body %q", body, desiredBody)
	}
	if backend.overwriteSeen != 2 {
		t.Errorf("overwrite attempts = %d, want 2 (initial conflict + one retry; gate fired)", backend.overwriteSeen)
	}
}
