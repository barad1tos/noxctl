package bear_state_test

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/state"
)

// captureSlog installs a per-test slog text handler at LevelInfo (so
// both Info and Warn records flow through) and restores the previous
// default on cleanup. Per-test reset of package-level state instead
// of @AfterEach.
func captureSlog(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return buf
}

// seedLegacy writes deterministic JSON pins to legacyPath and returns
// the pre-write mtime for non-mutation assertions in case (a) and (e).
func seedLegacy(t *testing.T, legacyPath string) (string, time.Time) {
	t.Helper()
	dir := filepath.Dir(legacyPath)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("MkdirAll legacy: %v", err)
	}
	body := `{"ABC":{"domain":"quicknote/daily","pinnedAt":"2026-05-07T22:30:00Z"}}`
	if err := os.WriteFile(legacyPath, []byte(body), 0o600); err != nil {
		t.Fatalf("seed legacy: %v", err)
	}
	info, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatalf("Stat legacy: %v", err)
	}
	return body, info.ModTime()
}

// TestMigratePins_SourceExistsTargetAbsent is case (a) of MigratePins:
// copy preserves bytes, perm 0o600 on target, source untouched, exactly
// one slog.Info emitted with from/to keys.
func TestMigratePins_SourceExistsTargetAbsent(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "legacy", "regen-watchd-pins.json")
	targetPath := filepath.Join(root, "project", ".noxctl", "pins.json")
	wantBody, srcMTimeBefore := seedLegacy(t, legacyPath)
	logBuf := captureSlog(t)

	if err := state.MigratePins(legacyPath, targetPath); err != nil {
		t.Fatalf("MigratePins: %v", err)
	}
	gotBody, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(gotBody) != wantBody {
		t.Errorf("byte mismatch: got %q want %q", gotBody, wantBody)
	}
	info, err := os.Stat(targetPath)
	if err != nil {
		t.Fatalf("Stat target: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("target perm: got %v want %v", got, want)
	}
	srcInfoAfter, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatalf("Stat legacy after: %v", err)
	}
	if delta := srcInfoAfter.ModTime().Sub(srcMTimeBefore); delta > time.Second || delta < -time.Second {
		t.Errorf("legacy mtime drift: before=%v after=%v", srcMTimeBefore, srcInfoAfter.ModTime())
	}
	srcBodyAfter, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy after: %v", err)
	}
	if string(srcBodyAfter) != wantBody {
		t.Errorf("legacy content mutated: got %q want %q", srcBodyAfter, wantBody)
	}
	logged := logBuf.String()
	if !strings.Contains(logged, "pin registry migrated") {
		t.Errorf("expected slog.Info containing %q; got %q", "pin registry migrated", logged)
	}
	if !strings.Contains(logged, "from=") || !strings.Contains(logged, "to=") {
		t.Errorf("slog record missing from/to attrs: %q", logged)
	}
	if n := strings.Count(logged, "pin registry migrated"); n != 1 {
		t.Errorf("expected exactly 1 migration log, got %d: %q", n, logged)
	}
}

// assertUnchanged fails if the file at path differs in content or
// mtime from the snapshot taken before the call. Tolerance is 1s.
func assertUnchanged(t *testing.T, path, wantBody string, beforeMTime time.Time, label string) {
	t.Helper()
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", label, err)
	}
	if string(got) != wantBody {
		t.Errorf("%s mutated: got %q want %q", label, got, wantBody)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", label, err)
	}
	if delta := info.ModTime().Sub(beforeMTime); delta > time.Second || delta < -time.Second {
		t.Errorf("%s mtime drift: before=%v after=%v", label, beforeMTime, info.ModTime())
	}
}

