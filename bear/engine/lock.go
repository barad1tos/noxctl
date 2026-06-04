// Package engine flock helpers — per-cycle exclusive lock at./.noxctl/.lock
// plus the apply-pending sentinel protocol that lets noxctl apply yield
// priority over the long-running daemon.
//
// Layering: stdlib + golang.org/x/sys/unix only. Lock fd lifetime is the
// caller's responsibility — kernel auto-releases on fd close (macOS
// flock(2) "Locks are on files, not file descriptors") so the returned
// release func wraps unix.Flock(LOCK_UN) + unix.Close in one closure.
//
// Sentinel protocol: apply touches./.noxctl/.apply-pending BEFORE the
// blocking flock call so daemon's sentinel check during cycle-start
// observes it and skips. flock alone guarantees correctness; the
// sentinel is a best-effort priority hint, NOT a strict ordering
// primitive.
//
// Threat surface: symlink attacks on lockPath are blocked via
// O_NOFOLLOW. Stale-PID lockfile content is purely informational
// (`lsof` diagnostic) — flock auto-release on process death is the
// actual mutual-exclusion control; we never `kill` based on it.
package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

// SentinelName is the basename of the apply-pending priority sentinel.
// Lives in filepath.Dir(lockPath) — typically `./.noxctl/.apply-pending`.
const SentinelName = ".apply-pending"

// lockOpenFlags is the canonical open(2) flag set for both apply-side
// and daemon-side lock acquisition.
//
// - O_CREAT — create if absent (first-ever acquire bootstraps the file)
// - O_RDWR — write PID-only diagnostic content after flock succeeds
// - O_CLOEXEC — keep the lock fd from leaking into bearcli subprocesses
// - O_NOFOLLOW — refuse to follow a pre-planted symlink
const lockOpenFlags = unix.O_CREAT | unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW

// AcquireApply opens lockPath, writes the sentinel, blocks on LOCK_EX
// (or fails fast if noWait), and returns a release closure that
// unlocks + closes + cleans the sentinel. lockPath parent dir is
// created with mode 0o700 if absent (matching domain.AtomicWriteJSON).
//
// ctx is currently informational — flock blocks at syscall level and
// does not honor ctx. SIGINT during acquire surfaces via the signal
// handler installed by cmd/noxctl/apply.go, which
// unwinds before AcquireApply is called or kills the process.
func AcquireApply(ctx context.Context, lockPath string, noWait bool, stderr io.Writer) (release func(), err error) {
	_ = ctx // ctx reserved for future extension; flock is not ctx-aware
	if stderr == nil {
		stderr = os.Stderr
	}
	if mkdirErr := os.MkdirAll(filepath.Dir(lockPath), 0o700); mkdirErr != nil {
		return nil, fmt.Errorf("AcquireApply mkdir: %w", mkdirErr)
	}
	sentinelPath := filepath.Join(filepath.Dir(lockPath), SentinelName)
	// Touch sentinel BEFORE blocking flock so daemon's cycle-start
	// sentinel check observes it and yields.
	if f, touchErr := os.Create(sentinelPath); touchErr == nil {
		_ = f.Close()
	}
	fd, openErr := openLockFile(lockPath, "AcquireApply")
	if openErr != nil {
		_ = os.Remove(sentinelPath)
		return nil, openErr
	}
	if noWait {
		if flockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); flockErr != nil {
			_ = unix.Close(fd)
			_ = os.Remove(sentinelPath)
			return nil, fmt.Errorf("AcquireApply flock: %w", flockErr)
		}
	} else if flockErr := acquireBlockingFlock(fd, lockPath, "AcquireApply", stderr); flockErr != nil {
		_ = unix.Close(fd)
		_ = os.Remove(sentinelPath)
		return nil, flockErr
	}
	writeLockPID(fd)
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
		_ = os.Remove(sentinelPath)
	}, nil
}

// AcquireVerify is the read-only verifier's blocking lock acquire.
// It serializes with daemon/apply cycles but deliberately does NOT
// touch the apply-pending sentinel: verify will not apply after the
// daemon yields, so advertising apply intent could drop a real daemon
// event when polling is disabled.
func AcquireVerify(ctx context.Context, lockPath string, stderr io.Writer) (release func(), err error) {
	_ = ctx
	if stderr == nil {
		stderr = os.Stderr
	}
	fd, openErr := openLockFile(lockPath, "AcquireVerify")
	if openErr != nil {
		return nil, openErr
	}
	if flockErr := acquireBlockingFlock(fd, lockPath, "AcquireVerify", stderr); flockErr != nil {
		_ = unix.Close(fd)
		return nil, flockErr
	}
	writeLockPID(fd)
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

func isLockHeld(err error) bool {
	return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN)
}

func openLockFile(lockPath, operation string) (int, error) {
	if mkdirErr := os.MkdirAll(filepath.Dir(lockPath), 0o700); mkdirErr != nil {
		return -1, fmt.Errorf("%s mkdir: %w", operation, mkdirErr)
	}
	fd, openErr := unix.Open(lockPath, lockOpenFlags, 0o600)
	if openErr != nil {
		return -1, fmt.Errorf("%s open %s: %w", operation, lockPath, openErr)
	}
	return fd, nil
}

func acquireBlockingFlock(fd int, lockPath, operation string, stderr io.Writer) error {
	if flockErr := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); flockErr != nil {
		if !isLockHeld(flockErr) {
			return fmt.Errorf("%s flock: %w", operation, flockErr)
		}
		if stderr != nil {
			_, _ = fmt.Fprintf(stderr, "noxctl: waiting for lock at %s\n", lockPath)
		}
		if flockErr = unix.Flock(fd, unix.LOCK_EX); flockErr != nil {
			return fmt.Errorf("%s flock: %w", operation, flockErr)
		}
	}
	return nil
}

// AcquireDaemon is the daemon-side blocking lock acquire. No sentinel
// write (daemon never yields to itself); release closure unlocks +
// closes only.
func AcquireDaemon(ctx context.Context, lockPath string) (release func(), err error) {
	_ = ctx
	fd, openErr := openLockFile(lockPath, "AcquireDaemon")
	if openErr != nil {
		return nil, openErr
	}
	if flockErr := acquireBlockingFlock(fd, lockPath, "AcquireDaemon", nil); flockErr != nil {
		_ = unix.Close(fd)
		return nil, flockErr
	}
	writeLockPID(fd)
	return func() {
		_ = unix.Flock(fd, unix.LOCK_UN)
		_ = unix.Close(fd)
	}, nil
}

// writeLockPID stamps the lockfile with `<pid>\n` for `lsof`-style
// diagnostics. Truncate first in case the lockfile was reused with
// longer prior content (e.g. a previous run by a higher-PID process).
// Errors ignored — content is purely informational and the
// kernel-level flock is the real mutual-exclusion control.
func writeLockPID(fd int) {
	_ = unix.Ftruncate(fd, 0)
	_, _ = unix.Write(fd, []byte(strconv.Itoa(os.Getpid())+"\n"))
}

// IsApplyPending checks for `<dir(lockPath)>/.apply-pending` without
// opening the lock file. Returns false on any error other than
// fs.ErrNotExist so a stat hiccup never wedges the daemon — better to
// over-run a cycle than over-yield.
func IsApplyPending(lockPath string) bool {
	sentinel := filepath.Join(filepath.Dir(lockPath), SentinelName)
	_, err := os.Stat(sentinel)
	if err == nil {
		return true
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return false
	}
	return false
}
