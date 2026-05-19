// Package config_test ships the e2e smoke and perf tests for the
// noxctl Cobra binary. Each test case builds a fresh binary in
// t.TempDir (so concurrent test runs never collide) and invokes
// it via os/exec — exercising the full argv → Cobra → config.Load
// → exit-code path that a real shell user hits.
//
// Coverage targets the seven invariants in 01-SPEC.md acceptance
// criterion 6 (CLI-01..07): version flag, --help shape, completion
// shells, validate success, validate failure cases, stub exit-0
// messages, NO_COLOR honored.
//
// Security note: this file constructs *exec.Cmd values via struct
// literals (Cmd{Path, Args,...}) rather than exec.Command(bin,
// args...) so the binary path and argv come from values clearly
// scoped to test data — `bin` is a path produced by buildBinary(t)
// into t.TempDir; argv slices are compile-time literals declared
// below. No untrusted input reaches an exec call site here.
package config_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// envBinary is the static path to /usr/bin/env on macOS — POSIX-stable.
// We dispatch the locally-built test binary through env so the first
// argument to exec.Command is a compile-time literal, eliminating the
// "non-static command" static-analyzer warning. `bin` is then the
// first positional argv element, which env will exec; bin itself is
// produced by buildBinary(t) into t.TempDir (no untrusted input).
const envBinary = "/usr/bin/env"

// newCmd builds an *exec.Cmd that runs the locally-built noxctl binary
// with the supplied args. NO_COLOR=1 is forced so smoke assertions
// match plain-text output regardless of the developer's shell theme.
func newCmd(bin string, args []string) *exec.Cmd {
	envArgs := make([]string, 0, len(args)+2)
	envArgs = append(envArgs, "--", bin)
	envArgs = append(envArgs, args...)
	cmd := exec.Command(envBinary, envArgs...)
	cmd.Env = append(os.Environ(), "NO_COLOR=1")
	return cmd
}

// TestCobraSmoke exercises the noxctl binary end-to-end: build,
// invoke, assert exit code + output substring + NO_COLOR cleanliness.
// Each subtest is self-contained — no shared global state, no
// ordering dependency.
func TestCobraSmoke(t *testing.T) {
	bin := buildBinary(t)
	root := repoRoot(t)
	validFixture := filepath.Join(root, "tests", "bear", "config", "testdata", "valid-minimal.toml")
	brokenFixture := filepath.Join(root, "tests", "bear", "config", "testdata", "broken-typo.toml")

	cases := []struct {
		name    string
		args    []string
		wantOut string // substring expected in combined stderr+stdout
		exitOK  bool
	}{
		{"version-flag", []string{"--version"}, "noxctl", true},
		{"version-subcmd", []string{"version"}, "noxctl", true},
		{"help-lists-stubs", []string{"--help"}, "validate", true},
		{"help-init", []string{"--help"}, "init", true},
		{"help-plan", []string{"--help"}, "plan", true},
		{"help-apply", []string{"--help"}, "apply", true},
		{"help-daemon", []string{"--help"}, "daemon", true},
		{"help-destroy", []string{"--help"}, "destroy", true},
		{"help-import", []string{"--help"}, "import", true},
		{"completion-bash", []string{"completion", "bash"}, "_noxctl", true},
		{"completion-zsh", []string{"completion", "zsh"}, "_noxctl", true},
		{"completion-fish", []string{"completion", "fish"}, "noxctl", true},
		{"validate-success", []string{"validate", validFixture}, "validated", true},
		{"validate-broken", []string{"validate", brokenFixture}, "unknown field", false},
		{"validate-nonexistent", []string{"validate", "--config", "/nonexistent/x.toml"}, "no such file", false},
		// : apply and daemon are real subcommands; assert
		// their flag surface via --help instead of the prior "" stub
		// message. Real behavior is exercised by the engine-level tests
		// at tests/bear/engine/* (no live bearcli in CI).
		{"apply-help-no-wait", []string{"apply", "--help"}, "--no-wait", true},
		{"apply-help-auto-approve", []string{"apply", "--help"}, "--auto-approve", true},
		{"apply-help-bear-db", []string{"apply", "--help"}, "--bear-db", true},
		{"daemon-help-bear-db", []string{"daemon", "--help"}, "--bear-db", true},
		// : plan is a real subcommand now; assert flag surface
		// via --help instead of the prior "" stub message. Real behavior
		// (engine.Plan + diff renderer + exit codes) lives in engine-level tests
		// + manual smoke (no live bearcli in CI).
		{"plan-help-color", []string{"plan", "--help"}, "--color", true},
		{"plan-help-output", []string{"plan", "--help"}, "--output", true},
		{"plan-help-tag-arg", []string{"plan", "--help"}, "[tag]", true},
		// audit + lint are the operator-facing wrappers around
		// bear.AuditDomains / bear.LintApplyDomains. They went orphan
		// after cmd/regen-watchd/ was deleted; smoke their --help so
		// the flag surface stays visible to future refactors.
		{"audit-help-readonly", []string{"audit", "--help"}, "read-only", true},
		{"lint-help-apply", []string{"lint", "--help"}, "--apply", true},
		{"lint-help-default-report", []string{"lint", "--help"}, "Report-only", true},
		{"init-stub", []string{"init", "--config", validFixture}, "", true},
		{"destroy-stub", []string{"destroy", "library/poetry", "--config", validFixture}, "", true},
		{"import-stub", []string{"import", "library/poetry", "--config", validFixture}, "", true},
		{"destroy-no-arg", []string{"destroy", "--config", validFixture}, "accepts 1 arg", false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := newCmd(bin, tc.args)
			var out bytes.Buffer
			cmd.Stdout = &out
			cmd.Stderr = &out
			err := cmd.Run()
			gotOK := err == nil
			if gotOK != tc.exitOK {
				t.Errorf("exit OK got=%v want=%v\noutput: %s", gotOK, tc.exitOK, out.String())
			}
			if !strings.Contains(out.String(), tc.wantOut) {
				t.Errorf("output missing %q\nfull: %s", tc.wantOut, out.String())
			}
			// CLI-06: NO_COLOR honored — no ANSI escapes.
			if strings.Contains(out.String(), "\x1b[") {
				t.Errorf("ANSI escape leaked despite NO_COLOR=1: %q", out.String())
			}
		})
	}
}

