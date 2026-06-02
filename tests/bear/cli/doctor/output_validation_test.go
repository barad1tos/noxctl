// Package doctor_test — fail-fast output-validation coverage.
//
// WR-04: an invalid -o value must be rejected BEFORE any check runs, so
// `doctor -o yaml` never spawns the launchctl/pgrep subprocess probes.
package doctor_test

import (
	"context"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/diag"
	"github.com/barad1tos/noxctl/bear/cli/doctor"
)

// TestRunInvalidOutputShortCircuitsBeforeChecks pins WR-04: with an
// invalid -o, Run returns the validation error AND never touches the
// subprocess-backed seams. The recording seams must stay un-called.
func TestRunInvalidOutputShortCircuitsBeforeChecks(t *testing.T) {
	opts := happyOptions(t)
	opts.Output = "yaml"

	launchctlCalled := false
	pgrepCalled := false
	opts.LaunchctlPrintFn = func(string) (string, error) {
		launchctlCalled = true
		return "", nil
	}
	opts.ProcessRunningFn = func(string) (bool, error) {
		pgrepCalled = true
		return false, nil
	}

	err := doctor.Run(context.Background(), opts)
	if err == nil {
		t.Fatal("Run with -o yaml = nil, want a validation error")
	}
	if want := diag.ValidateOutput("yaml"); err.Error() != want.Error() {
		t.Errorf("Run error = %q, want validation error %q", err, want)
	}
	if launchctlCalled {
		t.Error("launchctl seam was invoked despite an invalid -o (fail-late, not fail-fast)")
	}
	if pgrepCalled {
		t.Error("pgrep seam was invoked despite an invalid -o (fail-late, not fail-fast)")
	}
}
