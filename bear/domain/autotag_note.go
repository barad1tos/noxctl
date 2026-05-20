package domain

import "strings"

// AutoTagNote is the bearcli `list --format json` shape extended with
// the per-note tags + content payloads that the autotag fast-pass and
// the untracked-tag scanner both rely on. domain.Note carries only id
// + title; AutoTagNote adds the side data fetched alongside.
//
// Lives in package domain because both the fastpass family (autotag,
// bootstrap, foreigntag, placeholder) and the audit family (untracked
// scan) consume it. Keeping the type in domain prevents a fastpass↔
// domain cycle when the audit-side caller reaches for the shared shape.
type AutoTagNote struct {
	ID      string   `json:"id"`
	Title   string   `json:"title"`
	Tags    []string `json:"tags"`
	Content string   `json:"content"`
}

// TopLevelSegment returns the part of a tag string before the first
// `/`, stripping any leading `#`. Tag-tree-aware classifiers (the
// foreign-tag escape pass, untracked-tag aggregator) use it to find
// the family root that domain catalogs key on.
func TopLevelSegment(tag string) string {
	stripped := strings.TrimPrefix(tag, "#")
	if root, _, ok := strings.Cut(stripped, "/"); ok {
		return root
	}
	return stripped
}
