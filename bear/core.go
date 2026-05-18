package bear

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/barad1tos/noxctl/bear/overwriteoutcome"
)

// recordOutcome wires overwriteWithRetry's three counter-relevant exit
// paths to overwriteoutcome.Record. The sub-package keeps the metric
// arithmetic testable from tests/bear/overwriteoutcome/ without exposing
// bearcliMetrics or its fields in the bear public API.
func recordOutcome(outcome overwriteoutcome.Outcome) {
	overwriteoutcome.Record(
		outcome,
		&bearcliMetrics.hashConflicts,
		&bearcliMetrics.retriesOK,
		&bearcliMetrics.retriesFail,
	)
}

// overwriteWithRetry performs `bearcli overwrite --base <hash>` and retries
// once if bearcli rejects with ErrHashConflict (the note changed between our
// hash read and the write). The retry re-fetches the current hash before the
// second attempt — the only sensible recovery short of a full new regen.
//
// Increments the bearcli pool's hash-conflict counters via recordOutcome →
// overwriteoutcome.Record so the audit reporter can
// surface conflict rate and retry success ratio per regen cycle.
func overwriteWithRetry(ctx context.Context, noteID, body string) error {
	hash, err := showHash(ctx, noteID)
	if err != nil {
		return err
	}
	_, err = runBearcli(ctx, []string{"overwrite", noteID, flagBase, hash}, body)
	if err == nil {
		recordOutcome(overwriteoutcome.NoConflict)
		return nil
	}
	if !errors.Is(err, ErrHashConflict) {
		return err
	}
	hash, err = showHash(ctx, noteID)
	if err != nil {
		recordOutcome(overwriteoutcome.RetryFail)
		return fmt.Errorf("retry-after-conflict: %w", err)
	}
	if _, err = runBearcli(ctx, []string{"overwrite", noteID, flagBase, hash}, body); err != nil {
		recordOutcome(overwriteoutcome.RetryFail)
		return fmt.Errorf("retry-after-conflict: %w", err)
	}
	recordOutcome(overwriteoutcome.RetrySucceed)
	return nil
}

// listNotes returns every note tagged d.Tag with id, title, content, and
// created fields. The created timestamp drives time-based promotion logic
// (see specs/2026-05-07-quicknote-time-promotion-design.md): mod-time is
// bumped on every canonicalization pass so creation date is the only
// stable age signal.
func (d *Domain) listNotes(ctx context.Context) ([]Note, error) {
	out, err := runBearcli(ctx, []string{"list", "--tag", d.Tag, flagFormat, formatJSON, flagFields, "id,title,content,tags,created"}, "")
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

// showHash returns the current optimistic-concurrency hash for the note. An
// empty string with no error from bearcli would silently disable concurrency
// guards — guard against that by treating empty hash as a fault.
func showHash(ctx context.Context, noteID string) (string, error) {
	out, err := runBearcli(ctx, []string{"show", noteID, flagFormat, formatJSON, flagFields, "hash"}, "")
	if err != nil {
		return "", fmt.Errorf("showHash(%s): %w", noteID, err)
	}
	var hashOnly Note
	if err = json.Unmarshal(out, &hashOnly); err != nil {
		return "", fmt.Errorf("showHash(%s): parse: %w", noteID, err)
	}
	if hashOnly.Hash == "" {
		return "", fmt.Errorf("showHash(%s): bearcli returned empty hash", noteID)
	}
	return hashOnly.Hash, nil
}

// firstWikilinkAuthor scans the header zone for any wikilink target that isn't
// empty or the master index title. Maps OwnAliases to OwnGroup. Returns "" if
// nothing useful found. Used as second-tier fallback by detectAuthor.
func (d *Domain) firstWikilinkAuthor(header string) string {
	rest := header
	for {
		openIdx := strings.Index(rest, "[[")
		if openIdx < 0 {
			return ""
		}
		rest = rest[openIdx+2:]
		closeIdx := strings.Index(rest, "]]")
		if closeIdx < 0 {
			return ""
		}
		target := rest[:closeIdx]
		rest = rest[closeIdx+2:]
		if pipe := strings.Index(target, "|"); pipe >= 0 {
			target = target[:pipe]
		}
		target = strings.TrimSpace(target)
		if target == "" || target == d.IndexTitle {
			continue
		}
		if _, ok := d.OwnAliases[target]; ok {
			return d.OwnGroup
		}
		return target
	}
}

// isHubNote returns true if the note looks like an auto-regenerated Tier-2 hub.
// When d.IsHubNote callback is wired, it wins; otherwise falls back to the H2
// heuristic (first non-section H2 starts with HubH2Prefix or any HubH2Legacy
// prefix). Always false for domains without Tier-2 hubs (no callback and no
// HubH2Prefix). Takes the whole Note so title-based detectors (sub-tag-
// preserving hub-routed domains keying off `<top> · <bucket>`) work.
func (d *Domain) isHubNote(n Note) bool {
	if d.IsHubNote != nil {
		return d.IsHubNote(d, n)
	}
	if d.HubH2Prefix == "" {
		return false
	}
	h2 := firstNonSectionH2(n.Content)
	if strings.HasPrefix(h2, d.HubH2Prefix) {
		return true
	}
	for _, legacy := range d.HubH2Legacy {
		if strings.HasPrefix(h2, legacy) {
			return true
		}
	}
	return false
}

// detectAuthor returns the bucket key for an atomic note. Source-of-truth
// priority:
// 1. Domain.ParseMeta — canonical header line (preferred).
// 2. firstWikilinkAuthor in header zone — covers legacy non-canonical
// cases for hub-routed domains. Skipped for sub-tag-preserving
// blueprints (CanonicalTagFor != nil): those derive bucket from the
// Bear tag array via BucketFromSubTag in groupAtomics, never from
// free-form wikilinks. Scanning HeaderZone there would mis-identify
// body-content wikilinks (an orphan `| [[✱ Daily]] |...` line
// leftover from a foreign-tag escape, a user-typed `[[reference]]`
// in an atom body) as bucket names — see the May 2026 quicknote→
// development drag regression.
// 3. Legacy fallback: first non-section ## H2 in body — guarded by
// LegacyAuthorFallback (poetry only; aphorisms quote H2s would misread).
func (d *Domain) detectAuthor(body string) string {
	if meta := d.ParseMeta(d, body); meta.Bucket != "" {
		return meta.Bucket
	}
	if d.CanonicalTagFor == nil {
		if author := d.firstWikilinkAuthor(HeaderZone(body)); author != "" {
			return author
		}
	}
	if !d.LegacyAuthorFallback {
		return ""
	}
	heading := firstNonSectionH2(body)
	if _, isOwnAlias := d.OwnAliases[heading]; isOwnAlias {
		return d.OwnGroup
	}
	return heading
}

// groupAtomics partitions atomic notes by bucket key. Hub notes and the master
// are skipped via Domain.skipNote. Notes without a detectable bucket fall into
// Domain.UnknownBucket. The override map (keyed by note ID) wins over the
// canonical-header bucket — this is how cut/paste moves in the master propagate
// into atomic re-bucketing on the next regen.
func (d *Domain) groupAtomics(notes []Note, overrides map[string]string) map[string][]Note {
	groups := make(map[string][]Note)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		bucket, hasOverride := overrides[note.ID]
		if !hasOverride {
			bucket = d.detectAuthor(note.Content)
			if bucket == "" {
				bucket = BucketFromSubTag(d, note.Tags)
			}
			if bucket == "" {
				bucket = d.UnknownBucket
			}
		}
		groups[bucket] = append(groups[bucket], note)
	}
	return groups
}

