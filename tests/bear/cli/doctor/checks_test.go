// Package doctor_test holds hermetic, seam-driven coverage for the
// `noxctl doctor` package. Every check is exercised through injected
// StatFn / OpenFn / LaunchctlPrintFn / ProcessRunningFn seams so no
// test touches a real /Applications, a real launchctl, or a live Bear.
//
// The two contracts under test:
//   - severity mapping: which input yields pass / warn / error per check
//   - the read-only invariant: doctor never invokes a mutation; the
//     launchctl seam is only ever asked to inspect (the package's own
//     default seam uses `launchctl print`).
package doctor_test

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli/diag"
	"github.com/barad1tos/noxctl/bear/cli/doctor"
	"github.com/barad1tos/noxctl/bear/engine"
)

// statAll is a StatFn seam that reports every path as a present regular
// file, so checks that only need "exists" pass.
func statAll(string) (os.FileInfo, error) { return fakeInfo{mode: 0o644}, nil }

// statDir reports every path as a present directory.
func statDir(string) (os.FileInfo, error) { return fakeInfo{mode: fs.ModeDir | 0o755}, nil }

// statMissing reports every path as absent.
func statMissing(string) (os.FileInfo, error) { return nil, fs.ErrNotExist }

// openOK is an OpenFn seam that returns a real (closable) temp file so
// the readability check's immediate Close succeeds without a live DB.
func openOK(t *testing.T) func(string) (*os.File, error) {
	t.Helper()
	return func(string) (*os.File, error) {
		return os.CreateTemp(t.TempDir(), "doctor-db-*")
	}
}

// openFail is an OpenFn seam that always fails (DB unreadable).
func openFail(string) (*os.File, error) { return nil, fs.ErrPermission }

// happyOptions returns Options whose every seam yields the "ready"
// answer: present Bear.app + bearcli, readable DB, valid config,
// loaded daemon, fresh log, Bear not running. Individual tests override
// one seam to drive a single check off the happy path.
func happyOptions(t *testing.T) doctor.Options {
	t.Helper()
	cfg := writeValidConfig(t)
	dbDir := t.TempDir()
	return doctor.Options{
		ConfigPath:       cfg,
		BearDBDir:        dbDir,
		StatePath:        filepath.Join(t.TempDir(), "state.json"),
		LogPath:          writeFreshLog(t),
		Output:           "text",
		Stdout:           new(bytes.Buffer),
		Stderr:           new(bytes.Buffer),
		StatFn:           statHappy(dbDir),
		OpenFn:           openOK(t),
		LaunchctlPrintFn: func(string) error { return nil },
		ProcessRunningFn: func(string) (bool, error) { return false, nil },
		GOOS:             "darwin",
	}
}

// statHappy reports the Bear DB dir as a directory and every other path
// as a present regular file — the "everything exists" stat seam for the
// happy path, with the one directory the db-dir check requires.
func statHappy(dbDir string) func(string) (os.FileInfo, error) {
	return func(path string) (os.FileInfo, error) {
		if path == dbDir {
			return statDir(path)
		}
		return statAll(path)
	}
}

func TestCheckSystemBearcliMissingIsError(t *testing.T) {
	opts := happyOptions(t)
	opts.StatFn = func(path string) (os.FileInfo, error) {
		if path == bearcli.BinaryPath {
			return nil, fs.ErrNotExist
		}
		return statAll(path)
	}
	got := findCheck(t, opts, "system.bearcli")
	if got.Status != diag.StatusError {
		t.Errorf("system.bearcli with missing binary = %q, want error", got.Status)
	}
}

func TestCheckSystemBearAppMissingIsError(t *testing.T) {
	opts := happyOptions(t)
	opts.StatFn = statMissing
	got := findCheck(t, opts, "system.bear-app")
	if got.Status != diag.StatusError {
		t.Errorf("system.bear-app with missing app = %q, want error", got.Status)
	}
}

