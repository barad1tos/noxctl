// Package engine_test — integration locks for the tag-override layer.
//
// computeTagOverrides participates as the third override source in the
// snapshot + regen pipelines (priority: master > hub > tag). This file
// pins the end-to-end behavior:
//
//  1. DragToTag_ReBuckets (snapshot path) — a note with canonical bucket
//     A and a drag-added whitelisted sub-tag B (B != A, B in Buckets)
//     surfaces in Groups[B] after one SnapshotDomainRenderInputs call.
//  2. DragToTag_Idempotent (snapshot path) — two consecutive snapshots
//     produce deep-equal Groups maps. The ≤3-pass idempotency contract
//     demands no flap, no growing override map.
//  3. FullRunRegen_RewritesCanonical (full regen path) — RunRegen on
//     the same scenario re-stamps the atomic canonical to the new
//     bucket, strips the stale family sub-tag from the body, and updates
//     the master to show the atom under the new H2 section. Second run
//     is a strict no-op (idempotent).
//  4. MasterWinsOverTag (priority lock) — an atom whose master row claims
//     bucket M and whose Bear tags claim bucket T routes to M; the master
//     override pre-empts the tag override on collision.
//  5. MembershipGuardSkipsForeignDomain — a note that carries a stray
//     `#work/tasks` tag without the parent `#work` tag MUST NOT surface
//     in any work-domain group. Locks computeTagOverrides' membership
//     guard alongside the existing processAtomic guard exercised by
//     atomic_tag_guard_test.go.
//
// Test seam: a purpose-built fakeWorkBackend captures overwrites by
// noteID and serves list/cat payloads. Kept local to this file so the
// existing fakeAutoTagBackend in autotag_poll_test.go stays unmodified
// (it is shared across the bootstrap + autotag integration tests).
//
//cyrillic:permit
package engine_test

import (
	"context"
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// workDomainBuckets mirrors the priority bucket order Roman's `#work`
// catalog entry exposes (examples/personal.toml). The whitelist MUST
// include "tasks" (the drag target across the re-bucket cases) so the
// override fires; "інше" is the UnknownBucket and intentionally NOT in
// the whitelist — gatherWhitelistedSubTags accepts the unknown-bucket
// value via its `sub != d.UnknownBucket` branch.
var workDomainBuckets = []string{
	"tasks", "development", "english", "health",
	"humor", "leisure", "instagram", "travel",
}

// buildWorkDomainForIntegration constructs the grouped-vertical work
// domain via the production factory. Using the factory (rather than a
// hand-rolled *Domain literal) is load-bearing: Task 1 wired Buckets
// inside the factory body, so any future regression that drops the
// wiring trips this test before the algorithm ever runs.
//
//cyrillic:permit
func buildWorkDomainForIntegration() *domain.Domain {
	return render.NewGroupedVerticalDomain("work", "✱ Робота", "інше", workDomainBuckets)
}

// fakeWorkCall records one bearcli invocation routed through the
// BearcliBackend seam. Body carries the stdin payload (the rewritten
// note body on `overwrite`, empty on `list`/`show`/`cat`).
type fakeWorkCall struct {
	Kind string
	Args []string
	Body string
}

// fakeWorkBackend serves a curated note corpus and captures every
// `overwrite` call by noteID into Writes. Concurrency-safe so the
// production code's pool semaphores stay correct under test.
type fakeWorkBackend struct {
	listPayload []byte            // canned bearcli list response
	notesByID   map[string]string // noteID → JSON for `cat <id>`
	hashByID    map[string]string // noteID → optimistic-concurrency hash (default "deadbeef")

	mu     sync.Mutex
	calls  []fakeWorkCall
	writes map[string]string // noteID → most recent overwrite body
}

// newFakeWorkBackend constructs an empty fake. Callers populate
// listPayload + notesByID via Stage* helpers before driving production.
func newFakeWorkBackend() *fakeWorkBackend {
	return &fakeWorkBackend{
		notesByID: make(map[string]string),
		hashByID:  make(map[string]string),
		writes:    make(map[string]string),
	}
}

// StageList sets the canned response for `bearcli list --tag work ...`.
// Accepts the same map shape as listPayload() in bootstrap_pass_test.go.
func (f *fakeWorkBackend) StageList(t *testing.T, notes []map[string]any) {
	t.Helper()
	raw, err := json.Marshal(notes)
	if err != nil {
		t.Fatalf("fakeWorkBackend.StageList: marshal: %v", err)
	}
	f.listPayload = raw
}

// StageNote registers a single note's `cat <id>` payload. Caller must
// supply the full JSON shape including id/title/content/tags/hash.
func (f *fakeWorkBackend) StageNote(t *testing.T, id string, payload map[string]any) {
	t.Helper()
	if _, hasHash := payload["hash"]; !hasHash {
		payload["hash"] = "deadbeef"
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("fakeWorkBackend.StageNote(%s): marshal: %v", id, err)
	}
	f.notesByID[id] = string(raw)
}

// Write returns the most recent `overwrite` body for the given noteID,
// or "" if no overwrite was captured.
func (f *fakeWorkBackend) Write(noteID string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.writes[noteID]
}

// WriteCount returns the total number of `overwrite` calls captured
// (across all noteIDs). Useful for idempotency assertions.
func (f *fakeWorkBackend) WriteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.writes)
}

