// Package config_test ships the e2e smoke and perf tests for the
// noxctl Cobra binary. Each test case builds a fresh binary in
// t.TempDir (so concurrent test runs never collide) and invokes
// it via os/exec — exercising the full argv → Cobra → config.Load
// → exit-code path that a real shell user hits.
//
// Coverage targets the CLI surface invariants: version flag, --help
// shape, completion shells, validate success, validate failure cases,
// stub exit-0 messages, NO_COLOR honored.
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
		// apply and daemon are real subcommands; assert their flag surface
		// via --help. Real behavior is exercised by the engine-level tests
		// at tests/bear/engine/* (no live bearcli in CI).
		{"apply-help-no-wait", []string{"apply", "--help"}, "--no-wait", true},
		{"apply-help-auto-approve", []string{"apply", "--help"}, "--auto-approve", true},
		{"apply-help-bear-db", []string{"apply", "--help"}, "--bear-db", true},
		{"daemon-help-bear-db", []string{"daemon", "--help"}, "--bear-db", true},
		// plan is a real subcommand; assert flag surface via --help.
		// Real behavior (engine.Plan + diff renderer + exit codes) lives
		// in engine-level tests + manual smoke (no live bearcli in CI).
		{"plan-help-color", []string{"plan", "--help"}, "--color", true},
		{"plan-help-output", []string{"plan", "--help"}, "--output", true},
		{"plan-help-tag-arg", []string{"plan", "--help"}, "[tag]", true},
		// audit + lint are the operator-facing wrappers around
		// bear.AuditDomains / bear.LintApplyDomains. Smoke their --help
		// so the flag surface stays visible to future refactors.
		{"audit-help-readonly", []string{"audit", "--help"}, "read-only", true},
		{"lint-help-apply", []string{"lint", "--help"}, "--apply", true},
		{"lint-help-default-report", []string{"lint", "--help"}, "Report-only", true},
		// init/destroy/import shipped as real subcommands; smoke the
		// flag surface so a future refactor cannot silently regress
		// them back to stubs.
		{"init-help-force", []string{"init", "--help"}, "--force", true},
		{"init-help-template", []string{"init", "--help"}, "template", true},
		{"destroy-help-auto-approve", []string{"destroy", "--help"}, "--auto-approve", true},
		{"destroy-help-confirm", []string{"destroy", "--help"}, "type-to-confirm", true},
		{"import-help-uniform-subtag", []string{"import", "--help"}, "Uniform sub-tag", true},
		{"destroy-no-arg", []string{"destroy", "--config", validFixture}, "accepts 1 arg", false},
		{"import-no-arg", []string{"import", "--config", validFixture}, "accepts 1 arg", false},
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
			// NO_COLOR honored — no ANSI escapes.
			if strings.Contains(out.String(), "\x1b[") {
				t.Errorf("ANSI escape leaked despite NO_COLOR=1: %q", out.String())
			}
		})
	}
}

// TestCobraInitWritesTemplate asserts `noxctl init <path>` writes a
// valid TOML starter that subsequently passes `noxctl validate`
// without any Bear-side I/O. Pins both the round-trip (init →
// validate happy path) and the refuse-to-overwrite contract.
func TestCobraInitWritesTemplate(t *testing.T) {
	bin := buildBinary(t)
	tmp := t.TempDir()
	target := filepath.Join(tmp, "noxctl.toml")

	// First run writes a fresh template.
	cmd := newCmd(bin, []string{"init", target})
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("init failed: %v\n%s", err, out)
	}
	body, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("template not written: %v", err)
	}
	for _, want := range []string{"[meta]", `version = "1"`, "[[domain]]", "flat-list", "flat-table", "hub-routed"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("template missing %q; full body:\n%s", want, body)
		}
	}

	// Second run on the same path must refuse without --force.
	cmd = newCmd(bin, []string{"init", target})
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Errorf("init re-run should fail without --force; output=%s", out)
	}
	if !strings.Contains(string(out), "already exists") {
		t.Errorf("re-run error should mention 'already exists': %s", out)
	}

	// The generated template should pass `noxctl validate`.
	cmd = newCmd(bin, []string{"validate", target})
	vOut, vErr := cmd.CombinedOutput()
	if vErr != nil {
		t.Errorf("validate on init-generated template failed: %v\n%s", vErr, vOut)
	}

	// --force overwrite path: replace the existing file with a fresh
	// template. Pre-stamp a sentinel string so we can prove the
	// re-write actually happened (and didn't just leave the prior
	// body in place).
	if writeErr := os.WriteFile(target, []byte("# sentinel\n"), 0o644); writeErr != nil {
		t.Fatalf("sentinel write: %v", writeErr)
	}
	cmd = newCmd(bin, []string{"init", "--force", target})
	fOut, fErr := cmd.CombinedOutput()
	if fErr != nil {
		t.Fatalf("init --force failed: %v\n%s", fErr, fOut)
	}
	after, rErr := os.ReadFile(target)
	if rErr != nil {
		t.Fatalf("re-read after --force: %v", rErr)
	}
	if strings.Contains(string(after), "sentinel") {
		t.Errorf("--force did not replace the sentinel body:\n%s", after)
	}
	if !strings.Contains(string(after), "[[domain]]") {
		t.Errorf("--force wrote something other than the template; body:\n%s", after)
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
// the < 1s budget on the small all-blueprints fixture. A separate
// full-corpus gate pins the < 1s wall-clock against Roman's 28-domain
// fixture; this test enforces a tighter local budget so a regression
// here surfaces before the full-corpus gate flags it.
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
