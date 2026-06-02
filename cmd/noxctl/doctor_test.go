package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
)

func TestResolveDoctorLogPathPrecedence(t *testing.T) {
	t.Run("flag wins", func(t *testing.T) {
		t.Setenv(config.EnvLogPath, "/from/env.log")
		got, err := resolveDoctorLogPath("/from/flag.log")
		if err != nil {
			t.Fatalf("resolveDoctorLogPath: %v", err)
		}
		if got != "/from/flag.log" {
			t.Errorf("got %q, want flag value", got)
		}
	})

	t.Run("env wins over daemon config", func(t *testing.T) {
		writeDaemonLogConfig(t, "/from/file.log")
		t.Setenv(config.EnvLogPath, "/from/env.log")
		got, err := resolveDoctorLogPath("")
		if err != nil {
			t.Fatalf("resolveDoctorLogPath: %v", err)
		}
		if got != "/from/env.log" {
			t.Errorf("got %q, want env value", got)
		}
	})

	t.Run("daemon config wins over default", func(t *testing.T) {
		writeDaemonLogConfig(t, "/from/file.log")
		t.Setenv(config.EnvLogPath, "")
		got, err := resolveDoctorLogPath("")
		if err != nil {
			t.Fatalf("resolveDoctorLogPath: %v", err)
		}
		if got != "/from/file.log" {
			t.Errorf("got %q, want daemon config value", got)
		}
	})
}

func writeDaemonLogConfig(t *testing.T, logPath string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".noxctl", "daemon.toml")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir daemon config dir: %v", err)
	}
	body := "[daemon.paths]\nlog = " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write daemon config: %v", err)
	}
}
