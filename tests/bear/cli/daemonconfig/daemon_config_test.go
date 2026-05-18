// Package daemonconfig_test exercises the `noxctl daemon-config show`
// CLI surface end-to-end. Each test builds the binary into a TempDir,
// runs it with a controlled HOME / env, and asserts on its stdout.
//
// Follows the per-subcommand subdirectory convention established by
// tests/bear/cli/parity and tests/bear/cli/plan.
package daemonconfig_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestDaemonConfigShow_DefaultsOnly runs `noxctl daemon-config show`
// with HOME pointing at an empty TempDir; output must indicate "not
// found" and show every field annotated as "default".
func TestDaemonConfigShow_DefaultsOnly(t *testing.T) {
	bin := buildNoxctlForTest(t)
	home := t.TempDir()

	cmd := exec.Command(bin, "daemon-config", "show")
	cmd.Env = append(cmd.Env, "HOME="+home, "PATH="+filepath.Dir(bin))
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("noxctl daemon-config show: %v\nstderr: %s", err, stderr.String())
	}
	out := stdout.String()
	for _, want := range []string{
		"# Config file:",
		"(not found)",
		"debounce_pause",
		"# default",
		"[daemon.paths]",
		"state",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\nfull output:\n%s", want, out)
		}
	}
}

// TestDaemonConfigShow_FilePresentAnnotatesSource builds a daemon.toml
// in a TempDir HOME and asserts overridden fields show "from file"
// while untouched fields show "default".
func TestDaemonConfigShow_FilePresentAnnotatesSource(t *testing.T) {
	bin := buildNoxctlForTest(t)
	home := t.TempDir()
	noxctlDir := filepath.Join(home, ".noxctl")
	if err := os.MkdirAll(noxctlDir, 0o755); err != nil {
		t.Fatal(err)
	}
	contents := `[daemon]
debounce_pause = "3s"
[daemon.paths]
log = "/var/log/test.log"
`
	if err := os.WriteFile(filepath.Join(noxctlDir, "daemon.toml"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(bin, "daemon-config", "show")
	cmd.Env = append(cmd.Env, "HOME="+home)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("noxctl daemon-config show: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `debounce_pause   = "3s"`) {
		t.Errorf("missing file value for debounce_pause: %s", out)
	}
	if !strings.Contains(out, "from file") {
		t.Errorf("output should annotate file source: %s", out)
	}
	if !strings.Contains(out, "(present)") {
		t.Errorf("output should report file present: %s", out)
	}
}

// TestDaemonConfigShow_EnvAnnotatesSource asserts env-overlay paths
// surface "from env <NAME>" in output.
func TestDaemonConfigShow_EnvAnnotatesSource(t *testing.T) {
	bin := buildNoxctlForTest(t)
	home := t.TempDir()

	cmd := exec.Command(bin, "daemon-config", "show")
	cmd.Env = append(cmd.Env,
		"HOME="+home,
		"REGEN_DEBOUNCE_PAUSE=750ms",
	)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	if err := cmd.Run(); err != nil {
		t.Fatalf("noxctl daemon-config show: %v", err)
	}
	out := stdout.String()
	if !strings.Contains(out, "from env REGEN_DEBOUNCE_PAUSE") {
		t.Errorf("output should annotate env source: %s", out)
	}
}

// buildNoxctlForTest compiles cmd/noxctl into a TempDir and returns
// the path. Unique name (suffix ForTest) so it doesn't collide with
// helpers in tests/bear/cli/parity or tests/bear/cli/plan if they're
// ever consolidated into one package.
func buildNoxctlForTest(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "noxctl")
	cmd := exec.Command("go", "build", "-o", bin, "./cmd/noxctl")
	cmd.Dir = repoRootForCLITest(t)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build noxctl: %v\n%s", err, out)
	}
	return bin
}

// repoRootForCLITest walks up from cwd until a go.mod is found.
func repoRootForCLITest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("go.mod not found from %q", wd)
		}
		dir = parent
	}
}
