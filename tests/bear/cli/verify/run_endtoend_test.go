// Package verify_test — end-to-end `verify.Run` coverage.
//
// User-scenario framing: every test is "operator runs `noxctl verify`
// in <state>; the gate's verdict and exit-code sentinel must match
// what CI / cron / ship-gate.sh consumers depend on". The point is
// to exercise the FULL pipeline (catalog load → render dispatch)
// with realistic state, not poke individual helpers in isolation.
//
// Most of these tests use the `benignBearcliBackend` from
// testfixture_test.go so plan-parity / apply-idempotency complete
// without hitting a real Bear. Daemon-log uses temp files.
package verify_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/engine"
)

// runVerify drives verify.Run with the supplied options + ctx and
// returns the rendered stdout + the sentinel-shaped error. Stderr is
// captured into a buffer and reported via t.Logf when non-empty so a
// future warning emitted there is visible under `go test -v` instead
// of being silently absorbed. Used by every test in this file.
func runVerify(t *testing.T, opts verify.Options) (stdout string, err error) {
	t.Helper()
	var outBuf, errBuf bytes.Buffer
	opts.Stdout = &outBuf
	opts.Stderr = &errBuf
	err = verify.Run(ctxWithBenignBackend(t), opts)
	if errBuf.Len() > 0 {
		t.Logf("verify.Run wrote %d bytes to stderr:\n%s",
			errBuf.Len(), errBuf.String())
	}
	return outBuf.String(), err
}

// TestRun_OperatorRunsWithBadConfigPath_ReturnsRuntimeError — operator
// supplied a `--config <path>` that doesn't exist. Gate ERRORs (not
// FAILs) because verify can't ask the parity question — caller
// branches on exit 1 (ERROR), not exit 2 (FAIL).
func TestRun_OperatorRunsWithBadConfigPath_ReturnsRuntimeError(t *testing.T) {
	stdout, err := runVerify(t, verify.Options{
		ConfigPath: "/tmp/noxctl-verify-test-bad-config-xxxxx.toml",
		Output:     "text",
	})
	if !errors.Is(err, verify.ErrVerifyRuntimeError) {
		t.Errorf("err = %v, want ErrVerifyRuntimeError", err)
	}
	catalogLine := findCheckLine(t, stdout, "catalog-load")
	if !strings.Contains(catalogLine, "⚠") {
		t.Errorf("expected ⚠ glyph for ERROR; got: %q", catalogLine)
	}
	if !strings.Contains(stdout, "verify: ERROR") {
		t.Errorf("expected overall verdict ERROR; rendered:\n%s", stdout)
	}
}

// TestRun_OperatorRunsClean_ApplyIdempotencySkipped — operator
// invokes verify without --with-apply. apply-idempotency check
// MUST surface as Skipped (not Pass) so the operator knows the
// destructive leg was not exercised. Surface via • glyph.
func TestRun_OperatorRunsClean_ApplyIdempotencySkipped(t *testing.T) {
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
		"2026/05/18 10:00:01 regen[poetry]: complete",
	})
	catalog := writeMinimalCatalog(t)
	stdout, _ := runVerify(t, verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Output:     "text",
	})
	idem := findCheckLine(t, stdout, "apply-idempotency")
	if !strings.Contains(idem, "•") {
		t.Errorf("opt-out --with-apply must surface • glyph; got: %q", idem)
	}
	if !strings.Contains(idem, "opt-in via --with-apply") {
		t.Errorf("operator must see how to enable the check; got: %q", idem)
	}
}

// TestRun_ReadOnlyVerifyWaitsForDaemonLock — even without
// --with-apply, plan-parity fans out many bearcli reads. It must
// serialize with the daemon's cycle lock so ship-gate does not race a
// live regen cycle and misreport a transient bearcli abort as a
// verify runtime error.
func TestRun_ReadOnlyVerifyWaitsForDaemonLock(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	daemonRelease, err := engine.AcquireDaemon(t.Context(), lockPath)
	if err != nil {
		t.Fatalf("AcquireDaemon: %v", err)
	}

	const hold = 150 * time.Millisecond
	released := make(chan struct{})
	go func() {
		time.Sleep(hold)
		daemonRelease()
		close(released)
	}()

	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	catalog := writeMinimalCatalog(t)
	var outBuf, errBuf bytes.Buffer
	err = verify.Run(ctxWithBenignBackend(t), verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Output:     "text",
		Stdout:     &outBuf,
		Stderr:     &errBuf,
		ApplyOpts: engine.ApplyOpts{
			LockPath: lockPath,
		},
	})
	if err != nil && !errors.Is(err, verify.ErrVerifyFailed) {
		t.Fatalf("verify.Run: %v\nstdout:\n%s\nstderr:\n%s", err, outBuf.String(), errBuf.String())
	}
	select {
	case <-released:
	default:
		t.Fatalf("verify returned before daemon lock release")
	}
	if !strings.Contains(errBuf.String(), "waiting for lock") {
		t.Errorf("expected lock wait advisory on stderr; got:\n%s", errBuf.String())
	}
	sentinel := filepath.Join(dir, engine.SentinelName)
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("verify left apply-pending sentinel behind: %v", statErr)
	}
}