func TestCheckSystemBearRunningWarns(t *testing.T) {
	opts := happyOptions(t)
	opts.ProcessRunningFn = func(string) (bool, error) { return true, nil }
	got := findCheck(t, opts, "system.bear-running")
	if got.Status != diag.StatusWarn {
		t.Errorf("system.bear-running while running = %q, want warn", got.Status)
	}
}

func TestCheckBearDBReadableOpenFailureIsError(t *testing.T) {
	opts := happyOptions(t)
	opts.OpenFn = openFail
	got := findCheck(t, opts, "bear.db-readable")
	if got.Status != diag.StatusError {
		t.Errorf("bear.db-readable with open failure = %q, want error", got.Status)
	}
}

func TestCheckBearDBDirMissingIsError(t *testing.T) {
	opts := happyOptions(t)
	opts.StatFn = statMissing
	got := findCheck(t, opts, "bear.db-dir")
	if got.Status != diag.StatusError {
		t.Errorf("bear.db-dir with missing dir = %q, want error", got.Status)
	}
}

func TestCheckConfigMissingIsError(t *testing.T) {
	opts := happyOptions(t)
	opts.ConfigPath = filepath.Join(t.TempDir(), "does-not-exist.toml")
	opts.StatFn = func(path string) (os.FileInfo, error) {
		if path == opts.ConfigPath {
			return nil, fs.ErrNotExist
		}
		return statAll(path)
	}
	got := findCheck(t, opts, "config.found")
	if got.Status != diag.StatusError {
		t.Errorf("config.found with missing file = %q, want error", got.Status)
	}
}

func TestCheckConfigInvalidIsError(t *testing.T) {
	opts := happyOptions(t)
	opts.ConfigPath = writeBrokenConfig(t)
	got := findCheck(t, opts, "config.valid")
	if got.Status != diag.StatusError {
		t.Errorf("config.valid with broken config = %q, want error", got.Status)
	}
	if got.Message == "" {
		t.Error("config.valid error must carry a formatted message")
	}
}

func TestCheckDaemonServiceNotLoadedWarns(t *testing.T) {
	opts := happyOptions(t)
	opts.LaunchctlPrintFn = func(string) error { return errors.New("could not find service") }
	got := findCheck(t, opts, "daemon.service")
	if got.Status != diag.StatusWarn {
		t.Errorf("daemon.service not loaded = %q, want warn", got.Status)
	}
	if got.Remediation == "" {
		t.Error("daemon.service warn must carry a non-empty remediation hint")
	}
}

// TestDaemonServiceSeamReceivesLabel pins the SSOT wiring: the launchctl
// seam is called with engine.LaunchdServiceLabel, not a re-typed
// literal.
func TestDaemonServiceSeamReceivesLabel(t *testing.T) {
	opts := happyOptions(t)
	var gotLabel string
	opts.LaunchctlPrintFn = func(label string) error {
		gotLabel = label
		return nil
	}
	_ = findCheck(t, opts, "daemon.service")
	if gotLabel != engine.LaunchdServiceLabel {
		t.Errorf("launchctl seam got label %q, want engine.LaunchdServiceLabel %q",
			gotLabel, engine.LaunchdServiceLabel)
	}
}

func TestCheckStateFirstRunWarns(t *testing.T) {
	opts := happyOptions(t)
	opts.StatePath = filepath.Join(t.TempDir(), "missing-state.json")
	got := findCheck(t, opts, "state.present")
	if got.Status != diag.StatusWarn {
		t.Errorf("state.present on first run = %q, want warn", got.Status)
	}
}

func TestCheckDaemonLogAbsentWarns(t *testing.T) {
	opts := happyOptions(t)
	opts.LogPath = filepath.Join(t.TempDir(), "no-such.log")
	opts.StatFn = func(path string) (os.FileInfo, error) {
		if path == opts.LogPath {
			return nil, fs.ErrNotExist
		}
		return statAll(path)
	}
	got := findCheck(t, opts, "daemon.log")
	if got.Status != diag.StatusWarn {
		t.Errorf("daemon.log absent = %q, want warn", got.Status)
	}
}

