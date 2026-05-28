package cliutil_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/barad1tos/noxctl/bear/cliutil"
)

func TestRunWithSignalContext_MapsWrappedContextCancel(t *testing.T) {
	err := cliutil.RunWithSignalContext(context.Background(), cliutil.ErrInterrupted, func(_ context.Context) error {
		return fmt.Errorf("wrapped: %w", context.Canceled)
	})
	if !errors.Is(err, cliutil.ErrInterrupted) {
		t.Fatalf("err = %v, want shared interrupted sentinel", err)
	}
}

func TestRunWithSignalContext_MapsGenericErrorAfterContextCancel(t *testing.T) {
	parent, cancel := context.WithCancel(context.Background())
	err := cliutil.RunWithSignalContext(parent, cliutil.ErrInterrupted, func(ctx context.Context) error {
		cancel()
		<-ctx.Done()
		return errors.New("bearcli failed: signal: killed")
	})
	if !errors.Is(err, cliutil.ErrInterrupted) {
		t.Fatalf("err = %v, want shared interrupted sentinel after context cancellation", err)
	}
}