func TestRun_ReadOnlyVerifyLockErrorIsRuntimeError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	if err := os.Symlink(filepath.Join(dir, "target"), lockPath); err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	catalog := writeMinimalCatalog(t)
	var outBuf bytes.Buffer
	err := verify.Run(ctxWithBenignBackend(t), verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Output:     "text",
		Stdout:     &outBuf,
		ApplyOpts: engine.ApplyOpts{
			LockPath: lockPath,
		},
	})
	if !errors.Is(err, verify.ErrVerifyRuntimeError) {
		t.Fatalf("err = %v, want ErrVerifyRuntimeError\nstdout:\n%s", err, outBuf.String())
	}
	if !strings.Contains(outBuf.String(), "verify-lock") {
		t.Fatalf("expected verify-lock check in output; got:\n%s", outBuf.String())
	}
	sentinel := filepath.Join(dir, engine.SentinelName)
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("verify left apply-pending sentinel behind: %v", statErr)
	}
}

// TestRun_OperatorRequestsJSON_OutputIsParseable — operator pipes
// `verify -o json` to jq / a tooling consumer. Output must be valid
// JSON, contain schema_version=1, and the four expected fields.
func TestRun_OperatorRequestsJSON_OutputIsParseable(t *testing.T) {
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	catalog := writeMinimalCatalog(t)
	stdout, _ := runVerify(t, verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Output:     "json",
	})
	var decoded struct {
		SchemaVersion int            `json:"schema_version"`
		Checks        []verify.Check `json:"checks"`
		Summary       verify.Summary `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &decoded); err != nil {
		t.Fatalf("operator's jq would fail: invalid JSON: %v\nstdout:\n%s", err, stdout)
	}
	if decoded.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", decoded.SchemaVersion)
	}
	if len(decoded.Checks) != 3 {
		t.Errorf("len(checks) = %d, want 3 (plan-parity, daemon-log, apply-idempotency)",
			len(decoded.Checks))
	}
}

// TestRun_OperatorRequestsBadOutputFormat_ReturnsValidationError —
// `-o yaml` is not supported; render() must reject before writing
// anything. The error is a render-layer error, NOT one of the
// pass-through sentinels (no exit-code 2 dispatch).
func TestRun_OperatorRequestsBadOutputFormat_ReturnsValidationError(t *testing.T) {
	catalog := writeMinimalCatalog(t)
	_, err := runVerify(t, verify.Options{
		ConfigPath: catalog,
		Output:     "yaml",
	})
	if err == nil {
		t.Fatalf("err = nil, want validation error for unsupported -o yaml")
	}
	// MUST NOT be one of the three verdict sentinels — output
	// validation is a CLI-flag error, not a gate verdict.
	for _, sentinel := range []error{
		verify.ErrVerifyFailed,
		verify.ErrVerifyRuntimeError,
		verify.ErrVerifyInterrupted,
	} {
		if errors.Is(err, sentinel) {
			t.Errorf("invalid -o must not surface as %v; got %v", sentinel, err)
		}
	}
}

// TestRun_OperatorRunsDirtyLog_OverallVerdictReflectsFail — full
// pipeline: dirty daemon log → FAIL surfaces in overall verdict +
// sentinel. The other checks (plan-parity, apply-idempotency) are
// PASS under the benign backend; daemon-log is the sole driver.
func TestRun_OperatorRunsDirtyLog_OverallVerdictReflectsFail(t *testing.T) {
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
		"2026/05/18 10:01:00 LOOP detected for note Q",
	})
	catalog := writeMinimalCatalog(t)
	stdout, err := runVerify(t, verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Output:     "text",
	})
	if !errors.Is(err, verify.ErrVerifyFailed) {
		t.Fatalf("err = %v, want ErrVerifyFailed", err)
	}
	if !strings.Contains(stdout, "verify: FAIL") {
		t.Errorf("expected 'verify: FAIL' verdict; rendered:\n%s", stdout)
	}
}

// TestRun_OperatorWithStrictMode_FlagSurfacesInOpts — strict mode
// is a published flag; this test pins that toggling it does not
// crash the runner. The actual strict-failure-on-untracked path
// needs a vault with notes outside the catalog; here we just verify
// the option flows through without breaking the no-untracked path.
func TestRun_OperatorWithStrictMode_FlagSurfacesInOpts(t *testing.T) {
	logPath := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
	})
	catalog := writeMinimalCatalog(t)
	stdout, _ := runVerify(t, verify.Options{
		ConfigPath: catalog,
		LogPath:    logPath,
		Output:     "text",
		Strict:     true,
	})
	if !strings.Contains(stdout, "plan-parity") {
		t.Errorf("expected plan-parity check line in strict-mode output; rendered:\n%s", stdout)
	}
}

// TestRun_NilStdout_DefaultsApplied — `verify.Run` defends against
// callers that forgot to set Stdout/Stderr. defaultIOAndPool fills
// `os.Stdout` / `os.Stderr` so render() doesn't panic on a nil
// writer. Test passes nil for both and asserts the call completes
// without panic.
func TestRun_NilStdout_DefaultsApplied(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("panic with nil stdout/stderr: %v", r)
		}
	}()
	catalog := writeMinimalCatalog(t)
	// Don't use runVerify helper here — it always sets stdout/stderr.
	// Run directly with nil to exercise the default-injection path.
	_ = verify.Run(ctxWithBenignBackend(t), verify.Options{
		ConfigPath: catalog,
		Output:     "text",
		// Stdout & Stderr deliberately omitted — must default to
		// os.Stdout / os.Stderr without panic.
	})
}
