// Package verify_test — checkApplyIdempotency error-path coverage
// driven through verify.Run (vs the direct unit tests in
// apply_idempotency_unit_test.go).
package verify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// TestRun_OperatorRunsWithApply_NilApplyOpts_SurfacesError —
// regression-pin: with `--with-apply` set but `Options.ApplyOpts`
// left at zero value (no LockPath / StatePath / Pins / Features),
// `engine.Apply` errors at `AcquireApply` because `LockPath=""`. The
// check MUST surface as StatusError so the cmd-layer exit-code
// dispatch routes to exit 1 (gate could not ask) rather than exit 0
// (gate said yes).
func TestRun_OperatorRunsWithApply_NilApplyOpts_SurfacesError(t *testing.T) {
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	catalog := writeMinimalCatalog(t)
	stdout, err := runVerify(t, verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		WithApply:  true,
		// ApplyOpts deliberately left at zero-value to reproduce
		// the "caller forgot to prime it" failure mode.
		Output: "text",
	})
	if !errors.Is(err, verify.ErrVerifyRuntimeError) {
		t.Errorf("expected ErrVerifyRuntimeError on missing ApplyOpts; got %v", err)
	}
	line := findCheckLine(t, stdout, "apply-idempotency")
	if !strings.Contains(line, "⚠") {
		t.Errorf("expected ⚠ glyph for ERROR; got: %q", line)
	}
	if !strings.Contains(line, "first apply failed") {
		t.Errorf("expected 'first apply failed' message so the operator routes "+
			"to the daemon log; got: %q", line)
	}
}
