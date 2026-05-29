package apply_test

// sweep_test pins the --sweep orchestration an operator drives with
// `noxctl apply --sweep=4,8`: each value runs one full apply, and the global
// bearcli pool is re-armed to each iteration's capacity so the LAST value is
// what the pool ends at. Without the per-iteration re-arm the pool's
// sync.Once-gated SetConcurrency would freeze at the first value — the silent
// regression this guards. The single-value / empty path is covered by the
// surrounding RunApply tests; here we exercise the multi-value loop end to end.

import (
	"bytes"
	"context"
	"path/filepath"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// TestRunApplySweep_ReArmsPoolPerValue drives a two-value sweep "4,8" and
// asserts RunApply ran once per value AND the pool Capacity ends at the LAST
// value (8). The pool is re-armed to a deliberately-different baseline (3)
// first, so a sweep that failed to re-arm between iterations would leave
// Capacity at 4 (the first value, frozen by the sync.Once gate) — failing the
// final-value assertion. This is the multi-value analog of the single-run
// pool-capacity guard in bench_wiring_test.
func TestRunApplySweep_ReArmsPoolPerValue(t *testing.T) {
	// Baseline differs from every swept value so a missing re-arm is visible.
	bearcli.ResetPoolForTest(3)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	dir := t.TempDir()
	d := render.NewFlatListDomain("test/sweep", "Sweep")
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{
		d.Tag: {{ID: "atom-1", Title: "Atom", Content: "# Atom\n#test/sweep | [[Sweep]]\n---\nbody\n"}},
	})
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	var seenConcurrency []int
	var runs int
	optsFor := func(concurrency int) cli.ApplyOptions {
		seenConcurrency = append(seenConcurrency, concurrency)
		runs++
		// Each iteration writes its own state/lock under a per-value subdir so
		// the runs do not contend on one lock file.
		sub := filepath.Join(dir, "run", string(rune('a'+runs)))
		var stdout, stderr bytes.Buffer
		return cli.ApplyOptions{
			Domains:     []*domain.Domain{d},
			Catalog:     disabledFeatureCatalog(),
			PinTarget:   filepath.Join(sub, "pins.json"),
			StatePath:   filepath.Join(sub, "state.json"),
			LockPath:    filepath.Join(sub, ".lock"),
			Quiet:       true,
			Stdout:      &stdout,
			Stderr:      &stderr,
			Bench:       true,
			Concurrency: concurrency,
		}
	}

	if err := cli.RunApplySweep(ctx, []int{4, 8}, optsFor); err != nil {
		t.Fatalf("RunApplySweep(4,8): %v", err)
	}

	// One apply per swept value, in order.
	if runs != 2 {
		t.Errorf("RunApply ran %d times, want 2 (one per swept value)", runs)
	}
	if len(seenConcurrency) != 2 || seenConcurrency[0] != 4 || seenConcurrency[1] != 8 {
		t.Errorf("per-iteration concurrency = %v, want [4 8]", seenConcurrency)
	}

	// The re-arm took effect: the pool ends at the LAST swept value. A frozen
	// sync.Once gate (no re-arm) would leave Capacity at 4.
	if got := bearcli.MetricsSnapshot().Capacity; got != 8 {
		t.Errorf("pool Capacity = %d, want 8 (last swept value; per-iteration ResetPoolForTest re-arm did not take effect)", got)
	}
}

// TestRunApplySweep_EmptyRunsOnceAtDefault pins the single-run path: an empty
// sweep calls optsFor exactly once with concurrency 0 (engine default) and runs
// one apply. This is the `noxctl apply` (no --sweep) shape.
func TestRunApplySweep_EmptyRunsOnceAtDefault(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	dir := t.TempDir()
	d := render.NewFlatListDomain("test/single", "Single")
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{
		d.Tag: {{ID: "atom-1", Title: "Atom", Content: "# Atom\n#test/single | [[Single]]\n---\nbody\n"}},
	})
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	var seenConcurrency []int
	optsFor := func(concurrency int) cli.ApplyOptions {
		seenConcurrency = append(seenConcurrency, concurrency)
		var stdout, stderr bytes.Buffer
		return cli.ApplyOptions{
			Domains:   []*domain.Domain{d},
			Catalog:   disabledFeatureCatalog(),
			PinTarget: filepath.Join(dir, "pins.json"),
			StatePath: filepath.Join(dir, "state.json"),
			LockPath:  filepath.Join(dir, ".lock"),
			Quiet:     true,
			Stdout:    &stdout,
			Stderr:    &stderr,
		}
	}

	if err := cli.RunApplySweep(ctx, nil, optsFor); err != nil {
		t.Fatalf("RunApplySweep(nil): %v", err)
	}
	if len(seenConcurrency) != 1 || seenConcurrency[0] != 0 {
		t.Errorf("empty sweep concurrency = %v, want exactly one run at 0 (engine default)", seenConcurrency)
	}
}
