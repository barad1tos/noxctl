package testutil

// RecordingBackend is the shared bearcli.Backend fake every I/O-amplification
// test drives. It records (Kind, Tag) for each Run call and serves a fixed
// note corpus from memory so regen.Run / engine.Apply complete without real
// subprocesses. Co-located with the domain fixtures (domains.go) so the regen
// and engine external test packages reach one backend, not per-file copies.

import (
	"context"
	"encoding/json"
	"strings"
	"sync"

	"github.com/barad1tos/noxctl/bear/domain"
)

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

	// NormalizeReadBack, when set, collapses trailing whitespace on every
	// `cat` read-back. It models a backend (like Bear) that normalizes stored
	// markdown so the read-back bytes differ from what was written — the D-02
	// hash-stability landmine. Combined with write-through (overwrite mutates
	// the corpus), it proves the changed-branch read-back captures the STORED
	// form, keeping the next cycle's hash stable.
	NormalizeReadBack bool

	mu      sync.Mutex
	records []Record
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
	return &RecordingBackend{notesByTag: notesByTag, noteByID: byID}
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
		return json.Marshal(b.catNote(args))
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "create":
		return recordingCreatePayload(args), nil
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

// normalizeBody collapses trailing whitespace on each line — a minimal stand-in
// for Bear's on-write markdown normalization.
func normalizeBody(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	return strings.Join(lines, "\n")
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
