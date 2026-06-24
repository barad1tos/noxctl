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

// RoutingResult is the pure in-memory routing output shared by plan snapshots
// and the write-side regen runtime.
type RoutingResult struct {
	Groups       map[string][]Note
	MasterClaims int
	HubClaims    int
	TagClaims    int
	TagConflicts int
}

// RouteAtomics computes the post-override bucket grouping for a domain's note
// set. It is intentionally pure over the supplied notes: callers own all Bear
// I/O, while domain owns the routing rules that interpret canonical headers,
// master table edits, hub bullet edits, and sidebar sub-tag drags.
func (d *Domain) RouteAtomics(
	notes []Note,
	onTagOverrideSkip func(atomID, kept, suppressed string),
) RoutingResult {
	overrides := d.computeMasterOverrides(notes)
	result := RoutingResult{MasterClaims: len(overrides)}
	beforeHub := len(overrides)
	overrides = mergeOverrideLayer(overrides, d.computeHubOverrides(notes), nil)
	result.HubClaims = len(overrides) - beforeHub
	beforeTag := len(overrides)
	tagOverrides, tagConflicts := d.computeTagOverrides(notes)
	overrides = mergeOverrideLayer(overrides, tagOverrides, onTagOverrideSkip)
	result.TagClaims = len(overrides) - beforeTag
	result.TagConflicts = tagConflicts
	result.Groups = d.groupAtomics(notes, overrides)
	if result.Groups == nil {
		result.Groups = map[string][]Note{}
	}
	return result
}

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

// IsManagedHubNote reports whether n is this domain's generated Tier-2 hub.
func (d *Domain) IsManagedHubNote(n Note) bool {
	return d.isHubNote(n)
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
// in an atom body) as bucket names.
// 3. Legacy fallback: first non-section ## H2 in body — guarded by
// LegacyAuthorFallback (poetry only; aphorisms quote H2s would misread).
func (d *Domain) DetectAuthor(body string) AtomicMeta {
	if meta := d.ParseMeta(d, body); meta.Bucket != "" || meta.ExplicitlyUncategorized {
		return meta
	}
	if d.CanonicalTagFor == nil {
		if author := d.FirstWikilinkAuthor(HeaderZone(body)); author != "" {
			return AtomicMeta{Bucket: author}
		}
	}
	if !d.LegacyAuthorFallback {
		return AtomicMeta{}
	}
	heading := firstNonSectionH2(body)
	if _, isOwnAlias := d.OwnAliases[heading]; isOwnAlias {
		return AtomicMeta{Bucket: d.OwnGroup}
	}
	return AtomicMeta{Bucket: heading}
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
// wins. mergeOverrideLayer is the single source of truth for the merge
// semantics; see its doc-comment for the byte-equivalent invariant shared
// between snapshot.go::SnapshotDomainRenderInputs and regen.go::RunRegen.
func (d *Domain) groupAtomics(notes []Note, overrides map[string]string) map[string][]Note {
	groups := make(map[string][]Note)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		bucket, hasOverride := overrides[note.ID]
		if !hasOverride {
			meta := d.DetectAuthor(note.Content)
			bucket = meta.Bucket
			if bucket == "" {
				bucket = BucketFromSubTag(d, note.Tags)
			}
			if bucket == "" && meta.ExplicitlyUncategorized {
				continue
			}
			if bucket == "" {
				bucket = d.UnknownBucket
			}
		}
		groups[bucket] = append(groups[bucket], note)
	}
	return groups
}

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

// overrideForNote checks whether `identifierToBucket` (parsed master) places
// the note in a different bucket than its canonical header detects. Returns
// the new bucket and `true` when an override is needed; (`""`, `false`) when
// the note isn't tracked by the master or already matches its canonical bucket.
//
// Lookup is ID-first, then title when the title is unique among current
// atomics. Plain legacy `[[Title]]` entries become ambiguous once duplicate
// titles exist, so they must not rebucket multiple notes by the same collapsed
// map entry.
func (d *Domain) overrideForNote(
	note Note,
	identifierToBucket map[string]string,
	titleCounts map[string]int,
) (string, bool) {
	masterBucket, inMaster := identifierToBucket[note.ID]
	if !inMaster && titleCounts[note.Title] == 1 {
		masterBucket, inMaster = identifierToBucket[note.Title]
	}
	if !inMaster {
		return "", false
	}
	meta := d.DetectAuthor(note.Content)
	canonicalBucket := meta.Bucket
	if meta.ExplicitlyUncategorized {
		return "", false
	}
	if canonicalBucket == "" {
		canonicalBucket = d.UnknownBucket
	}
	if canonicalBucket == masterBucket {
		return "", false
	}
	return masterBucket, true
}

// computeMasterOverrides reads the current master, parses its table via
// Domain.ParseMasterTable, and returns a noteID→bucket map for atomics whose
// table position disagrees with their canonical header. Empty map (nil-safe)
// when ParseMasterTable is unset, the master is missing, or the user hasn't
// moved anything since the last regen.
//
// Master is the source of truth for 2-level grouped-vertical domains: a user who cuts a
// bullet from one column and pastes it into another expects that bullet's
// atomic to follow. The next regen sees the disagreement here and rewrites the
// atomic's canonical header on its way through runAtomicsPass.
func (d *Domain) computeMasterOverrides(notes []Note) map[string]string {
	if d.ParseMasterTable == nil {
		return nil
	}
	identifierToBucket := d.parseMasterTableForNotes(notes)
	if len(identifierToBucket) == 0 {
		return nil
	}
	titleCounts := d.countAtomicTitles(notes)
	overrides := make(map[string]string)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		if override, ok := d.overrideForNote(note, identifierToBucket, titleCounts); ok {
			overrides[note.ID] = override
		}
	}
	return overrides
}

