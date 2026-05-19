package config_test

import (
	"bytes"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
)

// loadDaemonCase is the table-driven shape shared by every
// TestLoadDaemon_X happy-path subtest in this file: file body OR env
// value OR neither, plus the expected resolved value (typed) and the
// provenance source ("default" | "file" | "env"). Generic over the
// value type so a single runner covers both Duration-valued and
// bool-valued resolution chains without duplicating the
// `t.Run`/loop block (which would trip `dupl`).
type loadDaemonCase[V any] struct {
	name       string
	tomlBody   string // empty → file absent
	envValue   string // empty → env unset
	wantValue  V
	wantSource string
}

// runLoadDaemonCases applies `assert` to each table case under its own
// `t.Run`. Extracted to dodge `dupl` on the for-range/`t.Run` block
// without an `//nolint` suppression.
//
// Closure-capture safety: `c` is captured by the `t.Run` closure, which
// would alias to the last iteration on Go < 1.22. The module pins
// `go 1.26.2` in go.mod, so per-iteration scoping is guaranteed by the
// language spec and no defensive `c := c` shadow is needed.
func runLoadDaemonCases[V any](
	t *testing.T,
	cases []loadDaemonCase[V],
	assert func(*testing.T, loadDaemonCase[V]),
) {
	t.Helper()
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { assert(t, c) })
	}
}

// TestLoadDaemon_FileAbsent_ReturnsDefaults asserts spec contract:
// "File absent -> defaults populated, no error, source = 'default'
// for all fields".
func TestLoadDaemon_FileAbsent_ReturnsDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon(absent) error = %v, want nil", err)
	}
	if cfg.DebouncePause != engine.DefaultDebouncePause {
		t.Errorf("DebouncePause = %v, want %v", cfg.DebouncePause, engine.DefaultDebouncePause)
	}
	if cfg.MaxBurstWindow != engine.DefaultMaxBurstWindow {
		t.Errorf("MaxBurstWindow = %v, want %v", cfg.MaxBurstWindow, engine.DefaultMaxBurstWindow)
	}
	if !cfg.AuditEnabled {
		t.Errorf("AuditEnabled = %v, want true (default)", cfg.AuditEnabled)
	}
	assertAllSources(t, cfg, "default")
	// time-typed fields can't be the empty Duration when defaults are populated
	if cfg.DebouncePause == 0 {
		t.Errorf("DebouncePause must not be zero after default population")
	}
}

// assertAllSources checks that every tracked field in cfg.Sources
// equals want. Extracted so the assertion is dupl-clean across the
// defaults / file-overlay tests.
func assertAllSources(t *testing.T, cfg config.DaemonConfig, want string) {
	t.Helper()
	for _, field := range []string{
		"DebouncePause", "MaxBurstWindow", "AuditEnabled",
		"StatePath", "LockPath", "PinsPath", "LogPath", "BearDBDir",
	} {
		if got := cfg.Sources[field]; got != want {
			t.Errorf("Sources[%q] = %q, want %q", field, got, want)
		}
	}
}

// TestLoadDaemon_FilePresent_ValuesApplied asserts that values from a
// valid TOML file override the hardcoded defaults and Sources map
// reflects "file" for the overridden fields.
func TestLoadDaemon_FilePresent_ValuesApplied(t *testing.T) {
	path := repoRootForTest(t) + "/tests/bear/config/testdata/daemon/all_fields.toml"

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.DebouncePause, 3*time.Second; got != want {
		t.Errorf("DebouncePause = %v, want %v", got, want)
	}
	if got, want := cfg.MaxBurstWindow, 2*time.Minute; got != want {
		t.Errorf("MaxBurstWindow = %v, want %v", got, want)
	}
	if cfg.AuditEnabled {
		t.Errorf("AuditEnabled = true, want false (file sets it false)")
	}
	if got, want := cfg.StatePath, "/var/lib/noxctl/state.json"; got != want {
		t.Errorf("StatePath = %q, want %q", got, want)
	}
	assertAllSources(t, cfg, "file")
}

