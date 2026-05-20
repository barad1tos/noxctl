package bear

// Section rendering helpers — note grouping by section/sub-section,
// hub-order stabilization, and the alphabetic-insert primitives the
// renderers use to keep deterministic output across regen cycles.

import (
	"fmt"
	"sort"
	"strings"
)

// GroupNotesBySection partitions atoms by the Section value their
// canonical-header line declares (via Domain.ParseMeta). Used by
// sectioned-master renderers that want one bullet group per section.
func (d *Domain) GroupNotesBySection(notes []Note) map[string][]Note {
	out := make(map[string][]Note)
	for _, note := range notes {
		meta := d.ParseMeta(d, note.Content)
		out[meta.Section] = append(out[meta.Section], note)
	}
	return out
}

// NestSections folds a flat path→notes map into a two-level structure: top
// segment → sub-path → notes (sub-path "" means notes directly under the top).
// The empty-path bucket is excluded; caller renders it as flat unsectioned.
// Returned topKeys is sorted alphabetically.
func NestSections(bySection map[string][]Note) ([]string, map[string]map[string][]Note) {
	topGroups := make(map[string]map[string][]Note)
	for path, items := range bySection {
		if path == "" {
			continue
		}
		parts := strings.SplitN(path, "/", 2)
		top := parts[0]
		sub := ""
		if len(parts) > 1 {
			sub = parts[1]
		}
		if _, ok := topGroups[top]; !ok {
			topGroups[top] = make(map[string][]Note)
		}
		topGroups[top][sub] = append(topGroups[top][sub], items...)
	}
	topKeys := make([]string, 0, len(topGroups))
	for top := range topGroups {
		topKeys = append(topKeys, top)
	}
	sort.Strings(topKeys)
	return topKeys, topGroups
}

// RenderNoteList writes `- <wikilink>` lines for each note. The link form
// (plain `[[Title]]` vs bear://x-callback URL) is decided per-note by
// AtomicWikilink based on the domain's duplicate registry.
func RenderNoteList(b *strings.Builder, d *Domain, items []Note) {
	for _, note := range items {
		_, _ = fmt.Fprintf(b, "- %s\n", AtomicWikilink(d, note))
	}
}

// RenderSectionGroup renders a single H3 section with its direct notes and any
// H4 sub-sections. The H3 count reflects only items listed *directly* under it.
func RenderSectionGroup(b *strings.Builder, d *Domain, top string, subMap map[string][]Note) {
	direct := subMap[""]
	_, _ = fmt.Fprintf(b, "### %s (%d)\n", top, len(direct))
	RenderNoteList(b, d, direct)
	subKeys := make([]string, 0, len(subMap))
	for sub := range subMap {
		if sub != "" {
			subKeys = append(subKeys, sub)
		}
	}
	sort.Strings(subKeys)
	for _, sub := range subKeys {
		_, _ = fmt.Fprintf(b, "#### %s (%d)\n", sub, len(subMap[sub]))
		RenderNoteList(b, d, subMap[sub])
	}
}

// parseHubOrder reads a Hub note's auto-zone and returns the bullet-title order
// per section path. Unsectioned bullets keyed by ""; H3 by "<top>"; H4 by
// "<top>/<sub>". Used to preserve user-reordered bullets across regen.
func parseHubOrder(autoZone string) map[string][]string {
	out := make(map[string][]string)
	currentSection := ""
	currentTop := ""
	for line := range strings.SplitSeq(autoZone, "\n") {
		if strings.HasPrefix(line, "### ") {
			currentTop = stripHeaderCount(line, "### ")
			currentSection = currentTop
			continue
		}
		if strings.HasPrefix(line, "#### ") {
			currentSection = currentTop + "/" + stripHeaderCount(line, "#### ")
			continue
		}
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		if title := ExtractWikilinkTarget(strings.TrimPrefix(line, "- ")); title != "" {
			out[currentSection] = append(out[currentSection], title)
		}
	}
	return out
}

// reorderForOutput orders notes by the given title sequence (from a previous
// Hub render). Titles found in `order` are emitted first in that order;
// newcomers — entries whose current title is absent from `order`, including
// notes renamed on another device since the last regen — are spliced into
// their alphabetical position among already-emitted entries instead of being
// appended at the end. Duplicate titles are matched first-found.
//
// Why splice instead of append: when a user renames an atomic on phone, the
// old title vanishes from `order` and the new title is unmatched. Appending
// would park the rename at the bottom of its section until the next user-
// driven reorder; splicing keeps the alphabet stable across renames.
//
// `notes` is expected pre-sorted alphabetically (callers run sort.Sort(ByTitle)
// before ApplyOrder), so the linear-scan splice is stable.
func reorderForOutput(notes []Note, order []string) []Note {
	if len(order) == 0 {
		return notes
	}
	out, used := emitInPriorOrder(notes, order)
	for index, note := range notes {
		if !used[index] {
			out = insertAlphabetically(out, note)
		}
	}
	return out
}

// emitInPriorOrder walks `order` and emits each matching note exactly once,
// in the order requested. Returns the partial result and a `used` mask the
// caller uses to find newcomers. Duplicate titles are matched first-found.
func emitInPriorOrder(notes []Note, order []string) (out []Note, used []bool) {
	indexByTitle := make(map[string][]int, len(notes))
	for index, note := range notes {
		indexByTitle[note.Title] = append(indexByTitle[note.Title], index)
	}
	used = make([]bool, len(notes))
	out = make([]Note, 0, len(notes))
	for _, title := range order {
		noteIndices := indexByTitle[title]
		for slot, noteIndex := range noteIndices {
			if used[noteIndex] {
				continue
			}
			used[noteIndex] = true
			out = append(out, notes[noteIndex])
			indexByTitle[title] = noteIndices[slot+1:]
			break
		}
	}
	return out, used
}

// insertAlphabetically splices `note` into `target` at the first position
// whose existing title sorts after `note.Title` (CompareTitles). Stable when
// titles compare equal — `note` inserts before its tie. Linear scan; callers
// keep `target` short by routing newcomers here instead of resorting whole
// sections.
func insertAlphabetically(target []Note, note Note) []Note {
	insertAt := len(target)
	for position, existing := range target {
		if CompareTitles(note.Title, existing.Title) < 0 {
			insertAt = position
			break
		}
	}
	target = append(target, Note{})
	copy(target[insertAt+1:], target[insertAt:])
	target[insertAt] = note
	return target
}

// ApplyOrder reorders every section's notes according to the existing Hub
// render's bullet sequence. Sections not present in `order` keep alphabetical default.
func ApplyOrder(bySection map[string][]Note, order map[string][]string) {
	for path, items := range bySection {
		bySection[path] = reorderForOutput(items, order[path])
	}
}

// upsertHub creates or updates a Tier-2 Hub note for one bucket. No-op when
// d.RenderHub == nil (domain doesn't have Tier-2 hubs). Returns a human-
// readable summary; an err signals the caller to aggregate failure counts.
//
// `bucket` is the canonical bucket name (matches atomic canonical-header
// segment). The note title comes from d.hubTitleFor(bucket) so sub-tag
// preserving domains can namespace hubs as `<top> · <bucket>` while keeping
// bucket-keyed group lookups intact. RenderHub still receives bucket — it
// can resolve the title via d.hubTitleFor when needed.
