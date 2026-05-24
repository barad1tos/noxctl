package bear_test

// fastpass_apply_test.go drives the full daemon fast-pass SWEEPS —
// ApplyTimeBasedPromotion and ApplyCrossDomainMoves — through their bearcli
// boundary, where the existing promotion_test.go / cross_domain_test.go only
// cover the pure helpers (PromoteByCalendar, RewriteCanonicalTagForTest).
//
// These are real daemon journeys: a quicknote ages past a calendar boundary
// and the daemon rewrites its canonical tag to the promotion target; the
// operator drags an atom's bullet into a different flat-list master and the
// daemon rewrites the atom's canonical tag to follow. Each test asserts the
// observable outcome — the bearcli overwrite and the rewritten canonical
// line — via a per-tag fake backend.

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/fastpass"
	"github.com/barad1tos/noxctl/bear/render"
)

// fastpassApplyBackend is a bearcli.Backend that serves per-tag `list`
// payloads (so a sweep that lists several domains gets the right atoms for
// each), `cat` master content keyed by note ID, a stub `show` hash, and
// records every `overwrite`. Reuses fakeOverwriteCall from autotag_test.go.
type fastpassApplyBackend struct {
	byTag        map[string]string // tag → `list --tag` JSON array payload
	masters      map[string]string // note ID → master content (served via `cat`)
	overwriteErr error             // when set, every `overwrite` fails (sweep error-handling path)
	overwrites   []fakeOverwriteCall
}

func (f *fastpassApplyBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("[]"), nil
	}
	switch args[0] {
	case "list":
		return []byte(f.listForTag(args)), nil
	case "cat":
		return f.catMaster(args), nil
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		f.recordOverwrite(args, stdin)
		if f.overwriteErr != nil {
			return nil, f.overwriteErr
		}
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

// listForTag returns the payload registered for the `--tag <x>` value in
// args, or an empty array when the tag (or `--tag` flag) is absent.
func (f *fastpassApplyBackend) listForTag(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tag" {
			if payload, ok := f.byTag[args[i+1]]; ok {
				return payload
			}
			break
		}
	}
	return "[]"
}

// catMaster serves the master content registered under the note ID in
// args[1] as a JSON Note, or an empty-content stub.
func (f *fastpassApplyBackend) catMaster(args []string) []byte {
	if len(args) >= 2 {
		if content, ok := f.masters[args[1]]; ok {
			row, _ := json.Marshal(map[string]any{"id": args[1], "content": content})
			return row
		}
	}
	return []byte(`{"content":""}`)
}

// recordOverwrite captures the note ID + body of an `overwrite` call.
func (f *fastpassApplyBackend) recordOverwrite(args []string, stdin string) {
	noteID := ""
	if len(args) >= 2 && !strings.HasPrefix(args[1], "--") {
		noteID = args[1]
	}
	f.overwrites = append(f.overwrites, fakeOverwriteCall{NoteID: noteID, Content: stdin})
}

// notesJSON marshals a set of bearcli `list` rows into the JSON array shape
// the production parsers expect.
func notesJSON(t *testing.T, rows ...map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(rows)
	if err != nil {
		t.Fatalf("marshal notes payload: %v", err)
	}
	return string(payload)
}

// TestApplyTimeBasedPromotion_PromotesAgedAtom_AndRewritesCanonical pins the
// time-promotion journey: an atom tagged inbox/daily created far in the past
// (predating any calendar boundary) must be rewritten to the inbox/weekly
// canonical on the sweep. User-facing bug if this regresses: aged quicknote
// atoms never roll forward and pile up in the daily bucket forever.
//
//cyrillic:permit
func TestApplyTimeBasedPromotion_PromotesAgedAtom_AndRewritesCanonical(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	daily := render.NewFlatListDomain("inbox/daily", "✱ Daily")
	weekly := render.NewFlatListDomain("inbox/weekly", "✱ Weekly")
	rules := []fastpass.PromotionRule{{From: "inbox/daily", To: "inbox/weekly", Boundary: "day"}}

	atom := notesJSON(t, map[string]any{
		"id":      "atom-aged",
		"title":   "Старий запис",
		"tags":    []string{"#inbox/daily"},
		"content": "# Старий запис\n#inbox/daily | [[✱ Daily]]\n---\nтіло\n",
		"created": "2020-01-01T09:00:00Z",
	})
	backend := &fastpassApplyBackend{byTag: map[string]string{"inbox/daily": atom, "inbox/weekly": "[]"}}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	if err := fastpass.ApplyTimeBasedPromotion(ctx, []*domain.Domain{daily, weekly}, nil, rules); err != nil {
		t.Fatalf("ApplyTimeBasedPromotion: %v", err)
	}
	if len(backend.overwrites) != 1 {
		t.Fatalf("overwrite calls = %d, want 1 (aged atom must be promoted)", len(backend.overwrites))
	}
	if !strings.Contains(backend.overwrites[0].Content, "#inbox/weekly | [[") {
		t.Errorf("promoted atom canonical not rewritten to inbox/weekly; got:\n%s", backend.overwrites[0].Content)
	}
}

