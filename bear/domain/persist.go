// Package domain core helpers — atomic write boundary.
//
// AtomicWriteJSON marshals v as indented JSON and writes it to path
// atomically: temp file in the parent dir → chmod → fsync(file) →
// close → rename → fsync(parent dir). On macOS APFS this protects
// against partial-write corruption from SIGKILL or power loss. Parent
// dir is created with mode 0o700 if absent. The file is created with
// the explicit perm parameter — no default — so callers can't fall
// back to a wide umask-derived mode for sensitive state files.
//
// perm MUST be the actual desired mode (e.g. 0o600 for state.json /
// pins.json). Concurrent writers using the same path are safe: the tmp
// file uses os.CreateTemp for a unique name per writer, and rename is
// atomic on POSIX-conforming filesystems including APFS.
package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// AtomicWriteJSON writes v to path atomically. tmp+rename+fsync(file)+
// fsync(parent). perm controls the final file mode (0o600 for
// state.json / pins.json). The parent directory is created with mode
// 0o700 if absent; existing directory permissions are preserved.
func AtomicWriteJSON(path string, v any, perm os.FileMode) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("AtomicWriteJSON marshal: %w", err)
	}
	dir := filepath.Dir(path)
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return fmt.Errorf("AtomicWriteJSON mkdir %s: %w", dir, mkErr)
	}
	base := filepath.Base(path)
	tmp, err := os.CreateTemp(dir, base+".*.tmp")
	if err != nil {
		return fmt.Errorf("AtomicWriteJSON tmp: %w", err)
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("AtomicWriteJSON write: %w", werr)
	}
	// chmod BEFORE Sync so the durable bytes-on-disk carry the right
	// perm — avoids a window where 0o600 flips to default on a
	// re-mounted filesystem between Sync and an out-of-band chmod.
	if cherr := tmp.Chmod(perm); cherr != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("AtomicWriteJSON chmod: %w", cherr)
	}
	if serr := tmp.Sync(); serr != nil {
		_ = tmp.Close()
		cleanup()
		return fmt.Errorf("AtomicWriteJSON sync: %w", serr)
	}
	if cerr := tmp.Close(); cerr != nil {
		cleanup()
		return fmt.Errorf("AtomicWriteJSON close: %w", cerr)
	}
	if rerr := os.Rename(tmpName, path); rerr != nil {
		cleanup()
		return fmt.Errorf("AtomicWriteJSON rename: %w", rerr)
	}
	// dir-fsync — best-effort durability of the rename's directory
	// entry. Ignored on systems without dirsync support.
	if dirfd, derr := os.Open(dir); derr == nil {
		_ = dirfd.Sync()
		_ = dirfd.Close()
	}
	return nil
}
