package doctor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/doctor"
	"github.com/barad1tos/noxctl/bear/config"
)

// TestResolveStatePathPrecedence pins WR-01: explicit operator input
// wins, then env, then the project-local apply state path. This lets
// doctor report the state file the ordinary `noxctl apply` path writes
// without hiding a fresh project behind an unrelated daemon/home state.
func TestResolveStatePathPrecedence(t *testing.T) {
	t.Run("flag wins over env", resolveStatePathFlagWins)
	t.Run("env wins over default", resolveStatePathEnvWins)
	t.Run("env path expands", resolveStatePathEnvExpands)
	t.Run("project-local state wins over home default", resolveStatePathProjectLocalWins)
	t.Run("project-local state is used even when absent", resolveStatePathProjectLocalAbsent)
}

func resolveStatePathFlagWins(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvStatePath, "/from/env/state.json")
	got, err := doctor.ResolveStatePath("/from/flag/state.json")
	if err != nil {
		t.Fatalf("ResolveStatePath: %v", err)
	}
	if got != "/from/flag/state.json" {
		t.Errorf("got %q, want the flag value", got)
	}
}

func resolveStatePathEnvWins(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvStatePath, "/from/env/state.json")
	got, err := doctor.ResolveStatePath("")
	if err != nil {
		t.Fatalf("ResolveStatePath: %v", err)
	}
	if got != "/from/env/state.json" {
		t.Errorf("got %q, want the env value", got)
	}
}

func resolveStatePathEnvExpands(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(config.EnvStatePath, "~/.noxctl/state.json")
	got, err := doctor.ResolveStatePath("")
	if err != nil {
		t.Fatalf("ResolveStatePath: %v", err)
	}
	want := filepath.Join(home, ".noxctl", "state.json")
	if got != want {
		t.Errorf("got %q, want expanded env path %q", got, want)
	}
}

func resolveStatePathProjectLocalWins(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvStatePath, "")
	t.Chdir(t.TempDir())
	projectState := filepath.Join(".noxctl", "state.json")
	if err := os.MkdirAll(filepath.Dir(projectState), 0o755); err != nil {
		t.Fatalf("mkdir project state dir: %v", err)
	}
	if err := os.WriteFile(projectState, []byte(`{"version":"1"}`), 0o600); err != nil {
		t.Fatalf("write project state: %v", err)
	}

	got, err := doctor.ResolveStatePath("")
	if err != nil {
		t.Fatalf("ResolveStatePath: %v", err)
	}
	if got != "./.noxctl/state.json" {
		t.Errorf("got %q, want project-local apply state", got)
	}
}

func resolveStatePathProjectLocalAbsent(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvStatePath, "")
	t.Chdir(t.TempDir())
	home := t.TempDir()
	t.Setenv("HOME", home)
	homeState := filepath.Join(home, ".noxctl", "state.json")
	if err := os.MkdirAll(filepath.Dir(homeState), 0o755); err != nil {
		t.Fatalf("mkdir home state dir: %v", err)
	}
	if err := os.WriteFile(homeState, []byte(`{"version":"1"}`), 0o600); err != nil {
		t.Fatalf("write home state: %v", err)
	}

	got, err := doctor.ResolveStatePath("")
	if err != nil {
		t.Fatalf("ResolveStatePath: %v", err)
	}
	if got != "./.noxctl/state.json" {
		t.Errorf("got %q, want project-local apply state", got)
	}
}
