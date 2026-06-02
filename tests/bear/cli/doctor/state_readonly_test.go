// Package doctor_test — read-only invariant coverage for doctor's State
// group.
//
// doctor's hard invariant is "MUTATES NOTHING." The State group must
// therefore read state.json without the corrupt-file rename side effect
// that state.Load performs for apply/daemon. These tests seed a corrupt
// state.json on disk, run doctor.Run, and assert the file is left
// exactly where it was — no `.corrupt-*` sibling, no removal.
package doctor_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/diag"
	"github.com/barad1tos/noxctl/bear/cli/doctor"
)

// TestDoctorLeavesCorruptStateUntouched is the CR-01 regression: a
// syntactically-corrupt state.json must survive a doctor run intact.
// doctor reports it as a warn but never renames or deletes it (that
// mutation budget belongs to apply/daemon, not the read-only doctor).
func TestDoctorLeavesCorruptStateUntouched(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	const corruptBody = "{ this is not valid json"
	if err := os.WriteFile(statePath, []byte(corruptBody), 0o600); err != nil {
		t.Fatalf("seed corrupt state.json: %v", err)
	}

	opts := happyOptions(t)
	opts.StatePath = statePath
	if err := doctor.Run(context.Background(), opts); err != nil {
		t.Fatalf("doctor.Run on a corrupt-state environment = %v, want nil (warn only)", err)
	}

	// The corrupt file must still exist, with its bytes unchanged.
	got, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatalf("corrupt state.json is gone after doctor.Run (mutated!): %v", err)
	}
	if string(got) != corruptBody {
		t.Errorf("corrupt state.json bytes changed: got %q, want %q", got, corruptBody)
	}

	// No `.corrupt-*` sibling may have been created.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read state dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".corrupt-") {
			t.Errorf("doctor created a corrupt-rename sibling %q — it must not mutate", e.Name())
		}
	}
}

// TestDoctorCorruptStateReportsWarn pins that the corrupt-state case
// surfaces as a warn (not a blocking error) so the gate stays passable
// and the operator is told to investigate.
func TestDoctorCorruptStateReportsWarn(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(statePath, []byte("not json"), 0o600); err != nil {
		t.Fatalf("seed corrupt state.json: %v", err)
	}
	opts := happyOptions(t)
	opts.StatePath = statePath
	got := findCheck(t, opts, "state.present")
	if got.Status != diag.StatusWarn {
		t.Errorf("state.present on corrupt state = %q, want warn", got.Status)
	}
}
