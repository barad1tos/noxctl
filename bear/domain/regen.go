package domain

// RunRegen — the per-domain orchestration entry point. Composes the
// listing, grouping, hub upsert, master upsert, and atomics-pass
// sub-steps that live in their own files (fetches.go,
// routing.go, upserts.go, canonical.go). The
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
	// Priority merge: master > hub > tag. Each layer's overrides skip atoms
	// already claimed by a higher-priority layer — deliberate gestures (master
	// cut/paste, hub bullet move) beat the single quick sidebar drag.
	// mergeOverrideLayer is the single source of truth — snapshot.go routes
	// through the same helper so the post-merge override map stays
	// byte-equivalent between plan and apply. Log lines are regen-only
	// (snapshot is silent for engine.Plan).
	overrides := d.computeMasterOverrides(notes)
	if len(overrides) > 0 {
		d.Logf("master layer claimed %d additional atomic(s)", len(overrides))
	}
	beforeHub := len(overrides)
	overrides = mergeOverrideLayer(overrides, d.computeHubOverrides(notes), nil)
	if hubAdded := len(overrides) - beforeHub; hubAdded > 0 {
		d.Logf("hub layer claimed %d additional atomic(s)", hubAdded)
	}
	beforeTag := len(overrides)
	tagOverrides, tagConflicts := d.computeTagOverrides(notes)
	overrides = mergeOverrideLayer(overrides, tagOverrides, func(atomID, kept, suppressed string) {
		d.Logf("WARN: tag override suppressed by higher layer: note %s wanted %s, kept %s",
			atomID, suppressed, kept)
	})
	if tagAdded := len(overrides) - beforeTag; tagAdded > 0 {
		d.Logf("tag layer claimed %d additional atomic(s)", tagAdded)
	}
	if tagConflicts > 0 {
		d.Logf("tag conflicts: %d (no override applied)", tagConflicts)
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
