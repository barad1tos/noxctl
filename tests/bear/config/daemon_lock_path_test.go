package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
)

func TestResolveDaemonLockPathUsesDaemonConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	configDir := filepath.Join(home, ".noxctl")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	body := "[daemon.paths]\nlock = \"$HOME/custom.lock\"\n"
	if err := os.WriteFile(filepath.Join(configDir, "daemon.toml"), []byte(body), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	got, err := config.ResolveDaemonLockPath("verify", filepath.Join(configDir, "daemon.toml"))
	if err != nil {
		t.Fatalf("ResolveDaemonLockPath: %v", err)
	}
	want := filepath.Join(home, "custom.lock")
	if got != want {
		t.Fatalf("lock path = %q, want %q", got, want)
	}
}