// SnapshotWrites returns a copy of the noteID→body map so callers can
// deep-equal two snapshots taken across regen runs without racing the
// next overwrite.
func (f *fakeWorkBackend) SnapshotWrites() map[string]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return maps.Clone(f.writes)
}

// Run satisfies domain.BearcliBackend. Dispatches by args[0] (the
// bearcli subcommand). Unknown subcommands return "{}" so production
// code that defaults to empty JSON parses cleanly.
func (f *fakeWorkBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	kind := "other"
	if len(args) > 0 {
		kind = args[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeWorkCall{Kind: kind, Args: append([]string(nil), args...), Body: stdin})
	f.mu.Unlock()
	switch kind {
	case "list":
		return f.listPayload, nil
	case "cat":
		// `cat <noteID> --format json --fields ...`
		if len(args) >= 2 {
			if payload, ok := f.notesByID[args[1]]; ok {
				return []byte(payload), nil
			}
		}
		// Default: empty note (FetchMasterContent treats this as "no
		// master yet" via empty content).
		return []byte(`{"id":"","title":"","content":"","hash":"","tags":[],"created":"0001-01-01T00:00:00Z"}`), nil
	case "show":
		// ShowHash requires a non-empty hash; treat each note as having
		// a stable hash so overwrite-with-retry proceeds first attempt.
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		// `overwrite <noteID> --base <hash>` with body on stdin.
		if len(args) >= 2 {
			f.mu.Lock()
			f.writes[args[1]] = stdin
			f.mu.Unlock()
		}
		return []byte(`{"ok":true}`), nil
	case "create":
		return []byte(`{"id":"new-id","title":"new"}`), nil
	}
	return []byte("{}"), nil
}

// atomNoteID, masterNoteID, foreignNoteID are stable IDs reused across
// every case so failure messages name the same atom across the file.
const (
	atomNoteID    = "atom-work-tasks-001"
	masterNoteID  = "master-robota-001"
	foreignNoteID = "atom-foreign-poetry-001"
)

// canonicalAtomBody renders the minimal atomic body the production
// parser recognizes: H1 / canonical tag-line / --- / body.
//
//cyrillic:permit
func canonicalAtomBody(bucket string) string {
	return "# Нова нотатка\n#work/" + bucket + " | [[✱ Робота]]\n---\nbody\n"
}

// masterContentInitial seeds the master with one atom listed under the
// "інше" section — the pre-drag state.
//
//cyrillic:permit
func masterContentInitial() string {
	return "# ✱ Робота\n#work\n---\n## інше (1)\n- [[Нова нотатка]]\n"
}

// stageDragScenario primes the fake with one atom currently canonical
// under bucket "інше" but carrying an extra `#work/tasks` sub-tag (the
// drag-add gesture). Plus the master note in its pre-drag state.
//
//cyrillic:permit
func stageDragScenario(t *testing.T, fake *fakeWorkBackend) {
	t.Helper()
	fake.StageList(t, []map[string]any{
		{
			"id":      atomNoteID,
			"title":   "Нова нотатка",
			"tags":    []string{"#work", "#work/tasks"},
			"content": canonicalAtomBody("інше"),
		},
		{
			"id":      masterNoteID,
			"title":   "✱ Робота",
			"tags":    []string{"#work"},
			"content": masterContentInitial(),
		},
	})
	fake.StageNote(t, masterNoteID, map[string]any{
		"id":      masterNoteID,
		"title":   "✱ Робота",
		"content": masterContentInitial(),
		"tags":    []string{"#work"},
	})
	fake.StageNote(t, atomNoteID, map[string]any{
		"id":      atomNoteID,
		"title":   "Нова нотатка",
		"content": canonicalAtomBody("інше"),
		"tags":    []string{"#work", "#work/tasks"},
	})
}

// noteIDs returns the sorted slice of IDs in `notes`. Used to compare
// per-bucket membership without depending on map iteration order.
func noteIDs(notes []domain.Note) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.ID
	}
	slices.Sort(out)
	return out
}

