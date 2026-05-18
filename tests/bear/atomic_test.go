package bear_test

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/barad1tos/noxctl/bear"
)

// TestAtomicWriteJSON_WritesJSONWithRequestedPerm round-trips a small
// payload and asserts the on-disk file mode equals the requested perm.
func TestAtomicWriteJSON_WritesJSONWithRequestedPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.json")
	if err := bear.AtomicWriteJSON(path, map[string]int{"k": 1}, 0o600); err != nil {
		t.Fatalf("AtomicWriteJSON: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got map[string]int
	if uerr := json.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("Unmarshal: %v (raw=%q)", uerr, string(data))
	}
	if got["k"] != 1 {
		t.Errorf("round-trip mismatch: got %v, want {k:1}", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Errorf("perm: got %v, want %v", got, want)
	}
}

// TestAtomicWriteJSON_CreatesParentDirIfAbsent verifies that two-level
// nested parent directories are created with mode 0o700.
func TestAtomicWriteJSON_CreatesParentDirIfAbsent(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "nested", "deeper", "a.json")
	if err := bear.AtomicWriteJSON(path, "v", 0o600); err != nil {
		t.Fatalf("AtomicWriteJSON: %v", err)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat parent: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o700); got != want {
		t.Errorf("parent perm: got %v, want %v", got, want)
	}
}

// TestAtomicWriteJSON_ConcurrentWritersNoEEXIST runs two goroutines
// writing the same path. Both must succeed (unique tmp names via
// os.CreateTemp), the resulting file must contain one of the two
// payloads, and no.tmp leftover may remain in the directory.
func TestAtomicWriteJSON_ConcurrentWritersNoEEXIST(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "race.json")
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		errs <- bear.AtomicWriteJSON(path, map[string]string{"who": "A"}, 0o600)
	}()
	go func() {
		defer wg.Done()
		errs <- bear.AtomicWriteJSON(path, map[string]string{"who": "B"}, 0o600)
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent write: %v", err)
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var got map[string]string
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("Unmarshal after concurrent writes: %v (raw=%q)", uerr, string(raw))
	}
	if who := got["who"]; who != "A" && who != "B" {
		t.Errorf("expected one of A/B; got %q", who)
	}
	assertNoTmpLeftovers(t, dir)
}

// TestAtomicWriteJSON_CrashLeavesNoPartialJSON simulates a crash window
// between tmp creation and rename: a "garbage" half-written tmp sits
// next to the target file. The target itself must still parse — the
// crash invariant is that callers never observe partial JSON at the
// canonical path.
func TestAtomicWriteJSON_CrashLeavesNoPartialJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "crash.json")
	if err := bear.AtomicWriteJSON(path, map[string]int{"v": 1}, 0o600); err != nil {
		t.Fatalf("seed v1: %v", err)
	}
	fakeTmp := path + ".garbage.tmp"
	if err := os.WriteFile(fakeTmp, []byte("{\"v\":"), 0o600); err != nil {
		t.Fatalf("seed garbage tmp: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile post-crash: %v", err)
	}
	var got map[string]int
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("partial-JSON observed at target path: %v (raw=%q)", uerr, string(raw))
	}
	if got["v"] != 1 {
		t.Errorf("crash invariant broken: target rolled forward to %v, want {v:1}", got)
	}
}

// TestAtomicWriteJSON_PermExplicitNoImplicitOverride confirms that
// passing 0o644 yields exactly 0o644 (no implicit umask interaction).
func TestAtomicWriteJSON_PermExplicitNoImplicitOverride(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "perm.json")
	if err := bear.AtomicWriteJSON(path, "v", 0o644); err != nil {
		t.Fatalf("AtomicWriteJSON: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o644); got != want {
		t.Errorf("perm: got %v, want %v (no implicit umask override expected)", got, want)
	}
}

// TestAtomicWriteJSON_MarshalErrorWrappedAndNoTmpLeftBehind asserts
// that an unserializable value produces a wrapped error containing
// "AtomicWriteJSON marshal", that errors.As still extracts
// json.UnsupportedTypeError, and that no temp file leaks.
func TestAtomicWriteJSON_MarshalErrorWrappedAndNoTmpLeftBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	err := bear.AtomicWriteJSON(path, make(chan int), 0o600)
	if err == nil {
		t.Fatal("expected marshal error, got nil")
	}
	if !strings.Contains(err.Error(), "AtomicWriteJSON marshal") {
		t.Errorf("error %q missing %q prefix", err.Error(), "AtomicWriteJSON marshal")
	}
	if _, ok := errors.AsType[*json.UnsupportedTypeError](err); !ok {
		t.Errorf("error chain lost json.UnsupportedTypeError: %v", err)
	}
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		t.Fatalf("ReadDir: %v", derr)
	}
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("marshal failure left files behind: %v", names)
	}
}

// assertNoTmpLeftovers fails the test if any directory entry contains
// ".tmp" in its name. Used by the concurrent-writers case to verify
// the loser cleans up its os.CreateTemp file after losing the rename
// race.
func assertNoTmpLeftovers(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("leftover tmp file %q after concurrent writes", e.Name())
		}
	}
}

// TestAtomicWikilink_EmptyTitleFallback locks the empty-title rendering
// fix: an atom whose Bear note has no title (typically because the user
// clicked an old "Нова нотатка" link without title= param) used to
// render as `[[]]` — broken wikilink, unclickable bullet. Now it falls
// back to a bear://open-note URL with the localized "Без назви"
// placeholder so the bullet stays clickable and the orphan is visible
// to the operator.
func TestAtomicWikilink_EmptyTitleFallback(t *testing.T) {
	note := bear.Note{ID: "DEADBEEF-1234", Title: ""}
	got := bear.AtomicWikilink(nil, note)
	if strings.Contains(got, "[[]]") {
		t.Errorf("empty-title atom should not render as broken `[[]]`; got %q", got)
	}
	if !strings.Contains(got, "bear://x-callback-url/open-note?id=DEADBEEF-1234") {
		t.Errorf("empty-title atom should fall back to bear://open-note URL; got %q", got)
	}
	// The placeholder label must be a non-empty string (locale-dependent),
	// matching the form `[<label>](bear://...)`.
	if !strings.HasPrefix(got, "[") || strings.HasPrefix(got, "[](") {
		t.Errorf("empty-title atom needs a non-empty placeholder label; got %q", got)
	}
}

// TestAtomicWikilink_NonEmptyTitleStaysWikilink guards that the
// empty-title fallback didn't regress the common path: a normal
// unique-title atom still renders as `[[Title]]`.
func TestAtomicWikilink_NonEmptyTitleStaysWikilink(t *testing.T) {
	note := bear.Note{ID: "X", Title: "Paranova"}
	got := bear.AtomicWikilink(nil, note)
	if got != "[[Paranova]]" {
		t.Errorf("unique-title atom should render as `[[Title]]`; got %q", got)
	}
}
