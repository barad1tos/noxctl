package verify_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// runVerifyWithChecks invokes verify.Run via a tiny shim that
// short-circuits the catalog load (passing an obviously-invalid
// ConfigPath forces a catalog-load StatusError) so the test can
// assert the sentinel dispatch without touching bearcli. Returns the
// error verify.Run produced + the buffered output.
//
// Not exhaustive of verify.Run's real shape — but the only behavior
// the test cares about is "given N status of each kind, which
// sentinel comes out?" and the catalog-load StatusError is a
// canonical example of that mapping. The full per-check coverage
// lives in the (gated) operator-side ship-gate runs.
func runVerifyForCatalogError(t *testing.T) error {
	t.Helper()
	var stdout, stderr bytes.Buffer
	return verify.Run(t.Context(), verify.Options{
		// Non-existent path → config.Load errors → verify emits a
		// single catalog-load StatusError check and falls into
		// finalize early. Summary.Error == 1 → ErrVerifyRuntimeError.
		ConfigPath: "/tmp/noxctl-test-nonexistent-config-xxxxx.toml",
		Output:     "text",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
}

// TestRun_CatalogLoadError_ReturnsRuntimeErrorSentinel pins the
// finalize() exit-code contract for the StatusError dispatch path.
// Catalog-load failure is the cleanest hermetic trigger — no
// bearcli, no daemon log, no live vault required.
func TestRun_CatalogLoadError_ReturnsRuntimeErrorSentinel(t *testing.T) {
	err := runVerifyForCatalogError(t)
	if err == nil {
		t.Fatalf("Run err = nil, want ErrVerifyRuntimeError")
	}
	if !errors.Is(err, verify.ErrVerifyRuntimeError) {
		t.Errorf("Run err = %v, want errors.Is == ErrVerifyRuntimeError", err)
	}
	// Pin the cross-direction too: the runtime-error sentinel must
	// NOT match the gate-failed sentinel. Without this, a regression
	// that re-uses one sentinel for both kinds passes silently.
	if errors.Is(err, verify.ErrVerifyFailed) {
		t.Errorf("ErrVerifyRuntimeError must NOT match ErrVerifyFailed via errors.Is")
	}
}

// TestSentinels_Distinct documents the three-sentinel contract: the
// exit-code dispatcher at cmd/noxctl/main.go branches on
// errors.Is(err, errVerifyFailed) vs errors.Is(err, errVerifyRuntime)
// vs errors.Is(err, errInterrupted) — those mappings depend on the
// package-level sentinels being distinct error values.
func TestSentinels_Distinct(t *testing.T) {
	cases := []struct {
		name string
		a, b error
	}{
		{"FailedVsRuntime", verify.ErrVerifyFailed, verify.ErrVerifyRuntimeError},
		{"RuntimeVsFailed", verify.ErrVerifyRuntimeError, verify.ErrVerifyFailed},
		{"InterruptedVsFailed", verify.ErrVerifyInterrupted, verify.ErrVerifyFailed},
		{"InterruptedVsRuntime", verify.ErrVerifyInterrupted, verify.ErrVerifyRuntimeError},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if errors.Is(c.a, c.b) {
				t.Errorf("%v MUST NOT match %v via errors.Is", c.a, c.b)
			}
		})
	}
}

// TestRun_CtxCanceledOnEntry_ReturnsInterruptedSentinel pins the
// "interrupted trumps verdict" contract in finalize. Pre-cancel the
// ctx before Run, and even though the catalog-load fails (would
// otherwise return ErrVerifyRuntimeError), the ctx-cancellation
// check wins and ErrVerifyInterrupted comes out. cmd/noxctl/main.go
// then maps this to ExitInterrupted = 130 via errInterrupted.
func TestRun_CtxCanceledOnEntry_ReturnsInterruptedSentinel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // pre-cancel before Run runs any check
	var stdout, stderr bytes.Buffer
	err := verify.Run(ctx, verify.Options{
		ConfigPath: "/tmp/noxctl-test-nonexistent-config-xxxxx.toml",
		Output:     "text",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if err == nil {
		t.Fatalf("Run err = nil, want ErrVerifyInterrupted")
	}
	if !errors.Is(err, verify.ErrVerifyInterrupted) {
		t.Errorf("Run err = %v, want errors.Is == ErrVerifyInterrupted", err)
	}
	// Make sure the dispatch priority is correct: interrupted
	// trumps the catalog-load StatusError that would otherwise
	// surface as ErrVerifyRuntimeError.
	if errors.Is(err, verify.ErrVerifyRuntimeError) {
		t.Errorf("interrupted ctx must NOT surface as ErrVerifyRuntimeError; got %v", err)
	}
}
