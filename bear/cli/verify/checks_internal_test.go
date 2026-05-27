package verify

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/engine"
)

func TestClassifySecondApplyPass_FailureWinsOverWrites(t *testing.T) {
	second := &engine.ApplyResult{
		PrePasses: map[string]engine.PrePassCounts{
			"foreign_tag": {Changed: 1, Failed: 1},
		},
		Domains: map[string]engine.DomainCounts{
			"test/domain": {Changed: 1},
		},
	}

	check, done := classifySecondApplyPass("apply-idempotency", second)
	if !done {
		t.Fatal("done = false, want classification")
	}
	if check.Status != StatusError {
		t.Fatalf("status = %v, want StatusError", check.Status)
	}
	if strings.Contains(check.Message, "wrote") {
		t.Fatalf("message = %q, want runtime failure classification before write drift", check.Message)
	}
}