// TestApplyTimeBasedPromotion_SkipsAtom_WhenCreationDateMissing pins the
// guard: a promotion-eligible atom with no creation date must be skipped, not
// promoted on a guess. User-facing bug if this regresses: a note Bear never
// stamped a creation date on gets shuffled between buckets unpredictably.
//
//cyrillic:permit
func TestApplyTimeBasedPromotion_SkipsAtom_WhenCreationDateMissing(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	daily := render.NewFlatListDomain("inbox/daily", "✱ Daily")
	weekly := render.NewFlatListDomain("inbox/weekly", "✱ Weekly")
	rules := []fastpass.PromotionRule{{From: "inbox/daily", To: "inbox/weekly", Boundary: "day"}}

	// No "created" key → zero time.Time → IsZero() guard fires.
	atom := notesJSON(t, map[string]any{
		"id":      "atom-missing-date",
		"title":   "Без дати",
		"tags":    []string{"#inbox/daily"},
		"content": "# Без дати\n#inbox/daily | [[✱ Daily]]\n---\nтіло\n",
	})
	backend := &fastpassApplyBackend{byTag: map[string]string{"inbox/daily": atom, "inbox/weekly": "[]"}}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	if err := fastpass.ApplyTimeBasedPromotion(ctx, []*domain.Domain{daily, weekly}, nil, rules); err != nil {
		t.Fatalf("ApplyTimeBasedPromotion: %v", err)
	}
	if len(backend.overwrites) != 0 {
		t.Errorf("overwrite calls = %d, want 0 (no creation date must skip, not guess)", len(backend.overwrites))
	}
}

// TestApplyCrossDomainMoves_RewritesTag_WhenAtomClaimedByOtherMaster pins the
// drag-between-masters journey: an atom tagged inbox/a whose bullet now lives
// in inbox/b's master gets its canonical tag rewritten to inbox/b. User-facing
// bug if this regresses: dragging a bullet between flat-list masters does
// nothing and the atom snaps back to its old domain next regen.
//
//cyrillic:permit
func TestApplyCrossDomainMoves_RewritesTag_WhenAtomClaimedByOtherMaster(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	a := render.NewFlatListDomain("inbox/a", "✱ Inbox A")
	b := render.NewFlatListDomain("inbox/b", "✱ Inbox B")
	const atomTitle = "Перенесена нотатка"

	listA := notesJSON(
		t,
		map[string]any{"id": "master-a", "title": "✱ Inbox A", "tags": []string{"#inbox/a"}, "content": ""},
		map[string]any{
			"id": "atom-1", "title": atomTitle, "tags": []string{"#inbox/a"},
			"content": "# " + atomTitle + "\n#inbox/a | [[✱ Inbox A]]\n---\nтіло\n",
		},
	)
	listB := notesJSON(
		t,
		map[string]any{"id": "master-b", "title": "✱ Inbox B", "tags": []string{"#inbox/b"}, "content": ""},
	)
	backend := &fastpassApplyBackend{
		byTag: map[string]string{"inbox/a": listA, "inbox/b": listB},
		masters: map[string]string{
			"master-a": "# ✱ Inbox A\n",                          // claims nothing
			"master-b": "# ✱ Inbox B\n- [[" + atomTitle + "]]\n", // claims the atom by title
		},
	}
	ctx := domain.ContextWithBackend(context.Background(), backend)
	pins, err := domain.LoadPinRegistry(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatalf("LoadPinRegistry: %v", err)
	}

	if err = fastpass.ApplyCrossDomainMoves(ctx, []*domain.Domain{a, b}, pins); err != nil {
		t.Fatalf("ApplyCrossDomainMoves: %v", err)
	}
	if len(backend.overwrites) != 1 {
		t.Fatalf("overwrite calls = %d, want 1 (claimed atom must move to B)", len(backend.overwrites))
	}
	if backend.overwrites[0].NoteID != "atom-1" {
		t.Errorf("overwrote wrong note: %q, want atom-1", backend.overwrites[0].NoteID)
	}
	if !strings.Contains(backend.overwrites[0].Content, "#inbox/b | [[") {
		t.Errorf("atom canonical not rewritten to target inbox/b; got:\n%s", backend.overwrites[0].Content)
	}
}

