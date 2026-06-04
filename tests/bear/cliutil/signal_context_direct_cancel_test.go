package cliutil_test

import (
	"context"
	"errors"
	"testing"

	"github.com/barad1tos/noxctl/bear/cliutil"
)

func TestRunWithSignalContext_MapsDirectContextCancel(t *testing.T) {
	err := cliutil.RunWithSignalContext(context.Background(), cliutil.ErrInterrupted, func(_ context.Context) error {
		return context.Canceled
	})
	if !errors.Is(err, cliutil.ErrInterrupted) {
		t.Fatalf("err = %v, want shared interrupted sentinel", err)
	}
}
