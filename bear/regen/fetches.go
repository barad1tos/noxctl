package regen

// Bear CLI read primitives the regen pipeline uses to enumerate notes per
// domain, find a note by title, and look up hub / master IDs.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// listNotes returns every note tagged d.Tag with id, title, content, and
// created fields. The created timestamp drives time-based promotion logic
// (see specs/2026-05-07-quicknote-time-promotion-design.md): mod-time is
// bumped on every canonicalization pass so creation date is the only
// stable age signal.
func listNotes(ctx context.Context, d *domain.Domain) ([]domain.Note, error) {
	out, err := bearcli.Run(ctx,
		[]string{
			"list", "--tag", d.Tag,
			bearcli.FlagFormat, bearcli.FormatJSON,
			bearcli.FlagFields, "id,title,content,tags,created",
		},
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("listNotes(tag=%s): %w", d.Tag, err)
	}
	var notes []domain.Note
	if err = json.Unmarshal(out, &notes); err != nil {
		return nil, fmt.Errorf("listNotes(tag=%s): parse: %w", d.Tag, err)
	}
	return notes, nil
}

// findNoteByTitle scans all notes tagged d.Tag and returns the ID of the first
// matching title, or "" with nil error if none.
func findNoteByTitle(ctx context.Context, d *domain.Domain, title string) (string, error) {
	out, err := bearcli.Run(ctx,
		[]string{
			"list", "--tag", d.Tag,
			bearcli.FlagFormat, bearcli.FormatJSON,
			bearcli.FlagFields, bearcli.FieldsIDTitle,
		},
		"",
	)
	if err != nil {
		return "", fmt.Errorf("findNoteByTitle(%q): %w", title, err)
	}
	var notes []domain.Note
	if err = json.Unmarshal(out, &notes); err != nil {
		return "", fmt.Errorf("findNoteByTitle(%q): parse: %w", title, err)
	}
	for _, note := range notes {
		if note.Title == title {
			return note.ID, nil
		}
	}
	return "", nil
}

func findHubID(ctx context.Context, d *domain.Domain, title string) (string, error) {
	return findNoteByTitle(ctx, d, title)
}

// FindIndexID returns the bearcli note ID of this domain's master
// (index) note via title-based lookup. Returns an empty string +
// nil error when no matching note exists yet. Used by fast-pass
// code (e.g. cross-domain moves) that needs the master ID to
// generate inter-note links.
func FindIndexID(ctx context.Context, d *domain.Domain) (string, error) {
	return findNoteByTitle(ctx, d, d.IndexTitle)
}