// TestTagOverrideIntegration_DragToTag_ReBuckets — Case 1 (snapshot path).
// Confirms a drag-added whitelisted sub-tag re-buckets the atom on one
// SnapshotDomainRenderInputs pass. Pins the snapshot-side end-to-end
// wiring of computeTagOverrides as the third override layer.
//
//cyrillic:permit
func TestTagOverrideIntegration_DragToTag_ReBuckets(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		stageDragScenario(t, fake)
		ctx := domain.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		inputs, err := domain.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}
		if got := noteIDs(inputs.Groups["tasks"]); !slices.Equal(got, []string{atomNoteID}) {
			t.Errorf("Groups[\"tasks\"] = %v, want [%s] (tag override should re-bucket the atom)",
				got, atomNoteID)
		}
		if got := noteIDs(inputs.Groups["інше"]); slices.Contains(got, atomNoteID) {
			t.Errorf("Groups[\"інше\"] still contains %s; override failed to move it out", atomNoteID)
		}
	})
}

// TestTagOverrideIntegration_DragToTag_Idempotent — Case 2 (snapshot path).
// Locks the ≤3-pass idempotency contract: a second snapshot with no
// Bear-side state change MUST produce a deep-equal Groups map. Without
// this, a stale override layer could flap the bucket back-and-forth
// across regen ticks.
//
//cyrillic:permit
func TestTagOverrideIntegration_DragToTag_Idempotent(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		stageDragScenario(t, fake)
		ctx := domain.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		first, err := domain.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("first snapshot: %v", err)
		}
		second, err := domain.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("second snapshot: %v", err)
		}
		if !reflect.DeepEqual(first.Groups, second.Groups) {
			t.Errorf("snapshot Groups flapped between pass 1 and pass 2\n  pass1=%v\n  pass2=%v",
				first.Groups, second.Groups)
		}
	})
}

// TestTagOverrideIntegration_FullRunRegen_RewritesCanonical — Case 3
// (full regen path, captured writes). Drives the full RunRegen and
// asserts on the rewritten canonical body + master content. Second run
// must produce no NEW writes (idempotent re-run).
//
//cyrillic:permit
func TestTagOverrideIntegration_FullRunRegen_RewritesCanonical(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		stageDragScenario(t, fake)
		ctx := domain.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		d.RunRegen(ctx)

		atomBody := fake.Write(atomNoteID)
		masterBody := fake.Write(masterNoteID)
		assertAtomReBucketed(t, atomBody)
		assertMasterReBucketed(t, masterBody)
		assertSecondRunIsNoop(t, fake, ctx, d, atomBody, masterBody)
	})
}

// assertAtomReBucketed checks the atom body was rewritten with the new
// canonical sub-tag, the stale sub-tag stripped, and user body preserved.
//
//cyrillic:permit
func assertAtomReBucketed(t *testing.T, atomBody string) {
	t.Helper()
	if atomBody == "" {
		t.Fatalf("no overwrite captured for atom %s", atomNoteID)
	}
	if got := strings.Count(atomBody, "#work/tasks | [[✱ Робота]]"); got != 1 {
		t.Errorf("canonical re-stamp count = %d, want 1 (atom body should carry exactly one #work/tasks canonical line)\nbody:\n%s",
			got, atomBody)
	}
	if strings.Contains(atomBody, "#work/інше") {
		t.Errorf("stale #work/інше still present in atom body — family-filter regression\nbody:\n%s", atomBody)
	}
	if !strings.Contains(atomBody, "\nbody\n") {
		t.Errorf("user body content missing below ---; got:\n%s", atomBody)
	}
}

