// Package verify_test — checkApplyIdempotency coverage.
//
// User-scenario framing: the destructive `--with-apply` leg is the
// hard-to-mock half of verify because engine.Apply walks every
// bearcli command shape (list / show / overwrite / create / etc.)
// against multiple domain blueprints. The benign test backend
// returns shape-valid responses for the common commands but won't
// satisfy the full per-domain regen surface for every blueprint —
// hermetic tests therefore focus on operator-realistic ERROR paths
// (the cases an operator most needs the gate to catch loudly), not
// the all-clean PASS path that depends on backend realism. The PASS
// path is exercised end-to-end by `bash scripts/ship-gate.sh
// --with-apply` against Roman's live vault.
package verify_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// TestRun_OperatorRunsWithApply_NilApplyOpts_SurfacesError —
// regression-pin for the Sourcery iter-#3 finding: with `--with-apply`
// set but `Options.ApplyOpts` left at zero value (no LockPath /
// StatePath / Pins / Features), `engine.Apply` errors at
// `AcquireApply` because `LockPath=""`. The check MUST surface as
// StatusError so the cmd-layer exit-code dispatch routes to exit 1
// (gate could not ask) rather than exit 0 (gate said yes).
func TestRun_OperatorRunsWithApply_NilApplyOpts_SurfacesError(t *testing.T) {
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	cfg := writeMinimalCatalog(t)
	stdout, err := runVerify(t, verify.Options{
		ConfigPath: cfg,
		LogPath:    logPath,
		WithApply:  true,
		// ApplyOpts deliberately left at zero-value to reproduce
		// the "caller forgot to prime it" failure mode the Sourcery
		// iter-#3 review caught in PR #6.
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
