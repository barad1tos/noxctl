package engine_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/engine"
)

// TestDefaultDaemonLogPath pins the centralized daemon-log default both
// verify and doctor resolve through: ~/.cache/regen-watchd.log. Having
// one source of truth (IN-01) keeps the two commands from disagreeing on
// where the daemon writes.
func TestDefaultDaemonLogPath(t *testing.T) {
	got, err := engine.DefaultDaemonLogPath()
	if err != nil {
		t.Fatalf("DefaultDaemonLogPath: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("UserHomeDir: %v", err)
	}
	want := filepath.Join(home, ".cache", "regen-watchd.log")
	if got != want {
		t.Errorf("DefaultDaemonLogPath() = %q, want %q", got, want)
	}
}