// computeMasterOverrides reads the current master, parses its table via
// Domain.ParseMasterTable, and returns a noteID→bucket map for atomics whose
// table position disagrees with their canonical header. Empty map (nil-safe)
// when ParseMasterTable is unset, the master is missing, or the user hasn't
// moved anything since the last regen.
//
// Master is the source of truth for flat-table domains: a user who cuts a
// bullet from one column and pastes it into another expects that bullet's
// atomic to follow. The next regen sees the disagreement here and rewrites the
// atomic's canonical header on its way through runAtomicsPass.
// parseMasterTableForNotes locates the master in the supplied note slice
// and returns its parsed identifier→bucket map. Empty map when the master
// is missing or has no rows the parser recognizes.
func (d *Domain) parseMasterTableForNotes(notes []Note) map[string]string {
	for _, note := range notes {
		if note.Title == d.IndexTitle {
			return d.ParseMasterTable(d, note.Content)
		}
	}
	return nil
}

// overrideForNote checks whether `titleToBucket` (parsed master) places the
// note in a different bucket than its canonical header detects. Returns the
// new bucket and `true` when an override is needed; (`""`, `false`) when the
// note isn't tracked by the master or already matches its canonical bucket.
//
// Lookup is title-first then ID-fallback: plain `[[Title]]` wikilinks key by
// title, while bear://x-callback URLs (used for duplicate titles) key by
// note ID. IDs win when both forms point at the same atomic.
func (d *Domain) overrideForNote(note Note, titleToBucket map[string]string) (string, bool) {
	masterBucket, inMaster := titleToBucket[note.Title]
	if idBucket, hasID := titleToBucket[note.ID]; hasID {
		masterBucket, inMaster = idBucket, true
	}
	if !inMaster {
		return "", false
	}
	canonicalBucket := d.detectAuthor(note.Content)
	if canonicalBucket == "" {
		canonicalBucket = d.UnknownBucket
	}
	if canonicalBucket == masterBucket {
		return "", false
	}
	return masterBucket, true
}

func (d *Domain) computeMasterOverrides(notes []Note) map[string]string {
	if d.ParseMasterTable == nil {
		return nil
	}
	titleToBucket := d.parseMasterTableForNotes(notes)
	if len(titleToBucket) == 0 {
		return nil
	}
	overrides := make(map[string]string)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		if override, ok := d.overrideForNote(note, titleToBucket); ok {
			overrides[note.ID] = override
		}
	}
	return overrides
}