// TestLoadDaemon_FilePartial_DefaultsPreservedForMissingKeys asserts
// that fields absent from the TOML keep their default values + default
// source label.
func TestLoadDaemon_FilePartial_DefaultsPreservedForMissingKeys(t *testing.T) {
	path := repoRootForTest(t) + "/tests/bear/config/testdata/daemon/timers_only.toml"

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.DebouncePause, 1*time.Second; got != want {
		t.Errorf("DebouncePause = %v, want %v", got, want)
	}
	if got, want := cfg.Sources["DebouncePause"], "file"; got != want {
		t.Errorf("Sources[DebouncePause] = %q, want %q", got, want)
	}
	// StatePath not in file → still default
	if cfg.Sources["StatePath"] != "default" {
		t.Errorf("Sources[StatePath] = %q, want %q", cfg.Sources["StatePath"], "default")
	}
}

// TestLoadDaemon_InvalidTOML_ReturnsError asserts the spec "file
// present but unparseable → error" contract.
func TestLoadDaemon_InvalidTOML_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	if err := os.WriteFile(path, []byte("not-toml-at-all\n[unclosed\n"), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	_, err := config.LoadDaemon(path)
	if err == nil {
		t.Fatal("LoadDaemon(unparseable) returned nil error, want non-nil")
	}
}

// TestLoadDaemon_InvalidDuration_ReturnsError asserts the "invalid
// duration → fatal" contract — including the bad-value mention.
func TestLoadDaemon_InvalidDuration_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\ndebounce_pause = \"not-a-duration\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	_, err := config.LoadDaemon(path)
	if err == nil {
		t.Fatal("LoadDaemon(bad duration) returned nil error")
	}
	if !strings.Contains(err.Error(), "debounce_pause") {
		t.Errorf("error %q should mention debounce_pause", err.Error())
	}
}

// TestLoadDaemon_EnvOverridesFile asserts env > file > defaults
// precedence chain. Uses t.Setenv (auto-restored on test cleanup).
func TestLoadDaemon_EnvOverridesFile(t *testing.T) {
	path := repoRootForTest(t) + "/tests/bear/config/testdata/daemon/all_fields.toml"
	t.Setenv("REGEN_DEBOUNCE_PAUSE", "500ms")
	t.Setenv("REGEN_STATE_PATH", "/tmp/env-override/state.json")

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v", err)
	}
	if got, want := cfg.DebouncePause, 500*time.Millisecond; got != want {
		t.Errorf("DebouncePause = %v, want %v (env override)", got, want)
	}
	if cfg.Sources["DebouncePause"] != "env" {
		t.Errorf("Sources[DebouncePause] = %q, want %q", cfg.Sources["DebouncePause"], "env")
	}
	// max_burst_window not overridden by env → file value
	if got, want := cfg.MaxBurstWindow, 2*time.Minute; got != want {
		t.Errorf("MaxBurstWindow = %v, want %v (file value)", got, want)
	}
	if cfg.Sources["MaxBurstWindow"] != "file" {
		t.Errorf("Sources[MaxBurstWindow] = %q, want %q", cfg.Sources["MaxBurstWindow"], "file")
	}
	if got, want := cfg.StatePath, "/tmp/env-override/state.json"; got != want {
		t.Errorf("StatePath = %q, want %q", got, want)
	}
}

// TestLoadDaemon_EnvInvalidDuration_ReturnsError asserts env-var
// duration parsing fails loud, mirroring file-side behavior.
func TestLoadDaemon_EnvInvalidDuration_ReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml") // absent
	t.Setenv("REGEN_DEBOUNCE_PAUSE", "abc")

	_, err := config.LoadDaemon(path)
	if err == nil {
		t.Fatal("LoadDaemon(env bad duration) returned nil error")
	}
	if !strings.Contains(err.Error(), "REGEN_DEBOUNCE_PAUSE") {
		t.Errorf("error %q should mention REGEN_DEBOUNCE_PAUSE", err.Error())
	}
}

