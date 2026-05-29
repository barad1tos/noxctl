package testutil

// RecordingBackend is the shared bearcli.Backend fake every I/O-amplification
// test drives. It records (Kind, Tag) for each Run call and serves a fixed
// note corpus from memory so regen.Run / engine.Apply complete without real
// subprocesses. Co-located with the domain fixtures (domains.go) so the regen
// and engine external test packages reach one backend, not per-file copies.

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"

	"github.com/barad1tos/noxctl/bear/domain"
)

// errReadBackFailed is the transient post-write `cat` failure the
// FailReadBackAfterWrite mode injects. Exported behavior is observed via the
// upsert outcome, not the error value, so a sentinel suffices here.
var errReadBackFailed = errors.New("recording backend: read-back cat failed (transient)")

// Record is one observed bearcli invocation, classified by sub-command
// (Kind) and the --tag value (Tag, empty for tag-less id-addressed calls).
type Record struct {
	Kind string
	Tag  string
}

// RecordingBackend implements bearcli.Backend, recording every Run call
// and answering reads from an in-memory corpus.
//
// Read semantics:
//   - list (any --fields shape): returns the full corpus for the requested
//     --tag. bearcli would project columns per --fields, but every consumer
//     tolerates the extra fields, so we marshal the whole Note.
//   - cat <id>: returns the corpus note whose ID matches, or an empty note.
//   - show <id>: returns a stub hash so OverwriteWithRetry's optimistic-
//     concurrency probe succeeds.
//   - create / overwrite: minimal valid JSON; create echoes id+title derived
//     from the requested title so patch-on-create paths get a usable ID.
//
// The zero value is not usable — construct via NewRecordingBackend.
type RecordingBackend struct {
	notesByTag map[string][]domain.Note
	noteByID   map[string]domain.Note

	// NormalizeReadBack, when set, normalizes stored markdown on every `cat`
	// read-back: it collapses per-line trailing whitespace AND strips the
	// end-of-file trailing newline(s). It models a backend (like Bear) that
	// normalizes stored markdown so the read-back bytes differ from what was
	// written — the D-02 hash-stability landmine. The trailing-newline strip is
	// the realistic divergence: the diff-check (TrimRight " \n") still reports
	// `unchanged`, but the unstripped hash input (StripNewNoteURLsFromBody
	// alone) diverges, so a branch that hashes the rendered bytes flips the
	// domain to "changed" forever. Combined with write-through (overwrite AND
	// create mutate the corpus), it proves both the changed-branch and the
	// create-branch read-back capture the STORED form, keeping the next cycle's
	// hash stable.
	NormalizeReadBack bool

	// FailReadBackAfterWrite, when set, makes the FIRST `cat` of an id that was
	// just written (create or overwrite) in this backend's lifetime return an
	// error. It models a transient read-back failure AFTER a successful vault
	// write — the robustness landmine: the write is durable, but the post-write
	// hash read fails. A correct upsert reports created/updated (not failed) and
	// signals the snapshot incomplete so the prior ContentHash is preserved.
	FailReadBackAfterWrite bool

	mu          sync.Mutex
	records     []Record
	writtenIDs  map[string]bool // ids written this lifetime (create/overwrite)
	failedReads map[string]bool // ids whose post-write read-back already failed once
}

// NewRecordingBackend builds a backend whose list calls return corpus
// filtered by --tag. Notes are also indexed by ID for cat. Pass the full
// per-domain corpus (atomics + hubs + master) keyed by the tag each domain
// lists under (d.Tag).
func NewRecordingBackend(notesByTag map[string][]domain.Note) *RecordingBackend {
	byID := make(map[string]domain.Note)
	for _, notes := range notesByTag {
		for _, n := range notes {
			byID[n.ID] = n
		}
	}
	return &RecordingBackend{
		notesByTag:  notesByTag,
		noteByID:    byID,
		writtenIDs:  make(map[string]bool),
		failedReads: make(map[string]bool),
	}
}

// Run satisfies bearcli.Backend: classify, record, then answer from corpus.
func (b *RecordingBackend) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	kind := recordingKindFromArgs(args)
	tag := recordingTagFromArgs(args)
	b.mu.Lock()
	b.records = append(b.records, Record{Kind: kind, Tag: tag})
	b.mu.Unlock()
	return b.payload(kind, tag, args, stdin)
}

// payload answers a classified call from the in-memory corpus.
func (b *RecordingBackend) payload(kind, tag string, args []string, stdin string) ([]byte, error) {
	switch kind {
	case "list":
		return json.Marshal(b.notesByTag[tag])
	case "cat":
		if len(args) >= 2 && b.shouldFailReadBack(args[1]) {
			return nil, errReadBackFailed
		}
		return json.Marshal(b.catNote(args))
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "create":
		return b.applyCreate(args, stdin), nil
	case "overwrite":
		b.applyOverwrite(args, stdin)
		return []byte(`{"ok":true}`), nil
	default:
		return []byte(`{}`), nil
	}
}

