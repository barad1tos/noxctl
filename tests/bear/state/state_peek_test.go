package bear_state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/state"
)

// TestPeek_MissingFile — a missing state.json reports present=false,
// corrupt=false, no error (the read-only first-run signal).
func TestPeek_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	lastApply, present, corrupt, err := state.Peek(path)
	if err != nil {
		t.Fatalf("Peek missing: %v", err)
	}
	if present || corrupt {
		t.Errorf("Peek missing: present=%v corrupt=%v, want both false", present, corrupt)
	}
	if !lastApply.IsZero() {
		t.Errorf("Peek missing: lastApply = %v, want zero", lastApply)
	}
}

// TestPeek_CorruptFileIsNotRenamed is the read-only invariant Peek
// exists for: a corrupt file reports corrupt=true AND is left exactly
// where it was — no rename, no removal (contrast state.Load, which
// renames for forensics).
func TestPeek_CorruptFileIsNotRenamed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	const body = "{ not valid json"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("seed corrupt state: %v", err)
	}

	_, present, corrupt, err := state.Peek(path)
	if err != nil {
		t.Fatalf("Peek corrupt: %v", err)
	}
	if !present || !corrupt {
		t.Errorf("Peek corrupt: present=%v corrupt=%v, want both true", present, corrupt)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("corrupt file gone after Peek (mutated!): %v", err)
	}
	if string(got) != body {
		t.Errorf("corrupt file changed: got %q, want %q", got, body)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			t.Errorf("Peek created a corrupt-rename sibling %q — it must be read-only", e.Name())
		}
	}
}

// TestPeek_ValidFileReturnsLastApply — a valid state.json round-trips
// its LastApply through Peek, present=true, corrupt=false.
func TestPeek_ValidFileReturnsLastApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	want := time.Now().UTC().Truncate(time.Second)
	seeded := &state.State{Version: state.SchemaVersion, LastApply: want}
	if err := seeded.Save(path); err != nil {
		t.Fatalf("seed valid state: %v", err)
	}

	lastApply, present, corrupt, err := state.Peek(path)
	if err != nil {
		t.Fatalf("Peek valid: %v", err)
	}
	if !present || corrupt {
		t.Errorf("Peek valid: present=%v corrupt=%v, want present=true corrupt=false", present, corrupt)
	}
	if !lastApply.Equal(want) {
		t.Errorf("Peek valid: lastApply = %v, want %v", lastApply, want)
	}
}