// TestLoadDaemon_EnvAuditOffSemantics asserts the existing
// REGEN_AUDIT="off" → disable, anything-else → enable quirk is
// preserved.
func TestLoadDaemon_EnvAuditOffSemantics(t *testing.T) {
	cases := []struct {
		envValue string
		want     bool
	}{
		{"off", false},
		{"on", true},
		{"true", true},
		{"yes", true},
	}
	for _, c := range cases {
		t.Run(c.envValue, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "daemon.toml")
			t.Setenv("REGEN_AUDIT", c.envValue)
			cfg, err := config.LoadDaemon(path)
			if err != nil {
				t.Fatalf("LoadDaemon error = %v", err)
			}
			if cfg.AuditEnabled != c.want {
				t.Errorf("AuditEnabled = %v, want %v (REGEN_AUDIT=%q)",
					cfg.AuditEnabled, c.want, c.envValue)
			}
			if cfg.Sources["AuditEnabled"] != "env" {
				t.Errorf("Sources[AuditEnabled] = %q, want env",
					cfg.Sources["AuditEnabled"])
			}
		})
	}
}

// TestLoadDaemon_ExpandsPathsFromFile asserts that both "~/" and
// "$VAR" prefixes in file-supplied paths are resolved post-overlay.
// Table-driven so the two near-identical setups don't trip dupl.
func TestLoadDaemon_ExpandsPathsFromFile(t *testing.T) {
	cases := []struct {
		name     string
		tomlBody string
		want     string
		got      func(config.DaemonConfig) string
	}{
		{
			name: "tilde",
			tomlBody: `[daemon.paths]
state = "~/.noxctl/custom-state.json"
`,
			want: "/Users/testuser/.noxctl/custom-state.json",
			got:  func(c config.DaemonConfig) string { return c.StatePath },
		},
		{
			name: "dollar_var",
			tomlBody: `[daemon.paths]
log = "$HOME/logs/daemon.log"
`,
			want: "/Users/testuser/logs/daemon.log",
			got:  func(c config.DaemonConfig) string { return c.LogPath },
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "daemon.toml")
			if err := os.WriteFile(path, []byte(c.tomlBody), 0o644); err != nil {
				t.Fatalf("setup: %v", err)
			}
			t.Setenv("HOME", "/Users/testuser")
			cfg, err := config.LoadDaemon(path)
			if err != nil {
				t.Fatalf("LoadDaemon error = %v", err)
			}
			if got := c.got(cfg); got != c.want {
				t.Errorf("path = %q, want %q", got, c.want)
			}
		})
	}
}

// TestLoadDaemon_ExpandsPathsFromEnv asserts that an env-var-supplied
// path also gets ~/-expansion (consistent with file-supplied values).
func TestLoadDaemon_ExpandsPathsFromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	t.Setenv("HOME", "/Users/testuser")
	t.Setenv("REGEN_LOG_PATH", "~/logs/from-env.log")

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v", err)
	}
	want := "/Users/testuser/logs/from-env.log"
	if cfg.LogPath != want {
		t.Errorf("LogPath = %q, want %q", cfg.LogPath, want)
	}
}

// === Tests for DaemonConfig.BearcliConcurrency ===
//
// Covers DaemonConfig.BearcliConcurrency + the REGEN_BEARCLI_CONCURRENCY
// env overlay + the >16 soft-cap WARN.

// TestLoadDaemon_BearcliConcurrency_Default asserts that when the file
// has no bearcli_concurrency key (and no env override is set), LoadDaemon
// resolves to the ship default 8 with Sources["BearcliConcurrency"]
// = "default".
func TestLoadDaemon_BearcliConcurrency_Default(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml") // absent

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.BearcliConcurrency, 8; got != want {
		t.Errorf("BearcliConcurrency = %d, want %d (ship default)", got, want)
	}
	if got, want := cfg.Sources["BearcliConcurrency"], "default"; got != want {
		t.Errorf("Sources[BearcliConcurrency] = %q, want %q", got, want)
	}
}

