package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunWithSignalContext_MapsWrappedContextCancel(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runWithSignalContext(cmd, func(_ context.Context) error {
		return fmt.Errorf("wrapped: %w", context.Canceled)
	})
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("err = %v, want errInterrupted", err)
	}
}
