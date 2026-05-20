package bear

import (
	"context"
	"encoding/json"
	"fmt"
)

// ListNotesForTag returns every Bear note carrying the given
// canonical tag. Convenience wrapper for CLI helpers that need to
// enumerate notes by tag without owning a *Domain (`noxctl destroy`,
// `noxctl import`). Production `RunRegen` paths use the *Domain
// method `listNotes` directly.
//
// Returns id + title + tags + content. Created/modified timestamps
// are intentionally omitted — callers that need them should call
// bearcli directly.
func ListNotesForTag(ctx context.Context, tag string) ([]Note, error) {
	out, err := runBearcli(ctx,
		[]string{
			"list", "--tag", tag,
			flagFormat, formatJSON,
			flagFields, "id,title,tags,content",
		},
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("ListNotesForTag(%s): %w", tag, err)
	}
	var notes []Note
	if parseErr := json.Unmarshal(out, &notes); parseErr != nil {
		return nil, fmt.Errorf("ListNotesForTag(%s) parse: %w", tag, parseErr)
	}
	return notes, nil
}

// TrashNote soft-deletes a note via `bearcli trash <id>`. The note
// remains restorable from Bear's trash UI (or via `bearcli
// restore`). Used by `noxctl destroy` to remove auto-generated
// master + hub notes; never used on atomic note content.
func TrashNote(ctx context.Context, noteID string) error {
	if _, err := runBearcli(ctx, []string{"trash", noteID}, ""); err != nil {
		return fmt.Errorf("TrashNote(%s): %w", noteID, err)
	}
	return nil
}

// OverwriteNoteContent rewrites a note body via the same retry loop
// the regen pipeline uses for canonical-line edits. Exported so CLI
// helpers (`noxctl destroy`'s canonical-strip path) can perform a
// single overwrite without duplicating the optimistic-concurrency
// retry logic.
func OverwriteNoteContent(ctx context.Context, noteID, body string) error {
	return overwriteWithRetry(ctx, noteID, body)
}

// IsAuxNote reports whether a note is an auto-generated master or
// hub (true) versus an operator-authored atom (false). Mirrors the
// classifier the regen pipeline uses to decide which notes to skip
// during groupAtomics. CLI helpers (`noxctl destroy`) use this to
// split a tag's note set into "trash these" vs "strip canonical".
func IsAuxNote(d *Domain, n Note) bool {
	return d.skipNote(n)
}
