package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveDaemonLockPath_UsesDaemonConfig(t *testing.T) {
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

	got, err := resolveDaemonLockPath("verify")
	if err != nil {
		t.Fatalf("resolveDaemonLockPath: %v", err)
	}
	want := filepath.Join(home, "custom.lock")
	if got != want {
		t.Fatalf("lock path = %q, want %q", got, want)
	}
}

func TestApplyOptionsFor_ThreadsResolvedLockPath(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), ".lock")
	got := applyOptionsFor(nil, nil, "", lockPath, 0)
	if got.LockPath != lockPath {
		t.Fatalf("LockPath = %q, want %q", got.LockPath, lockPath)
	}
}
