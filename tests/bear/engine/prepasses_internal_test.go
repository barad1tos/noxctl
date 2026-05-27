package engine_test

import (
	"context"
	"errors"
	"testing"

	"github.com/barad1tos/noxctl/bear/engine"
)

func TestRunPrePass_ContextCancelDoesNotCountFailure(t *testing.T) {
	result := &engine.ApplyResult{PrePasses: make(map[string]engine.PrePassCounts)}
	engine.RunPrePassForTest(result, true, "foreign_tag", "foreign-tag escape", func() (engine.PrePassCounts, error) {
		return engine.PrePassCounts{Changed: 1}, context.Canceled
	})

	counts := result.PrePasses["foreign_tag"]
	if counts.Changed != 1 || counts.Failed != 0 {
		t.Fatalf("counts = %+v, want changed=1 failed=0", counts)
	}
	if !result.Interrupted {
		t.Fatal("Interrupted = false, want true for canceled pre-pass")
	}
}

func TestRunPrePass_PreservesStructuredFailureCount(t *testing.T) {
	result := &engine.ApplyResult{PrePasses: make(map[string]engine.PrePassCounts)}
	engine.RunPrePassForTest(result, true, "cross_domain", "cross-domain moves", func() (engine.PrePassCounts, error) {
		return engine.PrePassCounts{Changed: 1, Failed: 1}, errors.New("pre-pass failed")
	})

	counts := result.PrePasses["cross_domain"]
	if counts.Changed != 1 || counts.Failed != 1 {
		t.Fatalf("counts = %+v, want changed=1 failed=1 without double-counting", counts)
	}
}