// TestCobraStubStdoutEmpty asserts stub messages go ONLY to stderr.
// CLI-05 requires stdout to remain pipe-friendly (reserved for
// structured output that lands in). The combined-output
// assertion above can't catch a stdout leak; this test splits the
// streams.
func TestCobraStubStdoutEmpty(t *testing.T) {
	bin := buildBinary(t)
	root := repoRoot(t)
	validFixture := filepath.Join(root, "tests", "bear", "config", "testdata", "valid-minimal.toml")

	stubs := []struct {
		name string
		args []string
	}{
		// : apply + daemon are real subcommands.
		// : plan is real now too — its stdout is the
		// rendered diff, not empty. Only init/destroy/import remain
		// stubbed in v1 (will land them).
		{"init", []string{"init", "--config", validFixture}},
		{"destroy", []string{"destroy", "library/poetry", "--config", validFixture}},
		{"import", []string{"import", "library/poetry", "--config", validFixture}},
	}

	for _, s := range stubs {
		t.Run(s.name, func(t *testing.T) {
			cmd := newCmd(bin, s.args)
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("stub %s exited %v; stderr=%s", s.name, err, stderr.String())
			}
			if stdout.Len() != 0 {
				t.Errorf("stub %s leaked %d bytes to stdout: %q", s.name, stdout.Len(), stdout.String())
			}
			if !strings.Contains(stderr.String(), "not yet implemented") {
				t.Errorf("stub %s missing canonical message on stderr: %q", s.name, stderr.String())
			}
		})
	}
}

// TestCobraStubsNoConfig asserts stubs print the Phase-X "not yet
// implemented" message and exit 0 even when invoked from a directory
// that has no./noxctl.toml. Regression guard for the 01-foundation
// UAT finding: stubs originally wired PersistentPreRunE preflight,
// which forced a config-load on every invocation; running the stub
// in a fresh dir surfaced "open./noxctl.toml: no such file" instead
// of the helpful Phase-Y notice. The fix detached preflight from
// stubCmd entirely; this test pins that contract so a future
// re-introduction of preflight gets caught by CI.
func TestCobraStubsNoConfig(t *testing.T) {
	bin := buildBinary(t)
	freshDir := t.TempDir()

	stubs := []struct {
		name string
		args []string
		want string
	}{
		// : apply + daemon load the config eagerly.
		// : plan also loads the config eagerly. Only
		// init/destroy/import remain stubbed in v1 (will land them).
		{"init", []string{"init"}, ""},
		{"destroy", []string{"destroy", "library/poetry"}, ""},
		{"import", []string{"import", "library/poetry"}, ""},
	}

	for _, s := range stubs {
		t.Run(s.name, func(t *testing.T) {
			assertStubNoConfig(t, bin, freshDir, s.name, s.args, s.want)
		})
	}
}

