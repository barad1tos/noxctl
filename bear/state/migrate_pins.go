package state

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
)

// MigratePins copies legacyPath → targetPath idempotently and
// non-destructively. Behavior matrix:
//
// - case (a) source exists + target absent → copy bytes,
// slog.Info("pin registry migrated", "from", legacyPath,
// "to", targetPath), return nil.
// - case (b) both exist → no-op, no log, nil.
// - case (c) neither exists → no-op, no log, nil.
// - case (d) concurrent racers → at most one wins the
// O_EXCL create; the loser observes fs.ErrExist and returns
// nil (idempotent shape of case b).
//
// The legacy file is NEVER deleted. Operator keeps it for manual
// recovery. Symlink attacks at targetPath are mitigated by O_EXCL
// refusing to follow an existing symlink — the helper would fail the
// create, observe fs.ErrExist, and treat as case (b).
func MigratePins(legacyPath, targetPath string) error {
	// case (b) — target already exists.
	if _, err := os.Stat(targetPath); err == nil {
		return nil
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("MigratePins stat target %s: %w", targetPath, err)
	}
	// case (c) — legacy absent.
	src, err := os.Open(legacyPath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("MigratePins open legacy %s: %w", legacyPath, err)
	}
	defer func() { _ = src.Close() }()

	if mkerr := os.MkdirAll(filepath.Dir(targetPath), 0o700); mkerr != nil {
		return fmt.Errorf("MigratePins mkdir %s: %w", filepath.Dir(targetPath), mkerr)
	}
	// case (a) + (d) — race-safety via O_EXCL: only one concurrent
	// caller wins the create; the loser observes fs.ErrExist and
	// returns nil (degenerate to case b).
	dst, err := os.OpenFile(targetPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return nil
		}
		return fmt.Errorf("MigratePins open target %s: %w", targetPath, err)
	}
	defer func() { _ = dst.Close() }()

	if _, cerr := io.Copy(dst, src); cerr != nil {
		_ = os.Remove(targetPath) // partial copy — clean up so retry sees a clean slate
		return fmt.Errorf("MigratePins copy: %w", cerr)
	}
	if serr := dst.Sync(); serr != nil {
		_ = os.Remove(targetPath)
		return fmt.Errorf("MigratePins sync: %w", serr)
	}
	slog.Info("pin registry migrated", "from", legacyPath, "to", targetPath)
	return nil
}
