// Package verify_test — checkDaemonLog coverage via temp files.
//
// User-scenario framing: each test mimics what the daemon would have
// written to ~/.cache/regen-watchd.log in a real session, then asks
// `verify.Run` whether the gate accepts or rejects it. Categories:
//
//   - clean run since the most recent startup → PASS
//   - loop / emergency / error lines since startup → FAIL with the
//     warning lines surfaced for operator triage
//   - daemon never ran (no startup marker) → ERROR
//   - log file absent (operator on a fresh machine) → ERROR
//   - historical warnings dropped on a fresh boot → PASS
//
// These mirror the real categories the operator sees in production —
// not abstract scanner-state probes.
package verify_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// writeDaemonLog drops a synthetic daemon log into t.TempDir() and
// returns the path — the same shape `verify --log-path <path>`
// consumes from an operator's machine.
func writeDaemonLog(t *testing.T, lines []string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "regen-watchd.log")
	body := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write log: %v", err)
	}
	return path
}

// runVerifyWithLog drives `verify.Run` against a minimal valid
// catalog with a benign bearcli backend stamped on ctx (plan-parity
// and apply-idempotency become hermetic no-ops). The daemon-log
// check is the only one with test-controlled state. Callers assert
// on the rendered text output by check-name to ignore the unrelated
// checks' verdicts.
func runVerifyWithLog(t *testing.T, logPath string) (string, error) {
	t.Helper()
	cfg := writeMinimalCatalog(t)
	ctx := ctxWithBenignBackend(t)
	var stdout, stderr strings.Builder
	err := verify.Run(ctx, verify.Options{
		ConfigPath: cfg,
		LogPath:    logPath,
		Output:     "text",
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	return stdout.String(), err
}

// findCheckLine extracts the rendered text line for `name` so a test
// can assert glyph + message without coupling to surrounding output.
// Used by daemon-log + plan-parity + apply-idempotency tests; the
// `name` parameter exists so a single helper serves all three.
func findCheckLine(t *testing.T, rendered, name string) string {
	t.Helper()
	for line := range strings.SplitSeq(rendered, "\n") {
		if strings.Contains(line, name) {
			return line
		}
	}
	t.Fatalf("check line for %q not found in rendered output:\n%s", name, rendered)
	return ""
}

// TestCheckDaemonLog_CleanRunSinceLastStart — operator scenario:
// the daemon restarted and has been running quietly. Verify accepts.
func TestCheckDaemonLog_CleanRunSinceLastStart(t *testing.T) {
	path := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
		"2026/05/18 10:00:01 regen[poetry]: complete (6 buckets, 800ms)",
		"2026/05/18 10:00:02 regen[lyrics]: unchanged",
		"2026/05/18 10:00:03 noxctl daemon ready",
	})
	rendered, _ := runVerifyWithLog(t, path)
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "✓") {
		t.Errorf("clean log must surface ✓ glyph; got line: %q", line)
	}
	if !strings.Contains(line, "clean since last daemon startup") {
		t.Errorf("expected 'clean since last daemon startup' marker; got: %q", line)
	}
}

// TestCheckDaemonLog_LoopDetectedAfterStart — operator scenario: a
// LOOP detected entry appeared post-startup. Gate must FAIL and
// surface the offending line so the operator can investigate.
func TestCheckDaemonLog_LoopDetectedAfterStart(t *testing.T) {
	path := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
		"2026/05/18 10:05:00 LOOP detected for note Я (rewrite_count=5)",
		"2026/05/18 10:05:01 regen[poetry]: complete",
	})
	rendered, err := runVerifyWithLog(t, path)
	if !errors.Is(err, verify.ErrVerifyFailed) {
		t.Fatalf("err = %v, want ErrVerifyFailed (daemon-log FAIL)", err)
	}
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "✗") {
		t.Errorf("expected ✗ glyph for FAIL; got: %q", line)
	}
	if !strings.Contains(line, "warning(s)") {
		t.Errorf("expected warning count in message; got: %q", line)
	}
	if !strings.Contains(rendered, "LOOP detected for note Я") {
		t.Errorf("expected offending line in Details for operator triage; rendered:\n%s", rendered)
	}
}

// TestCheckDaemonLog_SingleWarningCategories — each warning class
// (EMERGENCY DISABLE, ERROR:) must independently FAIL the gate so
// the operator never silently misses a single isolated occurrence.
// Table-driven so adding a new warning category (e.g. PANIC, FATAL)
// is one line of fixture data instead of a new test function.
func TestCheckDaemonLog_SingleWarningCategories(t *testing.T) {
	cases := []struct {
		name        string
		warningLine string
		wantInBody  string
	}{
		{
			name:        "EmergencyDisable",
			warningLine: "2026/05/18 10:30:00 domain-bootstrap: EMERGENCY DISABLE — 20 stuck notes",
			wantInBody:  "EMERGENCY DISABLE",
		},
		{
			name:        "ErrorLine",
			warningLine: "2026/05/18 10:15:00 regen[lyrics]: ERROR: bearcli timeout after 5s",
			wantInBody:  "ERROR:",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeDaemonLog(t, []string{
				"2026/05/18 10:00:00 regen-watchd starting",
				c.warningLine,
			})
			rendered, _ := runVerifyWithLog(t, path)
			line := findCheckLine(t, rendered, "daemon-log")
			if !strings.Contains(line, "✗") {
				t.Errorf("%s must FAIL the gate; got: %q", c.name, line)
			}
			if !strings.Contains(rendered, c.wantInBody) {
				t.Errorf("expected %q in Details; rendered:\n%s", c.wantInBody, rendered)
			}
		})
	}
}

