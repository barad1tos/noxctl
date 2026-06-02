// Package doctor_test — the doctor exit-code contract, table-driven.
//
// This file pins the verdict mapping that the cmd layer turns into a
// process exit code: a usable seamed environment makes doctor.Run return
// nil (exit 0) even when optional infra is missing (warnings), while a
// blocking environment — a missing bearcli binary or an invalid config —
// makes Run return ErrNotReady (exit 1).
//
// Unlike checks_test.go's per-check coverage (which injects an OpenFn
// seam), the DB-readability leg here is exercised HONESTLY: a real
// database.sqlite temp file is created and the default OpenFn (os.Open)
// runs against it, so the os.Open + immediate-close readability path is
// covered for real, not stubbed.
package doctor_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli/doctor"
)

// exitCodeOptions builds doctor.Options for the exit-code table. It seeds
// a real Bear DB directory containing a real, openable database.sqlite
// and leaves OpenFn nil so the production os.Open readability path runs
// against that real file. StatFn is seamed (the System/Config/DB-dir
// existence checks must not depend on the host's real /Applications or
// bearcli install), but every "exists" answer it gives is true except
// for the daemon log, which is reported absent to drive a warning. The
// daemon/state seams resolve to warn so the usable case proves warnings
// never fail the gate.
func exitCodeOptions(t *testing.T) doctor.Options {
	t.Helper()
	dbDir := t.TempDir()
	dbPath := filepath.Join(dbDir, "database.sqlite")
	if err := os.WriteFile(dbPath, []byte("SQLite format 3\x00"), 0o600); err != nil {
		t.Fatalf("seed real database.sqlite: %v", err)
	}
	missingLog := filepath.Join(t.TempDir(), "absent.log")
	return doctor.Options{
		ConfigPath: writeValidConfig(t),
		BearDBDir:  dbDir,
		// First-run state (missing file) → warn; never blocks.
		StatePath: filepath.Join(t.TempDir(), "missing-state.json"),
		LogPath:   missingLog,
		Output:    "text",
		Stdout:    new(bytes.Buffer),
		Stderr:    new(bytes.Buffer),
		// StatFn reports the DB dir as a directory, the daemon log as
		// absent (→ warn), and every other stat target as a present
		// regular file. OpenFn is deliberately nil so os.Open exercises
		// the real database.sqlite created above.
		StatFn: func(path string) (os.FileInfo, error) {
			switch path {
			case missingLog:
				return nil, os.ErrNotExist
			case dbDir:
				return os.Stat(dbDir)
			default:
				return statAll(path)
			}
		},
		// Daemon not loaded → warn; never blocks.
		LaunchctlPrintFn: func(string) (string, error) { return "", errors.New("could not find service") },
		ProcessRunningFn: func(string) (bool, error) { return false, nil },
		GOOS:             "darwin",
	}
}

// TestDoctorExitCodeContract is the table-driven exit-code proof: a
// usable seamed environment exits 0 (nil) despite warnings, and each
// blocking variant (missing bearcli, invalid config) exits 1
// (ErrNotReady).
func TestDoctorExitCodeContract(t *testing.T) {
	cases := []struct {
		name       string
		mutate     func(t *testing.T, opts *doctor.Options)
		wantErr    error // nil means "want exit 0"
		wantNotNil bool  // true means "want ErrNotReady (exit 1)"
	}{
		{
			name:   "usable environment exits 0 despite warnings",
			mutate: func(*testing.T, *doctor.Options) {},
		},
		{
			name: "missing bearcli blocks and exits 1",
			mutate: func(t *testing.T, opts *doctor.Options) {
				t.Helper()
				prev := opts.StatFn
				opts.StatFn = func(path string) (os.FileInfo, error) {
					if path == bearcli.BinaryPath {
						return nil, os.ErrNotExist
					}
					return prev(path)
				}
			},
			wantNotNil: true,
		},
		{
			name: "invalid config blocks and exits 1",
			mutate: func(t *testing.T, opts *doctor.Options) {
				t.Helper()
				opts.ConfigPath = writeBrokenConfig(t)
			},
			wantNotNil: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := exitCodeOptions(t)
			tc.mutate(t, &opts)

			err := doctor.Run(context.Background(), opts)

			if tc.wantNotNil {
				if !errors.Is(err, doctor.ErrNotReady) {
					t.Fatalf("Run = %v, want ErrNotReady (exit 1)", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Run = %v, want nil (exit 0)", err)
			}
		})
	}
}

// TestDoctorDBReadablePathExercisedForReal proves the readability check
// runs the real os.Open + close against a real file: with a usable
// environment whose only seam is StatFn (OpenFn nil → os.Open), Run
// reaches a non-blocking verdict. Removing the real database.sqlite then
// flips the readability check to error and Run to ErrNotReady — proving
// the os.Open leg, not a stub, decides the verdict.
func TestDoctorDBReadablePathExercisedForReal(t *testing.T) {
	opts := exitCodeOptions(t)
	if err := doctor.Run(context.Background(), opts); err != nil {
		t.Fatalf("usable env with a real openable DB = %v, want nil", err)
	}

	// Delete the real database.sqlite so the production os.Open fails.
	dbPath := filepath.Join(opts.BearDBDir, "database.sqlite")
	if err := os.Remove(dbPath); err != nil {
		t.Fatalf("remove seeded DB: %v", err)
	}
	if err := doctor.Run(context.Background(), opts); !errors.Is(err, doctor.ErrNotReady) {
		t.Fatalf("Run after removing the real DB = %v, want ErrNotReady (os.Open must fail)", err)
	}
}
