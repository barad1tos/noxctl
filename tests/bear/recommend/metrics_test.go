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

// TestComputeMetrics_ExcludesManagedMaster scans a real-shaped note set that
// includes the managed master note (titled with the index marker, whose
// canonical line carries a "new note" bear:// link). The master must NOT be
// counted as a note nor its link parsed as a bucket — otherwise a flat-list
// tag is mis-read as bucketed. This pins the gap the synthetic calibration
// table could not catch (it never ran ComputeMetrics on real notes).
func TestComputeMetrics_ExcludesManagedMaster(t *testing.T) {
	notes := []domain.Note{
		{
			ID: "m", Title: "✱ Rules", Tags: []string{"#llm/rules"},
			Content: "# ✱ Rules\n#llm/rules | [[✱ Rules]] | [New note](bear://x-callback-url/create?x=1)\n---\n- [[A]]",
		},
		{ID: "1", Title: "Rule A", Tags: []string{"#llm/rules"}, Content: "#llm/rules\nbody one"},
		{ID: "2", Title: "Rule B", Tags: []string{"#llm/rules"}, Content: "#llm/rules\nbody two"},
	}
	m := recommend.ComputeMetrics("llm/rules", notes, nil)
	if m.NoteCount != 2 {
		t.Errorf("NoteCount = %d, want 2 (managed master excluded)", m.NoteCount)
	}
	if m.BucketCardinality != 0 {
		t.Errorf("BucketCardinality = %d, want 0 (master link is not a bucket); buckets=%v", m.BucketCardinality, m.Buckets)
	}
	if got := recommend.Recommend(m).Blueprint; got != "flat-list" {
		t.Errorf("Recommend = %q, want flat-list", got)
	}
}
