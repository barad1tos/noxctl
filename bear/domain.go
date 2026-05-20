// Package bear is the Bear-specific framework that powers the regen-watchd
// daemon: bearcli wrappers, canonical-header parsing, sort comparators, and
// the *Domain abstraction with its core algorithms.
//
// Per-tag configurations live in the sibling `modules/` package — adding a
// new tag is one new file there with a `var FooDomain = &bear.Domain{...}`
// literal, plus one append to main's `domains` slice.
package bear

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/note"
)

// ErrHashConflict + BearcliTimeout + bearcli command-line constants
// (FlagFormat, FlagFields, FlagBase, FormatJSON, FieldsIDTitle,
// FieldsAutoTag) now live in bear/bearcli. bear/ re-exports the
// most-used ones via bear/bearcli_aliases.go for backward
// compatibility.

// HubMarker splits a Hub note's auto-zone (above) from the
// curator-managed zone (below). Same convention is reused across all
// domains' hubs and master notes that may carry hand-written
// commentary.
//
//cyrillic:permit
const HubMarker = "## ✱ Куратор"

// Cyrillic alphabet ordering for the ByAuthor sort comparator. Latin first,
// then Ukrainian, then Russian-only, then anything else.
//
// These two strings are alphabet tables, not user-facing copy — bear.T
// would not be a meaningful indirection.
const (
	//cyrillic:permit
	uaOrder = "АБВГҐДЕЄЖЗИІЇЙКЛМНОПРСТУФХЦЧШЩЬЮЯ"
	//cyrillic:permit
	ruOrder = "АБВГДЕЁЖЗИЙКЛМНОПРСТУФХЦЧШЩЪЫЬЭЮЯ"
)

// Note is re-exported here as a type alias so existing call sites
// (bear.Note across 30+ files) keep compiling unchanged. The canonical
// definition lives in bear/note/note.go — a leaf package every layer
// (bearcli, render, fastpass, audit) imports without forming a cycle
// back through bear/.
//
// New code should prefer the bear/note import; this alias exists for
// migration compatibility and may be removed once every caller has
// been moved over.
type Note = note.Note

// AtomicMeta is the structured form of an atomic note's canonical header line.
// Semantics of `bucket` and `section` differ per domain; see ParseMeta callback.
type AtomicMeta struct {
	Bucket  string
	Section string
}

// AtomicParts holds the destructured form of an atomic note's content during
// parseAtomicContent / renderAtomicCanonical round-trip.
//
// PreambleLines captures non-tag-line content that lives BETWEEN the H1
// and the first canonical header line. Per spec component 5 those
// lines must round-trip in-place at render time (between H1 and the
// canonical tag-line) — not get pushed below `---` into the body zone.
type AtomicParts struct {
	H1Line        string
	PreambleLines []string
	ExtraTags     []string
	Section       string
	BodyLines     []string
}

