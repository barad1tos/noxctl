package bear

import "strings"

// ParseMasterTable inverts a flat markdown-table master into an identifier
// → bucket map. Pairs with Domain.ParseMasterTable to enable the
// bidirectional master pattern: cut a bullet from one column, paste into
// another, save, and on the next regen the daemon rewrites the matching
// atomic's canonical header to the new bucket. Used by every flat-table
// domain (prose, vendors, technologies, future).
//
// Identifier semantics: for plain `[[Title]]` wikilinks the key is the
// title (back-compatible). For `[Title](bear://x-callback-url/open-note?id=X)`
// markdown links — emitted only for duplicate-titled atomics — the key is
// the note ID extracted from the URL. computeMasterOverrides looks up by
// title first, then by ID, so both forms route correctly even when the
// master mixes them.
//
// Lenient by design: extra blank lines, comment rows that don't start with
// `|`, and column-count mismatches between header and body rows are all
// tolerated. Whatever can't be classified is silently dropped — the worst
// outcome is a missed re-bucket, which the user notices and corrects.
//
// Header row format: `| Group A (N) | Group B (M) |...` — the trailing
// `(count)` suffix is stripped, leaving the bucket name. Body rows hold
// `<br>`-joined wikilinks per cell.
//
// Currently no factory wires this: `ParseMasterFlatGrouped` (H2-section
// layout) covers every shipping domain. ParseMasterTable stays exported
// as the canonical inverse of RenderFlatColumnTable — when a flat-table
// (markdown-pipe) master domain ships, its factory will assign this
// function to Domain.ParseMasterTable. See "Reuse before
// writing" table.
func ParseMasterTable(_ *Domain, masterContent string) map[string]string {
	out := make(map[string]string)
	lines := strings.Split(masterContent, "\n")

	headerIdx := findFirstTableRow(lines)
	if headerIdx < 0 {
		return out
	}
	bucketNames := parseTableHeader(lines[headerIdx])
	if len(bucketNames) == 0 {
		return out
	}

	for index := headerIdx + 1; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if !strings.HasPrefix(line, "|") || isTableSeparatorRow(line) {
			continue
		}
		cells := splitTableRow(line)
		for colIdx, cell := range cells {
			if colIdx >= len(bucketNames) {
				break
			}
			for _, ident := range extractCellIdentifiers(cell) {
				out[ident] = bucketNames[colIdx]
			}
		}
	}
	return out
}

// extractCellIdentifiers pulls every atomic identifier out of a single
// table cell — wikilink targets (`[[Title]]` → "Title") plus note IDs
// embedded in bear://x-callback URLs (`[Label](bear://x-callback-url/open-note?id=X)` → "X").
// Order: wikilinks first (as they appear left-to-right), then URL-form IDs.
func extractCellIdentifiers(cell string) []string {
	out := extractCellWikilinks(cell)
	out = append(out, extractCellNoteIDs(cell)...)
	return out
}

// extractCellNoteIDs scans a cell for `bear://x-callback-url/open-note?id=<ID>`
// occurrences and returns the IDs. Tolerant of arbitrary surrounding markdown
// — only the URL substring matters, link text is ignored.
func extractCellNoteIDs(cell string) []string {
	var out []string
	const prefix = "bear://x-callback-url/open-note?id="
	rest := cell
	for {
		start := strings.Index(rest, prefix)
		if start < 0 {
			return out
		}
		rest = rest[start+len(prefix):]
		end := strings.IndexAny(rest, ")& \t\n")
		if end < 0 {
			end = len(rest)
		}
		id := rest[:end]
		rest = rest[end:]
		if id != "" {
			out = append(out, id)
		}
	}
}

// findFirstTableRow returns the index of the first non-separator table row
// (the header row), or -1 when none found.
func findFirstTableRow(lines []string) int {
	for index, line := range lines {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "|") || isTableSeparatorRow(line) {
			continue
		}
		return index
	}
	return -1
}

// isTableSeparatorRow reports whether a `|`-prefixed line consists only of
// dashes, pipes, colons and whitespace — the markdown table delimiter row.
func isTableSeparatorRow(line string) bool {
	stripped := strings.ReplaceAll(line, "-", "")
	stripped = strings.ReplaceAll(stripped, "|", "")
	stripped = strings.ReplaceAll(stripped, ":", "")
	return strings.TrimSpace(stripped) == ""
}

// splitTableRow splits a markdown table row by `|`, trimming each cell and
// dropping the leading/trailing empty pieces produced by the outer pipes.
func splitTableRow(line string) []string {
	raw := strings.Split(line, "|")
	cells := make([]string, 0, len(raw))
	for _, cell := range raw {
		cells = append(cells, strings.TrimSpace(cell))
	}
	for len(cells) > 0 && cells[0] == "" {
		cells = cells[1:]
	}
	for len(cells) > 0 && cells[len(cells)-1] == "" {
		cells = cells[:len(cells)-1]
	}
	return cells
}

// parseTableHeader returns the bucket names from a table header row,
// stripping the trailing `(N)` count suffix that flat-table renderers
// always write.
func parseTableHeader(line string) []string {
	cells := splitTableRow(line)
	out := make([]string, len(cells))
	for index, cell := range cells {
		if parenStart := strings.LastIndex(cell, " ("); parenStart >= 0 {
			cell = cell[:parenStart]
		}
		out[index] = strings.TrimSpace(cell)
	}
	return out
}

// extractCellWikilinks pulls every `[[Target]]` (or `[[Target|Alias]]`)
// target out of a single table cell. Cells use `<br>` as a visual newline;
// the wikilink scan is delimiter-agnostic so no special split is needed.
func extractCellWikilinks(cell string) []string {
	var out []string
	rest := cell
	for {
		openIdx := strings.Index(rest, "[[")
		if openIdx < 0 {
			return out
		}
		rest = rest[openIdx+2:]
		closeIdx := strings.Index(rest, "]]")
		if closeIdx < 0 {
			return out
		}
		target := rest[:closeIdx]
		rest = rest[closeIdx+2:]
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = target[:pipe]
		}
		target = strings.TrimSpace(target)
		if target != "" {
			out = append(out, target)
		}
	}
}

// Keeper for the flat-table API trio per "Reuse before writing".
// No shipping factory wires these yet (grouped-vertical-flat covers every
// current domain), but they're the canonical inverse of RenderFlatColumnTable
// and stay exported so the next markdown-pipe-table domain finds them via
// the documented surface, not by re-deriving the parser.
var _ = ParseMasterTable
