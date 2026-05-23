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
//  5. MembershipGuardSkipsForeignDomain — a foreign-family atom MUST
//     NOT surface in any work-domain group. The fake mirrors production
//     listNotes' `--tag work` filter so the guard is observed at the
//     same surface bearcli enforces. Complements the existing
//     processAtomic-side guard exercised by atomic_tag_guard_test.go.
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
// hand-rolled *Domain literal) is load-bearing: the factory populates
// Domain.Buckets, so any future regression that drops the wiring trips
// this test before the algorithm ever runs.
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

	mu             sync.Mutex
	calls          []fakeWorkCall
	writes         map[string]string // noteID → most recent overwrite body
	overwriteCalls int               // total `overwrite` invocations across all notes
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

// OverwriteCount returns the total number of `overwrite` calls captured —
// every invocation increments the counter, even when the same note is
// rewritten with an identical body. Idempotency assertions compare this
// counter before/after a second RunRegen; a literal zero delta means the
// production code emitted no new writes.
func (f *fakeWorkBackend) OverwriteCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.overwriteCalls
}

// filterListByTag honors the `--tag <family>` argument the production
// listNotes call always supplies. Notes are kept when their tags array
// carries `#<family>` or any `#<family>/<sub>`; everything else is
// dropped — same shape as the real bearcli list output for an isolated
// family query. Returns the raw payload unchanged when no --tag arg is
// present (defensive: should not happen in production paths).
func (f *fakeWorkBackend) filterListByTag(args []string) []byte {
	tag := ""
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tag" {
			tag = args[i+1]
			break
		}
	}
	if tag == "" {
		return f.listPayload
	}
	var raw []map[string]any
	if err := json.Unmarshal(f.listPayload, &raw); err != nil {
		return f.listPayload
	}
	want := "#" + tag
	wantSubPrefix := want + "/"
	kept := make([]map[string]any, 0, len(raw))
	for _, n := range raw {
		tagsAny, _ := n["tags"].([]any)
		if hasFamilyTag(tagsAny, want, wantSubPrefix) {
			kept = append(kept, n)
		}
	}
	out, err := json.Marshal(kept)
	if err != nil {
		return f.listPayload
	}
	return out
}

