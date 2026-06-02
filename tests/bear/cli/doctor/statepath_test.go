package doctor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/doctor"
	"github.com/barad1tos/noxctl/bear/config"
)

// TestResolveStatePathPrecedence pins WR-01: explicit operator input
// wins, then env, then project-local apply state if present, then the
// daemon/home default. This lets doctor report the state file the
// ordinary `noxctl apply` path writes without hiding daemon overrides.
func TestResolveStatePathPrecedence(t *testing.T) {
	t.Run("flag wins over env", resolveStatePathFlagWins)
	t.Run("env wins over default", resolveStatePathEnvWins)
	t.Run("project-local state wins over home default", resolveStatePathProjectLocalWins)
	t.Run("home default is fallback when project state is absent", resolveStatePathHomeFallback)
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

func resolveStatePathHomeFallback(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvStatePath, "")
	t.Chdir(t.TempDir())
	got, err := doctor.ResolveStatePath("")
	if err != nil {
		t.Fatalf("ResolveStatePath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".noxctl", "state.json")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
