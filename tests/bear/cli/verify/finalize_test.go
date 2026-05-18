package verify_test

import (
	"bytes"
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

// TestSentinels_Distinct documents the two-sentinel contract: the
// exit-code dispatcher at cmd/noxctl/main.go branches on
// errors.Is(err, errVerifyFailed) vs errors.Is(err, errVerifyRuntime)
// — those mappings depend on the package-level sentinels being
// distinct error values.
func TestSentinels_Distinct(t *testing.T) {
	if errors.Is(verify.ErrVerifyFailed, verify.ErrVerifyRuntimeError) {
		t.Errorf("ErrVerifyFailed must NOT be identity-equal to ErrVerifyRuntimeError")
	}
	if errors.Is(verify.ErrVerifyRuntimeError, verify.ErrVerifyFailed) {
		t.Errorf("ErrVerifyRuntimeError must NOT be identity-equal to ErrVerifyFailed")
	}
}
