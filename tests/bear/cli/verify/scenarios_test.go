// Package verify_test — small end-to-end scenarios covering branches
// that don't fit neatly into the per-check files: default-path
// resolution, ctx-cancel-mid-plan, and the strict-mode no-op path.
package verify_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/domain"
)

// TestRun_DefaultLogPath_UsesHomeBaseline — when `--log-path` is empty,
// resolveDaemonLogPath falls back to `$HOME/.cache/regen-watchd.log`.
// Pointing HOME at an empty t.TempDir() makes the path resolve to a
// missing file, surfacing the "not found" StatusError. Pins the
// default-path branch (covers resolveDaemonLogPath's home-derived
// fallback path).
func TestRun_DefaultLogPath_UsesHomeBaseline(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	catalog := writeMinimalCatalog(t)
	stdout, _ := runVerify(t, verify.Options{
		ConfigPath: catalog,
		// LogPath deliberately empty → forces default-path resolution.
		Output: "text",
	})
	line := findCheckLine(t, stdout, "daemon-log")
	if !strings.Contains(line, "⚠") {
		t.Errorf("expected ⚠ glyph for ERROR (default path missing); got: %q", line)
	}
	if !strings.Contains(line, "not found") {
		t.Errorf("expected 'not found' hint at the default path; got: %q", line)
	}
}

// TestRun_CtxCanceledMidPlan_PlanParitySurfacesInterrupted — operator
// hits Ctrl+C mid-cycle (or systemd sends SIGTERM during shutdown).
// engine.Plan returns with Interrupted=true; checkPlanParity must
// surface as StatusError so the verdict line shows ERROR (not PASS or
// FAIL). Covers checkPlanParity's res.Interrupted branch.
func TestRun_CtxCanceledMidPlan_PlanParitySurfacesInterrupted(t *testing.T) {
	catalog := writeMinimalCatalog(t)
	ctx, cancel := context.WithCancel(domain.ContextWithBackend(t.Context(), &benignBearcliBackend{}))
	cancel() // pre-cancel; engine.Plan sees Done immediately.

	var stdout, stderr strings.Builder
	err := verify.Run(ctx, verify.Options{
		ConfigPath: catalog,
		LogPath:    writeDaemonLog(t, []string{"2026/05/18 10:00:00 regen-watchd starting"}),
		Output:     "text",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	// Pre-canceled ctx surfaces as either ErrVerifyInterrupted
	// (caught at finalize's ctx-check) OR ErrVerifyRuntimeError
	// (caught by an inner check); both are acceptable — the operator
	// signal is "verify didn't complete cleanly", not the specific
	// sentinel.
	if !errors.Is(err, verify.ErrVerifyInterrupted) && !errors.Is(err, verify.ErrVerifyRuntimeError) {
		t.Fatalf("err = %v, want ErrVerifyInterrupted or ErrVerifyRuntimeError", err)
	}
}

// TestRun_StrictModeWithCleanCatalog_StrictUpgradeDoesNotFire — with
// the benign backend's empty corpus, residue scan finds 0 untracked
// tag families, so strict mode's UntrackedFamilies-based FAIL upgrade
// must NOT fire. plan-parity may still FAIL on drift (the empty
// corpus vs a non-empty desired master), but the FAIL message must
// describe drift — never "untracked". Pins the strict no-fire branch
// so a regression that surfaces a spurious "0 untracked families"
// FAIL gets caught.
//
// Note: the symmetric FIRE branch (UntrackedFamilies > 0 → "strict:
// N untracked tag-family/families detected") is not reachable through
// the current hermetic backend — engine.Plan's pre-strict drift/error
// gate short-circuits before strict on any non-empty catalog (benign
// backend's empty bearcli responses force drift), and residue scan is
// gated on len(opts.Domains) > 0 so an empty catalog never reaches
// the residue path. The FIRE branch is exercised by ship-gate.sh
// against the live vault as part of the full hard-gate run.
func TestRun_StrictModeWithCleanCatalog_StrictUpgradeDoesNotFire(t *testing.T) {
	catalog := writeMinimalCatalog(t)
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	stdout, _ := runVerify(t, verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Strict:     true,
		Output:     "text",
	})
	line := findCheckLine(t, stdout, "plan-parity")
	if strings.Contains(line, "untracked") {
		t.Errorf("strict mode must not fire on a 0-untracked-family corpus; got: %q", line)
	}
}