// Domain encapsulates everything that varies between library/* tag domains.
// Adding a new domain = one new Domain literal + register in main.go.
type Domain struct {
	// Identity
	Tag          string // "library/poetry"      bearcli --tag value
	CanonicalTag string // "#" + Tag             header-line first token
	IndexTitle   string // "✱ Поезія" / "03-1-афоризми"

	// Bucketing
	OwnGroup      string              // "" disables; "Моя поезія" for poetry
	OwnAliases    map[string]struct{} // legacy markers folding into OwnGroup
	UnknownBucket string              // fallback: "Невідомі" / "Книги"

	// Tier-2 Hub semantics
	HubH2Prefix string   // "Поеми"; "" disables Tier-2 entirely
	HubH2Legacy []string // ["Поезії"] for one-off transition

	// Behavior toggles
	// poetry=true (firstNonSectionH2 fallback); aphorisms=false (quote H2s would be misread).
	LegacyAuthorFallback bool
	// poetry=true (parseAtomicContent strips "## <author>"); aphorisms=false (preserve content H2s).
	StripLegacyAuthorH2 bool

	// Pluggable callbacks (REQUIRED unless explicitly marked optional)
	ParseMeta func(d *Domain, body string) AtomicMeta    // REQUIRED
	SkipNote  func(d *Domain, n Note) bool               // optional; default skips master + hub notes
	RenderHub func(d *Domain, name string, notes []Note, // nil => no Tier-2 phase
		order map[string][]string) string
	RenderMaster func(d *Domain, groups map[string][]Note) string     // REQUIRED
	BacklinkFor  func(d *Domain, bucket string) string                // optional; default: "[[" + bucket + "]]"
	SectionFor   func(d *Domain, bucket string, p AtomicParts) string // optional; default: p.Section

	// Duplicates is set by the orchestrator before each regen cycle and
	// holds the registry of atomic titles shared by ≥ 2 notes across all
	// managed domains. Renderers consult it via bear.AtomicWikilink to
	// switch ambiguous titles to bear://x-callback URL form. Nil-safe —
	// renderers handle the zero registry as "no duplicates known".
	Duplicates *DuplicateRegistry

	// ParseMasterTable, when set, makes the master a two-way contract: the
	// daemon reads the existing master before each regen and pulls a
	// title→bucket map out of its rendered table. Whenever an atomic's
	// canonical-header bucket disagrees with where it currently sits in the
	// master, the master wins — the daemon rewrites the atomic's canonical
	// header to match. Lets users move atomics between buckets by cut/paste
	// in the master itself, without ever opening the atomic note. Only
	// meaningful for flat-table domains (prose, aphorisms, lyrics-style);
	// nil for hub-of-hubs domains (poetry, articles).
	ParseMasterTable func(d *Domain, masterContent string) map[string]string

	// CanonicalTagFor, when set, lets a domain emit a per-atomic canonical
	// tag-line that varies by bucket — e.g. sub-tag preserving shapes where
	// `#<top>/<bucket>` replaces the static `#<top>`. Returning d.CanonicalTag
	// (or any value not starting with `#<top>`) is acceptable for the default
	// case. When nil, renderAtomicCanonical falls back to d.CanonicalTag for
	// every atomic — the existing flat-table / hub-routed shape.
	CanonicalTagFor func(d *Domain, bucket string) string

	// IsHubNote, when set, replaces the default H2-prefix-based hub
	// detection. Sub-tag preserving hub-routed domains key off the title
	// pattern `<top> · <bucket>` instead of an H2 marker. Receives the whole
	// Note so callback can match by title, content, or both.
	IsHubNote func(d *Domain, n Note) bool

	// HubTitleFor maps a bucket name to its Tier-2 hub note title. Defaults
	// to identity (bucket == title — the legacy hub-routed shape used by
	// llm/agents). Sub-tag preserving domains return `<top> · <bucket>` so
	// hub titles don't collide with arbitrary user notes that happen to
	// share a bucket name.
	HubTitleFor func(d *Domain, bucket string) string

	// BucketFromHubTitle inverts HubTitleFor — given a hub note title,
	// returns the bucket it represents. Returning "" signals "this title is
	// not one of our hubs" (caller skips). Default: identity. Used by
	// computeHubOverrides to decide which bucket a hub-bullet claims for
	// an atomic.
	BucketFromHubTitle func(d *Domain, title string) string

	// SkipAtomicsPass tells RunRegen to bypass the atomic-canonicalization
	// step. Used by umbrella domains (`#it`, `#library`, `#llm`) whose
	// "atoms" are actually atomics owned by sibling domains — running the
	// canonical-rewrite would clobber their per-domain canonical headers.
	// The grouping + master-render steps still run, so the umbrella master
	// can read live atom counts per child via its RenderMaster.
	SkipAtomicsPass bool

	// ParentMaster, when set, makes the domain's master emit its tag-line as
	// `#<tag> | [[<ParentMaster>]]` instead of bare `#<tag>` — giving the
	// reader a one-click jump up to the umbrella that aggregates this and
	// its sibling sub-domain masters. Auto-wired by NewUmbrellaDomain on
	// each child. Empty for top-level domains (umbrellas themselves and
	// personal/*).
	ParentMaster string

	// DefaultChild names the leaf-domain tag that the umbrella master's
	// "Нова нотатка" link targets. Spec component 4: without this field,
	// umbrella clicks land on the umbrella's bare tag (e.g. `#library`),
	// Bear creates a note with only that top-level tag, daemon's
	// SkipAtomicsPass=true means the note is never canonicalized, and
	// the new note becomes a permanent orphan. Required for any domain
	// with SkipAtomicsPass=true (every current umbrella); Validate
	// rejects an empty value or one that doesn't match a registered
	// child's Tag, OR a value pointing at a nested umbrella.
	//
	// Wired by NewUmbrellaDomain (factory) + bear/config dispatch from
	// the TOML `default_child` key. Leaf domains leave it empty.
	DefaultChild string

	// defaultChildDomain is the leaf *Domain pointer resolved by
	// NewUmbrellaDomain at factory time from DefaultChild + children.
	// newNoteLinkForDomain delegates to this leaf so the umbrella's
	// "Нова нотатка" click produces a leaf-tagged note with leaf-correct
	// canonical body (CanonicalTag, backlinkFor(UnknownBucket), IndexTitle)
	// instead of the umbrella's internal "_umbrella" placeholder. Nil for
	// leaf domains; non-nil only on umbrellas after successful construction.
	defaultChildDomain *Domain

	// QuickPlaceholderH1 is the literal H1 string ("Quicknote", "Article",
	// etc.) the master's new-note x-callback URL embeds as a marker in
	// the bootstrap-URL text= payload. The daemon's fast-pass
	// (ApplyPlaceholderRefresh) spots this marker post-click and swaps
	// it for a fresh timestamp.
	//
	// Empty string falls back to DefaultQuickPlaceholderH1 via
	// effectiveQuickPlaceholderH1 — every domain still emits the
	// bootstrap URL form; the override only customizes the visible
	// placeholder text.
	QuickPlaceholderH1 string

	// MasterSections, when non-empty, replaces the default 3-tier
	// hub-routed master with a vertical-sections layout. Sections
	// render top-to-bottom in declaration order; each section picks
	// the buckets it lists via an explicit bucket list, a script-class
	// predicate, or a catch-all (no predicate). Empty → domain keeps
	// the default master.
	MasterSections []MasterSection

	// validationError carries a factory-level error so Validate can
	// surface it without the test-helper having to crash on misconfig.
	// Set by NewUmbrellaDomainForTest. Production callers use
	// NewUmbrellaDomain which panics on the same error class.
	validationError error
}

