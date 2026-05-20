package bearcli

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/barad1tos/noxctl/bear/note"
)

// ListNotesForTag returns every Bear note carrying the given
// canonical tag. Convenience wrapper for CLI helpers that need to
// enumerate notes by tag without owning a *Domain (`noxctl destroy`,
// `noxctl import`). Production RunRegen paths use the *Domain method
// listNotes (in bear/) directly.
//
// Returns id + title + tags + content. Created/modified timestamps
// are intentionally omitted — callers that need them should call Run
// directly with the appropriate --fields list.
func ListNotesForTag(ctx context.Context, tag string) ([]note.Note, error) {
	out, err := Run(ctx,
		[]string{
			"list", "--tag", tag,
			FlagFormat, FormatJSON,
			FlagFields, "id,title,tags,content",
		},
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("ListNotesForTag(%s): %w", tag, err)
	}
	var notes []note.Note
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
	if _, err := Run(ctx, []string{"trash", noteID}, ""); err != nil {
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
	return OverwriteWithRetry(ctx, noteID, body)
}