// computeHubOverrides walks every Tier-2 Hub note in `notes` and treats each
// hub's bullet list as the authoritative membership claim: any atomic
// referenced from inside a hub is asserted to belong to that hub's bucket
// (== hub note title). When the atomic's canonical-header bucket disagrees,
// an override fires and runAtomicsPass rewrites the canonical header on
// the same regen cycle.
//
// Mirrors computeMasterOverrides for 3-tier domains: cut/paste a `[[Title]]`
// bullet from one Hub note into another (or drop a freshly tagged atomic
// into the right Hub) and the daemon catches up without the user editing
// the canonical header by hand. No-op for domains without Tier-2 hubs
// (RenderHub == nil) — flat-table and flat-list domains handle bidirectional
// flow exclusively through ParseMasterTable.
//
// Identifier resolution mirrors computeMasterOverrides: try note ID first
// (bear://x-callback URL form, used for duplicate titles), then title.
// Title collisions inside a single hub fall through silently — the user
// has already been steered to the URL form by AtomicWikilink.
func (d *Domain) computeHubOverrides(notes []Note) map[string]string {
	if d.RenderHub == nil {
		return nil
	}
	atomByID, atomByTitle := d.buildAtomLookups(notes)
	overrides := make(map[string]string)
	for _, hub := range notes {
		if !d.isHubNote(hub) {
			continue
		}
		d.collectHubOverrides(hub, atomByID, atomByTitle, overrides)
	}
	return overrides
}

// buildAtomLookups indexes every non-skipped atomic by ID and by title.
// Title index is multi-valued because duplicate titles are allowed (and
// disambiguated through bear://x-callback URLs at render time).
func (d *Domain) buildAtomLookups(notes []Note) (map[string]Note, map[string][]Note) {
	byID := make(map[string]Note, len(notes))
	byTitle := make(map[string][]Note)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		byID[note.ID] = note
		byTitle[note.Title] = append(byTitle[note.Title], note)
	}
	return byID, byTitle
}

// collectHubOverrides walks one hub's bullet identifiers and records any
// atomic whose canonical bucket disagrees with the hub bucket. Mutates
// `overrides` in place — caller controls the lifetime.
//
// Conflict resolution when several hubs claim the same atomic via stale
// bullets:
//
// - Canonical bucket matches THIS hub → canonical wins, drop any prior
// override (the atomic is already where it should be).
// - Override already set by an earlier hub → leave it alone (first
// non-canonical claimant wins; subsequent stale claims are ignored).
// - Otherwise → record this hub as the override target.
//
// The first rule matters most: without it, two hubs both claiming the
// same atomic ping-pong its canonical bucket between cycles, depending on
// note iteration order. Anchoring on the canonical bucket breaks the
// loop and lets stale-bullet hubs self-heal next regen.
func (d *Domain) collectHubOverrides(
	hub Note,
	atomByID map[string]Note,
	atomByTitle map[string][]Note,
	overrides map[string]string,
) {
	hubBucket := d.bucketFromHubTitle(hub.Title)
	if hubBucket == "" {
		return
	}
	for _, ident := range parseHubBulletIdentifiers(hub.Content) {
		atom, ok := resolveAtom(ident, atomByID, atomByTitle)
		if !ok {
			continue
		}
		canonicalBucket := d.detectAuthor(atom.Content)
		if canonicalBucket == "" {
			canonicalBucket = d.UnknownBucket
		}
		if canonicalBucket == hubBucket {
			delete(overrides, atom.ID)
			continue
		}
		if _, alreadyOverridden := overrides[atom.ID]; alreadyOverridden {
			continue
		}
		overrides[atom.ID] = hubBucket
	}
}

// resolveAtom maps a bullet-identifier (note ID from a bear://x-callback
// URL, or a title from `[[wikilink]]`) to the corresponding atomic. ID
// lookup wins; title lookup succeeds only when exactly one atomic claims
// that title — ambiguous titles are skipped because AtomicWikilink already
// routes duplicates through their unique IDs.
func resolveAtom(
	ident string,
	atomByID map[string]Note,
	atomByTitle map[string][]Note,
) (Note, bool) {
	if atom, ok := atomByID[ident]; ok {
		return atom, true
	}
	candidates := atomByTitle[ident]
	if len(candidates) != 1 {
		return Note{}, false
	}
	return candidates[0], true
}

// parseHubBulletIdentifiers extracts every atomic identifier from a Hub
// note's bullet list — wikilink targets (`[[Title]]` → "Title") and note
// IDs embedded in `bear://x-callback-url/open-note?id=X` markdown links.
// Lines that don't start with `- ` are ignored, so H2/H3 section headers,
// blank lines, and curator-zone prose pass through silently.
func parseHubBulletIdentifiers(content string) []string {
	var out []string
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- ") {
			continue
		}
		out = append(out, extractCellWikilinks(trimmed)...)
		out = append(out, extractCellNoteIDs(trimmed)...)
	}
	return out
}