// MasterSection is one entry of a hub-routed domain's vertical-sections
// master layout. Title surfaces in the section header; the predicate
// (Buckets / Script / catch-all) decides which Tier-2 buckets fold
// into this section. Mirrors the TOML `[[domain.master_section]]`
// block in bear/config/schema.go.
//
// Selection rules — set at most one:
//
//   - Buckets non-nil → explicit bucket-name match.
//   - Script != "" → first-letter script class match
//     ("latin" / "non-latin").
//   - Both empty → catch-all (claims every still-unclaimed bucket).
//
// CountMode selects what `(N)` in the section header reports:
//
//   - CountModeNotes (default): sum of note counts across the
//     section's buckets.
//   - CountModeBuckets: number of distinct buckets in the section.
//
// ShowBulletCounts toggles the `(count)` suffix on each bullet.
type MasterSection struct {
	Title            string
	Buckets          []string
	Script           string
	CountMode        CountMode
	ShowBulletCounts bool
}

// CountMode picks the counting strategy for MasterSection headers.
// Zero value is CountModeNotes, matching the most common renderer
// expectation (sum of atomics under the section).
type CountMode int

const (
	// CountModeNotes counts the total notes across every bucket the
	// section claims — answers "how many atomics live here?".
	CountModeNotes CountMode = iota
	// CountModeBuckets counts the distinct buckets the section claims
	// — answers "how many Tier-2 hubs does this section list?".
	CountModeBuckets
)

