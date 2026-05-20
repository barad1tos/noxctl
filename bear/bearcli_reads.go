package bear

// Core listing primitives — bearcli read paths the regen pipeline
// uses to enumerate notes per domain, find a note by title, and look
// up the hub / master index IDs. Plus the bear-package shim for
// bearcli.OverwriteWithRetry. Split from core.go to keep the I/O
// boundary visible at file scope.

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// overwriteWithRetry is the bear-package shim over
// bearcli.OverwriteWithRetry — kept here so existing call sites
// (RunRegen, fast-passes, lint AutoFix) continue compiling without a
// rename. The hash-conflict counter, retry logic, and showHash live
// in bear/bearcli; this file no longer owns any bearcli machinery.
func overwriteWithRetry(ctx context.Context, noteID, body string) error {
	return bearcli.OverwriteWithRetry(ctx, noteID, body)
}

// listNotes returns every note tagged d.Tag with id, title, content, and
// created fields. The created timestamp drives time-based promotion logic
// (see specs/2026-05-07-quicknote-time-promotion-design.md): mod-time is
// bumped on every canonicalization pass so creation date is the only
// stable age signal.
func (d *Domain) listNotes(ctx context.Context) ([]Note, error) {
	out, err := runBearcli(ctx,
		[]string{"list", "--tag", d.Tag, flagFormat, formatJSON, flagFields, "id,title,content,tags,created"},
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("listNotes(tag=%s): %w", d.Tag, err)
	}
	var notes []Note
	if err = json.Unmarshal(out, &notes); err != nil {
		return nil, fmt.Errorf("listNotes(tag=%s): parse: %w", d.Tag, err)
	}
	return notes, nil
}

// findNoteByTitle scans all notes tagged d.Tag and returns the ID of the first
// matching title, or "" with nil error if none.
func (d *Domain) findNoteByTitle(ctx context.Context, title string) (string, error) {
	out, err := runBearcli(ctx, []string{"list", "--tag", d.Tag, flagFormat, formatJSON, flagFields, fieldsIDTitle}, "")
	if err != nil {
		return "", fmt.Errorf("findNoteByTitle(%q): %w", title, err)
	}
	var notes []Note
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

func (d *Domain) findHubID(ctx context.Context, title string) (string, error) {
	return d.findNoteByTitle(ctx, title)
}

func (d *Domain) findIndexID(ctx context.Context) (string, error) {
	return d.findNoteByTitle(ctx, d.IndexTitle)
}

// showHash moved to bearcli.ShowHash. No bear-package callers; the
// previous overwriteWithRetry was its only consumer and now lives in
// bear/bearcli/overwrite.go.
