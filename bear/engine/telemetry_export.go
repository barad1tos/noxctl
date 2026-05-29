package engine

// Test-export seam for the cycle-telemetry formatter. External tests live under
// tests/bear/engine/ — a different directory from the package source — so an
// in-package _test.go file cannot bridge the unexported emitCycleTelemetry /
// domainTiming symbols across the directory gap. Exporting a thin ForTest
// wrapper matches the ComputeContentHash + bearcli.AcquireForTest precedent.
//
// Production callers MUST use the unexported emitCycleTelemetry; the ForTest
// suffix surfaces any production use as a code-review bug.

import (
	"io"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
)

// DomainTimingForTest is the exported mirror of the unexported domainTiming
// struct, letting external tests construct per-domain timing inputs for the
// formatter seam.
type DomainTimingForTest struct {
	Tag     string
	Elapsed time.Duration
}

// EmitCycleTelemetryForTest exposes emitCycleTelemetry to external tests. It
// converts the exported timing mirror into the internal slice and delegates.
func EmitCycleTelemetryForTest(w io.Writer, m bearcli.Metrics, timings []DomainTimingForTest, totalWall time.Duration) {
	internal := make([]domainTiming, len(timings))
	for i, t := range timings {
		internal[i] = domainTiming(t)
	}
	emitCycleTelemetry(w, m, internal, totalWall)
}

// CycleDeltaForTest exposes the unexported cycleDelta to external tests so the
// per-cycle delta math (additive baseline subtraction + the negative-clamp, and
// the pass-through of the already-scoped PeakConcurrent) is pinned at the
// directory gap, same precedent as EmitCycleTelemetryForTest.
func CycleDeltaForTest(baseline, end bearcli.Metrics) bearcli.Metrics {
	return cycleDelta(baseline, end)
}