// tagSuffix returns the part of d.Tag after the last "/", e.g. "poetry" for
// "library/poetry". Used to label log lines and pilot env-vars per domain.
func (d *Domain) tagSuffix() string {
	suffix := d.Tag
	if slash := strings.LastIndex(suffix, "/"); slash >= 0 {
		suffix = suffix[slash+1:]
	}
	return suffix
}

// LogPrefix returns "regen[<tag-suffix>]" for log lines, e.g. "regen[poetry]".
// Disambiguates concurrent multi-domain regens in the daemon log stream.
func (d *Domain) LogPrefix() string {
	return "regen[" + d.tagSuffix() + "]"
}

// Logf writes a log line prefixed with the domain's LogPrefix. Centralizes the
// prefix concern so individual call sites can't accidentally drop it.
func (d *Domain) Logf(format string, args ...any) {
	log.Printf(d.LogPrefix()+": "+format, args...)
}

// Validate returns a non-nil error if the Domain is missing required fields.
// Called by the daemon at startup so misconfiguration surfaces immediately
// instead of as a deferred nil-pointer-dereference deep in the first regen.
func (d *Domain) Validate() error {
	if d.validationError != nil {
		return d.validationError
	}
	if d.Tag == "" {
		return errors.New("Domain.Tag is required")
	}
	if d.CanonicalTag == "" {
		return fmt.Errorf("domain %q: CanonicalTag is required", d.Tag)
	}
	if d.IndexTitle == "" {
		return fmt.Errorf("domain %q: IndexTitle is required", d.Tag)
	}
	if d.ParseMeta == nil {
		return fmt.Errorf("domain %q: ParseMeta callback is required", d.Tag)
	}
	if d.RenderMaster == nil {
		return fmt.Errorf("domain %q: RenderMaster callback is required", d.Tag)
	}
	if d.SkipAtomicsPass && d.DefaultChild == "" {
		return fmt.Errorf("domain %q: DefaultChild is required for umbrella (SkipAtomicsPass=true) domains", d.Tag)
	}
	return nil
}

// newNoteRawTag returns the rawTag value to embed in the new-note
// bootstrap URL's inner `tags=` parameter. For umbrella domains
// (SkipAtomicsPass=true) it returns DefaultChild so clicks on the
// umbrella master land in a leaf-domain-tagged note that the leaf's
// regen pipeline can canonicalize. For leaf domains it returns Tag —
// the existing behavior. Centralizes the choice so newNoteBootstrapLink
// doesn't branch on SkipAtomicsPass.
func (d *Domain) newNoteRawTag() string {
	if d.SkipAtomicsPass && d.DefaultChild != "" {
		return d.DefaultChild
	}
	return d.Tag
}

// ResolveURLDomain returns the leaf domain whose configuration drives
// URL emission. Umbrella domains (SkipAtomicsPass=true) recurse through
// their resolved DefaultChild so the embedded body in bootstrap URLs
// reflects the leaf's tag, backlink, and placeholder H1 — not the
// umbrella's internal "_umbrella" placeholder. Leaves return self.
func (d *Domain) ResolveURLDomain() *Domain {
	if d.defaultChildDomain != nil {
		return d.defaultChildDomain.ResolveURLDomain()
	}
	return d
}

