// Package bear_test — atomic tag-membership guard tests.
//
// Bug fix: canonical-pingpong (, 2026-05-14).
//
// When a Bear note is dragged onto a tag, Bear emits multiple SQLite writes
// (text edit + tag set + sync), each of which fires a separate FSEvent burst
// after the 2s debounce. Between bursts the note's tag index inside Bear can
// transiently still return the note via `--tag quicknote/daily` even though
// its canonical tag-line has already been rewritten to `#development/noxctl`
// by a prior cycle. Without a tag-membership guard inside processAtomic, the
// daily domain happily restamps the note as `❋ Daily | Нова нотатка`,
// flipping its canonical to the wrong domain — visible to the user as
// canonical ping-pong across 2-3 cycles.
//
// The guard: a domain must refuse to canonicalize an atom whose current
// Note.Tags array does not contain its own d.Tag. This is a strict
// invariant — domains may only operate on notes that explicitly carry
// their tag, not on tag-index residue from in-flight Bear writes.
package bear_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// recordingBackend counts every Run call so the test can prove processAtomic
// short-circuited before reaching the bearcli boundary. Returns an error if
// the backend is ever invoked — the guard MUST prevent any subprocess work.
type recordingBackend struct {
	calls atomic.Int64
}

func (b *recordingBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	b.calls.Add(1)
	return nil, errors.New("recordingBackend: must not be reached when tag guard is in place")
}

// TestProcessAtomicSkipsNoteNotCarryingDomainTag proves the daily domain
// refuses to canonicalize a note whose Tags array does not include
// quicknote/daily. Without the guard, daily would stamp `❋ Daily | …`
// over a note that actually belongs to development/noxctl (canonical
// ping-pong bug observed live on 2026-05-14).
func TestProcessAtomicSkipsNoteNotCarryingDomainTag(t *testing.T) {
	domain.ResetBearcliPoolForTest(1)
	domain.ResetBearcliMetrics()

	backend := &recordingBackend{}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	note := domain.Note{
		ID:      "test-note-id-001",
		Title:   "14 May 2026 at 17:57",
		Content: "# 14 May 2026 at 17:57\n\n#development/noxctl | [[✱ Development]] | noxctl",
		Tags:    []string{"#development", "#development/noxctl"}, // note does NOT carry #quicknote/daily
	}

	touched, failed := testutil.Domain(t, "quicknote/daily").ProcessAtomicForTest(ctx, note, "Daily")

	if got := backend.calls.Load(); got != 0 {
		t.Errorf("recordingBackend was invoked %d times; expected 0 — the tag guard should have skipped before any bearcli call", got)
	}
	if touched != 0 {
		t.Errorf("touched = %d, want 0 — daily must not canonicalize a non-daily note", touched)
	}
	if failed != 0 {
		t.Errorf("failed = %d, want 0 — guard should be a clean skip, not a failure", failed)
	}
}

// TestProcessAtomicCanonicalizesNoteCarryingDomainTag is the positive
// counterpart: a note that DOES carry the domain's tag must reach the
// bearcli boundary (i.e. the guard must not be over-eager and block valid
// canonicalization).
func TestProcessAtomicCanonicalizesNoteCarryingDomainTag(t *testing.T) {
	domain.ResetBearcliPoolForTest(1)
	domain.ResetBearcliMetrics()

	backend := &recordingBackend{}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	note := domain.Note{
		ID:      "test-note-id-002",
		Title:   "13 May 2026 at 09:00",
		Content: "# 13 May 2026 at 09:00\n\n#quicknote/daily | [[❋ Daily]] | Нова нотатка",
		Tags:    []string{"#quicknote/daily"}, // bearcli returns tags with leading "#"
	}

	// processAtomic should attempt the bearcli call. The recordingBackend
	// returns an error, so processAtomic reports failed=1 — that's fine for
	// this test: we only assert the guard did NOT short-circuit.
	_, _ = testutil.Domain(t, "quicknote/daily").ProcessAtomicForTest(ctx, note, "Daily")

	if got := backend.calls.Load(); got == 0 {
		t.Errorf("recordingBackend was never invoked; expected ≥1 — daily SHOULD canonicalize a note that carries quicknote/daily")
	}
}
