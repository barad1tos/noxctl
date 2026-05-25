package recommend_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/recommend"
)

func TestComputeMetrics_AuthorSignal(t *testing.T) {
	notes := []domain.Note{
		{ID: "1", Title: "Poem A", Content: "#library/poetry\n---\n## Frost\n- line"},
		{ID: "2", Title: "Poem B", Content: "#library/poetry\n---\n## Rilke\n- line"},
		{ID: "3", Title: "Plain", Content: "#library/poetry\n---\njust prose, no H2"},
	}
	m := recommend.ComputeMetrics("library/poetry", notes, nil)
	if m.BodyAuthorSignal <= 0.6 || m.BodyAuthorSignal > 0.67 {
		t.Errorf("BodyAuthorSignal = %v, want ~0.667 (2 of 3 have an H2)", m.BodyAuthorSignal)
	}
}

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
	if m.BucketCoverage != 1.0 {
		t.Errorf("BucketCoverage = %v, want 1.0", m.BucketCoverage)
	}
	if m.AtomsPerBucket != 1 {
		t.Errorf("AtomsPerBucket(median of [2,1]) = %d, want 1", m.AtomsPerBucket)
	}
	if got := strings.Join(m.Buckets, ","); got != "homework,vocab" {
		t.Errorf("Buckets = %q, want sorted homework,vocab", got)
	}
}