// collectExtraTags pulls non-canonical tags from a header line — anything
// besides the daemon-managed family (#<family> and #<family>/*). The family
// is the top-level segment of d.Tag (e.g. "library" for "library/poetry",
// "llm" for "llm/agents"); we never re-emit these as extras because the
// canonicaliser writes a single authoritative tag-line. Without this filter
// each regen pass would accumulate the canonical tag in ExtraTags and
// produce header lines like `#llm/agents #llm/agents #llm/agents |...`.
//
// Defensive against cross-tag transitions: a stray `#library/poetry` left
// in an `llm/agents` note is preserved here as an extra so the user can
// notice and clean it up.
func collectExtraTags(line, family string) []string {
	var out []string
	for token := range strings.FieldsSeq(line) {
		if !strings.HasPrefix(token, "#") {
			continue
		}
		bare := strings.TrimPrefix(token, "#")
		if bare == family || strings.HasPrefix(bare, family+"/") {
			continue
		}
		out = append(out, token)
	}
	return out
}

// atomicParseState carries small mutable state across parseAtomicContent's loop.
type atomicParseState struct {
	seenH1       bool
	seenTagLine  bool // flipped once consumeHeader claims a header-shape line or `---`
	skipAuthorH2 bool
	stripAuthor  bool // domain toggle: when false, never treat "## <author>" as legacy marker
}

// consumeH1 captures the first H1 into p.h1Line. Returns true if consumed.
func (s *atomicParseState) consumeH1(trimmed string, p *AtomicParts) bool {
	if s.seenH1 || !strings.HasPrefix(trimmed, "# ") || strings.HasPrefix(trimmed, "## ") {
		return false
	}
	p.H1Line = trimmed
	s.seenH1 = true
	return true
}

// consumeHeader consumes anything that lives in the header zone: separator,
// header-shape lines, optionally the legacy `## Author` H2 plus its trailing
// blanks (only when stripAuthor=true). `family` scopes which tags count as
// canonical (and therefore get filtered out of ExtraTags) — see
// collectExtraTags doc.
func (s *atomicParseState) consumeHeader(trimmed, authorH2Marker, family string, p *AtomicParts) bool {
	if trimmed == "---" {
		s.seenTagLine = true
		return true
	}
	if isHeaderLine(trimmed) {
		if section := extractSectionFromHeaderLine(trimmed); section != "" {
			p.Section = section
		}
		p.ExtraTags = append(p.ExtraTags, collectExtraTags(trimmed, family)...)
		s.seenTagLine = true
		return true
	}
	if s.stripAuthor && trimmed == authorH2Marker {
		s.skipAuthorH2 = true
		return true
	}
	if s.skipAuthorH2 && trimmed == "" {
		return true
	}
	s.skipAuthorH2 = false
	return false
}

// consumePreamble captures non-tag-line content that lives between the
// H1 and the first canonical header line. Lines here are preserved
// in-place at render time (between H1 and tag-line) per spec
// component 5. Dispatch order is H1 → header → preamble → leading-blank
// → body; preamble runs only AFTER consumeHeader has had its chance to
// claim header-shape lines.
//
// Rejects lines that LOOK like canonical-line debris — `#<token> |...`
// shapes that failed isHeaderLine (e.g. `#development/✱ Daily |...`
// where the bucket name carried a space and broke segment[0]'s
// tag-only check). Claiming such lines as preamble would re-emit them
// on every regen tick, growing the body without bound — see the May
// 2026 accumulation cascade triggered by a foreign-tag escape that
// left `[[✱ Daily]]` debris in the body, which detectAuthor then
// mis-identified as a bucket name, which the renderer emitted as
// `#development/✱ Daily |...`, which preamble then preserved...
// Sending them to BodyLines instead at least keeps the user's
// historical content visible below `---` for manual cleanup.
func (s *atomicParseState) consumePreamble(trimmed string, p *AtomicParts) bool {
	if !s.seenH1 || s.seenTagLine {
		return false
	}
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "#") && strings.Contains(trimmed, " | ") {
		return false
	}
	p.PreambleLines = append(p.PreambleLines, trimmed)
	return true
}

// consumeLeadingBlank drops blank lines preceding the first body line.
func (s *atomicParseState) consumeLeadingBlank(trimmed string, p *AtomicParts) bool {
	return len(p.BodyLines) == 0 && trimmed == ""
}

// parseAtomicContent destructures an atomic note's content into header H1,
// preserved extra tags, optional section path, and body lines.
//
// Each line dispatches to one of three consumers via switch — explicit short-
// circuit semantics. consume* methods mutate `s` and `p` as side effects; the
// switch documents that order matters (H1 before header before blank-skip).
func (d *Domain) parseAtomicContent(content, author string) AtomicParts {
	authorH2Marker := "## " + author
	family := strings.SplitN(d.Tag, "/", 2)[0]
	var parts AtomicParts
	state := atomicParseState{stripAuthor: d.StripLegacyAuthorH2}
	for line := range strings.SplitSeq(content, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case state.consumeH1(trimmed, &parts):
		case state.consumeHeader(trimmed, authorH2Marker, family, &parts):
		case state.consumePreamble(trimmed, &parts):
		case state.consumeLeadingBlank(trimmed, &parts):
		default:
			parts.BodyLines = append(parts.BodyLines, line)
		}
	}
	return parts
}

