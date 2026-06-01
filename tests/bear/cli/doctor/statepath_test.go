package doctor_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/doctor"
	"github.com/barad1tos/noxctl/bear/config"
)

// TestResolveStatePathPrecedence pins WR-01: doctor resolves its state
// path the same way the daemon does — flag > REGEN_STATE_PATH env >
// $HOME/.noxctl/state.json default — so the two commands inspect the
// same file instead of doctor reporting a false "first run" against a
// project-relative literal the daemon never writes.
func TestResolveStatePathPrecedence(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(config.EnvStatePath, "/from/env/state.json")
		got, err := doctor.ResolveStatePath("/from/flag/state.json")
		if err != nil {
			t.Fatalf("ResolveStatePath: %v", err)
		}
		if got != "/from/flag/state.json" {
			t.Errorf("got %q, want the flag value", got)
		}
	})

	t.Run("env wins over default", func(t *testing.T) {
		t.Setenv(config.EnvStatePath, "/from/env/state.json")
		got, err := doctor.ResolveStatePath("")
		if err != nil {
			t.Fatalf("ResolveStatePath: %v", err)
		}
		if got != "/from/env/state.json" {
			t.Errorf("got %q, want the env value", got)
		}
	})

	t.Run("default is $HOME/.noxctl/state.json", func(t *testing.T) {
		t.Setenv(config.EnvStatePath, "")
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
	})
}
