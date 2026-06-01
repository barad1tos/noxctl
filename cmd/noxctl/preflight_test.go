package main

import (
	"context"
	"errors"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunWithSignalContext_MapsCancelToCmdInterruptedSentinel(t *testing.T) {
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := runWithSignalContext(cmd, func(_ context.Context) error {
		return context.Canceled
	})
	if !errors.Is(err, errInterrupted) {
		t.Fatalf("err = %v, want cmd-level interrupted sentinel", err)
	}
}