// renderAtomicCanonical produces the canonical atomic body shape:
//
//	# Title
//	<preamble lines, if any>
//	#<tag> [extra tags] | [[Backlink]] [| section]
//	---
//
//	<text>
//
// The leading `#<tag>` token comes from d.canonicalTagFor(bucket) so domains
// that preserve sub-tags can emit `#<top>/<bucket>` per atomic without
// touching the rest of the canonicalization flow. `preamble` lines (non-
// tag-line content captured between H1 and the canonical tag-line) are
// emitted in place above the tag-line per spec component 5.
func (d *Domain) renderAtomicCanonical(
	h1Line string,
	preamble,
	extraTags []string,
	bucket,
	backlink,
	section,
	contentBody string,
) string {
	canonicalTag := d.canonicalTagFor(bucket)
	tagLine := canonicalTag
	if len(extraTags) > 0 {
		tagLine = canonicalTag + " " + strings.Join(extraTags, " ")
	}
	suffix := ""
	if section != "" {
		suffix = " | " + section
	}
	suffix += NewNoteURLFromDomain(d).Emit()
	var b strings.Builder
	b.WriteString(h1Line + "\n")
	for _, line := range preamble {
		b.WriteString(line + "\n")
	}
	_, _ = fmt.Fprintf(&b, "%s | %s%s\n---\n\n", tagLine, backlink, suffix)
	if contentBody != "" {
		b.WriteString(contentBody + "\n")
	}
	return b.String()
}

// upsertAtomicBacklink restructures one atomic note into canonical header form.
// Idempotent: returns ("", nil) when content already matches the canonical
// render. On a successful rewrite returns a human-readable summary; on failure
// returns ("", err) so the caller can aggregate failure counts.
//
// H1 handling (spec components 2 + 8): when the atom's body lacks a
// recognized H1 (or carries an empty `# ` H1), the daemon stamps
// `# <NOW>` from the package-level time seam. Bear then derives the
// displayed title from the H1. The legacy noteTitle fallback is gone —
// every canonicalized atom carries a datetime H1, eliminating the
// `# #tag` recursive-corruption class.
//
// Idempotency comparison strips the trailing `[Нова нотатка](bear://...)`
// segment from both sides — its label/URL drift across regen cycles would
// otherwise force a no-op write per tick.
func (d *Domain) upsertAtomicBacklink(
	ctx context.Context,
	noteID,
	noteTitle,
	bucket,
	content string,
) (string, error) {
	parts := d.parseAtomicContent(content, bucket)
	if parts.H1Line == "" || isEmptyH1(parts.H1Line) {
		parts.H1Line = "# " + nowForNewNoteLink().Format(h1DatetimeFormat)
	}
	contentBody := strings.Trim(strings.Join(parts.BodyLines, "\n"), "\n ")
	desired := d.renderAtomicCanonical(
		parts.H1Line, parts.PreambleLines, parts.ExtraTags, bucket,
		d.backlinkFor(bucket), d.sectionFor(bucket, parts), contentBody,
	)

	if equalIgnoringNewNoteLinkStrict(desired, content) {
		return "", nil
	}
	if err := overwriteWithRetry(ctx, noteID, desired); err != nil {
		return "", fmt.Errorf("upsertAtomicBacklink %q: %w", noteTitle, err)
	}
	return fmt.Sprintf("%s → restructured", noteTitle), nil
}

// isEmptyH1 reports whether an H1 line carries no meaningful content
// (e.g. `# ` after trimming whitespace). Empty H1s are not user intent —
// the daemon overwrites them with a datetime stamp per spec component 7.
func isEmptyH1(line string) bool {
	return strings.TrimSpace(strings.TrimPrefix(line, "#")) == ""
}

// RenderAtomicCanonicalForTest exposes the in-memory rendering path of
// upsertAtomicBacklink without the bearcli round-trip. Tests use it to
// assert canonical body shape (H1 stamping, preamble preservation,
// canonical-line composition) deterministically. The noteTitle arg is
// kept in the signature so tests document the historical Bear-side
// title input — the new datetime-stamp path doesn't consult it.
func RenderAtomicCanonicalForTest(t interface{ Helper() }, d *Domain, noteTitle, bucket, content string) string {
	t.Helper()
	_ = noteTitle
	parts := d.parseAtomicContent(content, bucket)
	if parts.H1Line == "" || isEmptyH1(parts.H1Line) {
		parts.H1Line = "# " + nowForNewNoteLink().Format(h1DatetimeFormat)
	}
	contentBody := strings.Trim(strings.Join(parts.BodyLines, "\n"), "\n ")
	return d.renderAtomicCanonical(
		parts.H1Line, parts.PreambleLines, parts.ExtraTags, bucket,
		d.backlinkFor(bucket), d.sectionFor(bucket, parts), contentBody,
	)
}