// TestApplyTimeBasedPromotion_ContinuesSweep_WhenOverwriteFails pins the
// log-and-continue contract: when bearcli rejects one atom's overwrite, the
// promotion sweep must keep going and still return nil (one bad atom does not
// stall the whole pass). With two aged atoms and a failing overwrite, both are
// attempted. User-facing bug if this regresses: a single rejected write aborts
// the sweep and the remaining aged quicknotes never roll forward.
//
//cyrillic:permit
func TestApplyTimeBasedPromotion_ContinuesSweep_WhenOverwriteFails(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	daily := render.NewFlatListDomain("inbox/daily", "✱ Daily")
	weekly := render.NewFlatListDomain("inbox/weekly", "✱ Weekly")
	rules := []fastpass.PromotionRule{{From: "inbox/daily", To: "inbox/weekly", Boundary: "day"}}

	atoms := notesJSON(
		t,
		map[string]any{
			"id": "aged-1", "title": "Перший", "tags": []string{"#inbox/daily"},
			"content": "# Перший\n#inbox/daily | [[✱ Daily]]\n---\nx\n", "created": "2020-01-01T09:00:00Z",
		},
		map[string]any{
			"id": "aged-2", "title": "Другий", "tags": []string{"#inbox/daily"},
			"content": "# Другий\n#inbox/daily | [[✱ Daily]]\n---\ny\n", "created": "2020-01-02T09:00:00Z",
		},
	)
	backend := &fastpassApplyBackend{
		byTag:        map[string]string{"inbox/daily": atoms, "inbox/weekly": "[]"},
		overwriteErr: errors.New("bearcli overwrite: simulated rejection"),
	}
	ctx := domain.ContextWithBackend(context.Background(), backend)

	if err := fastpass.ApplyTimeBasedPromotion(ctx, []*domain.Domain{daily, weekly}, nil, rules); err != nil {
		t.Fatalf("ApplyTimeBasedPromotion must log-and-continue, not return; got %v", err)
	}
	if len(backend.overwrites) != 2 {
		t.Errorf("sweep stalled: attempted %d overwrites, want 2 (both aged atoms tried despite failures)",
			len(backend.overwrites))
	}
}

// TestApplyCrossDomainMoves_ReturnsError_WhenOverwriteFails pins the
// cross-move error-propagation contract: unlike promotion's log-and-continue,
// a failed tag rewrite during a cross-domain move surfaces as a returned error
// so the orchestrator logs it and falls back to per-domain regen. User-facing
// bug if this regresses: a failed cross-move is swallowed and the atom is left
// half-moved with no signal.
//
//cyrillic:permit
func TestApplyCrossDomainMoves_ReturnsError_WhenOverwriteFails(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	a := render.NewFlatListDomain("inbox/a", "✱ Inbox A")
	b := render.NewFlatListDomain("inbox/b", "✱ Inbox B")
	const atomTitle = "Перенесена нотатка"

	listA := notesJSON(
		t,
		map[string]any{"id": "master-a", "title": "✱ Inbox A", "tags": []string{"#inbox/a"}, "content": ""},
		map[string]any{
			"id": "atom-1", "title": atomTitle, "tags": []string{"#inbox/a"},
			"content": "# " + atomTitle + "\n#inbox/a | [[✱ Inbox A]]\n---\nтіло\n",
		},
	)
	listB := notesJSON(
		t,
		map[string]any{"id": "master-b", "title": "✱ Inbox B", "tags": []string{"#inbox/b"}, "content": ""},
	)
	backend := &fastpassApplyBackend{
		byTag: map[string]string{"inbox/a": listA, "inbox/b": listB},
		masters: map[string]string{
			"master-a": "# ✱ Inbox A\n",
			"master-b": "# ✱ Inbox B\n- [[" + atomTitle + "]]\n",
		},
		overwriteErr: errors.New("bearcli overwrite: simulated rejection"),
	}
	ctx := domain.ContextWithBackend(context.Background(), backend)
	pins, err := domain.LoadPinRegistry(filepath.Join(t.TempDir(), "pins.json"))
	if err != nil {
		t.Fatalf("LoadPinRegistry: %v", err)
	}

	if err = fastpass.ApplyCrossDomainMoves(ctx, []*domain.Domain{a, b}, pins); err == nil {
		t.Fatalf("ApplyCrossDomainMoves returned nil despite a failed rewrite; want a propagated error")
	}
}
