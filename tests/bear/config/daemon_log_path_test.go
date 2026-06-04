package config_test

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
)

func TestResolveDoctorLogPathPrecedence(t *testing.T) {
	t.Run("flag wins", resolveDoctorLogPathFlagWins)
	t.Run("env wins over daemon config", resolveDoctorLogPathEnvWins)
	t.Run("env wins over invalid daemon config", resolveDoctorLogPathEnvWinsOverInvalidConfig)
	t.Run("daemon config wins over default", resolveDoctorLogPathDaemonConfigWins)
}

func resolveDoctorLogPathFlagWins(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvLogPath, "/from/env.log")
	got, err := config.ResolveDaemonLogPath("/from/flag.log", daemonConfigPathForTest(t))
	if err != nil {
		t.Fatalf("ResolveDaemonLogPath: %v", err)
	}
	if got != "/from/flag.log" {
		t.Errorf("got %q, want flag value", got)
	}
}

func resolveDoctorLogPathEnvWins(t *testing.T) {
	t.Helper()
	path := writeDaemonLogConfig(t, "/from/file.log")
	t.Setenv(config.EnvLogPath, "/from/env.log")
	got, err := config.ResolveDaemonLogPath("", path)
	if err != nil {
		t.Fatalf("ResolveDaemonLogPath: %v", err)
	}
	if got != "/from/env.log" {
		t.Errorf("got %q, want env value", got)
	}
}

func resolveDoctorLogPathEnvWinsOverInvalidConfig(t *testing.T) {
	t.Helper()
	path := writeInvalidDaemonConfig(t)
	t.Setenv(config.EnvLogPath, "/from/env.log")
	got, err := config.ResolveDaemonLogPath("", path)
	if err != nil {
		t.Fatalf("ResolveDaemonLogPath: %v", err)
	}
	if got != "/from/env.log" {
		t.Errorf("got %q, want env value", got)
	}
}

func resolveDoctorLogPathDaemonConfigWins(t *testing.T) {
	t.Helper()
	path := writeDaemonLogConfig(t, "/from/file.log")
	t.Setenv(config.EnvLogPath, "")
	got, err := config.ResolveDaemonLogPath("", path)
	if err != nil {
		t.Fatalf("ResolveDaemonLogPath: %v", err)
	}
	if got != "/from/file.log" {
		t.Errorf("got %q, want daemon config value", got)
	}
}

func daemonConfigPathForTest(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	return filepath.Join(home, ".noxctl", "daemon.toml")
}

func writeDaemonLogConfig(t *testing.T, logPath string) string {
	t.Helper()
	path := daemonConfigPathForTest(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir daemon config dir: %v", err)
	}
	body := "[daemon.paths]\nlog = " + strconv.Quote(logPath) + "\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write daemon config: %v", err)
	}
	return path
}

func writeInvalidDaemonConfig(t *testing.T) string {
	t.Helper()
	path := daemonConfigPathForTest(t)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir daemon config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[daemon.paths\n"), 0o600); err != nil {
		t.Fatalf("write invalid daemon config: %v", err)
	}
	return path
}
