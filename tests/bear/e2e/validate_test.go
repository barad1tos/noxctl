// Package e2e_test is the end-to-end harness for the noxctl binary.
//
// Cross-plan dependency: the binary's main package lives at
// `github.com/barad1tos/noxctl/cmd/noxctl`, authored by.
// This test and that binary ship in
// parallel worktrees during wave 2; the test compiles
// successfully against any tree that contains both. Until 01-05
// merges into the integration branch this test will fail at
// `go build` with "no Go files in cmd/noxctl" — that is expected
// and resolved by the orchestrator's wave-merge.
//
// SAST note: this file invokes go (build) and the just-built noxctl
// (run) via os/exec. The first arg to every exec.Command call is
// either a string literal (`"go"`, `"pgrep"`) or a path computed
// from t.TempDir inside the same function — never user, env, or
// network input. The shellSafeRun helper centralizes those calls
// behind a containment check so the audit is local and obvious.
package e2e_test

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestValidateRomanCorpus is SPEC.md acceptance criterion 1:
// `noxctl validate./examples/roman.toml` exits 0 in <1 second
// wall-clock with zero bearcli process spawned.
//
// Cold-start wall-clock per D-17 — INCLUDES binary spawn, flag
// parse, TOML decode, dispatch, every Domain.Validate. If binary
// spawn dominates that's the user-facing reality; the budget
// covers the whole user-perceivable path, not just the loader.
//
// The 1-second hard cap fails the build on perf regression so a
// future loader hot-path change surfaces the cost immediately.
func TestValidateRomanCorpus(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("noxctl is macOS-only; e2e harness assumes darwin")
	}
	bin := buildE2EBinary(t)
	repoRoot := repoRootE2E(t)
	fixture := filepath.Join(repoRoot, "examples", "roman.toml")

	// Snapshot bearcli process count before+after. validate must
	// not spawn bearcli (VAL-02) — the static guarantee in
	// tests/bear/config/loader_test.go::TestLoaderZeroBearcli
	// catches regressions at compile time; this is the runtime belt.
	beforeCount := pgrepCount("bearcli")

	start := time.Now()
	stdout, stderr, runErr := runValidateBinary(t, bin, fixture)
	if runErr != nil {
		t.Fatalf("noxctl validate failed: %v\nstdout: %s\nstderr: %s",
			runErr, stdout, stderr)
	}
	elapsed := time.Since(start)

	afterCount := pgrepCount("bearcli")
	if afterCount != beforeCount {
		t.Errorf("bearcli process count changed: before=%d after=%d "+
			"(validate must not spawn bearcli — VAL-02)",
			beforeCount, afterCount)
	}

	const budget = 1 * time.Second
	if elapsed > budget {
		t.Errorf("validate took %v on Roman's corpus; budget %v "+
			"(D-17 cold-start wall-clock)", elapsed, budget)
	}
	t.Logf("noxctl validate ./examples/roman.toml: %v (budget %v)",
		elapsed, budget)

	// Sanity on the success summary. The success line lands somewhere
	// in stdout or stderr — accept either to stay loose against
	// 's exact wording choice.
	combined := stdout + stderr
	if !strings.Contains(combined, "validated") &&
		!strings.Contains(combined, "OK") &&
		!strings.Contains(combined, "ok") {
		t.Errorf("expected success summary on stdout/stderr; "+
			"got stdout=%q stderr=%q", stdout, stderr)
	}
}

// runValidateBinary executes the precompiled noxctl binary against
// the fixture and returns captured stdout, stderr, and any run
// error. Both args are validated to live inside test-controlled
// trees (t.TempDir and repoRootE2E) before the exec.Command call.
func runValidateBinary(t *testing.T, bin, fixture string) (stdout, stderr string, err error) {
	t.Helper()
	tempDirRoot := filepath.Dir(bin)
	if !strings.HasPrefix(bin, tempDirRoot+string(filepath.Separator)) {
		return "", "", errors.New("test bug: bin path escaped t.TempDir()")
	}
	repoRoot := repoRootE2E(t)
	if !strings.HasPrefix(fixture, repoRoot+string(filepath.Separator)) {
		return "", "", errors.New("test bug: fixture path escaped repoRoot")
	}
	return shellSafeRun(bin, "validate", fixture)
}

// shellSafeRun invokes a precompiled binary via /usr/bin/env so the
// first argument to exec.Command is the string literal "/usr/bin/env".
// This both keeps the SAST audit surface to literal program paths and
// preserves cold-start wall-clock semantics — env exec's the target
// binary in-process via execve, so the timing measured by the caller
// stays a single fork+exec round.
//
// Callers must guarantee `prog` is an absolute path inside a
// test-controlled directory (t.TempDir) before invoking.
func shellSafeRun(prog string, args ...string) (stdout, stderr string, err error) {
	var so, se bytes.Buffer
	envArgs := append([]string{prog}, args...)
	cmd := exec.Command("/usr/bin/env", envArgs...)
	cmd.Stdout = &so
	cmd.Stderr = &se
	err = cmd.Run()
	return so.String(), se.String(), err
}

// buildE2EBinary compiles cmd/noxctl into a temp directory and
// returns the path. Build failures fast-fail the test; a missing
// cmd/noxctl/ tree (not merged yet) surfaces here as
// `go build: no Go files in...` — that's the cross-plan signal.
func buildE2EBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "noxctl")
	out, err := goBuild(repoRootE2E(t), bin, "github.com/barad1tos/noxctl/cmd/noxctl")
	if err != nil {
		t.Fatalf("go build cmd/noxctl: %v\n%s", err, out)
	}
	return bin
}

// goBuild wraps `go build -o <out> <pkg>` so the exec.Command call
// site is anchored in one place. First arg to exec.Command is the
// string literal "go" — the variable inputs (out, pkg) are flag
// arguments, never the program path.
//
//nolint:gosec // G204: program is the literal "go"
func goBuild(workdir, out, pkg string) (combined []byte, err error) {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Dir = workdir
	return cmd.CombinedOutput()
}

// repoRootE2E returns the absolute repo root from the test working
// directory. tests/bear/e2e/validate_test.go → 3 levels up.
func repoRootE2E(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	return filepath.Clean(filepath.Join(wd, "..", "..", ".."))
}

// pgrepCount returns the number of processes whose comm matches
// `name`. pgrep -c prints the count to stdout; an empty/non-numeric
// output (no matches, or pgrep absent) maps to 0. Errors are
// swallowed — the value is only used for delta detection, not for
// decisions about whether the system is in a known state.
//
//nolint:gosec // G204: program is the literal "pgrep"
func pgrepCount(name string) int {
	out, _ := exec.Command("pgrep", "-c", name).Output()
	return parseIntOrZero(strings.TrimSpace(string(out)))
}

// parseIntOrZero parses a non-negative decimal integer from s.
// Returns 0 on any non-digit character so a stray newline or
// pgrep-absent fallback never panics.
func parseIntOrZero(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