// TestLoadDaemon_BearcliConcurrency_FromFile asserts the
// [daemon].bearcli_concurrency TOML key parses into the
// DaemonConfig.BearcliConcurrency field with Sources["BearcliConcurrency"]
// = "file".
func TestLoadDaemon_BearcliConcurrency_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\nbearcli_concurrency = 4\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.BearcliConcurrency, 4; got != want {
		t.Errorf("BearcliConcurrency = %d, want %d", got, want)
	}
	if got, want := cfg.Sources["BearcliConcurrency"], "file"; got != want {
		t.Errorf("Sources[BearcliConcurrency] = %q, want %q", got, want)
	}
}

// TestLoadDaemon_BearcliConcurrency_FromEnv asserts the
// REGEN_BEARCLI_CONCURRENCY env var overrides the file value with
// Sources["BearcliConcurrency"] = "env" (mirroring REGEN_DEBOUNCE_PAUSE
// precedence).
func TestLoadDaemon_BearcliConcurrency_FromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\nbearcli_concurrency = 4\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	t.Setenv("REGEN_BEARCLI_CONCURRENCY", "12")

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.BearcliConcurrency, 12; got != want {
		t.Errorf("BearcliConcurrency = %d, want %d (env overrides file)", got, want)
	}
	if got, want := cfg.Sources["BearcliConcurrency"], "env"; got != want {
		t.Errorf("Sources[BearcliConcurrency] = %q, want %q", got, want)
	}
}

// TestLoadDaemon_BearcliConcurrency_Invalid asserts negative / zero
// values return a non-nil error mentioning bearcli_concurrency
// (mirroring the bad-duration contract). Covers file-side zero,
// file-side negative, and env-side zero — each via a focused sub-test
// helper to keep gocognit ≤ 15.
func TestLoadDaemon_BearcliConcurrency_Invalid(t *testing.T) {
	t.Run("file_zero", func(t *testing.T) {
		assertLoadDaemonInvalid(t, "[daemon]\nbearcli_concurrency = 0\n", "REGEN_BEARCLI_CONCURRENCY", "", "bearcli_concurrency")
	})
	t.Run("file_negative", func(t *testing.T) {
		assertLoadDaemonInvalid(t, "[daemon]\nbearcli_concurrency = -1\n", "REGEN_BEARCLI_CONCURRENCY", "", "bearcli_concurrency")
	})
	t.Run("env_zero", func(t *testing.T) {
		assertLoadDaemonInvalid(t, "", "REGEN_BEARCLI_CONCURRENCY", "0", "REGEN_BEARCLI_CONCURRENCY")
	})
}

// assertLoadDaemonInvalid drives one invalid-config scenario: writes the
// file (when fileContent set), sets envName=envValue (when envValue set),
// then asserts LoadDaemon errors and the error mentions wantSubstr.
// Shared helper used by both BearcliConcurrency and
// MtimePollInterval invalid-input tests — parameterizing
// envName avoids dupl-flagged copy of two near-identical bodies.
// Extracted to keep parent tests under gocognit ≤ 15.
func assertLoadDaemonInvalid(t *testing.T, fileContent, envName, envValue, wantSubstr string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	if fileContent != "" {
		if err := os.WriteFile(path, []byte(fileContent), 0o644); err != nil {
			t.Fatalf("setup write: %v", err)
		}
	}
	if envValue != "" {
		t.Setenv(envName, envValue)
	}
	_, err := config.LoadDaemon(path)
	if err == nil {
		t.Fatalf("LoadDaemon(invalid) returned nil error, want non-nil")
	}
	if !strings.Contains(err.Error(), wantSubstr) {
		t.Errorf("error %q should mention %q", err.Error(), wantSubstr)
	}
}

