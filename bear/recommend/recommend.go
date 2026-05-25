// Package recommend infers the fitting blueprint for a Bear tag from
// structural metrics. Rule-based and explainable: an ordered decision tree,
// no scoring weights, no ML. Pure + read-only — callers emit the result as
// candidate config.
package recommend

// Confidence grades how cleanly the deciding metric cleared its threshold.
type Confidence int

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
