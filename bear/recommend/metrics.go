package recommend

import (
	"sort"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

// ComputeMetrics scans a tag's notes into the structural signals the decision
// tree reads. Pure: childFamilies is supplied by the vault-wide caller (nil for
// a single-tag scan).
func ComputeMetrics(tag string, notes []domain.Note, childFamilies []string) Metrics {
	notes = atomsOnly(notes)
	counts := bucketCounts(tag, notes)
	withBucket := 0
	for _, c := range counts {
		withBucket += c
	}
	m := Metrics{
		TagDepth:          strings.Count(tag, "/") + 1,
		NoteCount:         len(notes),
		ChildFamilies:     len(childFamilies),
		BucketCardinality: len(counts),
		AtomsPerBucket:    medianCount(counts),
		BodyAuthorSignal:  authorSignal(notes),
		Buckets:           sortedKeys(counts),
	}
	if len(notes) > 0 {
		m.BucketCoverage = float64(withBucket) / float64(len(notes))
	}
	return m
}

// atomsOnly drops managed master/hub notes (titled with the ✱ index marker)
// so their generated canonical lines and "new note" links never count as
// buckets or inflate the note total (spec: metrics exclude managed master/hubs).
func atomsOnly(notes []domain.Note) []domain.Note {
	out := make([]domain.Note, 0, len(notes))
	for _, n := range notes {
		if strings.HasPrefix(strings.TrimSpace(n.Title), "✱") {
			continue
		}
		out = append(out, n)
	}
	return out
}

// bucketCounts maps each observed bucket to its note count. A bucket comes from
// a note's sub-tag `#tag/<bucket>` (single extra segment) or, for an
// already-managed note, the canonical 3rd segment `#tag | [[Index]] | <bucket>`.
func bucketCounts(tag string, notes []domain.Note) map[string]int {
	counts := map[string]int{}
	for _, n := range notes {
		if b := subTagBucket(tag, n.Tags); b != "" {
			counts[b]++
			continue
		}
		if b := canonicalBucket(tag, n.Content); b != "" {
			counts[b]++
		}
	}
	return counts
}

// subTagBucket returns the single-segment sub-tag bucket for tag, or "".
func subTagBucket(tag string, tags []string) string {
	prefix := tag + "/"
	for _, t := range tags {
		clean := strings.TrimPrefix(t, "#")
		if sub, ok := strings.CutPrefix(clean, prefix); ok && sub != "" && !strings.Contains(sub, "/") {
			return sub
		}
	}
	return ""
}

// canonicalBucket reads the 3rd pipe-segment of a managed canonical header line
// (`#tag | [[Index]] | bucket`), or "" if the note is unmanaged.
func canonicalBucket(tag, content string) string {
	want := "#" + tag
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, want+" |") {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) >= 3 {
			if b := strings.TrimSpace(parts[2]); isPlausibleBucket(b) {
				return b
			}
		}
	}
	return ""
}

// isPlausibleBucket rejects values that are clearly not a bucket name — a
// markdown link or a bear:// URL (e.g. a master's "new note" placeholder that
// slipped past atomsOnly).
func isPlausibleBucket(s string) bool {
	return s != "" && !strings.HasPrefix(s, "[") && !strings.Contains(s, "](") && !strings.Contains(s, "://")
}

func sortedKeys(m map[string]int) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// medianCount returns the median of the per-bucket counts (0 for no buckets).
func medianCount(counts map[string]int) int {
	if len(counts) == 0 {
		return 0
	}
	vals := make([]int, 0, len(counts))
	for _, c := range counts {
		vals = append(vals, c)
	}
	sort.Ints(vals)
	return vals[(len(vals)-1)/2]
}

// authorSignal returns the fraction of notes whose body carries at least one
// `## ` H2 heading (an author/source section). Conservative: only counts H2,
// not bare lead lines, to avoid over-recommending Tier-2 hubs.
func authorSignal(notes []domain.Note) float64 {
	if len(notes) == 0 {
		return 0
	}
	withH2 := 0
	for _, n := range notes {
		for line := range strings.SplitSeq(n.Content, "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "## ") {
				withH2++
				break
			}
		}
	}
	return float64(withH2) / float64(len(notes))
}