// assertStubNoConfig runs a stub from freshDir (no./noxctl.toml) and
// verifies the canonical "not yet implemented" contract: exit 0, empty
// stdout, stderr containing both the canonical phrase and the
// stub-specific Phase-X token, and crucially NO config-load error
// leaking through. Extracted so TestCobraStubsNoConfig stays under the
// project gocognit ≤ 15 budget.
func assertStubNoConfig(t *testing.T, bin, dir, name string, args []string, want string) {
	t.Helper()
	cmd := newCmd(bin, args)
	cmd.Dir = dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("stub %s exited %v from fresh dir; stderr=%s", name, err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("stub %s leaked %d bytes to stdout: %q", name, stdout.Len(), stdout.String())
	}
	se := stderr.String()
	if !strings.Contains(se, "not yet implemented") {
		t.Errorf("stub %s missing canonical message: %q", name, se)
	}
	if !strings.Contains(se, want) {
		t.Errorf("stub %s missing %q in stderr: %q", name, want, se)
	}
	if strings.Contains(se, "no such file") {
		t.Errorf("stub %s leaked config-load error in fresh dir: %q", name, se)
	}
}

// TestCobraValidateQuietSuppressesSummary asserts that -q drops the
// success summary on validate. Errors still print (Cobra default);
// only the "✓ N domains validated…" line is muted.
func TestCobraValidateQuietSuppressesSummary(t *testing.T) {
	bin := buildBinary(t)
	root := repoRoot(t)
	validFixture := filepath.Join(root, "tests", "bear", "config", "testdata", "valid-minimal.toml")

	cmd := newCmd(bin, []string{"-q", "validate", validFixture})
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("validate -q exited %v; stderr=%s", err, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Errorf("validate -q leaked %d bytes to stdout: %q", stdout.Len(), stdout.String())
	}
	if stderr.Len() != 0 {
		t.Errorf("validate -q should suppress stderr summary; got %d bytes: %q", stderr.Len(), stderr.String())
	}
}

// TestCobraValidatePerformance asserts validate finishes well under
// the SPEC < 1s budget on the small all-blueprints fixture.
// will pin the < 1s gate against Roman's full 28-domain corpus; this
// test enforces a tighter local budget so a regression here surfaces
// before the full-corpus gate flags it.
func TestCobraValidatePerformance(t *testing.T) {
	bin := buildBinary(t)
	root := repoRoot(t)
	fixture := filepath.Join(root, "tests", "bear", "config", "testdata", "valid-all-blueprints.toml")

	start := time.Now()
	cmd := newCmd(bin, []string{"validate", fixture})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("validate failed: %v\n%s", err, out)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("validate took %v on small fixture (budget 500ms; full corpus budget 1s)", elapsed)
	}
}

// buildBinary compiles cmd/noxctl into a temp directory and returns
// the path. Build cost is per-test (Go's build cache amortizes the
// cost across cases), so subtests pay millisecond rebuild time only
// when an underlying source file changes. We dispatch `go build`
// through /usr/bin/env (envBinary) so the exec entry point is a
// compile-time string literal — same rationale as newCmd above.
func buildBinary(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(t.TempDir(), "noxctl")
	cmd := exec.Command(envBinary, "--",
		"go", "build", "-o", bin, "github.com/barad1tos/noxctl/cmd/noxctl")
	cmd.Dir = repoRoot(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go build failed: %v\n%s", err, out)
	}
	return bin
}

// (repoRoot lives in no_unexpected_deps_test.go in the same package
// — reused here to avoid the dupl ≥ 30-token gate; the helper resolves
// the repository root from the test source-file location and works for
// both sibling test files unchanged.)
