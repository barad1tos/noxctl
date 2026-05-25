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