// TestLoadDaemon_BearcliConcurrency_SoftCap asserts that values >16
// emit a WARN log line (but DO NOT fail) — a soft cap, not a hard one.
// Captures log.Default output into a bytes.Buffer and searches for the
// substring "soft cap" plus the configured number.
func TestLoadDaemon_BearcliConcurrency_SoftCap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\nbearcli_concurrency = 32\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	var buf bytes.Buffer
	prevWriter := log.Writer()
	prevFlags := log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil (soft cap accepts the value)", err)
	}
	if got, want := cfg.BearcliConcurrency, 32; got != want {
		t.Errorf("BearcliConcurrency = %d, want %d (NOT truncated to 16)", got, want)
	}
	logged := buf.String()
	if !strings.Contains(logged, "soft cap") {
		t.Errorf("log output %q should contain %q", logged, "soft cap")
	}
	if !strings.Contains(logged, "32") {
		t.Errorf("log output %q should mention configured value %q", logged, "32")
	}
}

// === Tests for DaemonConfig.MtimePollInterval ===
//
// Covers DaemonConfig.MtimePollInterval + the REGEN_MTIME_POLL_INTERVAL
// env overlay. Semantic divergence from BearcliConcurrency: zero is
// VALID (disables polling), only negative is fatal.

// TestLoadDaemon_MtimePollInterval_Default asserts that when the file
// has no mtime_poll_interval key (and no env override is set), LoadDaemon
// resolves to the ship default 30s with Sources["MtimePollInterval"]
// = "default".
func TestLoadDaemon_MtimePollInterval_Default(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml") // absent

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.MtimePollInterval, 30*time.Second; got != want {
		t.Errorf("MtimePollInterval = %v, want %v (ship default)", got, want)
	}
	if got, want := cfg.Sources["MtimePollInterval"], "default"; got != want {
		t.Errorf("Sources[MtimePollInterval] = %q, want %q", got, want)
	}
}

// TestLoadDaemon_MtimePollInterval_FromFile asserts the
// [daemon].mtime_poll_interval TOML key parses into the
// DaemonConfig.MtimePollInterval field with Sources["MtimePollInterval"]
// = "file". TOML form is a Duration string (mirrors debounce_pause).
func TestLoadDaemon_MtimePollInterval_FromFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\nmtime_poll_interval = \"10s\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.MtimePollInterval, 10*time.Second; got != want {
		t.Errorf("MtimePollInterval = %v, want %v", got, want)
	}
	if got, want := cfg.Sources["MtimePollInterval"], "file"; got != want {
		t.Errorf("Sources[MtimePollInterval] = %q, want %q", got, want)
	}
}

// TestLoadDaemon_MtimePollInterval_FromEnv asserts the
// REGEN_MTIME_POLL_INTERVAL env var overrides the file value with
// Sources["MtimePollInterval"] = "env" (mirroring REGEN_DEBOUNCE_PAUSE
// precedence).
func TestLoadDaemon_MtimePollInterval_FromEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\nmtime_poll_interval = \"10s\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	t.Setenv("REGEN_MTIME_POLL_INTERVAL", "15s")

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got, want := cfg.MtimePollInterval, 15*time.Second; got != want {
		t.Errorf("MtimePollInterval = %v, want %v (env overrides file)", got, want)
	}
	if got, want := cfg.Sources["MtimePollInterval"], "env"; got != want {
		t.Errorf("Sources[MtimePollInterval] = %q, want %q", got, want)
	}
}

// TestLoadDaemon_MtimePollInterval_ZeroDisables asserts that
// mtime_poll_interval = "0s" is a VALID setting that disables the poll
// loop. Semantic divergence from BearcliConcurrency (where zero is
// fatal): zero on this knob means "polling disabled, rely on FSEvent
// only". Sources reflects "file" because the operator explicitly chose
// to override the default.
func TestLoadDaemon_MtimePollInterval_ZeroDisables(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	contents := "[daemon]\nmtime_poll_interval = \"0s\"\n"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("setup write: %v", err)
	}

	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil (zero is a valid \"disabled\" setting)", err)
	}
	if got, want := cfg.MtimePollInterval, time.Duration(0); got != want {
		t.Errorf("MtimePollInterval = %v, want %v (zero disables polling)", got, want)
	}
	if got, want := cfg.Sources["MtimePollInterval"], "file"; got != want {
		t.Errorf("Sources[MtimePollInterval] = %q, want %q (operator override)", got, want)
	}
}

