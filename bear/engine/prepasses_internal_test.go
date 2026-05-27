package engine

import (
	"context"
	"errors"
	"testing"
)

func TestRunPrePass_ContextCancelDoesNotCountFailure(t *testing.T) {
	result := &ApplyResult{PrePasses: make(map[string]PrePassCounts)}
	runPrePass(prePassSpec{
		enabled: true,
		name:    "foreign_tag",
		label:   "foreign-tag escape",
		fn: func() (PrePassCounts, error) {
			return PrePassCounts{Changed: 1}, context.Canceled
		},
	}, result)

	counts := result.PrePasses["foreign_tag"]
	if counts.Changed != 1 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want changed=1 failed=0", counts)
	}
	if !result.Interrupted {
		t.Fatal("Interrupted = false, want true for canceled pre-pass")
	}
}

func TestRunPrePass_PreservesStructuredFailureCount(t *testing.T) {
	result := &ApplyResult{PrePasses: make(map[string]PrePassCounts)}
	runPrePass(prePassSpec{
		enabled: true,
		name:    "cross_domain",
		label:   "cross-domain moves",
		fn: func() (PrePassCounts, error) {
			return PrePassCounts{Changed: 1, Failed: 1}, errors.New("pre-pass failed")
		},
	}, result)

	counts := result.PrePasses["cross_domain"]
	if counts.Changed != 1 || counts.Failed != 1 {
		t.Fatalf("counts = %+v, want changed=1 failed=1 without double-counting", counts)
	}
}
