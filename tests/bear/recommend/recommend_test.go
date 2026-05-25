package recommend_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/recommend"
)

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
