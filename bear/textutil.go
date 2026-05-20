package bear

// Domain text utilities — pure-string helpers shared across the regen
// pipeline: header-zone slicing, wikilink extraction, hub/master
// section parsing, sort tables, and the curator-marker split. None
// of these take a *Domain; they all operate on raw note bodies or
// title strings.

import (
	"fmt"
	"strings"
)

// HeaderZone returns everything before the first standalone '---' separator.
// When no separator exists the whole body is treated as header.
func HeaderZone(body string) string {
	if before, _, ok := strings.Cut(body, "\n---\n"); ok {
		return before
	}
	return body
}

// firstNonSectionH2 walks the first ~50 lines after H1 and returns the text of
// the first H2 that isn't a `[bracket]` section marker. Returns "" if none.
func firstNonSectionH2(body string) string {
	inAfterH1 := false
	for lineIndex, line := range strings.Split(body, "\n") {
		if lineIndex > 50 {
			break
		}
		if strings.HasPrefix(line, "# ") && !strings.HasPrefix(line, "## ") {
			inAfterH1 = true
			continue
		}
		if !inAfterH1 || !strings.HasPrefix(line, "## ") {
			continue
		}
		heading := strings.TrimSpace(strings.TrimPrefix(line, "##"))
		if strings.HasPrefix(heading, "[") && strings.HasSuffix(heading, "]") {
			continue
		}
		return heading
	}
	return ""
}

// ExtractWikilinkTarget pulls the target out of `[[X]]` or `[[X|alias]]`.
// Returns "" when not a clean wikilink.
func ExtractWikilinkTarget(raw string) string {
	raw = strings.TrimSpace(raw)
	if !strings.HasPrefix(raw, "[[") || !strings.HasSuffix(raw, "]]") || len(raw) < 4 {
		return ""
	}
	inner := raw[2 : len(raw)-2]
	if pipe := strings.Index(inner, "|"); pipe >= 0 {
		inner = inner[:pipe]
	}
	return strings.TrimSpace(inner)
}

// extractSectionFromHeaderLine pulls the third pipe-segment from a canonical
// header line. Returns "" when the line has fewer than 3 segments. The
// trailing new-note link decoration is stripped first so its presence
// never leaks into the section field.
func extractSectionFromHeaderLine(line string) string {
	parts := DropTrailingNewNoteURLSegment(strings.Split(line, " | "))
	if len(parts) < 3 {
		return ""
	}
	return strings.TrimSpace(parts[2])
}

// stripHeaderCount turns `### Title (N)` or `#### Title (N)` into `Title`.
// Strips the heading prefix and any trailing ` (N)` count suffix.
func stripHeaderCount(line, prefix string) string {
	stripped := strings.TrimPrefix(line, prefix)
	if parenStart := strings.LastIndex(stripped, " ("); parenStart >= 0 {
		stripped = stripped[:parenStart]
	}
	return strings.TrimSpace(stripped)
}

// SplitMarker partitions a hub/master note body into its auto-zone (above the
// curator marker) and manual zone (from the marker onward). Exported so
// the plan path (`bear/engine/plan.go::computeDomainDelta`) can mirror
// `upsertMasterIndex`'s manual-zone preservation before comparing
// rendered output to live vault content — otherwise plan reports false
// drift on every master that has a curator zone.
func SplitMarker(body string) (auto, manual string) {
	markerStart := strings.Index(body, HubMarker)
	if markerStart < 0 {
		return body, ""
	}
	return body[:markerStart], body[markerStart:]
}

// isTagOnlyLine reports whether the line consists only of whitespace-separated #tags.
func isTagOnlyLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	for token := range strings.FieldsSeq(line) {
		if !strings.HasPrefix(token, "#") {
			return false
		}
	}
	return true
}

// isWikilinkOnly reports whether `text` is a single `[[X]]` wikilink (no nested wikilinks).
func isWikilinkOnly(text string) bool {
	if !strings.HasPrefix(text, "[[") || !strings.HasSuffix(text, "]]") || len(text) < 4 {
		return false
	}
	return !strings.Contains(text[2:len(text)-2], "[[")
}

