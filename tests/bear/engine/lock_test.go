package engine_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/engine"
)

func TestAcquireApply_Releases(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	release, err := engine.AcquireApply(context.Background(), lockPath, false, os.Stderr)
	if err != nil {
		t.Fatalf("AcquireApply: %v", err)
	}
	sentinel := filepath.Join(dir, engine.SentinelName)
	if _, statErr := os.Stat(sentinel); statErr != nil {
		t.Errorf("sentinel missing after AcquireApply: %v", statErr)
	}
	raw, _ := os.ReadFile(lockPath)
	wantPID := fmt.Sprintf("%d\n", os.Getpid())
	if string(raw) != wantPID {
		t.Errorf("lockfile content: want %q got %q", wantPID, raw)
	}
	release()
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("sentinel still present after release: %v", statErr)
	}
}

func TestAcquireApply_NoWaitFailsFast(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	firstRelease, err := engine.AcquireApply(context.Background(), lockPath, false, os.Stderr)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	defer firstRelease()
	start := time.Now()
	secondRelease, secondErr := engine.AcquireApply(context.Background(), lockPath, true, os.Stderr)
	elapsed := time.Since(start)
	if secondErr == nil {
		secondRelease()
		t.Fatalf("expected --no-wait to fail when lock held; got success after %s", elapsed)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("--no-wait took too long: %s (want <100ms)", elapsed)
	}
	if !errors.Is(secondErr, syscall.EWOULDBLOCK) &&
		!strings.Contains(secondErr.Error(), "would block") &&
		!strings.Contains(secondErr.Error(), "operation would block") {
		t.Logf("error chain: %v (acceptable if it wraps EWOULDBLOCK or surfaces as 'operation would block' on Darwin)", secondErr)
	}
}

func TestAcquireApply_BlockingSerializes(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	firstRelease, err := engine.AcquireApply(context.Background(), lockPath, false, os.Stderr)
	if err != nil {
		t.Fatalf("first acquire: %v", err)
	}
	const hold = 200 * time.Millisecond
	// Schedule first release in another goroutine.
	go func() {
		time.Sleep(hold)
		firstRelease()
	}()
	start := time.Now()
	secondRelease, secondErr := engine.AcquireApply(context.Background(), lockPath, false, os.Stderr)
	elapsed := time.Since(start)
	if secondErr != nil {
		t.Fatalf("second acquire: %v", secondErr)
	}
	defer secondRelease()
	if elapsed < hold {
		t.Errorf("second acquire returned too early (%s); should have blocked >= %s", elapsed, hold)
	}
}

func TestIsApplyPending_Transitions(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	if engine.IsApplyPending(lockPath) {
		t.Errorf("expected false on missing sentinel")
	}
	sentinel := filepath.Join(dir, engine.SentinelName)
	if err := os.WriteFile(sentinel, nil, 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !engine.IsApplyPending(lockPath) {
		t.Errorf("expected true after sentinel created")
	}
	if err := os.Remove(sentinel); err != nil {
		t.Fatalf("Remove: %v", err)
	}
	if engine.IsApplyPending(lockPath) {
		t.Errorf("expected false after sentinel removed")
	}
}

func TestAcquireApply_SymlinkRejected(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	target := filepath.Join(dir, "nonexistent-target")
	if err := os.Symlink(target, lockPath); err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}
	_, err := engine.AcquireApply(context.Background(), lockPath, false, os.Stderr)
	if err == nil {
		t.Errorf("expected AcquireApply to refuse symlinked lockPath (T-2-01); got success")
	}
	// Sentinel cleanup on failure: should NOT linger.
	sentinel := filepath.Join(dir, engine.SentinelName)
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("sentinel leaked on failure path: %v", statErr)
	}
}

func TestAcquireApply_MkdirsParent(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "sub", "nested", ".lock")
	release, err := engine.AcquireApply(context.Background(), lockPath, false, os.Stderr)
	if err != nil {
		t.Fatalf("AcquireApply with missing parent: %v", err)
	}
	defer release()
	info, statErr := os.Stat(filepath.Dir(lockPath))
	if statErr != nil {
		t.Fatalf("Stat parent: %v", statErr)
	}
	if mode := info.Mode().Perm(); mode != 0o700 {
		t.Errorf("parent dir mode: want 0o700 got %#o", mode)
	}
}

func TestAcquireDaemon_NoSentinelWritten(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	release, err := engine.AcquireDaemon(context.Background(), lockPath)
	if err != nil {
		t.Fatalf("AcquireDaemon: %v", err)
	}
	defer release()
	sentinel := filepath.Join(dir, engine.SentinelName)
	if _, statErr := os.Stat(sentinel); !errors.Is(statErr, os.ErrNotExist) {
		t.Errorf("daemon-side acquire wrote sentinel; should not")
	}
}
