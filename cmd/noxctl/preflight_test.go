package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/barad1tos/noxctl/bear/config"
)

func TestRunWithSignalContext_MapsCancelToCmdInterruptedSentinel(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runWithSignalContext(cmd, func(_ context.Context) error {
		return context.Canceled
	})
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("err = %v, want cmd-level interrupted sentinel", err)
	}
}

// TestResolveDoctorStatePath_Precedence pins WR-01: doctor resolves its
// state path the same way the daemon does — flag > REGEN_STATE_PATH env
// > $HOME/.noxctl/state.json default.
func TestResolveDoctorStatePath_Precedence(t *testing.T) {
	t.Run("flag wins over env", func(t *testing.T) {
		t.Setenv(config.EnvStatePath, "/from/env/state.json")
		got, err := resolveDoctorStatePath("/from/flag/state.json")
		if err != nil {
			t.Fatalf("resolveDoctorStatePath: %v", err)
		}
		if got != "/from/flag/state.json" {
			t.Errorf("got %q, want the flag value", got)
		}
	})

	t.Run("env wins over default", func(t *testing.T) {
		t.Setenv(config.EnvStatePath, "/from/env/state.json")
		got, err := resolveDoctorStatePath("")
		if err != nil {
			t.Fatalf("resolveDoctorStatePath: %v", err)
		}
		if got != "/from/env/state.json" {
			t.Errorf("got %q, want the env value", got)
		}
	})

	t.Run("default is $HOME/.noxctl/state.json", func(t *testing.T) {
		t.Setenv(config.EnvStatePath, "")
		got, err := resolveDoctorStatePath("")
		if err != nil {
			t.Fatalf("resolveDoctorStatePath: %v", err)
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