// RenderCanonicalForBootstrap returns the canonical body form for a note
// that is being tagged for the first time (auto-tag default flow) or
// being escaped from quicknote into a permanent domain (foreign-tag
// escape flow). Reuses parseAtomicContent + renderAtomicCanonical so the
// output is byte-equivalent to what upsertAtomicBacklink would produce
// on the next regen pass — letting the subsequent cycle no-op via
// equalIgnoringNewNoteLink. Bucket selection uses the domain's
// UnknownBucket since fresh or just-escaped atoms carry no
// canonical-header section yet — domains with per-bucket routing
// (poetry, articles, …) re-bucket on the next full regen via ParseMeta
// + cross-domain moves.
//
// Body lines that parseAtomicContent captured as preamble (free-form
// content that the user typed before any canonical tag-line existed)
// are MOVED BELOW the tag-line + `---` separator. This is the key
// difference from the legacy stampDailyTag append-at-end approach,
// which left user-typed body stranded as preamble above the tag-line.
// Legitimate preamble use cases (Bear's auto-inserted TOC line, poetry
// citations) only arise after a regen cycle has already canonicalized
// the atom; bootstrap by definition runs on pre-canonical content, so
// re-classifying preamble as body is safe.
func (d *Domain) RenderCanonicalForBootstrap(existingContent string) string {
	parts := d.parseAtomicContent(existingContent, d.UnknownBucket)
	if parts.H1Line == "" || isEmptyH1(parts.H1Line) {
		parts.H1Line = "# " + nowForNewNoteLink().Format(h1DatetimeFormat)
	}
	if len(parts.PreambleLines) > 0 {
		parts.BodyLines = append(append([]string{}, parts.PreambleLines...), parts.BodyLines...)
		parts.PreambleLines = nil
	}
	contentBody := strings.Trim(strings.Join(parts.BodyLines, "\n"), "\n ")
	return d.renderAtomicCanonical(
		parts.H1Line, parts.PreambleLines, parts.ExtraTags, d.UnknownBucket,
		d.backlinkFor(d.UnknownBucket), d.sectionFor(d.UnknownBucket, parts), contentBody,
	)
}

// equalIgnoringNewNoteLink (non-strict, atomic flavor) reports whether
// two note bodies match after stripping new-note URL decorations and
// trailing whitespace. URL-shape drift is ignored — an atom carrying
// a stale URL stays put until it's touched for another reason. Used
// by upsertAtomicBacklink's atom path (via equalIgnoringNewNoteLinkStrict's
// fallback) and by promoteAtomToDomain's soft-move no-op gate.
//
// Hub/master callers want the strict variant — see
// equalIgnoringNewNoteLinkStrict.
func equalIgnoringNewNoteLink(a, b string) bool {
	stripA := strings.TrimRight(StripNewNoteURLsFromBody(a), " \n")
	stripB := strings.TrimRight(StripNewNoteURLsFromBody(b), " \n")
	return stripA == stripB
}

// equalIgnoringNewNoteLinkStrict (master/hub/cross-domain flavor) is
// equalIgnoringNewNoteLink PLUS a structural URL drift check: bodies
// are compared position-by-position via FindAllNewNoteURLsInBody and
// NewNoteURL.Equals. ANY structural change (Backlink, PlaceholderH1,
// Label, Tag, CanonicalTag, Form, Inner) triggers rewrite — that's
// the the URL-emission SSOT contract that ends the recurring-bug pattern.
//
// The non-strict body compare runs as a fallback so trailing-whitespace
// drift on otherwise-identical bodies doesn't loop-rewrite (Pitfall 2).
func equalIgnoringNewNoteLinkStrict(a, b string) bool {
	urlsA := FindAllNewNoteURLsInBody(a)
	urlsB := FindAllNewNoteURLsInBody(b)
	if len(urlsA) != len(urlsB) {
		return false
	}
	for i := range urlsA {
		if !urlsA[i].Equals(urlsB[i]) {
			return false
		}
	}
	return equalIgnoringNewNoteLink(a, b)
}

// EqualIgnoringNewNoteLinkForTest exposes the non-strict predicate to tests/bear.
func EqualIgnoringNewNoteLinkForTest(a, b string) bool {
	return equalIgnoringNewNoteLink(a, b)
}

// EqualIgnoringNewNoteLinkStrictForTest exposes the strict predicate to tests/bear.
func EqualIgnoringNewNoteLinkStrictForTest(a, b string) bool {
	return equalIgnoringNewNoteLinkStrict(a, b)
}