// TestLoadDaemon_MtimePollInterval_NegativeFatal asserts that negative
// durations (file OR env) cause LoadDaemon to return a non-nil error
// mentioning the bad key/env-var name (negative-fatal contract).
// Mirrors TestLoadDaemon_BearcliConcurrency_Invalid table shape; reuses
// the shared assertLoadDaemonNegativeDurationCases helper.
func TestLoadDaemon_MtimePollInterval_NegativeFatal(t *testing.T) {
	assertLoadDaemonNegativeDurationCases(t,
		"mtime_poll_interval", "REGEN_MTIME_POLL_INTERVAL")
}

// assertLoadDaemonNegativeDurationCases drives the two-subtest
// negative-duration validation pattern used by both
// (MtimePollInterval) and (AutoTagPollInterval). Both blocks
// follow the same shape: one file-side negative case + one env-side
// negative case, each asserting via assertLoadDaemonInvalid that
// LoadDaemon fails with the bad-key substring in the error message.
// Extracting this driver collapses two near-identical 7-line bodies
// into a single helper, satisfying dupl ≥50-token policy.
func assertLoadDaemonNegativeDurationCases(t *testing.T, tomlKey, envName string) {
	t.Helper()
	t.Run("file_negative", func(t *testing.T) {
		assertLoadDaemonInvalid(t,
			"[daemon]\n"+tomlKey+" = \"-1s\"\n", envName, "", tomlKey)
	})
	t.Run("env_negative", func(t *testing.T) {
		assertLoadDaemonInvalid(t, "", envName, "-5s", envName)
	})
}

// === Tests for DaemonConfig.AutoTagPollInterval ===
//
// Covers DaemonConfig.AutoTagPollInterval + the REGEN_AUTOTAG_POLL_INTERVAL
// env overlay. Semantic chain identical to MtimePollInterval: env > file
// > default 2s; zero is a valid "disabled" setting; negative is fatal.
//
// Implementation note: the happy-path cases (Default/FromFile/FromEnv/
// ZeroDisables) are table-driven against the AutoTagPollInterval field
// instead of cloning the stand-alone MtimePollInterval functions. A
// literal copy-rename trips `dupl` (≥50-token duplicate-block linter)
// across the two blocks. Table-driven shape preserves every assertion
// while satisfying the "extract a helper instead of copy-pasting" rule.
// The NegativeFatal case keeps the shared assertLoadDaemonInvalid helper.

// TestLoadDaemon_AutoTagPollInterval covers the four happy-path legs
// of the auto-tag-poll resolution chain in a single table. Each sub-test
// asserts BOTH the parsed duration AND the provenance source. Negative
// rejection is covered by TestLoadDaemon_AutoTagPollInterval_NegativeFatal.
func TestLoadDaemon_AutoTagPollInterval(t *testing.T) {
	cases := []loadDaemonCase[time.Duration]{
		{
			name:       "Default",
			wantValue:  2 * time.Second,
			wantSource: "default",
		},
		{
			name:       "FromFile",
			tomlBody:   "[daemon]\nauto_tag_poll_interval = \"500ms\"\n",
			wantValue:  500 * time.Millisecond,
			wantSource: "file",
		},
		{
			name:       "FromEnv",
			tomlBody:   "[daemon]\nauto_tag_poll_interval = \"500ms\"\n",
			envValue:   "1s",
			wantValue:  1 * time.Second,
			wantSource: "env",
		},
		{
			name:       "ZeroDisables",
			tomlBody:   "[daemon]\nauto_tag_poll_interval = \"0s\"\n",
			wantValue:  0,
			wantSource: "file",
		},
	}
	runLoadDaemonCases(t, cases, func(t *testing.T, c loadDaemonCase[time.Duration]) {
		runAutoTagPollIntervalCase(t, c.tomlBody, c.envValue, c.wantValue, c.wantSource)
	})
}