// effectiveQuickPlaceholderH1 returns d.QuickPlaceholderH1 when set,
// otherwise the package default DefaultQuickPlaceholderH1. Centralizing
// the fallback here keeps empty-string-means-default semantics out of
// every caller. Co-located with newNoteRawTag / ResolveURLDomain because
// it's a *Domain config accessor, not an H1 emission primitive.
func (d *Domain) effectiveQuickPlaceholderH1() string {
	if d.QuickPlaceholderH1 == "" {
		return DefaultQuickPlaceholderH1
	}
	return d.QuickPlaceholderH1
}

// EffectiveQuickPlaceholderH1ForTest exposes effectiveQuickPlaceholderH1
// to tests/bear. Production code calls the unexported method directly.
func (d *Domain) EffectiveQuickPlaceholderH1ForTest() string {
	return d.effectiveQuickPlaceholderH1()
}

// hubTitleFor maps bucket → Tier-2 hub note title. Defaults to identity
// (bucket == title) for legacy hub-routed domains. Sub-tag preserving hubs
// override to return `<top> · <bucket>`.
func (d *Domain) hubTitleFor(bucket string) string {
	cb := d.HubTitleFor
	if cb == nil {
		return bucket
	}
	return cb(d, bucket)
}

// bucketFromHubTitle inverts hubTitleFor. Returns "" when the title doesn't
// belong to this domain (computeHubOverrides treats "" as "skip"). The
// callback path lets sub-tag preserving domains strip a `<top> · ` prefix
// before matching against bucket-keyed groups.
func (d *Domain) bucketFromHubTitle(title string) string {
	if cb := d.BucketFromHubTitle; cb != nil {
		return cb(d, title)
	}
	return title
}

// canonicalTagFor resolves the per-atomic canonical tag-line. Defaults to
// d.CanonicalTag when no callback is wired (existing flat-table / hub-routed
// behavior). Domains that preserve sub-tags (grouped-vertical,
// hub-routed-with-subtag) wire CanonicalTagFor to return `#<top>/<bucket>`
// so each atomic carries its sub-tag in the tag-line.
func (d *Domain) canonicalTagFor(bucket string) string {
	if d.CanonicalTagFor != nil {
		return d.CanonicalTagFor(d, bucket)
	}
	return d.CanonicalTag
}

// backlinkFor returns the canonical-header backlink target for a given bucket.
// Defaults to "[[bucket]]" (poetry: per-author hub link). Domains with no
// per-author hubs (aphorisms) override to always link to the master.
func (d *Domain) backlinkFor(bucket string) string {
	if d.BacklinkFor != nil {
		return d.BacklinkFor(d, bucket)
	}
	return "[[" + bucket + "]]"
}

// sectionFor returns the section path the canonicalized atomic should carry.
// Defaults to whatever ParseMeta extracted (poetry's sub-genre path). Aphorisms
// overrides because the section IS the bucket (category).
func (d *Domain) sectionFor(bucket string, p AtomicParts) string {
	if d.SectionFor != nil {
		return d.SectionFor(d, bucket, p)
	}
	return p.Section
}

// skipNote returns true for notes that should not be grouped as atomics
// (master, Tier-2 hubs, legacy [Index]/✱-prefixed system notes).
func (d *Domain) skipNote(n Note) bool {
	if d.SkipNote != nil {
		return d.SkipNote(d, n)
	}
	if n.Title == d.IndexTitle {
		return true
	}
	if strings.HasPrefix(n.Title, "[Index]") || strings.HasPrefix(n.Title, "✱ ") {
		return true
	}
	if d.isHubNote(n) {
		return true
	}
	return false
}

// bearcliKindFromArgs classifies bearcli args by their sub-command (the
// first element) for the per-kind metrics counter. bearcliKindFromArgs
// and runBearcli now live in bear/bearcli; the in-package runBearcli
// shim below delegates to bearcli.Run so existing call sites compile
// unchanged.
func runBearcli(ctx context.Context, args []string, stdin string) ([]byte, error) {
	return bearcli.Run(ctx, args, stdin)
}

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