// GroupNotesBySection partitions a sorted note list by canonical-header section
// path. Notes without a section land under the empty-string key.
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
func (d *Domain) upsertHub(ctx context.Context, bucket string, notes []Note) (string, error) {
	if d.RenderHub == nil {
		return bucket + ": skipped (no Tier-2)", nil
	}
	hubTitle := d.hubTitleFor(bucket)
	hubID, err := d.findHubID(ctx, hubTitle)
	if err != nil {
		return "", fmt.Errorf("upsertHub %q: %w", hubTitle, err)
	}

	if hubID == "" {
		// Fresh hub — no existing order, render alphabetical.
		newAuto := d.RenderHub(d, bucket, notes, nil)
		if _, err = runBearcli(ctx, []string{"create", hubTitle, flagFormat, formatJSON, flagFields, fieldsIDTitle}, newAuto); err != nil {
			return "", fmt.Errorf("upsertHub %q create: %w", hubTitle, err)
		}
		return fmt.Sprintf("%s: created", hubTitle), nil
	}

	out, err := runBearcli(ctx, []string{"cat", hubID, flagFormat, formatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("upsertHub %q cat: %w", hubTitle, err)
	}
	var existing Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", fmt.Errorf("upsertHub %q parse: %w", hubTitle, err)
	}

	autoZone, manual := splitMarker(existing.Content)
	existingOrder := parseHubOrder(autoZone)
	newAuto := d.RenderHub(d, bucket, notes, existingOrder)

	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if equalIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		return fmt.Sprintf("%s: unchanged", hubTitle), nil
	}
	if err = overwriteWithRetry(ctx, hubID, newBody); err != nil {
		return "", fmt.Errorf("upsertHub %q write: %w", hubTitle, err)
	}
	return fmt.Sprintf("%s: updated", hubTitle), nil
}

// upsertMasterIndex creates or updates the domain's master index note.
// Preserves the curator zone (below "## ✱ Куратор") on update. Returns a
// human-readable summary; an err signals the caller to aggregate failures.
func (d *Domain) upsertMasterIndex(ctx context.Context, groups map[string][]Note) (string, error) {
	newAuto := d.RenderMaster(d, groups)
	idxID, err := d.findIndexID(ctx)
	if err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s): %w", d.IndexTitle, err)
	}

	if idxID == "" {
		if _, err = runBearcli(ctx, []string{"create", d.IndexTitle, flagFormat, formatJSON, flagFields, fieldsIDTitle}, newAuto); err != nil {
			return "", fmt.Errorf("upsertMasterIndex(%s) create: %w", d.IndexTitle, err)
		}
		return "index: created", nil
	}

	out, err := runBearcli(ctx, []string{"cat", idxID, flagFormat, formatJSON}, "")
	if err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s) cat: %w", d.IndexTitle, err)
	}
	var existing Note
	if err = json.Unmarshal(out, &existing); err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s) parse: %w", d.IndexTitle, err)
	}

	_, manual := splitMarker(existing.Content)
	var newBody string
	if manual != "" {
		newBody = newAuto + "\n" + manual
	} else {
		newBody = newAuto
	}

	if equalIgnoringNewNoteLinkStrict(newBody, existing.Content) {
		return "index: unchanged", nil
	}
	if err = overwriteWithRetry(ctx, idxID, newBody); err != nil {
		return "", fmt.Errorf("upsertMasterIndex(%s) write: %w", d.IndexTitle, err)
	}
	return "index: updated", nil
}

// atomicsPilotBucket returns the bucket filter for the atomics pass, or "" for
// "process all". Per-domain `REGEN_ATOMICS_PILOT_<TAG>` takes precedence over
// the global `REGEN_ATOMICS_PILOT`.
func (d *Domain) atomicsPilotBucket() string {
	if pilot := os.Getenv("REGEN_ATOMICS_PILOT_" + strings.ToUpper(d.tagSuffix())); pilot != "" {
		return pilot
	}
	return os.Getenv("REGEN_ATOMICS_PILOT")
}

// processAtomic upserts one atomic note's canonical header and logs the
// outcome. Returns 1/0 in (touched, failed) so the caller can sum.
//
// Tag-membership guard (canonical-pingpong fix, 2026-05-14): a domain
// refuses to canonicalize an atom whose current Tags array does not
// contain d.CanonicalTag. bearcli returns tags with leading `#` (e.g.
// "#quicknote/daily"), so we compare against d.CanonicalTag, not d.Tag.
// Without this, drag-to-tag in Bear can leave transient tag-index
// residue that lets a non-owning domain (e.g. quicknote/daily) stamp a
// note that already belongs to development/noxctl, flipping the
// canonical body to the wrong domain across multiple FSEvent bursts.
func (d *Domain) processAtomic(ctx context.Context, n Note, bucket string) (touched, failed int) {
	if !slices.Contains(n.Tags, d.CanonicalTag) {
		return 0, 0
	}
	result, err := d.upsertAtomicBacklink(ctx, n.ID, n.Title, bucket, n.Content)
	if err != nil {
		d.Logf("atomic %q: ERROR: %v", n.Title, err)
		return 0, 1
	}
	if result != "" {
		d.Logf("atomic %s", result)
		return 1, 0
	}
	return 0, 0
}

// ProcessAtomicForTest exposes processAtomic for external tests in tests/bear/.
// Test seam — production callers MUST use RunRegen. Same precedent as
// ComputeContentHash on bear/engine/apply.go.
func (d *Domain) ProcessAtomicForTest(ctx context.Context, n Note, bucket string) (touched, failed int) {
	return d.processAtomic(ctx, n, bucket)
}

