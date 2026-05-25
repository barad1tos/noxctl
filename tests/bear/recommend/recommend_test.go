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
		TagDepth: 1, BucketCardinality: 5, SubtagCoverage: 1, AtomsPerBucket: 2,
	})
	if gv.Blueprint != "grouped-vertical" || gv.Alternative != "hub-routed-with-subtag" {
		t.Errorf("top-level low/bucket -> %q (alt %q), want grouped-vertical / hub-routed-with-subtag", gv.Blueprint, gv.Alternative)
	}
	// top-level, many atoms/bucket -> hub-routed-with-subtag
	hs := recommend.Recommend(recommend.Metrics{
		TagDepth: 1, BucketCardinality: 8, SubtagCoverage: 1, AtomsPerBucket: 15,
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
		TagDepth: 2, BucketCardinality: 3, SubtagCoverage: 1, AtomsPerBucket: 4,
	})
	if gv2.Blueprint != "grouped-vertical" {
		t.Errorf("2-level low-card -> %q, want grouped-vertical", gv2.Blueprint)
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
