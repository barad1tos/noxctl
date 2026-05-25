package recommend_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/recommend"
)

func TestRecommend_UmbrellaAndFlatList(t *testing.T) {
	umb := recommend.Recommend(recommend.Metrics{TagDepth: 1, ChildFamilies: 3})
	if umb.Blueprint != "umbrella" {
		t.Errorf("child_families=3 -> %q, want umbrella", umb.Blueprint)
	}
	flat := recommend.Recommend(recommend.Metrics{TagDepth: 2, NoteCount: 5})
	if flat.Blueprint != "flat-list" {
		t.Errorf("no buckets -> %q, want flat-list", flat.Blueprint)
	}
}

func TestRecommend_BucketedBranches(t *testing.T) {
	// top-level, few atoms/bucket -> grouped-vertical; alternative noted
	gv := recommend.Recommend(recommend.Metrics{
		TagDepth: 1, BucketCardinality: 5, BucketCoverage: 1, AtomsPerBucket: 2,
	})
	if gv.Blueprint != "grouped-vertical" || gv.Alternative != "hub-routed-with-subtag" {
		t.Errorf("top-level low/bucket -> %q (alt %q), want grouped-vertical / hub-routed-with-subtag", gv.Blueprint, gv.Alternative)
	}
	// top-level, many atoms/bucket -> hub-routed-with-subtag
	hs := recommend.Recommend(recommend.Metrics{
		TagDepth: 1, BucketCardinality: 8, BucketCoverage: 1, AtomsPerBucket: 15,
	})
	if hs.Blueprint != "hub-routed-with-subtag" {
		t.Errorf("top-level high/bucket -> %q, want hub-routed-with-subtag", hs.Blueprint)
	}
	// 2-level, strong author signal -> hub-routed
	hr := recommend.Recommend(recommend.Metrics{
		TagDepth: 2, BucketCardinality: 12, BodyAuthorSignal: 0.9, AtomsPerBucket: 30,
	})
	if hr.Blueprint != "hub-routed" {
		t.Errorf("2-level author-rich -> %q, want hub-routed", hr.Blueprint)
	}
	// 2-level, weak signal/low cardinality -> grouped-vertical
	gv2 := recommend.Recommend(recommend.Metrics{
		TagDepth: 2, BucketCardinality: 3, BucketCoverage: 1, AtomsPerBucket: 4,
	})
	if gv2.Blueprint != "grouped-vertical" {
		t.Errorf("2-level low-card -> %q, want grouped-vertical", gv2.Blueprint)
	}
}

func TestRecommend_NestedCardinalityArm(t *testing.T) {
	// I1: cardinality-only arm — BodyAuthorSignal below threshold, BucketCardinality at threshold.
	// Must yield hub-routed with DecidingMetric=="bucket_cardinality", not "body_author_signal".
	r := recommend.Recommend(recommend.Metrics{
		TagDepth: 2, BucketCardinality: 6, BodyAuthorSignal: 0, BucketCoverage: 1,
	})
	if r.Blueprint != "hub-routed" {
		t.Errorf("cardinality arm -> blueprint %q, want hub-routed", r.Blueprint)
	}
	if r.DecidingMetric != "bucket_cardinality" {
		t.Errorf("cardinality arm -> DecidingMetric %q, want bucket_cardinality", r.DecidingMetric)
	}
	if r.Confidence != recommend.High {
		t.Errorf("cardinality arm -> Confidence %v, want High", r.Confidence)
	}
}

func TestRecommend_MediumConfidence(t *testing.T) {
	// M4: large flat-list tag (NoteCount>15, no bucket signal) yields Medium confidence.
	r := recommend.Recommend(recommend.Metrics{TagDepth: 2, NoteCount: 40})
	if r.Blueprint != "flat-list" {
		t.Errorf("large flat-list -> blueprint %q, want flat-list", r.Blueprint)
	}
	if r.Confidence != recommend.Medium {
		t.Errorf("large flat-list -> Confidence %v, want Medium", r.Confidence)
	}

	// Nested grouped-vertical fallback also yields Medium.
	gv := recommend.Recommend(recommend.Metrics{
		TagDepth: 2, BucketCardinality: 3, BucketCoverage: 1, AtomsPerBucket: 4,
	})
	if gv.Blueprint != "grouped-vertical" {
		t.Errorf("nested low-card -> blueprint %q, want grouped-vertical", gv.Blueprint)
	}
	if gv.Confidence != recommend.Medium {
		t.Errorf("nested low-card -> Confidence %v, want Medium", gv.Confidence)
	}
}

func TestConfidenceString(t *testing.T) {
	cases := map[recommend.Confidence]string{
		recommend.Low: "low", recommend.Medium: "medium", recommend.High: "high",
	}
	for c, want := range cases {
		if got := c.String(); got != want {
			t.Errorf("Confidence(%d).String() = %q, want %q", c, got, want)
		}
	}
}