// runAutoTagPollIntervalCase drives one TestLoadDaemon_AutoTagPollInterval
// sub-case. Extracted to keep the parent under gocognit ≤ 15.
func runAutoTagPollIntervalCase(t *testing.T, tomlBody, envValue string, wantValue time.Duration, wantSource string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	if tomlBody != "" {
		if err := os.WriteFile(path, []byte(tomlBody), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	if envValue != "" {
		t.Setenv("REGEN_AUTOTAG_POLL_INTERVAL", envValue)
	}
	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got := cfg.AutoTagPollInterval; got != wantValue {
		t.Errorf("AutoTagPollInterval = %v, want %v", got, wantValue)
	}
	if got := cfg.Sources["AutoTagPollInterval"]; got != wantSource {
		t.Errorf("Sources[AutoTagPollInterval] = %q, want %q", got, wantSource)
	}
}

// TestLoadDaemon_AutoTagPollInterval_NegativeFatal asserts that negative
// durations (file OR env) cause LoadDaemon to return a non-nil error
// mentioning the bad key/env-var name (negative-fatal contract).
// Reuses the shared assertLoadDaemonNegativeDurationCases helper.
func TestLoadDaemon_AutoTagPollInterval_NegativeFatal(t *testing.T) {
	assertLoadDaemonNegativeDurationCases(t,
		"auto_tag_poll_interval", "REGEN_AUTOTAG_POLL_INTERVAL")
}

// TestLoadDaemon_DomainBootstrap covers the four happy-path legs of the
// resolution chain in a single table. Each sub-test
// asserts BOTH the resolved bool value AND the provenance source.
// Env semantics mirror `REGEN_AUDIT`: only "off" disables; anything
// else enables — matches the quirk already locked in for
// `EnvAuditEnabled`.
func TestLoadDaemon_DomainBootstrap(t *testing.T) {
	cases := []loadDaemonCase[bool]{
		{
			name:       "Default",
			wantValue:  true,
			wantSource: "default",
		},
		{
			name:       "FromFile",
			tomlBody:   "[daemon]\ndomain_bootstrap = false\n",
			wantValue:  false,
			wantSource: "file",
		},
		{
			name:       "FromEnv",
			envValue:   "off",
			wantValue:  false,
			wantSource: "env",
		},
		{
			name:       "EnvOverridesFile",
			tomlBody:   "[daemon]\ndomain_bootstrap = true\n",
			envValue:   "off",
			wantValue:  false,
			wantSource: "env",
		},
	}
	runLoadDaemonCases(t, cases, func(t *testing.T, c loadDaemonCase[bool]) {
		runDomainBootstrapCase(t, c.tomlBody, c.envValue, c.wantValue, c.wantSource)
	})
}

// runDomainBootstrapCase drives one TestLoadDaemon_DomainBootstrap
// sub-case. Extracted to keep the parent under gocognit ≤ 15.
func runDomainBootstrapCase(t *testing.T, tomlBody, envValue string, wantValue bool, wantSource string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.toml")
	if tomlBody != "" {
		if err := os.WriteFile(path, []byte(tomlBody), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	if envValue != "" {
		t.Setenv(config.EnvDomainBootstrap, envValue)
	}
	cfg, err := config.LoadDaemon(path)
	if err != nil {
		t.Fatalf("LoadDaemon error = %v, want nil", err)
	}
	if got := cfg.DomainBootstrap; got != wantValue {
		t.Errorf("DomainBootstrap = %v, want %v", got, wantValue)
	}
	if got := cfg.Sources["DomainBootstrap"]; got != wantSource {
		t.Errorf("Sources[DomainBootstrap] = %q, want %q", got, wantSource)
	}
}

// repoRootForTest walks up from cwd until a go.mod is found.
func repoRootForTest(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	dir := wd
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate repo root from %q", wd)
		}
		dir = parent
	}
}