func TestCheckDaemonLogStaleWarns(t *testing.T) {
	opts := happyOptions(t)
	stale := time.Now().Add(-30 * 24 * time.Hour)
	opts.StatFn = func(path string) (os.FileInfo, error) {
		if path == opts.LogPath {
			return fakeInfo{mode: 0o644, mod: stale}, nil
		}
		return statAll(path)
	}
	got := findCheck(t, opts, "daemon.log")
	if got.Status != diag.StatusWarn {
		t.Errorf("daemon.log stale = %q, want warn", got.Status)
	}
}

// TestHappyPathAllPassOrWarn asserts the fully-ready environment
// produces zero error-status checks across every group.
func TestHappyPathAllPassOrWarn(t *testing.T) {
	opts := happyOptions(t)
	checks := collectChecks(t, opts)
	for _, c := range checks {
		if c.Status == diag.StatusError {
			t.Errorf("happy path produced an error check: %s — %s", c.Name, c.Message)
		}
	}
	wantGroups := []string{"System", "Bear DB", "Config", "State", "Daemon"}
	for _, g := range wantGroups {
		if !hasGroup(checks, g) {
			t.Errorf("happy path missing group %q", g)
		}
	}
}

// findCheck runs doctor.Run with json output and returns the single
// check whose Name matches id. Fails the test if absent.
func findCheck(t *testing.T, opts doctor.Options, id string) diag.Check {
	t.Helper()
	for _, c := range collectChecks(t, opts) {
		if c.Name == id {
			return c
		}
	}
	t.Fatalf("check %q not found in doctor output", id)
	return diag.Check{}
}

// collectChecks runs doctor with JSON output and decodes the checks
// slice. Run's error is ignored — the per-check coverage cares about
// the statuses, asserted via the decoded checks; Run's verdict is
// pinned separately in doctor_test.go.
func collectChecks(t *testing.T, opts doctor.Options) []diag.Check {
	t.Helper()
	var buf bytes.Buffer
	opts.Output = "json"
	opts.Stdout = &buf
	_ = doctor.Run(context.Background(), opts)
	return decodeChecks(t, buf.Bytes())
}

func hasGroup(checks []diag.Check, group string) bool {
	for _, c := range checks {
		if c.Group == group {
			return true
		}
	}
	return false
}

// fakeInfo is a minimal os.FileInfo for the stat seams.
type fakeInfo struct {
	mode fs.FileMode
	mod  time.Time
}

func (f fakeInfo) Name() string       { return "fake" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() fs.FileMode  { return f.mode }
func (f fakeInfo) ModTime() time.Time { return f.mod }
func (f fakeInfo) IsDir() bool        { return f.mode.IsDir() }
func (f fakeInfo) Sys() any           { return nil }

// writeValidConfig writes a minimal valid noxctl.toml and returns its
// path.
func writeValidConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "noxctl.toml")
	body := strings.Join([]string{
		`[meta]`,
		`version = "1"`,
		``,
		`[[domain]]`,
		`tag         = "llm/characters"`,
		`index_title = "Characters"`,
		`blueprint   = "flat-list"`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write valid config: %v", err)
	}
	return path
}

// writeBrokenConfig writes a noxctl.toml with an unknown field so
// config.Load fails the strict-decode pass.
func writeBrokenConfig(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "broken.toml")
	body := strings.Join([]string{
		`[meta]`,
		`version = "1"`,
		`bogus_unknown_field = "x"`,
		``,
	}, "\n")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write broken config: %v", err)
	}
	return path
}

// writeFreshLog writes a daemon log file with a just-now mtime so the
// daemon.log freshness check passes on the happy path.
func writeFreshLog(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "regen-watchd.log")
	if err := os.WriteFile(path, []byte("regen-watchd starting\n"), 0o600); err != nil {
		t.Fatalf("write fresh log: %v", err)
	}
	return path
}
