package render

import (
	"fmt"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

// Section is one labeled bucket emitted by RenderVerticalSections.
// Header is the text rendered after `## ` (caller controls the count
// format, e.g. "Книги (44)" or "Власні" without a count). Bullets hold
// the pre-formatted bullet bodies (no leading `- `, no trailing newline).
//
// An empty Header renders the bullets without an H2 — the flat-list mode
// used by domains whose master is a single alphabetical stream of atomics
// (llm/characters, llm/rules, llm/tips, it/domains, …).
//
// An empty Bullets slice with a non-empty Header is silently dropped so
// renderers can pass derived sections without filtering empties first.
type Section struct {
	Header  string
	Bullets []string
}

// RenderVerticalSections is the single emission helper shared by every
// master renderer. Output shape:
//
//	# <IndexTitle>
//	#<tag>
//	---
//
//	## <Section.Header>
//	- <Section.Bullets[0]>
//	- <Section.Bullets[1]>
//
// ...
//
// Header="" suppresses the H2 line — the section's bullets emit straight
// after the master header, used by single-stream flat-list masters.
//
// Centralizing emission keeps every grouped/flat master in lock-step on
// blank-line layout, count format, and bullet syntax. Domains that need
// custom data shape supply a section-builder; the rendering itself never
// varies.
//
//nolint:revive // public API; rename is breaking change for callers
func RenderVerticalSections(d *domain.Domain, sections []Section) string {
	var body strings.Builder
	WriteMasterHeader(&body, d)
	for _, section := range sections {
		emitSection(&body, section)
	}
	return body.String()
}

// emitSection writes one section to body. Header="" → no H2 line, just
// bullets. Header set + zero bullets → silently dropped (matches the
// existing "skip empty buckets" behavior of grouped renderers).
func emitSection(body *strings.Builder, section Section) {
	if section.Header == "" {
		for _, bullet := range section.Bullets {
			_, _ = fmt.Fprintf(body, "- %s\n", bullet)
		}
		return
	}
	if len(section.Bullets) == 0 {
		return
	}
	_, _ = fmt.Fprintf(body, "\n## %s\n", section.Header)
	for _, bullet := range section.Bullets {
		_, _ = fmt.Fprintf(body, "- %s\n", bullet)
	}
}
