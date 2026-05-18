package bear_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// TestCheckCtx_NotCanceled_ReturnsNil — live ctx must propagate as nil
// so the head-of-loop check is a no-op on the happy path.
func TestCheckCtx_NotCanceled_ReturnsNil(t *testing.T) {
	if err := bear.CheckCtx(context.Background()); err != nil {
		t.Errorf("expected nil for live ctx, got %v", err)
	}
}

// TestCheckCtx_PreCanceled_ReturnsCanceled — canceled ctx must return
// context.Canceled (verified via errors.Is so wrapping at any later
// layer remains compatible).
func TestCheckCtx_PreCanceled_ReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := bear.CheckCtx(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}

// TestCheckCtx_DeadlineExceeded_ReturnsDeadline — timed-out ctx must
// return context.DeadlineExceeded so the engine.Apply orchestrator can
// distinguish hard cancel from soft timeout.
func TestCheckCtx_DeadlineExceeded_ReturnsDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Nanosecond)
	defer cancel()
	time.Sleep(5 * time.Millisecond) // ensure deadline passes
	err := bear.CheckCtx(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("expected context.DeadlineExceeded, got %v", err)
	}
}