// TestCheckDaemonLog_MultipleWarningsCounted — operator wants to see
// the FULL list of warnings, not just the first. Count surfaces in
// the message; lines surface in Details.
func TestCheckDaemonLog_MultipleWarningsCounted(t *testing.T) {
	path := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 regen-watchd starting",
		"2026/05/18 10:01:00 LOOP detected for note A",
		"2026/05/18 10:02:00 LOOP detected for note B",
		"2026/05/18 10:03:00 ERROR: bearcli timeout",
	})
	rendered, _ := runVerifyWithLog(t, path)
	if !strings.Contains(rendered, "3 warning(s)") {
		t.Errorf("expected count of 3 warnings; rendered:\n%s", rendered)
	}
}

// TestCheckDaemonLog_NoStartupMarkerEverPresent — operator on a
// fresh machine where the daemon binary hasn't run yet. Gate emits
// ERROR (verify cannot make a verdict), not FAIL.
func TestCheckDaemonLog_NoStartupMarkerEverPresent(t *testing.T) {
	path := writeDaemonLog(t, []string{
		"2026/05/18 10:00:00 some other process output",
		"2026/05/18 10:00:01 unrelated log noise",
	})
	rendered, _ := runVerifyWithLog(t, path)
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "⚠") {
		t.Errorf("missing-startup must surface ⚠ (ERROR), not ✗ (FAIL); got: %q", line)
	}
	if !strings.Contains(line, "daemon may have never started") {
		t.Errorf("expected operator-visible hint about daemon never running; got: %q", line)
	}
}

// TestCheckDaemonLog_PathDoesNotExist — operator on a fresh machine
// without ~/.cache/regen-watchd.log at all. Distinguish from
// "no startup marker" — different remediation.
func TestCheckDaemonLog_PathDoesNotExist(t *testing.T) {
	rendered, _ := runVerifyWithLog(t,
		"/tmp/noxctl-verify-test-nonexistent-log-xxxxxxx.log")
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "⚠") {
		t.Errorf("missing log file must surface ⚠ (ERROR); got: %q", line)
	}
	if !strings.Contains(line, "not found") {
		t.Errorf("expected 'not found' hint; got: %q", line)
	}
}

// TestCheckDaemonLog_PathIsDirectory_SurfacesScanError — operator
// misconfigured `--log-path` to point at a directory. `os.Open`
// succeeds but the subsequent `bufio.Scanner` read fails with EISDIR
// ("is a directory") on POSIX; `scanLogSinceStartup` propagates that
// as the scan-error branch, and the check surfaces ERROR with a
// "scan <path>:" hint that routes the operator to the path config.
func TestCheckDaemonLog_PathIsDirectory_SurfacesScanError(t *testing.T) {
	dirPath := t.TempDir() // an existing directory, not a file
	rendered, _ := runVerifyWithLog(t, dirPath)
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "⚠") {
		t.Errorf("directory-as-log must surface ⚠ (ERROR); got: %q", line)
	}
	if !strings.Contains(line, "scan ") {
		t.Errorf("expected 'scan <path>:' read-error hint; got: %q", line)
	}
}

// TestCheckDaemonLog_ScannerOverflow_SurfacesScanError — a single
// line larger than bufio.Scanner's default 64 KiB token cap triggers
// `scanner.Err() != nil` mid-rewind. `scanLogSinceStartup` propagates
// the error; checkDaemonLog must surface a "scan <path>: ..." StatusError
// so the operator sees the I/O class of failure, not the
// "no startup marker" fallback.
func TestCheckDaemonLog_ScannerOverflow_SurfacesScanError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "regen-watchd.log")
	huge := strings.Repeat("a", 2*1024*1024) // 2 MiB > default token cap
	body := "2026/05/18 10:00:00 regen-watchd starting\n" + huge + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write huge log: %v", err)
	}
	rendered, _ := runVerifyWithLog(t, path)
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "⚠") {
		t.Errorf("scanner-overflow must surface ⚠ (ERROR); got: %q", line)
	}
	if !strings.Contains(line, "scan ") {
		t.Errorf("expected 'scan <path>:' I/O-error hint; got: %q", line)
	}
}

// TestCheckDaemonLog_RewindsToLatestStartIgnoresOldWarnings — common
// operator scenario after a daemon restart: warnings from the OLD
// session are historical noise. Verify must rewind to the most
// recent startup and only count post-startup warnings.
func TestCheckDaemonLog_RewindsToLatestStartIgnoresOldWarnings(t *testing.T) {
	path := writeDaemonLog(t, []string{
		"2026/05/18 09:00:00 regen-watchd starting",
		"2026/05/18 09:30:00 LOOP detected for note Я — old session, ignore",
		"2026/05/18 10:00:00 regen-watchd starting",
		"2026/05/18 10:00:01 regen[poetry]: complete",
	})
	rendered, _ := runVerifyWithLog(t, path)
	line := findCheckLine(t, rendered, "daemon-log")
	if !strings.Contains(line, "✓") {
		t.Errorf("only post-latest-startup warnings should count; got: %q", line)
	}
	if strings.Contains(rendered, "old session") {
		t.Errorf("pre-latest-startup warning leaked into Details; rendered:\n%s", rendered)
	}
}
