package audit

// Lint pass — types + sort utility.
//
// Per-atom detection (LintAtom) and auto-fix logic (AutoFixAtom) live
// in lints.go; the audit orchestrator (Scan, PrintFindings,
// LintApplyDomains) lives in audit.go. This file owns only the
// shared types (LintCategory + Finding) and the SortFindings utility
// callers across both halves rely on.

import "sort"

// LintCategory enumerates the classes of atom-level data anomalies the lint
// pass detects. New categories slot in alongside the existing ones; the
// audit reporter groups findings by category.
type LintCategory string

const (
	// LintBrokenH1 — note title starts with the pipe/asterisk fragments of a
	// canonical header line; the original H1 was overwritten by an earlier
	// migration script that incorrectly treated a body line as the H1.
	LintBrokenH1 LintCategory = "broken-h1"
	// LintMultiCanonical — header zone has ≥2 canonical-shape lines. ParseMeta
	// picks the first one, so the rest is silent residue. Auto-fixable: keep
	// the first canonical, strip the rest.
	LintMultiCanonical LintCategory = "multi-canonical"
	// LintOrphanTag — body has a standalone `#<top>` or `#<top>/<sub>` token
	// outside the canonical line. Bear's tag tree is built from any such
	// token, so duplicate tags cause sidebar noise. Auto-fixable: strip the
	// orphan token, keep the canonical.
	LintOrphanTag LintCategory = "orphan-tag"
	// LintUnsafeTitle — title contains `|`, `]`, or `[` which break Bear's
	// `[[wikilink]]` parsing. Render layer (AtomicWikilink) emits URL-form
	// links automatically, so no atom-level fix is needed; the finding is
	// informational so the user can rename if desired.
	LintUnsafeTitle LintCategory = "unsafe-title"
	// LintMalformedCanonical — orphan `#<top>` token exists but no recognized
	// canonical-shape line is present. Likely a malformed canonical that
	// missed the parser (e.g. extra tokens between `#tag` and ` | `). Not
	// auto-fixable: scrubbing the orphan would eat the tag off the malformed
	// canonical too. Surface for manual review.
	LintMalformedCanonical LintCategory = "malformed-canonical"
	// LintUntracked — atomic note carries a tag whose top-level segment is
	// NOT in the closed catalog of TOML-managed domains. Emitted by the
	// residue scan (bear/audit/untracked.go), NOT by per-atom LintAtom.
	// Informational: noxctl deliberately does not touch unmanaged tags;
	// residue is separated from drift and does NOT contribute to plan
	// exit-code 2.
	LintUntracked LintCategory = "untracked"
	// LintOrphanFamily — atomic note carries a `#<family>/<sub>` tag where
	// `<family>` is NOT a managed catalog root. Corpus-level concern
	// (sibling class of LintUntracked, not scoped to any single managed
	// Domain): the DomainTag on the emitted Finding is the empty string.
	//
	// Detection fires only for the `#<family>/<sub>` shape; bare top-level
	// tags (`#randomthing`) are out of scope and handled by LintUntracked.
	// Atoms already carrying `#orphans` (with or without sub-tag) are
	// skipped — the apply step (`bearcli tags add <noteID> orphans`) is
	// therefore idempotent. The original stray-family tag is preserved so
	// the operator has full context for manual triage (rename the stray,
	// delete it, or move the atom to a proper domain).
	LintOrphanFamily LintCategory = "orphan-family"
)

// Finding is one anomaly detected by the lint pass. Multiple findings can
// fire for one atom (e.g. broken-h1 + multi-canonical co-occur after a
// botched migration).
type Finding struct {
	DomainTag string
	NoteID    string
	Title     string
	Category  LintCategory
	Detail    string
	Fixable   bool
}

// SortFindings orders findings by domain → category → title for stable
// audit output. Mutates in place.
func SortFindings(findings []Finding) {
	sort.Slice(findings, func(i, j int) bool {
		if findings[i].DomainTag != findings[j].DomainTag {
			return findings[i].DomainTag < findings[j].DomainTag
		}
		if findings[i].Category != findings[j].Category {
			return findings[i].Category < findings[j].Category
		}
		return findings[i].Title < findings[j].Title
	})
}