func (d *Domain) countAtomicTitles(notes []Note) map[string]int {
	titleCounts := make(map[string]int)
	for _, note := range notes {
		if d.skipNote(note) {
			continue
		}
		titleCounts[note.Title]++
	}
	return titleCounts
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
// (RenderHub == nil) — grouped-vertical (2-level) and flat-list domains handle bidirectional
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
		meta := d.DetectAuthor(atom.Content)
		canonicalBucket := meta.Bucket
		if canonicalBucket == "" && !meta.ExplicitlyUncategorized {
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
// `into` when nil so callers don't have to pre-allocate — important because
// computeMasterOverrides returns nil for domains without a ParseMasterTable
// and the hub/tag layers still need a place to deposit their overrides.
//
// `onSkip` (nil-safe) is invoked once per atom whose suppressed bucket
// disagrees with the kept bucket — agreeing duplicates pass silently.
// Callback receives (atomID, keptBucket, suppressedBucket). The write-side
// apply path wires this to a WARN log line so a curator drag that loses
// to a deliberate gesture is visible; the read-only plan/snapshot path
// passes nil to stay silent.
//
// Returns the (possibly-mutated) merged map. Single source of truth for the
// merge semantics: SnapshotDomainRenderInputs (snapshot.go) and RunRegen
// (regen.go) both route every override merge through this helper so the
// post-merge override map stays byte-equivalent between the read-only plan
// path and the write-side apply path.
func mergeOverrideLayer(into, from map[string]string, onSkip func(atomID, kept, suppressed string)) map[string]string {
	for atomID, bucket := range from {
		if kept, claimed := into[atomID]; claimed {
			if onSkip != nil && kept != bucket {
				onSkip(atomID, kept, bucket)
			}
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
// (grouped-vertical 2-level cut/paste) and computeHubOverrides (Tier-2 hub cut/paste).
//
// The sidebar drag is a single quick gesture vs the deliberate multi-step
// master/hub cut/paste flow. Merge priority therefore puts tag overrides
// LAST: master > hub > tag. When master or hub already claim an atom, the
// deliberate gesture wins — see SnapshotDomainRenderInputs (snapshot.go)
// and RunRegen (regen.go) for the merge sites; this primitive returns the
// candidate map only.
//
// Conflict resolution is strict: a single non-canonical whitelisted sub-tag
// fires the override (95% of real drags). Two or more non-canonical sub-tags
// emit a domain-prefixed warning via d.Logf and record no override —
// predictability over guessing what the user meant.
//
// No-op (returns (nil, 0)) for non-sub-tag preserving blueprints: only
// domains that wire CanonicalTagFor (grouped-vertical, hub-routed-with-subtag)
// participate. Also returns (nil, 0) when the blueprint gate passes but no
// note needs re-bucketing — the map is lazy-initialized at the first write
// so zero-override sweeps stay allocation-free; both mergeOverrideLayer
// and groupAtomics handle the nil map verbatim.
//
// conflictCount reports how many notes hit the ambiguous-intent branch
// (≥2 distinct non-canonical sub-tags). Caller surfaces the rollup as a
// cycle-summary line so 50 individual WARN lines don't drown the count.
func (d *Domain) computeTagOverrides(notes []Note) (overrides map[string]string, conflictCount int) {
	if d.CanonicalTagFor == nil {
		return nil, 0
	}
	family := strings.SplitN(d.Tag, "/", 2)[0]
	prefix := family + "/"
	for _, note := range notes {
		if !hasFamilyMembership(note.Tags, family) {
			continue
		}
		whitelistedSubTags := gatherWhitelistedSubTags(d, note, prefix, family)
		if len(whitelistedSubTags) == 0 {
			continue
		}
		meta := ParseMetaFromSubTag(d, note.Content)
		canonicalBucket := meta.Bucket
		if canonicalBucket == "" && !meta.ExplicitlyUncategorized {
			canonicalBucket = d.UnknownBucket
		}
		bucket, conflict := decideOverride(canonicalBucket, whitelistedSubTags)
		if conflict {
			d.Logf("WARN: ambiguous tag intent on note %s: %v — keeping canonical=%s",
				note.ID, nonCanonicalSubTags(canonicalBucket, whitelistedSubTags), canonicalBucket)
			conflictCount++
			continue
		}
		if bucket == "" {
			continue
		}
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides[note.ID] = bucket
	}
	return overrides, conflictCount
}

// hasFamilyMembership reports whether `tags` carries the daemon-managed
// family's bare parent tag (`<family>`). The `#` prefix is optional so
// callers that hand in trimmed tag arrays still match, mirroring the
// lenient `strings.TrimPrefix(tag, "#")` shape used by
// gatherWhitelistedSubTags. A note with only the leaf sub-tag and no
// parent (`#work/tasks` without `#work`) is treated as foreign — the
// existing MissingDomainTag_Skipped contract.
func hasFamilyMembership(tags []string, family string) bool {
	for _, tag := range tags {
		if strings.TrimPrefix(tag, "#") == family {
			return true
		}
	}
	return false
}

// gatherWhitelistedSubTags returns the ordered list of sub-tag segments from
// `note.Tags` that (a) carry the `<family>/` prefix, (b) are a single
// segment (depth=2 invariant — Bear tag-tree never goes deeper), and (c)
// appear in `d.Buckets ∪ {d.UnknownBucket}`. Order follows tags iteration so
// callers see a deterministic sequence in tests and log lines.
//
// Whitelist match is byte-exact (case-sensitive) by design: Bear's sidebar
// tag tree treats `#work/Tasks` and `#work/tasks` as two distinct nodes,
// so the daemon mirrors that exact-match semantics. If an operator's TOML
// whitelist (`Buckets = ["tasks"]`) disagrees with the Bear-typed chip
// case (`#work/Tasks`), the chip is rejected and a WARN line surfaces the
// case mismatch — same as any other non-whitelist sub-tag.
//
// Logs a WARN line for every `#<family>/<sub>` whose `<sub>` is rejected by
// the whitelist gate — the operator's drag silently disappearing was the
// failure mode that motivated this signal. Per-occurrence (no per-cycle
// dedup): a single Bear tag rarely fires twice in one note.
func gatherWhitelistedSubTags(d *Domain, note Note, prefix, family string) []string {
	var out []string
	for _, tag := range note.Tags {
		bare := strings.TrimPrefix(tag, "#")
		sub, ok := strings.CutPrefix(bare, prefix)
		if !ok || sub == "" || strings.Contains(sub, "/") {
			continue
		}
		if sub != d.UnknownBucket && !slices.Contains(d.Buckets, sub) {
			d.Logf("WARN: non-whitelist sub-tag #%s/%s on note %s ignored (extend Buckets to enable)",
				family, sub, note.ID)
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
//   - Exactly one distinct non-canonical entry → (bucket, false): override
//     fires (duplicates of the same sub-tag count as one — a self-collision
//     like ["tasks", "tasks"] is not a conflict).
//   - Two or more distinct non-canonical entries → ("", true): conflict,
//     caller logs and skips.
//
// Splitting this out keeps computeTagOverrides' cognitive complexity below
// the ≤15 threshold without losing the algorithm's readable shape. Shares
// the dedupNonCanonical pass with nonCanonicalSubTags so a single source
// of truth governs which sub-tags are considered "competing claims."
func decideOverride(canonicalBucket string, whitelistedSubTags []string) (bucket string, conflict bool) {
	distinct := dedupNonCanonical(canonicalBucket, whitelistedSubTags)
	switch len(distinct) {
	case 0:
		return "", false
	case 1:
		return distinct[0], false
	default:
		return "", true
	}
}

// nonCanonicalSubTags returns the distinct subset of `whitelistedSubTags`
// that disagrees with `canonicalBucket`, preserving first-occurrence order.
// Wrapper over dedupNonCanonical for naming-at-the-call-site clarity —
// the conflict log line reads naturally as `nonCanonicalSubTags(...)`
// while the shared helper carries the actual algorithm.
func nonCanonicalSubTags(canonicalBucket string, whitelistedSubTags []string) []string {
	return dedupNonCanonical(canonicalBucket, whitelistedSubTags)
}

// dedupNonCanonical filters whitelistedSubTags to the distinct entries
// that disagree with canonicalBucket, preserving first-occurrence order.
// Shared backbone for decideOverride (which only needs the count and the
// first entry) and nonCanonicalSubTags (which formats the WARN line).
func dedupNonCanonical(canonicalBucket string, whitelistedSubTags []string) []string {
	seen := make(map[string]struct{}, len(whitelistedSubTags))
	out := make([]string, 0, len(whitelistedSubTags))
	for _, sub := range whitelistedSubTags {
		if sub == canonicalBucket {
			continue
		}
		if _, dup := seen[sub]; dup {
			continue
		}
		seen[sub] = struct{}{}
		out = append(out, sub)
	}
	return out
}

// ComputeTagOverridesForTest exposes computeTagOverrides for external tests
// in tests/bear/. Test seam — production callers MUST use RunRegen. Same
// precedent as ProcessAtomicForTest in bear/regen/upserts.go.
//
// Returns (overrides, conflictCount) so the conflict-count branch is
// observable from tests.
func (d *Domain) ComputeTagOverridesForTest(notes []Note) (map[string]string, int) {
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