// isHybridHeader recognizes `#tag1 | [[Backlink]] [| section]` lines.
// First two pipe-segments must be tag-only or wikilink-only; further segments
// are treated as free-form section text and accepted when non-empty.
func isHybridHeader(line string) bool {
	if !strings.Contains(line, " | ") {
		return false
	}
	segments := strings.Split(line, " | ")
	for index, segment := range segments {
		segment = strings.TrimSpace(segment)
		if segment == "" {
			return false
		}
		if index < 2 && !isTagOnlyLine(segment) && !isWikilinkOnly(segment) {
			return false
		}
	}
	return true
}

// DomainsByTag indexes a domain slice by the domain's full tag
// (`d.Tag`, e.g. `"quicknote/daily"`). Used by fast-pass paths
// (ApplyDailyDefaultTag, ApplyForeignTagEscape) to resolve a
// destination domain from a tag string carried on the note, so the
// pre-pass can write canonical form for that domain in a single
// bearcli call. Skipping nil-Tag (defensive — Domain.Tag is always
// set by factories, but Domain{} zero value would otherwise produce
// a "" key that collides on lookup).
func DomainsByTag(ds []*Domain) map[string]*Domain {
	out := make(map[string]*Domain, len(ds))
	for _, d := range ds {
		if d == nil || d.Tag == "" {
			continue
		}
		out[d.Tag] = d
	}
	return out
}

// isHeaderLine reports whether the line lives in the header zone (tag-only,
// wikilink-only, or hybrid `tag | [[link]] [| section]`).
func isHeaderLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	return isTagOnlyLine(line) || isWikilinkOnly(line) || isHybridHeader(line)
}

// FirstLetter returns a sort key. Latin first, then Ukrainian, then Russian-only, then others.
func FirstLetter(name string) (string, string) {
	if name == "" {
		return "9", "?"
	}
	firstRune := []rune(strings.ToUpper(name))[0]
	firstChar := string(firstRune)
	if firstRune >= 'A' && firstRune <= 'Z' {
		return "1", firstChar
	}
	if group, key := lookupAlphabet(uaOrder, "2", firstChar); group != "" {
		return group, key
	}
	if group, key := lookupAlphabet(ruOrder, "3", firstChar); group != "" {
		return group, key
	}
	return "4", firstChar
}

// lookupAlphabet checks whether `firstChar` belongs to the given Cyrillic
// alphabet ordering. Returns the group label and a zero-padded sort key
// `<NN><letter>` when found; empty strings on miss so the caller can fall
// through to the next alphabet.
func lookupAlphabet(order, group, firstChar string) (string, string) {
	pos := strings.Index(order, firstChar)
	if pos < 0 {
		return "", ""
	}
	return group, fmt.Sprintf("%02d%s", pos, firstChar)
}

// CompareTitles returns -1/0/+1 ordering Latin → Ukrainian → Russian-only →
// other, then falls back to lowercase comparison within the same alphabet
// group. Used by ByTitle and by domain configs that sort string slices.
func CompareTitles(a, b string) int {
	g1, k1 := FirstLetter(a)
	g2, k2 := FirstLetter(b)
	if g1 != g2 {
		if g1 < g2 {
			return -1
		}
		return 1
	}
	if k1 != k2 {
		if k1 < k2 {
			return -1
		}
		return 1
	}
	la, lb := strings.ToLower(a), strings.ToLower(b)
	if la < lb {
		return -1
	}
	if la > lb {
		return 1
	}
	return 0
}

// ByTitle sorts notes by title with UA/RU-aware comparator (CompareTitles).
type ByTitle []Note

func (t ByTitle) Len() int           { return len(t) }
func (t ByTitle) Swap(i, j int)      { t[i], t[j] = t[j], t[i] }
func (t ByTitle) Less(i, j int) bool { return CompareTitles(t[i].Title, t[j].Title) < 0 }
