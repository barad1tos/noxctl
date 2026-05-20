package bear

// RunRegen — the per-domain orchestration entry point. Composes the
// listing, grouping, hub upsert, master upsert, and atomics-pass
// sub-steps that live in their own files (bearcli_reads.go,
// atom_routing.go, regen_writes.go, canonical_lifecycle.go). The
// split keeps RunRegen as a small, readable entrypoint while the
// heavy lifting stays in topic-specific files.

import (
	"context"
	"time"
)

// RunRegen reconciles one Domain's Bear corpus end-to-end: list its
// notes, compute master + hub overrides, run the atomics pass to
// stamp canonical lines, render and upsert each hub, then render and
// upsert the master index. Logs per-domain progress and aggregate
// counts; per-note failures are logged and surfaced via the final
// summary line without aborting the sweep.
func (d *Domain) RunRegen(ctx context.Context) {
	start := time.Now()
	notes, err := d.listNotes(ctx)
	if err != nil {
		d.Logf("list failed: %v", err)
		return
	}
	overrides := d.computeMasterOverrides(notes)
	if len(overrides) > 0 {
		d.Logf("master regroup: %d atomic(s) moved between columns", len(overrides))
	}
	// Hub-side overrides: a bullet inside a Tier-2 hub claims its atomic for
	// that hub's bucket. Master overrides win on collision because the master
	// is the more deliberate gesture (table cut/paste vs. dragging a bullet
	// into a sibling hub).
	hubOverrides := d.computeHubOverrides(notes)
	added := 0
	for atomID, bucket := range hubOverrides {
		if _, alreadySet := overrides[atomID]; alreadySet {
			continue
		}
		if overrides == nil {
			overrides = make(map[string]string)
		}
		overrides[atomID] = bucket
		added++
	}
	if added > 0 {
		d.Logf("hub regroup: %d atomic(s) moved between hubs", added)
	}
	groups := d.groupAtomics(notes, overrides)
	var atomicsTouched, atomicsFailed int
	if !d.SkipAtomicsPass {
		atomicsTouched, atomicsFailed = d.runAtomicsPass(ctx, groups)
	}
	hubsFailed := d.runHubsPass(ctx, groups)
	masterFailed := 0
	if summary, masterErr := d.upsertMasterIndex(ctx, groups); masterErr != nil {
		d.Logf("ERROR: %v", masterErr)
		masterFailed = 1
	} else {
		d.Logf("%s", summary)
	}
	totalFailed := atomicsFailed + hubsFailed + masterFailed
	if totalFailed > 0 {
		d.Logf(
			"complete WITH FAILURES (%d buckets, %d atomics touched, %d failed, %s elapsed)",
			len(groups), atomicsTouched, totalFailed, time.Since(start).Round(time.Millisecond),
		)
	} else {
		d.Logf("complete (%d buckets, %s elapsed)", len(groups), time.Since(start).Round(time.Millisecond))
	}
}
