package cliutil_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/barad1tos/noxctl/bear/cliutil"
)

func TestRunWithSignalContext_MapsWrappedContextCancel(t *testing.T) {
	interrupted := errors.New("interrupted")
	err := cliutil.RunWithSignalContext(context.Background(), interrupted, func(_ context.Context) error {
		return fmt.Errorf("wrapped: %w", context.Canceled)
	})
	if !errors.Is(err, interrupted) {
		t.Fatalf("err = %v, want interrupted", err)
	}
}