// TestRecommend_FlatMaxNotes_Edge pins the flatMaxNotes=15 boundary:
// NoteCount=15 yields High confidence on flat-list; NoteCount=16 yields Medium.
func TestRecommend_FlatMaxNotes_Edge(t *testing.T) {
	at15 := recommend.Recommend(recommend.Metrics{TagDepth: 1, NoteCount: 15})
	if at15.Blueprint != "flat-list" {
		t.Errorf("NoteCount=15 -> blueprint %q, want flat-list", at15.Blueprint)
	}
	if at15.Confidence != recommend.High {
		t.Errorf("NoteCount=15 -> confidence %v, want High", at15.Confidence)
	}
	at16 := recommend.Recommend(recommend.Metrics{TagDepth: 1, NoteCount: 16})
	if at16.Blueprint != "flat-list" {
		t.Errorf("NoteCount=16 -> blueprint %q, want flat-list", at16.Blueprint)
	}
	if at16.Confidence != recommend.Medium {
		t.Errorf("NoteCount=16 -> confidence %v, want Medium", at16.Confidence)
	}
}

// TestRecommend_HubMinPerBucket_Edge pins the hubMinPerBucket=8 boundary:
// AtomsPerBucket=8 yields hub-routed-with-subtag; 7 yields grouped-vertical.
func TestRecommend_HubMinPerBucket_Edge(t *testing.T) {
	at8 := recommend.Recommend(recommend.Metrics{
		TagDepth: 1, BucketCardinality: 4, BucketCoverage: 1, AtomsPerBucket: 8,
	})
	if at8.Blueprint != "hub-routed-with-subtag" {
		t.Errorf("AtomsPerBucket=8 -> blueprint %q, want hub-routed-with-subtag", at8.Blueprint)
	}
	at7 := recommend.Recommend(recommend.Metrics{
		TagDepth: 1, BucketCardinality: 4, BucketCoverage: 1, AtomsPerBucket: 7,
	})
	if at7.Blueprint != "grouped-vertical" {
		t.Errorf("AtomsPerBucket=7 -> blueprint %q, want grouped-vertical", at7.Blueprint)
	}
}

// TestRecommend_BucketMinCoverage_Edge pins the bucketMinCoverage=0.7 threshold:
// BucketCoverage=0.7 with no author signal must yield bucketed (not flat-list).
func TestRecommend_BucketMinCoverage_Edge(t *testing.T) {
	r := recommend.Recommend(recommend.Metrics{
		TagDepth: 1, BucketCardinality: 3, BucketCoverage: 0.7, BodyAuthorSignal: 0,
	})
	if r.Blueprint == "flat-list" {
		t.Errorf("BucketCoverage=0.7 should be bucketed, got flat-list")
	}
}

func TestRecommend_ReproducesPersonalCatalog(t *testing.T) {
	type row struct {
		name string
		m    recommend.Metrics
		want string
	}
	rows := []row{
		// Flat-list: no bucket signal, small or large note count.
		{"llm/characters", recommend.Metrics{TagDepth: 2, NoteCount: 8}, "flat-list"},
		{"quicknote/daily", recommend.Metrics{TagDepth: 2, NoteCount: 40}, "flat-list"},

		// Grouped-vertical: bucketed but few atoms/bucket (2-level, low cardinality).
		{"library/aphorisms", recommend.Metrics{TagDepth: 2, BucketCardinality: 3, BucketCoverage: 1, AtomsPerBucket: 15}, "grouped-vertical"},
		{"it/vendors", recommend.Metrics{TagDepth: 2, BucketCardinality: 4, BucketCoverage: 1, AtomsPerBucket: 5}, "grouped-vertical"},

		// Grouped-vertical: top-level with sub-tags but too few atoms/bucket for Tier-2.
		{"english", recommend.Metrics{TagDepth: 1, BucketCardinality: 5, BucketCoverage: 1, AtomsPerBucket: 3}, "grouped-vertical"},

		// Hub-routed: 2-level with strong author signal or high bucket cardinality.
		{"library/poetry", recommend.Metrics{TagDepth: 2, BucketCardinality: 40, BodyAuthorSignal: 0.9, AtomsPerBucket: 12}, "hub-routed"},
		{"llm/agents", recommend.Metrics{TagDepth: 2, BucketCardinality: 30, BodyAuthorSignal: 0.6, AtomsPerBucket: 4}, "hub-routed"},

		// Hub-routed-with-subtag: top-level, many atoms/bucket justifies Tier-2 hubs.
		{"claude", recommend.Metrics{TagDepth: 1, BucketCardinality: 8, BucketCoverage: 1, AtomsPerBucket: 9}, "hub-routed-with-subtag"},

		// Umbrella: has child tag-families.
		{"library", recommend.Metrics{TagDepth: 1, ChildFamilies: 6}, "umbrella"},
		{"it", recommend.Metrics{TagDepth: 1, ChildFamilies: 3}, "umbrella"},
	}
	for _, r := range rows {
		if got := recommend.Recommend(r.m).Blueprint; got != r.want {
			t.Errorf("%s: Recommend -> %q, want %q (metrics %+v)", r.name, got, r.want, r.m)
		}
	}
}
