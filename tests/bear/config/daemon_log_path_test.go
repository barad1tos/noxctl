package config_test

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
)

func TestResolveDaemonLogPathPrecedence(t *testing.T) {
	t.Run("flag wins", resolveDaemonLogPathFlagWins)
	t.Run("env wins over daemon config", resolveDaemonLogPathEnvWins)
	t.Run("env wins over invalid daemon config", resolveDaemonLogPathEnvWinsOverInvalidConfig)
	t.Run("daemon config wins over default", resolveDaemonLogPathDaemonConfigWins)
	t.Run("missing daemon config path returns error", resolveDaemonLogPathMissingConfigPathErrors)
	t.Run("invalid daemon config returns error", resolveDaemonLogPathInvalidConfigErrors)
}

func resolveDaemonLogPathFlagWins(t *testing.T) {
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

func resolveDaemonLogPathEnvWins(t *testing.T) {
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

func resolveDaemonLogPathEnvWinsOverInvalidConfig(t *testing.T) {
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

func resolveDaemonLogPathDaemonConfigWins(t *testing.T) {
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

func resolveDaemonLogPathMissingConfigPathErrors(t *testing.T) {
	t.Helper()
	t.Setenv(config.EnvLogPath, "")
	_, err := config.ResolveDaemonLogPath("", "")
	if err == nil {
		t.Fatal("ResolveDaemonLogPath returned nil error for an empty daemon config path")
	}
	if !strings.Contains(err.Error(), "daemon config path required") {
		t.Fatalf("error = %q, want missing daemon config path context", err.Error())
	}
}

func resolveDaemonLogPathInvalidConfigErrors(t *testing.T) {
	t.Helper()
	path := writeInvalidDaemonConfig(t)
	t.Setenv(config.EnvLogPath, "")
	_, err := config.ResolveDaemonLogPath("", path)
	if err == nil {
		t.Fatal("ResolveDaemonLogPath returned nil error for invalid daemon config")
	}
	if !strings.Contains(err.Error(), "daemon.toml") {
		t.Fatalf("error = %q, want daemon.toml path context", err.Error())
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
	writeInvalidDaemonConfigFile(t, path)
	return path
}

func writeInvalidDaemonConfigFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir daemon config dir: %v", err)
	}
	if err := os.WriteFile(path, []byte("[daemon.paths\n"), 0o600); err != nil {
		t.Fatalf("write invalid daemon config: %v", err)
	}
}
