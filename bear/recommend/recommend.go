// Package recommend infers the fitting blueprint for a Bear tag from
// structural metrics. Rule-based and explainable: an ordered decision tree,
// no scoring weights, no ML. Pure + read-only — callers emit the result as
// candidate config.
package recommend

// Confidence grades how cleanly the deciding metric cleared its threshold.
type Confidence int

// Confidence grade constants ordered from weakest to strongest signal.
const (
	Low Confidence = iota
	Medium
	High
)

func (c Confidence) String() string {
	switch c {
	case High:
		return "high"
	case Medium:
		return "medium"
	default:
		return "low"
	}
}

// Metrics are the structural signals scanned from a tag's notes. ChildFamilies
// is supplied by the vault-wide caller (0 for a single-tag scan).
type Metrics struct {
	TagDepth          int      // 1 (top-level) or 2 (nested)
	NoteCount         int      // atoms carrying the tag
	ChildFamilies     int      // distinct populated child tag-families
	SubtagCoverage    float64  // fraction of notes with a single-segment #tag/<bucket>
	BucketCardinality int      // distinct observed buckets
	AtomsPerBucket    int      // median notes per observed bucket
	BodyAuthorSignal  float64  // fraction of notes with an author-like body
	Buckets           []string // sorted distinct observed bucket names
}

// Recommendation is the engine's verdict for one tag.
type Recommendation struct {
	Blueprint      string
	Confidence     Confidence
	DecidingMetric string
	Alternative    string // "" when the choice is unambiguous
	Rationale      string
}

// Thresholds — calibrated against the 31-domain corpus (see Task 8). Tunable in
// one place; the decision tree reads only these named constants.
const (
	flatMaxNotes      = 15  // <= this with no buckets leans flat-list
	hubMinPerBucket   = 8   // atoms/bucket >= this makes a Tier-2 hub worth it
	hubMinCardinality = 6   // distinct buckets >= this leans hub-routed (2-level)
	umbrellaMinChild  = 2   // child families >= this => umbrella
	subtagMinCoverage = 0.7 // fraction with #tag/bucket to count as sub-tag-bucketed
	authorMinSignal   = 0.5 // fraction with author bodies to count as content-bucketed
)

// Recommend runs the ordered decision tree over the metrics. First match wins.
func Recommend(m Metrics) Recommendation {
	if m.ChildFamilies >= umbrellaMinChild {
		return Recommendation{
			Blueprint: "umbrella", Confidence: High, DecidingMetric: "child_families",
			Rationale: "tag has child tag-families that are themselves domains",
		}
	}
	if !isBucketed(m) {
		return Recommendation{
			Blueprint: "flat-list", Confidence: flatConfidence(m), DecidingMetric: "buckets",
			Rationale: "no bucket signal (sub-tags or author H2s) detected",
		}
	}
	if m.TagDepth == 1 {
		return recommendTopLevel(m)
	}
	return recommendNested(m)
}

// isBucketed reports whether the notes carry a usable grouping signal.
func isBucketed(m Metrics) bool {
	return m.BucketCardinality >= 1 &&
		(m.SubtagCoverage >= subtagMinCoverage || m.BodyAuthorSignal >= authorMinSignal)
}

// flatConfidence is High for a clearly-small tag, Medium otherwise.
func flatConfidence(m Metrics) Confidence {
	if m.NoteCount <= flatMaxNotes {
		return High
	}
	return Medium
}

// recommendTopLevel handles a bucketed top-level tag: sub-tags are legal, so the
// fork is grouped-vertical (atoms inline) vs hub-routed-with-subtag (Tier-2 hub
// per bucket). The single threshold hubMinPerBucket breaks this intent tie.
func recommendTopLevel(m Metrics) Recommendation {
	if m.AtomsPerBucket >= hubMinPerBucket {
		return Recommendation{
			Blueprint: "hub-routed-with-subtag", Confidence: forkConfidence(m), DecidingMetric: "atoms_per_bucket",
			Alternative: "grouped-vertical",
			Rationale:   "many atoms per sub-tag bucket — a Tier-2 hub per bucket pays off",
		}
	}
	return Recommendation{
		Blueprint: "grouped-vertical", Confidence: forkConfidence(m), DecidingMetric: "atoms_per_bucket",
		Alternative: "hub-routed-with-subtag",
		Rationale:   "few atoms per bucket — one master with inline sections is enough",
	}
}

// recommendNested handles a bucketed 2-level tag: a sub-tag would be 3-level
// (forbidden), so buckets live in content. Author signal / high cardinality means
// many discovered buckets -> Tier-2 hubs (hub-routed); else inline sections.
func recommendNested(m Metrics) Recommendation {
	if m.BodyAuthorSignal >= authorMinSignal || m.BucketCardinality >= hubMinCardinality {
		return Recommendation{
			Blueprint: "hub-routed", Confidence: High, DecidingMetric: "body_author_signal",
			Rationale: "many content-derived buckets (authors/sources) — Tier-2 hubs",
		}
	}
	return Recommendation{
		Blueprint: "grouped-vertical", Confidence: Medium, DecidingMetric: "bucket_cardinality",
		Rationale: "a small declared bucket set — one master with inline sections",
	}
}

// forkConfidence is Low near the hubMinPerBucket boundary (the intent tie),
// High when the metric is well clear of it.
func forkConfidence(m Metrics) Confidence {
	d := m.AtomsPerBucket - hubMinPerBucket
	if d < 0 {
		d = -d
	}
	if d <= 2 {
		return Low
	}
	return High
}
