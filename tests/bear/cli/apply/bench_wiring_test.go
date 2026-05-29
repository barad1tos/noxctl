package apply_test

// bench_wiring_test is the Pattern-A shim-audit guard for the
// --bench/--concurrency flags (RECURRING_PITFALLS Pattern A: two prior
// loaded-but-unthreaded incidents). A unit test of BenchOptsFromFlags proves the
// mapper is correct; it CANNOT prove the mapped value actually reaches
// engine.ApplyOpts. This test exercises cli.RunApply end-to-end and asserts the
// bench flag changed the live bearcli pool capacity — a no-op flag would leave
// it at the re-armed baseline.

import (
	"bytes"
	"context"
	"log"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestBenchWiring_ConcurrencyReachesPool drives a bench RunApply at
// concurrency=4 and asserts the global bearcli pool ended at Capacity==4. The
// pool is re-armed to a deliberately-different baseline (8) before the run so a
// parsed-but-unthreaded --concurrency would leave Capacity at 8, failing the
// assertion. It also asserts the cycle-telemetry line was emitted, proving the
// run completed through the metrics path.
func TestBenchWiring_ConcurrencyReachesPool(t *testing.T) {
	// Re-arm the process-global pool to a baseline that differs from the value
	// under test. ResetPoolForTest re-arms the sync.Once gate so engine.Apply's
	// SetConcurrency(4) takes effect (pool doc reserves this for tests + bench).
	bearcli.ResetPoolForTest(8)
	if got := bearcli.MetricsSnapshot().Capacity; got != 8 {
		t.Fatalf("baseline Capacity = %d, want 8 after re-arm", got)
	}

	dir := t.TempDir()
	d := render.NewFlatListDomain("test/bench", "Bench")
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{
		d.Tag: {{ID: "atom-1", Title: "Atom", Content: "# Atom\n#test/bench | [[Bench]]\n---\nbody\n"}},
	})
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	var logBuf bytes.Buffer
	restoreOutput := log.Writer()
	restoreFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(restoreOutput)
		log.SetFlags(restoreFlags)
	})

	var stdout, stderr bytes.Buffer
	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:     []*domain.Domain{d},
		Catalog:     disabledFeatureCatalog(),
		PinTarget:   filepath.Join(dir, "pins.json"),
		StatePath:   filepath.Join(dir, "state.json"),
		LockPath:    filepath.Join(dir, ".lock"),
		Quiet:       true,
		Stdout:      &stdout,
		Stderr:      &stderr,
		Bench:       true,
		Concurrency: 4,
	})
	if err != nil {
		t.Fatalf("RunApply(bench, concurrency=4): %v", err)
	}

	// Pattern-A assertion: the flag actually reached engine.ApplyOpts and drove
	// bearcli.SetConcurrency. A loaded-but-unthreaded flag leaves Capacity at 8.
	if got := bearcli.MetricsSnapshot().Capacity; got != 4 {
		t.Fatalf("Capacity = %d, want 4 (bench --concurrency=4 not threaded to engine.ApplyOpts)", got)
	}
	// The run completed through the unconditional telemetry path (Plan 14-03).
	if !strings.Contains(logBuf.String(), "regen cycle:") {
		t.Fatalf("log = %q, want a 'regen cycle:' telemetry line", logBuf.String())
	}
}
