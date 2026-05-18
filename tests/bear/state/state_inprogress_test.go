package bear_state_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/state"
)

// TestState_InProgressRoundTrip seeds a `State` with a populated `InProgress`
// (Verb + StartedAt), persists via Save, re-reads via Load, and asserts the
// nested struct round-trips byte-for-byte. Foundation gate for D-14
// (interrupted-run discrimination).
func TestState_InProgressRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	started := time.Now().UTC().Truncate(time.Second)
	want := &state.State{
		Version:    "1",
		InProgress: state.InProgress{Verb: "apply", StartedAt: started},
	}
	if err := want.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.InProgress.Verb != "apply" {
		t.Errorf("Verb mismatch: want %q got %q", "apply", got.InProgress.Verb)
	}
	if !got.InProgress.StartedAt.Equal(started) {
		t.Errorf("StartedAt mismatch: want %v got %v", started, got.InProgress.StartedAt)
	}
}

// TestState_LoadPhase1FileNoInProgress writes a Phase-1-vintage state.json
// (no `in_progress` key) and confirms the Phase-2 loader degrades cleanly:
// no error, zero-value `InProgress`. Pitfall 6 (RESEARCH.md): backwards
// compatibility for state files written before this schema bump.
func TestState_LoadPhase1FileNoInProgress(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	phase1 := `{"version":"1","applied_config_hash":"sha256:abc"}`
	if err := os.WriteFile(path, []byte(phase1), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	got, err := state.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.InProgress.Verb != "" {
		t.Errorf("expected zero-value InProgress.Verb, got %q", got.InProgress.Verb)
	}
	if !got.InProgress.StartedAt.IsZero() {
		t.Errorf("expected zero-value InProgress.StartedAt, got %v", got.InProgress.StartedAt)
	}
}

// TestState_InProgressEmpty_OmitsVerbFromJSON confirms `omitempty` works as
// documented for the inner string `Verb` field. Mirrors `LastApply` shape:
// the encoder still writes the parent `in_progress` key (Go's encoding/json
// does NOT skip non-pointer nested structs under `omitempty` — same gotcha
// as `last_apply` on `time.Time` per LEARNINGS), but the inner
// `verb` string omits cleanly when empty. The schema-clarity contract is
// that operators reading raw state.json never see a `"verb": ""` artifact
// for a no-op state.
func TestState_InProgressEmpty_OmitsVerbFromJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	s := &state.State{Version: "1"}
	if err := s.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if strings.Contains(string(raw), `"verb"`) {
		t.Errorf("zero-value Verb should be omitted; got JSON: %s", raw)
	}
}