// applyOverwrite write-through: persist the new body into the corpus so a
// later `cat` of the same ID reads back the stored form. When
// NormalizeReadBack is set, the stored body is normalized — modeling Bear's
// markdown normalization on write so the read-back differs from the rendered
// input (the D-02 landmine). No-op when the ID is unknown.
func (b *RecordingBackend) applyOverwrite(args []string, body string) {
	if len(args) < 2 {
		return
	}
	id := args[1]
	stored := body
	if b.NormalizeReadBack {
		stored = normalizeBody(body)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.storeContent(id, stored)
	b.writtenIDs[id] = true
}

// applyCreate write-through: persist the freshly-created note's body into the
// corpus under the title-derived ID and return the id+title payload the
// patch-on-create path parses. Mirrors applyOverwrite's normalization so a
// create-branch read-back of the new note captures Bear's stored-normalized
// form (the FIX-1 landmine: hashing rendered create bytes flips next cycle).
func (b *RecordingBackend) applyCreate(args []string, body string) []byte {
	payload := recordingCreatePayload(args)
	if len(args) < 2 {
		return payload
	}
	var created domain.Note
	if err := json.Unmarshal(payload, &created); err != nil || created.ID == "" {
		return payload
	}
	stored := body
	if b.NormalizeReadBack {
		stored = normalizeBody(body)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	created.Content = stored
	b.noteByID[created.ID] = created
	b.writtenIDs[created.ID] = true
	// Surface the freshly-created note in subsequent `list` responses so a
	// later-cycle title lookup resolves it (the index is rebuilt per cycle from
	// listNotes). Append to every tag bucket — single-domain corpora have one,
	// and the title is unique so an over-return is harmless. Skip if already
	// present (idempotent across repeat creates of the same id).
	for tag, notes := range b.notesByTag {
		if recordingNotesContain(notes, created.ID) {
			continue
		}
		b.notesByTag[tag] = append(notes, created)
	}
	return payload
}

// recordingNotesContain reports whether any note in the slice has the given ID.
func recordingNotesContain(notes []domain.Note, id string) bool {
	for _, n := range notes {
		if n.ID == id {
			return true
		}
	}
	return false
}

// storeContent persists stored into both the by-ID map and every tag bucket
// holding the id. Caller holds b.mu.
func (b *RecordingBackend) storeContent(id, stored string) {
	n := b.noteByID[id]
	n.ID = id
	n.Content = stored
	b.noteByID[id] = n
	for tag, notes := range b.notesByTag {
		for i := range notes {
			if notes[i].ID == id {
				b.notesByTag[tag][i].Content = stored
			}
		}
	}
}

// normalizeBody collapses per-line trailing whitespace AND strips end-of-file
// trailing newline(s) — a minimal stand-in for Bear's on-write markdown
// normalization. The EOF-newline strip is the realistic hash-divergence
// trigger: it is tolerated by the diff-check (TrimRight " \n") yet changes the
// StripNewNoteURLsFromBody hash input.
func normalizeBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// catNote resolves the corpus note for a `cat <id>` call, returning an
// empty note when the ID is unknown (matches bearcli returning a bare object).
// Honors NormalizeReadBack so a read-back of an un-overwritten note also
// models the stored-normalized form.
func (b *RecordingBackend) catNote(args []string) domain.Note {
	if len(args) < 2 {
		return domain.Note{}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	n, ok := b.noteByID[args[1]]
	if !ok {
		return domain.Note{ID: args[1]}
	}
	if b.NormalizeReadBack {
		n.Content = normalizeBody(n.Content)
	}
	return n
}

// shouldFailReadBack reports whether this `cat` must fail to model a transient
// post-write read-back failure: FailReadBackAfterWrite is set, the id was
// written this lifetime, and it has not already failed a read-back once (so the
// failure is transient — a later read of the same id succeeds). Records the
// failure so the next read of that id goes through.
func (b *RecordingBackend) shouldFailReadBack(id string) bool {
	if !b.FailReadBackAfterWrite {
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if !b.writtenIDs[id] || b.failedReads[id] {
		return false
	}
	b.failedReads[id] = true
	return true
}

// Records returns a copy of every observed call in invocation order.
func (b *RecordingBackend) Records() []Record {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]Record, len(b.records))
	copy(out, b.records)
	return out
}

// CountKind returns how many recorded calls match both kind and tag. Pass
// tag == "" to count tag-less calls (cat / show / overwrite / create by ID).
func (b *RecordingBackend) CountKind(kind, tag string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	count := 0
	for _, r := range b.records {
		if r.Kind == kind && r.Tag == tag {
			count++
		}
	}
	return count
}

// recordingKindFromArgs mirrors the unexported classifier in
// bear/bearcli/pool.go so assertions speak the production metrics vocabulary.
func recordingKindFromArgs(args []string) string {
	if len(args) == 0 {
		return "other"
	}
	switch args[0] {
	case "list", "cat", "show", "overwrite", "create", "find":
		return args[0]
	default:
		return "other"
	}
}

// recordingTagFromArgs extracts the --tag value (list / find paths) or returns
// "" for tag-less id-addressed calls (cat / show / overwrite / create).
func recordingTagFromArgs(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--tag" {
			return args[i+1]
		}
	}
	return ""
}

// recordingCreatePayload echoes a usable id+title for a `create <title>` call
// so patch-on-create paths can parse the new note's ID. The ID is derived
// from the title to keep it stable and human-readable in failure output.
func recordingCreatePayload(args []string) []byte {
	if len(args) < 2 {
		return []byte(`{"id":"created","title":"","content":"","tags":[]}`)
	}
	title := args[1]
	id := "created-" + strings.ReplaceAll(title, " ", "-")
	created := domain.Note{ID: id, Title: title}
	out, err := json.Marshal(created)
	if err != nil {
		return []byte(`{"id":"created","title":"","content":"","tags":[]}`)
	}
	return out
}
