package domain

import "fmt"

// SectionedMasterRenderer returns a RenderMaster callback that emits
// the domain's master as a vertical stack of named sections defined
// in d.MasterSections. Each section picks the Tier-2 buckets it
// claims via an explicit `Buckets` list, a `Script` predicate
// ("latin" / "non-latin"), or a catch-all (neither set).
//
// Sections render top-to-bottom in declaration order; earlier
// sections claim their buckets first, and the last catch-all (if
// any) sweeps up whatever the explicit + script-class sections did
// not consume. Empty sections drop out of the rendered output so
// the operator never sees a `## Whatever (0)` placeholder.
//
// Bullet format: `[[<bucket>]] (<count>)` — count is the number of
// notes in that Tier-2 hub. Order inside each section is
// alphabetical via SortTitles.
func SectionedMasterRenderer() func(*Domain, map[string][]Note) string {
	return func(d *Domain, groups map[string][]Note) string {
		return RenderVerticalSections(d, BuildMasterSections(d, groups))
	}
}

// BuildMasterSections is the pure function that derives Section
// slices from the domain's MasterSections + the per-bucket note
// map. Exposed for hermetic testing without driving a full domain
// render path.
func BuildMasterSections(d *Domain, groups map[string][]Note) []Section {
	claimed := make(map[string]struct{}, len(groups))
	sections := make([]Section, 0, len(d.MasterSections))
	for _, spec := range d.MasterSections {
		bucketsForSection := selectBucketsForSection(spec, groups, claimed, d.UnknownBucket)
		if len(bucketsForSection) == 0 {
			continue
		}
		SortTitles(bucketsForSection)
		bullets := make([]string, len(bucketsForSection))
		noteCount := 0
		for i, bucket := range bucketsForSection {
			count := len(groups[bucket])
			noteCount += count
			bullets[i] = renderSectionBullet(spec.ShowBulletCounts, bucket, count)
			claimed[bucket] = struct{}{}
		}
		headerCount := sectionHeaderCount(spec.CountMode, noteCount, len(bucketsForSection))
		sections = append(sections, Section{
			Header:  fmt.Sprintf("%s (%d)", spec.Title, headerCount),
			Bullets: bullets,
		})
	}
	return sections
}

// renderSectionBullet picks the bullet form: with `(count)` suffix
// when ShowBulletCounts is true, otherwise plain `[[bucket]]`.
func renderSectionBullet(showCounts bool, bucket string, count int) string {
	if showCounts {
		return fmt.Sprintf("[[%s]] (%d)", bucket, count)
	}
	return fmt.Sprintf("[[%s]]", bucket)
}

// sectionHeaderCount maps CountMode to the numeric value placed in
// the section header's trailing `(N)`.
func sectionHeaderCount(mode CountMode, noteCount, bucketCount int) int {
	if mode == CountModeBuckets {
		return bucketCount
	}
	return noteCount
}

// selectBucketsForSection returns the list of bucket names this
// section claims, given what's already been claimed by earlier
// sections. The selection rules are mutually exclusive in TOML;
// validate-time guard rejects sections that set more than one.
//
// The unknownBucket is silently excluded from script-class and
// catch-all selections — it has no semantic bucket identity. An
// explicit Buckets entry naming it still claims it.
func selectBucketsForSection(spec MasterSection, groups map[string][]Note,
	claimed map[string]struct{}, unknownBucket string,
) []string {
	switch {
	case len(spec.Buckets) > 0:
		return selectByExplicit(spec.Buckets, groups, claimed)
	case spec.Script != "":
		return selectByScript(spec.Script, groups, claimed, unknownBucket)
	default:
		return selectCatchAll(groups, claimed, unknownBucket)
	}
}

// selectByExplicit returns the subset of spec buckets that have at
// least one note and are not yet claimed. Order from spec is NOT
// preserved — caller sorts via SortTitles for deterministic output.
func selectByExplicit(wanted []string, groups map[string][]Note,
	claimed map[string]struct{},
) []string {
	out := make([]string, 0, len(wanted))
	for _, bucket := range wanted {
		if _, isClaimed := claimed[bucket]; isClaimed {
			continue
		}
		if items, ok := groups[bucket]; ok && len(items) > 0 {
			out = append(out, bucket)
		}
	}
	return out
}

// selectByScript filters unclaimed buckets by FirstLetter group:
// "latin" → group "1", "non-latin" → every other group. Unknown
// script names produce an empty slice and no error — validate-time
// guard catches misspellings before this point.
func selectByScript(class string, groups map[string][]Note,
	claimed map[string]struct{}, unknownBucket string,
) []string {
	out := make([]string, 0, len(groups))
	for bucket := range groups {
		if bucket == unknownBucket {
			continue
		}
		if _, isClaimed := claimed[bucket]; isClaimed {
			continue
		}
		if !scriptClassMatches(class, bucket) {
			continue
		}
		if len(groups[bucket]) == 0 {
			continue
		}
		out = append(out, bucket)
	}
	return out
}

// selectCatchAll returns every still-unclaimed bucket with at least
// one note. The unknownBucket is skipped — operators who want it
// surfaced must add it to an explicit Buckets list.
func selectCatchAll(groups map[string][]Note, claimed map[string]struct{},
	unknownBucket string,
) []string {
	out := make([]string, 0, len(groups))
	for bucket := range groups {
		if bucket == unknownBucket {
			continue
		}
		if _, isClaimed := claimed[bucket]; isClaimed {
			continue
		}
		if len(groups[bucket]) == 0 {
			continue
		}
		out = append(out, bucket)
	}
	return out
}

// scriptClassMatches maps the TOML `script` value to a FirstLetter
// group ID and reports whether bucket falls in it.
//
// Two-class binary partition: "latin" covers FirstLetter group "1"
// (the ASCII alphabet); "non-latin" covers everything else (Cyrillic,
// Greek, Hebrew, CJK, symbols, digits). Operators who need a finer
// partition spell it via explicit `buckets = [...]` blocks.
func scriptClassMatches(class, bucket string) bool {
	group, _ := FirstLetter(bucket)
	switch class {
	case "latin":
		return group == "1"
	case "non-latin":
		return group != "1"
	}
	return false
}
