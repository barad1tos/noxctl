package cliutil

import (
	"context"
	"errors"
	"os"
	"os/signal"
	"syscall"
)

// ErrInterrupted is the command-level cancellation sentinel that maps to
// POSIX exit code 130 in cmd/noxctl.
var ErrInterrupted = errors.New("noxctl: interrupted")

// RunWithSignalContext wraps fn in the standard SIGINT/SIGTERM-aware context
// flow and maps context cancellation to interruptedErr.
func RunWithSignalContext(
	parent context.Context,
	interruptedErr error,
	fn func(ctx context.Context) error,
) error {
	ctx, stop := signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := fn(ctx); err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return interruptedErr
		}
		return err
	}
	if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return interruptedErr
	}
	return nil
}
