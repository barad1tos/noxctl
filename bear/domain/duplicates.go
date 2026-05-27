package domain

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// DuplicateRegistry holds titles that map to multiple note IDs across the
// daemon's managed corpus. Render layers consult it to disambiguate wikilinks:
// when a title is shared by two or more atomics, `[[Title]]` resolves
// non-deterministically in Bear (first match wins), so the daemon switches
// the bullet to a bear://x-callback URL whose `id=` query param targets the
// specific note. Trade-off: Bear's Linked-Notes backlink panel only honors
// `[[wikilinks]]`, so ID-URL bullets lose backlink visibility — acceptable
// for the rare ambiguous-title case (12 of 2137 atomics in the current
// corpus). For unique titles the wikilink form stays unchanged so backlinks
// keep working.
//
// Bear forum stance: this is a documented limitation, not a bug. Aliased
// `[[id|alias]]` syntax is not supported; the bear://x-callback URL form is
// the developer-recommended workaround.
type DuplicateRegistry struct {
	// titleToIDs[title] holds the IDs of every atomic that shares this
	// title. Populated only with titles having ≥ 2 entries; unique titles
	// are absent. Read-only after construction.
	titleToIDs map[string][]string
}

// IsDuplicate reports whether `title` is shared by two or more atomics.
// Nil-safe: a zero-value receiver always returns false, so renderers can
// call without guarding for an unbuilt registry.
func (r *DuplicateRegistry) IsDuplicate(title string) bool {
	if r == nil {
		return false
	}
	return len(r.titleToIDs[title]) > 1
}

// BuildCorpusDuplicateRegistry indexes every Bear note in the notes location.
// Use this when rendering generated links: unmanaged notes can still collide
// with managed atom titles, and Bear's `[[Title]]` resolver does not know
// which corpus slice noxctl owns.
func BuildCorpusDuplicateRegistry(ctx context.Context) (*DuplicateRegistry, error) {
	notes, err := ListCorpusNotes(ctx)
	if err != nil {
		return nil, fmt.Errorf("BuildCorpusDuplicateRegistry: %w", err)
	}
	titleToIDSet := make(map[string]map[string]struct{})
	for _, note := range notes {
		indexNoteTitle(note, titleToIDSet)
	}
	return &DuplicateRegistry{titleToIDs: collectDuplicates(titleToIDSet)}, nil
}

// ListCorpusNotes reads every Bear note in the notes location using the field
// set needed by duplicate-title scanners and structural-note classifiers.
func ListCorpusNotes(ctx context.Context) ([]Note, error) {
	out, err := runBearcli(
		ctx,
		[]string{"list", "--location", "notes", flagFormat, formatJSON, flagFields, "id,title,content,tags"},
		"",
	)
	if err != nil {
		return nil, fmt.Errorf("ListCorpusNotes list: %w", err)
	}
	var notes []Note
	if err = json.Unmarshal(out, &notes); err != nil {
		return nil, fmt.Errorf("ListCorpusNotes parse: %w", err)
	}
	return notes, nil
}

func indexNoteTitle(note Note, titleToIDSet map[string]map[string]struct{}) {
	title := strings.TrimSpace(note.Title)
	if title == "" || note.ID == "" {
		return
	}
	ids, ok := titleToIDSet[title]
	if !ok {
		ids = make(map[string]struct{})
		titleToIDSet[title] = ids
	}
	ids[note.ID] = struct{}{}
}

// collectDuplicates filters the per-title ID sets down to titles owned by
// ≥ 2 distinct notes, returning the materialized map[title][]ids the
// registry stores.
func collectDuplicates(titleToIDSet map[string]map[string]struct{}) map[string][]string {
	dup := make(map[string][]string)
	for title, idSet := range titleToIDSet {
		if len(idSet) <= 1 {
			continue
		}
		ids := make([]string, 0, len(idSet))
		for id := range idSet {
			ids = append(ids, id)
		}
		dup[title] = ids
	}
	return dup
}

// TitleNeedsURLForm reports whether a title contains characters that break
// `[[Title]]` wikilink parsing in Bear. The pipe `|` is the alias separator
// (`[[target|alias]]` resolves target="target"), so `[[Foo | Bar]]` would
// mis-resolve to a non-existent note "Foo". `]` and `[` close/open wikilink
// targets prematurely. Switching to a bear://x-callback URL preserves the
// link to the actual note at the cost of losing Bear's Linked-Notes
// backlink visibility — same trade-off as duplicate-title disambiguation.
func TitleNeedsURLForm(title string) bool {
	return strings.ContainsAny(title, "|][")
}

// AtomicWikilink returns the rendered link for an atomic note: a plain
// `[[Title]]` wikilink when the title is unique and parser-safe, or a Bear
// x-callback markdown link `[Title](bear://x-callback-url/open-note?id=<ID>)`
// when the title is shared with another atomic OR contains characters that
// break wikilink parsing (`|`, `]`, `[`). The label inside the URL form is
// escaped via escapeMarkdownLabel — bracket chars in the title would
// otherwise close the markdown link prematurely (`[[X] Y](url)` mis-parses
// because the inner `]` ends the label early). Centralized so every renderer
// (hub bullets, master cells, flat lists) handles disambiguation identically.
func AtomicWikilink(d *Domain, note Note) string {
	if note.Title == "" {
		// Empty title can't form a valid `[[Title]]` wikilink — it would
		// render as `[[]]` (broken bullet, unclickable). Fall back to a
		// bear://open-note URL with a localized "Без назви" placeholder so
		// the bullet stays clickable and surfaces the orphan to the
		// operator. Title-less atoms appear when a user clicks an old
		// "Нова нотатка" link whose URL pre-dates commit 305dedb (no
		// title= param) — Bear creates the note with Bear's default
		// untitled state.
		return fmt.Sprintf("[%s](bear://x-callback-url/open-note?id=%s)", T("atom.untitled-label"), note.ID)
	}
	if TitleNeedsURLForm(note.Title) || (d != nil && d.Duplicates.IsDuplicate(note.Title)) {
		return fmt.Sprintf("[%s](bear://x-callback-url/open-note?id=%s)", escapeMarkdownLabel(note.Title), note.ID)
	}
	return "[[" + note.Title + "]]"
}

// escapeMarkdownLabel escapes characters that break a `[label](url)` markdown
// link's label. Backslashes are doubled first so the subsequent bracket
// substitutions don't double-escape themselves. Bear renders `\[` and `\]`
// as literal brackets, preserving the title's visual shape while keeping
// the link clickable.
func escapeMarkdownLabel(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `[`, `\[`)
	s = strings.ReplaceAll(s, `]`, `\]`)
	return s
}
