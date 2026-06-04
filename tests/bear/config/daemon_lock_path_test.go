package config_test

import (
	"os"
	"path/filepath"
	"strings"
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

	got, err := config.ResolveDaemonLockPath(filepath.Join(configDir, "daemon.toml"))
	if err != nil {
		t.Fatalf("ResolveDaemonLockPath: %v", err)
	}
	want := filepath.Join(home, "custom.lock")
	if got != want {
		t.Fatalf("lock path = %q, want %q", got, want)
	}
}

func TestResolveDaemonLockPathRequiresDaemonConfigPath(t *testing.T) {
	_, err := config.ResolveDaemonLockPath("")
	if err == nil {
		t.Fatal("ResolveDaemonLockPath returned nil error for an empty daemon config path")
	}
	if !strings.Contains(err.Error(), "daemon config path required") {
		t.Fatalf("error = %q, want missing daemon config path context", err.Error())
	}
}

func TestResolveDaemonLockPathReportsConfigError(t *testing.T) {
	home := t.TempDir()
	configDir := filepath.Join(home, ".noxctl")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	path := filepath.Join(configDir, "daemon.toml")
	if err := os.WriteFile(path, []byte("[daemon.paths\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := config.ResolveDaemonLockPath(path)
	if err == nil {
		t.Fatal("ResolveDaemonLockPath returned nil error for invalid daemon config")
	}
	if !strings.Contains(err.Error(), "config: parse") {
		t.Fatalf("error = %q, want daemon config parse context", err.Error())
	}
}
