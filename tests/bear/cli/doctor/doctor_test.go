package doctor_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/diag"
	"github.com/barad1tos/noxctl/bear/cli/doctor"
)

// TestRunUsableReturnsNil pins the exit-0 contract: when Bear + bearcli
// + DB + config are all present, doctor.Run returns nil even though the
// daemon/state checks emit warnings. Warnings never fail the gate.
func TestRunUsableReturnsNil(t *testing.T) {
	opts := happyOptions(t)
	// Drive the State + Daemon groups into warn territory so the test
	// proves warnings do NOT block: missing state (first run) + not-
	// loaded daemon + absent log.
	opts.StatePath = filepath.Join(t.TempDir(), "missing.json")
	opts.LaunchctlPrintFn = func(string) error { return errors.New("not loaded") }
	missingLog := filepath.Join(t.TempDir(), "no.log")
	opts.LogPath = missingLog
	happyStat := opts.StatFn
	opts.StatFn = func(path string) (os.FileInfo, error) {
		if path == missingLog {
			return nil, os.ErrNotExist
		}
		return happyStat(path)
	}

	if err := doctor.Run(context.Background(), opts); err != nil {
		t.Fatalf("Run on a usable (warnings-only) environment = %v, want nil", err)
	}
}

// TestRunBlockingReturnsErrNotReady pins the exit-1 contract: a missing
// bearcli (an error-status check) makes Run return ErrNotReady.
func TestRunBlockingReturnsErrNotReady(t *testing.T) {
	opts := happyOptions(t)
	opts.StatFn = statMissing // Bear.app, bearcli, config, db-dir all absent → errors
	opts.OpenFn = openFail

	err := doctor.Run(context.Background(), opts)
	if !errors.Is(err, doctor.ErrNotReady) {
		t.Fatalf("Run on a blocking environment = %v, want ErrNotReady", err)
	}
}

// TestRunInvalidConfigReturnsErrNotReady pins that an invalid config
// (delegated to config.Load) blocks the gate.
func TestRunInvalidConfigReturnsErrNotReady(t *testing.T) {
	opts := happyOptions(t)
	opts.ConfigPath = writeBrokenConfig(t)

	err := doctor.Run(context.Background(), opts)
	if !errors.Is(err, doctor.ErrNotReady) {
		t.Fatalf("Run with invalid config = %v, want ErrNotReady", err)
	}
}

// TestRunJSONOutputCarriesSchemaAndSummary pins the JSON surface: each
// check has id + status, plus top-level schema_version and summary
// counts.
func TestRunJSONOutputCarriesSchemaAndSummary(t *testing.T) {
	opts := happyOptions(t)
	opts.Output = "json"
	var buf bytes.Buffer
	opts.Stdout = &buf
	_ = doctor.Run(context.Background(), opts)

	var result diag.Result
	if err := json.Unmarshal(buf.Bytes(), &result); err != nil {
		t.Fatalf("decode doctor JSON: %v\nraw: %s", err, buf.String())
	}
	if result.SchemaVersion != diag.SchemaVersion {
		t.Errorf("schema_version = %d, want %d", result.SchemaVersion, diag.SchemaVersion)
	}
	if len(result.Checks) == 0 {
		t.Fatal("doctor JSON carried zero checks")
	}
	for _, c := range result.Checks {
		if c.Name == "" || c.Status == "" {
			t.Errorf("check missing id or status: %+v", c)
		}
	}
	total := result.Summary.Pass + result.Summary.Warn + result.Summary.Fail +
		result.Summary.Skipped + result.Summary.Error
	if total != len(result.Checks) {
		t.Errorf("summary counts (%d) do not sum to check count (%d)", total, len(result.Checks))
	}
}

// TestRunTextOutputPrintsAllGroupsAndVerdict pins the grouped text
// render: every group header appears and a "doctor:" verdict line
// closes the output.
func TestRunTextOutputPrintsAllGroupsAndVerdict(t *testing.T) {
	opts := happyOptions(t)
	var buf bytes.Buffer
	opts.Stdout = &buf
	_ = doctor.Run(context.Background(), opts)
	out := buf.String()

	for _, group := range []string{"System", "Bear DB", "Config", "State", "Daemon"} {
		if !strings.Contains(out, group) {
			t.Errorf("text output missing group header %q\nfull:\n%s", group, out)
		}
	}
	if !strings.Contains(out, "doctor:") {
		t.Errorf("text output missing 'doctor:' verdict line\nfull:\n%s", out)
	}
}

// TestRunNeverInvokesBearcli is the hard read-only invariant: the
// bearcli seam is exercised by NO doctor code path. doctor stats
// bearcli.BinaryPath via StatFn but must never call bearcli.Run. We
// prove the negative structurally: a StatFn that records every stat'd
// path must include bearcli.BinaryPath (doctor stats it) and the
// OpenFn must be called only for the DB file (read-only open), never
// for a write. There is no bearcli invocation seam to trip because the
// package exposes none — the absence is the proof, asserted here by
// confirming Run completes using ONLY the read-only seams.
func TestRunUsesOnlyReadOnlySeams(t *testing.T) {
	opts := happyOptions(t)
	var statted []string
	opts.StatFn = func(path string) (os.FileInfo, error) {
		statted = append(statted, path)
		return statAll(path)
	}
	var opened []string
	opts.OpenFn = func(path string) (*os.File, error) {
		opened = append(opened, path)
		return os.CreateTemp(t.TempDir(), "db-*")
	}
	_ = doctor.Run(context.Background(), opts)

	// doctor must stat the bearcli binary (existence check only).
	if !containsSuffix(statted, "bearcli") {
		t.Errorf("doctor never stat'd the bearcli binary; statted=%v", statted)
	}
	// doctor must open ONLY the database file, read-only — exactly one
	// open, ending in database.sqlite.
	if len(opened) != 1 || !strings.HasSuffix(opened[0], "database.sqlite") {
		t.Errorf("OpenFn called for non-DB paths or more than once: %v", opened)
	}
}

func containsSuffix(paths []string, suffix string) bool {
	for _, p := range paths {
		if strings.HasSuffix(p, suffix) {
			return true
		}
	}
	return false
}

// decodeChecks decodes a diag.Result JSON blob and returns its checks
// slice. Shared by checks_test.go's collectChecks helper.
func decodeChecks(t *testing.T, raw []byte) []diag.Check {
	t.Helper()
	var result diag.Result
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("decode doctor result JSON: %v\nraw: %s", err, raw)
	}
	return result.Checks
}
