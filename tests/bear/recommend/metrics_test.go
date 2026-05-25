package recommend_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/recommend"
)

func TestComputeMetrics_SubtagBuckets(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "A", Tags: []string{"#english", "#english/homework"}},
		{ID: "2", Title: "B", Tags: []string{"#english", "#english/homework"}},
		{ID: "3", Title: "C", Tags: []string{"#english", "#english/vocab"}},
	}
	m := recommend.ComputeMetrics("english", notes, nil)
	if m.TagDepth != 1 {
		t.Errorf("TagDepth = %d, want 1", m.TagDepth)
	}
	if m.NoteCount != 3 {
		t.Errorf("NoteCount = %d, want 3", m.NoteCount)
	}
	if m.BucketCardinality != 2 {
		t.Errorf("BucketCardinality = %d, want 2 (homework, vocab)", m.BucketCardinality)
	}
	if m.SubtagCoverage != 1.0 {
		t.Errorf("SubtagCoverage = %v, want 1.0", m.SubtagCoverage)
	}
	if m.AtomsPerBucket != 1 {
		t.Errorf("AtomsPerBucket(median of [2,1]) = %d, want 1", m.AtomsPerBucket)
	}
	if got := strings.Join(m.Buckets, ","); got != "homework,vocab" {
		t.Errorf("Buckets = %q, want sorted homework,vocab", got)
	}
}