// TestMigratePins_BothExistNoOp is case (b): if target already exists,
// MigratePins returns nil error, mutates nothing, emits no log.
func TestMigratePins_BothExistNoOp(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "legacy", "regen-watchd-pins.json")
	targetPath := filepath.Join(root, "project", ".noxctl", "pins.json")
	legacyBody, legacyMTime := seedLegacy(t, legacyPath)
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o700); err != nil {
		t.Fatal(err)
	}
	targetBody := `{"DEF":{"domain":"quicknote/weekly","pinnedAt":"2026-04-01T00:00:00Z"}}`
	if err := os.WriteFile(targetPath, []byte(targetBody), 0o600); err != nil {
		t.Fatal(err)
	}
	targetInfoBefore, err := os.Stat(targetPath)
	if err != nil {
		t.Fatal(err)
	}
	logBuf := captureSlog(t)

	if migErr := state.MigratePins(legacyPath, targetPath); migErr != nil {
		t.Fatalf("MigratePins case (b): %v", migErr)
	}
	assertUnchanged(t, targetPath, targetBody, targetInfoBefore.ModTime(), "target")
	assertUnchanged(t, legacyPath, legacyBody, legacyMTime, "legacy")
	if logged := logBuf.String(); strings.Contains(logged, "pin registry migrated") {
		t.Errorf("case (b) emitted log: %q", logged)
	}
}

// TestMigratePins_NeitherExistsNoOp is case (c): both paths absent →
// nil error, no log, no files created.
func TestMigratePins_NeitherExistsNoOp(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "legacy", "regen-watchd-pins.json")
	targetPath := filepath.Join(root, "project", ".noxctl", "pins.json")
	logBuf := captureSlog(t)

	if err := state.MigratePins(legacyPath, targetPath); err != nil {
		t.Fatalf("MigratePins case (c): %v", err)
	}
	if _, err := os.Stat(targetPath); !os.IsNotExist(err) {
		t.Errorf("case (c) created target unexpectedly")
	}
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Errorf("case (c) created legacy unexpectedly")
	}
	if logged := logBuf.String(); strings.Contains(logged, "pin registry migrated") {
		t.Errorf("case (c) emitted log: %q", logged)
	}
}

// TestMigratePins_ConcurrentRaceSafe is case (d): two goroutines call
// MigratePins concurrently with the same paths. O_EXCL ensures only
// one wins the create — both calls return nil, target ends up with
// valid content, at most one slog.Info is emitted.
func TestMigratePins_ConcurrentRaceSafe(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "legacy", "regen-watchd-pins.json")
	targetPath := filepath.Join(root, "project", ".noxctl", "pins.json")
	wantBody, _ := seedLegacy(t, legacyPath)
	logBuf := captureSlog(t)

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() { defer wg.Done(); errs <- state.MigratePins(legacyPath, targetPath) }()
	go func() { defer wg.Done(); errs <- state.MigratePins(legacyPath, targetPath) }()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent MigratePins: %v", err)
		}
	}
	gotBody, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("read target after race: %v", err)
	}
	if string(gotBody) != wantBody {
		t.Errorf("target body mismatch after race: got %q want %q", gotBody, wantBody)
	}
	logged := logBuf.String()
	if n := strings.Count(logged, "pin registry migrated"); n > 1 {
		t.Errorf("race emitted %d migration logs (want at most 1): %q", n, logged)
	}
}

// TestMigratePins_LegacyNeverDeleted is case (e): legacy is left in
// place for operator recovery. After case (a) completes, the legacy
// file is still readable.
func TestMigratePins_LegacyNeverDeleted(t *testing.T) {
	root := t.TempDir()
	legacyPath := filepath.Join(root, "legacy", "regen-watchd-pins.json")
	targetPath := filepath.Join(root, "project", ".noxctl", "pins.json")
	wantBody, _ := seedLegacy(t, legacyPath)
	captureSlog(t)

	if err := state.MigratePins(legacyPath, targetPath); err != nil {
		t.Fatalf("MigratePins: %v", err)
	}
	info, err := os.Stat(legacyPath)
	if err != nil {
		t.Fatalf("legacy disappeared: %v", err)
	}
	if !info.Mode().IsRegular() {
		t.Errorf("legacy not a regular file post-migration: %v", info.Mode())
	}
	got, err := os.ReadFile(legacyPath)
	if err != nil {
		t.Fatalf("read legacy: %v", err)
	}
	if string(got) != wantBody {
		t.Errorf("legacy content mutated: got %q want %q", got, wantBody)
	}
}