// runAtomicsPass rewrites each atomic note's header to canonical shape.
// Honors REGEN_ATOMICS_PILOT=<bucket> (or REGEN_ATOMICS_PILOT_<TAG>=<bucket>
// for per-domain limited-scope runs). Returns counts of touched/failed atomics
// so RunRegen can summarize the cycle.
func (d *Domain) runAtomicsPass(ctx context.Context, groups map[string][]Note) (touched, failed int) {
	pilot := d.atomicsPilotBucket()
	for bucket, items := range groups {
		if pilot != "" && bucket != pilot {
			continue
		}
		for _, note := range items {
			if CheckCtx(ctx) != nil {
				return
			}
			passTouched, passFailed := d.processAtomic(ctx, note, bucket)
			touched += passTouched
			failed += passFailed
		}
	}
	if pilot != "" {
		d.Logf("atomics pilot mode (bucket=%q), %d touched, %d failed", pilot, touched, failed)
	} else if touched > 0 || failed > 0 {
		d.Logf("atomics: %d touched, %d failed", touched, failed)
	}
	return touched, failed
}

// runHubsPass upserts each per-bucket Tier-2 Hub note. No-op for domains
// without Tier-2 hubs (d.RenderHub == nil). Returns count of failed hubs.
func (d *Domain) runHubsPass(ctx context.Context, groups map[string][]Note) (failed int) {
	if d.RenderHub == nil {
		return 0
	}
	for bucket, items := range groups {
		summary, err := d.upsertHub(ctx, bucket, items)
		if err != nil {
			d.Logf("ERROR: %v", err)
			failed++
			continue
		}
		d.Logf("%s", summary)
	}
	return failed
}

// RunRegen runs one full regeneration cycle for this domain: list → group →
// atomics-pass → hubs-pass (if Tier-2) → master. Logs progress with the
// per-domain prefix; aggregates failure counts so a noisy regen is visible at
// a glance. Caller (main.go orchestrator) brackets the self-write gate around
// all domains, NOT around individual RunRegen calls.
func (d *Domain) RunRegen(ctx context.Context) {
	start := time.Now()
	notes, err := d.listNotes(ctx)
	if err != nil {
		d.Logf("list failed: %v", err)
		return
	}
	overrides := d.computeMasterOverrides(notes)
	if len(overrides) > 0 {
		d.Logf("master regroup: %d atomic(s) moved between columns", len(overrides))
	}
	// Hub-side overrides: a bullet inside a Tier-2 hub claims its atomic for
	// that hub's bucket. Master overrides win on collision because the master
	// is the more deliberate gesture (table cut/paste vs. dragging a bullet
	// into a sibling hub).
	hubOverrides := d.computeHubOverrides(notes)
	added := 0
	for atomID, bucket := range hubOverrides {
		if _, alreadySet := overrides[atomID]; alreadySet {
			continue
		}
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides[atomID] = bucket
		added++
	}
	if added > 0 {
		d.Logf("hub regroup: %d atomic(s) moved between hubs", added)
	}
	groups := d.groupAtomics(notes, overrides)
	var atomicsTouched, atomicsFailed int
	if !d.SkipAtomicsPass {
		atomicsTouched, atomicsFailed = d.runAtomicsPass(ctx, groups)
	}
	hubsFailed := d.runHubsPass(ctx, groups)
	masterFailed := 0
	if summary, masterErr := d.upsertMasterIndex(ctx, groups); masterErr != nil {
		d.Logf("ERROR: %v", masterErr)
		masterFailed = 1
	} else {
		d.Logf("%s", summary)
	}
	totalFailed := atomicsFailed + hubsFailed + masterFailed
	if totalFailed > 0 {
		d.Logf(
			"complete WITH FAILURES (%d buckets, %d atomics touched, %d failed, %s elapsed)",
			len(groups), atomicsTouched, totalFailed, time.Since(start).Round(time.Millisecond),
		)
	} else {
		d.Logf("complete (%d buckets, %s elapsed)", len(groups), time.Since(start).Round(time.Millisecond))
	}
}

// EqualIgnoringNewNoteLink is the exported wrapper around the
// unexported equalIgnoringNewNoteLink predicate (non-strict, atomic
// flavor). plan engine and parity check read this from outside
// package bear. Internal callers continue using the lowercase original
// (RESEARCH Pitfall 8 — minimal export footprint).
//
// Non-strict semantics: URL-shape drift (legacy title= vs current
// no-title=) is ignored. Master/hub diffs should call
// EqualIgnoringNewNoteLinkStrict instead.
func EqualIgnoringNewNoteLink(a, b string) bool {
	return equalIgnoringNewNoteLink(a, b)
}

// EqualIgnoringNewNoteLinkStrict is the exported wrapper around the
// strict (master/hub) variant. Used by bear/engine/plan to surface
// URL-shape drift as a real desired-state diff so the engine forces a
// one-shot rewrite of master canonical lines on the first regen cycle
// after the Task 2 URL change deploys.
func EqualIgnoringNewNoteLinkStrict(a, b string) bool {
	return equalIgnoringNewNoteLinkStrict(a, b)
}