// assertMasterReBucketed checks the master shows the atom under the new
// ## tasks section, removed from ## інше, and the bullet appears exactly
// once.
//
//cyrillic:permit
func assertMasterReBucketed(t *testing.T, masterBody string) {
	t.Helper()
	if masterBody == "" {
		t.Fatalf("no overwrite captured for master %s", masterNoteID)
	}
	if !strings.Contains(masterBody, "## tasks (1)") {
		t.Errorf("master missing `## tasks (1)` section — atom did not move\nbody:\n%s", masterBody)
	}
	if strings.Contains(masterBody, "## інше (1)") {
		t.Errorf("master still shows `## інше (1)` — atom not removed from old bucket\nbody:\n%s", masterBody)
	}
	if got := strings.Count(masterBody, "- [[Нова нотатка]]"); got != 1 {
		t.Errorf("master bullet count for atom = %d, want 1 (atom should appear exactly once)\nbody:\n%s",
			got, masterBody)
	}
}

// assertSecondRunIsNoop stages the rewritten bodies as the new ground
// truth (what bearcli would return on the next list/cat) and drives
// RunRegen a second time. The overwrite counter MUST stay flat — the
// ≤3-pass idempotency contract.
//
//cyrillic:permit
func assertSecondRunIsNoop(t *testing.T, fake *fakeWorkBackend, ctx context.Context, d *domain.Domain, atomBody, masterBody string) {
	t.Helper()
	fake.StageList(t, []map[string]any{
		{"id": atomNoteID, "title": "Нова нотатка", "tags": []string{"#work", "#work/tasks"}, "content": atomBody},
		{"id": masterNoteID, "title": "✱ Робота", "tags": []string{"#work"}, "content": masterBody},
	})
	fake.StageNote(t, atomNoteID, map[string]any{"id": atomNoteID, "title": "Нова нотатка", "content": atomBody, "tags": []string{"#work", "#work/tasks"}})
	fake.StageNote(t, masterNoteID, map[string]any{"id": masterNoteID, "title": "✱ Робота", "content": masterBody, "tags": []string{"#work"}})

	writesBefore := fake.SnapshotWrites()
	d.RunRegen(ctx)
	writesAfter := fake.SnapshotWrites()
	if !reflect.DeepEqual(writesBefore, writesAfter) {
		t.Errorf("second RunRegen produced new/changed writes — idempotency broken\n  before=%v\n  after=%v",
			writesBefore, writesAfter)
	}
}

// TestTagOverrideIntegration_MembershipGuardSkipsForeignDomain — a
// foreign atom carrying a stray `#work/tasks` tag without the parent
// `#work` tag MUST NOT surface in any work-domain group. Locks the
// membership guard inside computeTagOverrides — independent of the
// existing processAtomic guard exercised by atomic_tag_guard_test.go.
//
// The assertion routes through SnapshotDomainRenderInputs so the guard
// is observed at its production surface: foreign atoms must be absent
// from every bucket in inputs.Groups. In production listNotes filters
// by `--tag work` and the foreign atom never enters the pipeline; this
// test deliberately stages both atoms to prove that even if a foreign
// atom DOES enter (e.g. via a bearcli tag-index residue race that
// surfaced a stray foreign atom in the work tag-tree), the override
// pipeline respects the guard.
//
//cyrillic:permit
func TestTagOverrideIntegration_MembershipGuardSkipsForeignDomain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		d := buildWorkDomainForIntegration()

		// Note A: legitimate work atom with drag-added sub-tag.
		// Note B: foreign atom with ONLY #library/poetry + a stray #work/tasks.
		notes := []domain.Note{
			{
				ID:      atomNoteID,
				Title:   "Нова нотатка",
				Tags:    []string{"#work", "#work/tasks"},
				Content: canonicalAtomBody("інше"),
			},
			{
				ID:    foreignNoteID,
				Title: "Шевченко",
				Tags:  []string{"#library/poetry", "#work/tasks"},
				// Foreign canonical — `#work` family domain has no claim
				// on this note regardless of the stray sub-tag.
				Content: "# Шевченко\n#library/poetry | [[Шевченко]]\n---\n\nverse\n",
			},
		}
		overrides, _ := d.ComputeTagOverridesForTest(notes)

		// Note A SHOULD receive an override into tasks.
		if got := overrides[atomNoteID]; got != "tasks" {
			t.Errorf("legitimate atom %s override = %q, want %q (work-domain override should fire)",
				atomNoteID, got, "tasks")
		}
		// Note B MUST NOT receive any override from the work domain —
		// step-2 membership guard rejects atoms lacking #work in Tags.
		if got, present := overrides[foreignNoteID]; present {
			t.Errorf("foreign atom %s received work-domain override %q — membership guard failed",
				foreignNoteID, got)
		}
	})
}
