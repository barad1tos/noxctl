package domain

// Atomic-to-bucket classification plus the master/hub override
// mechanics that let curator edits in the master drive per-atom
// re-bucketing on the next regen cycle. Kept separate from listing
// I/O and atomic-body parsing so the routing decisions live in one
// file the daemon orchestrator can reason about end-to-end.

import (
	"slices"
	"strings"
)

// FirstWikilinkAuthor scans `header` for the first `[[X]]` wikilink
// whose target is neither empty nor the domain's master index title,
// returning that target as the bucket key. Returns "" when nothing
// useful is found. Used as second-tier fallback by DetectAuthor.
func (d *Domain) FirstWikilinkAuthor(header string) string {
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

// DetectAuthor returns the bucket key for an atomic note. Source-of-truth
// priority:
// 1. Domain.ParseMeta — canonical header line (preferred).
// 2. FirstWikilinkAuthor in header zone — covers legacy non-canonical
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
func (d *Domain) DetectAuthor(body string) string {
	if meta := d.ParseMeta(d, body); meta.Bucket != "" {
		return meta.Bucket
	}
	if d.CanonicalTagFor == nil {
		if author := d.FirstWikilinkAuthor(HeaderZone(body)); author != "" {
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
// canonical-header bucket — three override sources feed it in priority order:
//
//	master > hub > tag > canonical-header > BucketFromSubTag > UnknownBucket
//
// Master and hub overrides are deliberate gestures (table cut/paste, hub bullet
// move); tag is a single quick sidebar drag. On collision the deliberate gesture
// wins — caller merges accordingly. The two merge call sites are
// snapshot.go::SnapshotDomainRenderInputs and regen.go::RunRegen and MUST stay
// byte-equivalent (T-12-02-01 threat). Both share mergeOverrideLayer for that
// invariant.
func (d *Domain) groupAtomics(notes []Note, overrides map[string]string) map[string][]Note {
	groups := make(map[string][]Note)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		bucket, hasOverride := overrides[note.ID]
		if !hasOverride {
			bucket = d.DetectAuthor(note.Content)
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
	canonicalBucket := d.DetectAuthor(note.Content)
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
	for _, ident := range ParseHubBulletIdentifiers(hub.Content) {
		atom, ok := resolveAtom(ident, atomByID, atomByTitle)
		if !ok {
			continue
		}
		canonicalBucket := d.DetectAuthor(atom.Content)
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

// ParseHubBulletIdentifiers extracts every atomic identifier from a Hub
// note's bullet list — wikilink targets (`[[Title]]` → "Title") and note
// IDs embedded in `bear://x-callback-url/open-note?id=X` markdown links.
// Lines that don't start with `- ` are ignored, so H2/H3 section headers,
// blank lines, and curator-zone prose pass through silently.
func ParseHubBulletIdentifiers(content string) []string {
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

// mergeOverrideLayer folds `from` into `into` with skip-if-already-claimed
// semantics: any atomID already present in `into` keeps its existing bucket,
// so the caller can chain layers in priority order (master first, hub second,
// tag last) and the earlier layer always wins on collision. Lazy-initializes
// `into` when nil so callers don't have to pre-allocate.
//
// Returns the (possibly-mutated) merged map. snapshot.go and regen.go both
// route every override merge through this helper — the byte-equivalent
// invariant the Phase 03/04 plan/apply parity contract depends on (see
// T-12-02-01 in 12-02-PLAN threat model).
func mergeOverrideLayer(into, from map[string]string) map[string]string {
	for atomID, bucket := range from {
		if _, claimed := into[atomID]; claimed {
			continue
		}
		if into == nil {
			into = make(map[string]string)
		}
		into[atomID] = bucket
	}
	return into
}

// computeTagOverrides reads each atomic's Bear tag-array and records a
// noteID→bucket override whenever the user has dragged a whitelisted
// `#<family>/<sub>` sub-tag onto the note in Bear's sidebar that disagrees
// with the canonical-header bucket. Sibling to computeMasterOverrides
// (flat-table cut/paste) and computeHubOverrides (Tier-2 hub cut/paste).
//
// The sidebar drag is a single quick gesture vs the deliberate multi-step
// master/hub cut/paste flow. Merge priority therefore puts tag overrides
// LAST: master > hub > tag. When master or hub already claim an atom, the
// deliberate gesture wins — see groupAtomics for the merge site (wired by
// Plan 12-02; this primitive returns the candidate map only).
//
// Conflict resolution is strict: a single non-canonical whitelisted sub-tag
// fires the override (95% of real drags). Two or more non-canonical sub-tags
// emit a domain-prefixed warning via d.Logf and record no override —
// predictability over guessing what the user meant.
//
// No-op (returns nil) for non-sub-tag preserving blueprints: only domains
// that wire CanonicalTagFor (grouped-vertical, hub-routed-with-subtag)
// participate. Returns an empty map (not nil) when the blueprint gate passes
// but no notes need re-bucketing — mirrors computeMasterOverrides shape.
func (d *Domain) computeTagOverrides(notes []Note) map[string]string {
	if d.CanonicalTagFor == nil {
		return nil
	}
	family := strings.SplitN(d.Tag, "/", 2)[0]
	prefix := family + "/"
	overrides := make(map[string]string)
	for _, note := range notes {
		if !slices.Contains(note.Tags, d.CanonicalTag) {
			continue
		}
		valid := gatherWhitelistedSubTags(d, note.Tags, prefix)
		if len(valid) == 0 {
			continue
		}
		canonicalBucket := ParseMetaFromSubTag(d, note.Content).Bucket
		if canonicalBucket == "" {
			canonicalBucket = d.UnknownBucket
		}
		bucket, conflict := decideOverride(canonicalBucket, valid)
		if conflict {
			d.Logf("ambiguous tag intent on note %s: %v — keeping canonical=%s",
				note.ID, nonCanonicalSubTags(canonicalBucket, valid), canonicalBucket)
			continue
		}
		if bucket == "" {
			continue
		}
		overrides[note.ID] = bucket
	}
	return overrides
}

// gatherWhitelistedSubTags returns the ordered list of sub-tag segments from
// `tags` that (a) carry the `<family>/` prefix, (b) are a single segment
// (depth=2 invariant — Bear tag-tree never goes deeper), and (c) appear in
// `d.Buckets ∪ {d.UnknownBucket}`. Order follows tags iteration so callers
// see a deterministic sequence in tests and log lines.
func gatherWhitelistedSubTags(d *Domain, tags []string, prefix string) []string {
	var out []string
	for _, tag := range tags {
		bare := strings.TrimPrefix(tag, "#")
		sub, ok := strings.CutPrefix(bare, prefix)
		if !ok || sub == "" || strings.Contains(sub, "/") {
			continue
		}
		if sub != d.UnknownBucket && !slices.Contains(d.Buckets, sub) {
			continue
		}
		out = append(out, sub)
	}
	return out
}

// decideOverride collapses the whitelist set against the canonical bucket
// into the final routing decision:
//
//   - All entries match canonical → ("", false): already canonical, no work.
//   - Exactly one non-canonical entry → (bucket, false): override fires.
//   - Two or more non-canonical entries → ("", true): conflict, caller logs
//     and skips.
//
// Splitting this out keeps computeTagOverrides' cognitive complexity below
// the ≤15 threshold without losing the algorithm's readable shape.
func decideOverride(canonicalBucket string, valid []string) (bucket string, conflict bool) {
	var firstNonCanonical string
	count := 0
	for _, sub := range valid {
		if sub == canonicalBucket {
			continue
		}
		if count == 0 {
			firstNonCanonical = sub
		}
		count++
	}
	switch count {
	case 0:
		return "", false
	case 1:
		return firstNonCanonical, false
	default:
		return "", true
	}
}

// nonCanonicalSubTags returns the subset of `valid` that disagrees with
// `canonicalBucket`, preserving order. Used solely to format the conflict
// warning — keeping it separate lets decideOverride stay branch-light while
// the log line still carries every offending sub-tag.
func nonCanonicalSubTags(canonicalBucket string, valid []string) []string {
	var out []string
	for _, sub := range valid {
		if sub != canonicalBucket {
			out = append(out, sub)
		}
	}
	return out
}

// ComputeTagOverridesForTest exposes computeTagOverrides for external tests
// in tests/bear/. Test seam — production callers MUST use RunRegen. Same
// precedent as ProcessAtomicForTest at bear/domain/upserts.go:153.
func (d *Domain) ComputeTagOverridesForTest(notes []Note) map[string]string {
	return d.computeTagOverrides(notes)
}

// collectExtraTags pulls non-canonical tags from a header line — anything
// besides the daemon-managed family (#<family> and #<family>/*). The family
// is the top-level segment of d.Tag (e.g. "library" for "library/poetry",
// "llm" for "llm/agents"); we never re-emit these as extras because the
// canonicalizer writes a single authoritative tag-line. Without this filter
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