// hasFamilyTag returns true when any entry in `tags` equals `family` or
// starts with `familySubPrefix`. Tolerant of mixed-type tag arrays the
// JSON decoder produces ([]any with string elements).
func hasFamilyTag(tags []any, family, familySubPrefix string) bool {
	for _, t := range tags {
		s, ok := t.(string)
		if !ok {
			continue
		}
		if s == family || strings.HasPrefix(s, familySubPrefix) {
			return true
		}
	}
	return false
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
		// Mirror production: `bearcli list --tag <family>` filters
		// by membership in <family>. The unfiltered payload would
		// leak foreign-domain atoms into the snapshot pipeline,
		// hiding the membership-guard contract.
		return f.filterListByTag(args), nil
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
		// `overwrite <noteID> --base <hash>` with body on stdin. Each call
		// bumps overwriteCalls regardless of whether the body changes —
		// idempotency assertions need the literal call count, not a
		// deduped per-note view.
		if len(args) >= 2 {
			f.mu.Lock()
			f.writes[args[1]] = stdin
			f.overwriteCalls++
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
// parser recognizes for the pre-drag fixture: H1 / canonical tag-line
// keyed to the UnknownBucket ("інше") / --- / body. Every integration
// case in this file stages the atom in the "інше" state and exercises
// some drag/master/tag interaction from there, so a parameterized
// bucket would invite scope creep without a real caller.
//
//cyrillic:permit
func canonicalAtomBody() string {
	return "# Нова нотатка\n#work/інше | [[✱ Робота]]\n---\nbody\n"
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
			"content": canonicalAtomBody(),
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
		"content": canonicalAtomBody(),
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

// TestTagOverrideIntegration_MasterWinsOverTag pins the merge-priority
// contract: when the master table claims an atom for bucket M and the
// Bear tag-array independently claims bucket T, the snapshot routes
// the atom into M. The deliberate cut/paste gesture in the master
// outranks the single sidebar drag. Without this lock, a swapped merge
// order or an inverted mergeOverrideLayer call would silently demote
// the master override and the curator's table edit would be discarded.
//
//cyrillic:permit
func TestTagOverrideIntegration_MasterWinsOverTag(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		// Atom carries canonical bucket "інше" plus a #work/tasks drag
		// (tag override candidate → tasks). Master lists the atom under
		// "## development" (master override candidate → development).
		// Priority master > tag must land the atom in development.
		masterContent := "# ✱ Робота\n#work\n---\n## development (1)\n- [[Нова нотатка]]\n"
		fake.StageList(t, []map[string]any{
			{
				"id":      atomNoteID,
				"title":   "Нова нотатка",
				"tags":    []string{"#work", "#work/tasks"},
				"content": canonicalAtomBody(),
			},
			{
				"id":      masterNoteID,
				"title":   "✱ Робота",
				"tags":    []string{"#work"},
				"content": masterContent,
			},
		})
		fake.StageNote(t, masterNoteID, map[string]any{
			"id":      masterNoteID,
			"title":   "✱ Робота",
			"content": masterContent,
			"tags":    []string{"#work"},
		})
		fake.StageNote(t, atomNoteID, map[string]any{
			"id":      atomNoteID,
			"title":   "Нова нотатка",
			"content": canonicalAtomBody(),
			"tags":    []string{"#work", "#work/tasks"},
		})
		ctx := domain.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		inputs, err := domain.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}
		if got := noteIDs(inputs.Groups["development"]); !slices.Equal(got, []string{atomNoteID}) {
			t.Errorf("Groups[\"development\"] = %v, want [%s] (master must outrank tag)",
				got, atomNoteID)
		}
		if got := noteIDs(inputs.Groups["tasks"]); slices.Contains(got, atomNoteID) {
			t.Errorf("Groups[\"tasks\"] contains %s — tag override leaked past master priority",
				atomNoteID)
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
// RunRegen a second time. The overwrite-call counter MUST stay flat —
// the ≤3-pass idempotency contract. Counting calls (not map size or last
// body) catches the failure mode where production re-overwrites the same
// note with the same body: the writes map looks identical but the daemon
// is doing redundant work that the contract forbids.
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

	overwritesBefore := fake.OverwriteCount()
	d.RunRegen(ctx)
	overwritesAfter := fake.OverwriteCount()
	if overwritesAfter != overwritesBefore {
		t.Errorf("second RunRegen issued %d new overwrite call(s) — idempotency broken (before=%d, after=%d)",
			overwritesAfter-overwritesBefore, overwritesBefore, overwritesAfter)
	}
}

// TestTagOverrideIntegration_MembershipGuardSkipsForeignDomain locks
// the production guard chain that keeps foreign-family atoms out of a
// work-domain snapshot:
//
//  1. listNotes filters by `--tag work` (the fake mirrors this via
//     filterListByTag) so foreign atoms without a matching family tag
//     never enter the pipeline at all.
//  2. computeTagOverrides additionally requires `#work` in note.Tags
//     before any sub-tag is honored, so an atom that slipped past the
//     listNotes filter via a stray sub-tag could still not be promoted
//     by the tag-override layer.
//
// This case stages a foreign atom whose tags exclude every work-family
// tag and asserts it is absent from every bucket of inputs.Groups. The
// matching atom for the work family must still surface normally so the
// negative result is not a false positive from a broken fixture.
//
//cyrillic:permit
func TestTagOverrideIntegration_MembershipGuardSkipsForeignDomain(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		fake.StageList(t, []map[string]any{
			{
				"id":      atomNoteID,
				"title":   "Нова нотатка",
				"tags":    []string{"#work", "#work/tasks"},
				"content": canonicalAtomBody(),
			},
			{
				// Pure foreign atom — only #library/poetry, no #work
				// family tag. listNotes' `--tag work` filter rejects
				// the note before SnapshotDomainRenderInputs ever sees
				// it, exactly as production does.
				"id":      foreignNoteID,
				"title":   "Шевченко",
				"tags":    []string{"#library/poetry"},
				"content": "# Шевченко\n#library/poetry | [[Шевченко]]\n---\n\nverse\n",
			},
		})
		ctx := domain.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		inputs, err := domain.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}

		// Legitimate atom must surface in the tasks bucket.
		if got := noteIDs(inputs.Groups["tasks"]); !slices.Equal(got, []string{atomNoteID}) {
			t.Errorf("Groups[\"tasks\"] = %v, want [%s] (work-domain drag should re-bucket)",
				got, atomNoteID)
		}
		// Foreign atom must be absent from EVERY bucket; both the
		// listNotes filter and the tag-override membership guard fail
		// the foreign-family note out before bucketing.
		for bucket, atoms := range inputs.Groups {
			if slices.Contains(noteIDs(atoms), foreignNoteID) {
				t.Errorf("foreign atom %s leaked into Groups[%q] = %v — membership guard failed",
					foreignNoteID, bucket, noteIDs(atoms))
			}
		}
	})
}
