package regen

// Run composes the per-domain reconciliation runtime: listing, routing,
// canonical atomic rewrites, hub upserts, and master upsert. The pure domain
// model owns routing/rendering rules; this package owns Bear I/O and mutation
// order. Heavy sub-steps live in topic-specific files.

import (
	"context"
	"time"

	"github.com/barad1tos/noxctl/bear/domain"
)

// DomainSnapshot carries the freshest master + hub BODIES observed during a
// regen run, so the engine's content-hash pass (bear/engine/hashing.go) can
// reuse them instead of re-reading Bear after the writes. The shape mirrors
// ComputeContentHash's inputs exactly: a master body plus a hub-title->body
// map (the same keying FetchHubContents uses), so the engine hashes the
// snapshot directly.
//
// Idempotency contract (D-02): the bodies are ALWAYS stripped of new-note
// URLs (domain.StripNewNoteURLsFromBody) before they land here, matching the
// pre-phase FetchMasterContent/FetchHubContents treatment so the state.json
// fingerprint stays byte-identical across the optimization. On the unchanged
// branch the body is the already-fetched existing.Content; on the changed
// branch it is a deliberate fresh read-back of the just-overwritten note
// (Bear's stored-normalized form) — see upsertHub/upsertMasterIndex.
type DomainSnapshot struct {
	Master string            // stripped master body, "" when no master exists yet
	Hubs   map[string]string // hub title -> stripped hub body
}

// Result is the structured outcome of one per-domain regen run.
// It preserves the existing log-and-continue behavior while giving
// callers a machine-readable failure signal for recap and verification
// gates.
type Result struct {
	Buckets         int
	AtomicsTouched  int
	AtomicsFailed   int
	HubsCreated     int
	HubsChanged     int
	HubsUnchanged   int
	HubsFailed      int
	MasterCreated   int
	MasterChanged   int
	MasterUnchanged int
	MasterFailed    int
	ListFailed      bool
	// Snapshot carries the post-fetch master/hub scaffold for plan 14-02.
	// Populated EMPTY in D-01 — see DomainSnapshot.
	Snapshot DomainSnapshot
}

// Failed returns the total failure count observed during the regen run.
func (r Result) Failed() int {
	failed := r.AtomicsFailed + r.HubsFailed + r.MasterFailed
	if r.ListFailed {
		failed++
	}
	return failed
}

// Created returns the number of structural notes created by the regen run.
func (r Result) Created() int {
	return r.HubsCreated + r.MasterCreated
}

// Changed returns the number of existing notes rewritten by the regen run.
func (r Result) Changed() int {
	return r.AtomicsTouched + r.HubsChanged + r.MasterChanged
}

// Unchanged returns the number of structural notes that were already current.
func (r Result) Unchanged() int {
	return r.HubsUnchanged + r.MasterUnchanged
}

// Run reconciles one Domain's Bear corpus end-to-end: list its
// notes, compute master, hub, and tag overrides, run the atomics pass to
// stamp canonical lines, render and upsert each hub, then render and
// upsert the master index. Logs per-domain progress and aggregate
// counts; per-note failures are logged and surfaced via the final
// summary line without aborting the sweep.
func Run(ctx context.Context, d *domain.Domain) Result {
	start := time.Now()
	notes, err := listNotes(ctx, d)
	if err != nil {
		d.Logf("list failed: %v", err)
		return Result{ListFailed: true}
	}
	// One goroutine-local title->ID index over the initial listNotes result.
	// It replaces the per-bucket findNoteByTitle scan (each previously its own
	// `bearcli list`) in the hub/master upsert path: for a hub-routed domain
	// with B buckets that drops B+1 redundant list calls per no-op cycle.
	// No mutex — Run is goroutine-local per domain (engine.runDomainAndSave).
	idx := newNoteIndex(notes)
	routing := d.RouteAtomics(notes, func(atomID, kept, suppressed string) {
		d.Logf("WARN: tag override suppressed by higher layer: note %s wanted %s, kept %s",
			atomID, suppressed, kept)
	})
	if routing.MasterClaims > 0 {
		d.Logf("master layer claimed %d additional atomic(s)", routing.MasterClaims)
	}
	if routing.HubClaims > 0 {
		d.Logf("hub layer claimed %d additional atomic(s)", routing.HubClaims)
	}
	if routing.TagClaims > 0 {
		d.Logf("tag layer claimed %d additional atomic(s)", routing.TagClaims)
	}
	if routing.TagConflicts > 0 {
		d.Logf("tag conflicts: %d (no override applied)", routing.TagConflicts)
	}
	groups := routing.Groups
	result := Result{Buckets: len(groups)}
	if !d.SkipAtomicsPass {
		result.AtomicsTouched, result.AtomicsFailed = runAtomicsPass(ctx, d, groups)
	}
	hubsPass := runHubsPass(ctx, d, idx, groups)
	result.HubsCreated = hubsPass.Created
	result.HubsChanged = hubsPass.Changed
	result.HubsUnchanged = hubsPass.Unchanged
	result.HubsFailed = hubsPass.Failed
	// D-02: the hub/master diff-check already fetched (or read back) every body
	// we need to hash. Carry them on Result.Snapshot so the engine's
	// content-hash pass reuses them — a no-op cycle does zero extra reads.
	result.Snapshot.Hubs = hubsPass.Hubs
	if master, masterErr := upsertMasterIndex(ctx, d, idx, groups); masterErr != nil {
		d.Logf("ERROR: %v", masterErr)
		result.MasterFailed = 1
	} else {
		incrementOutcome(master.Outcome, &result.MasterCreated, &result.MasterChanged, &result.MasterUnchanged)
		result.Snapshot.Master = master.Body
		d.Logf("%s", master.Summary)
	}
	totalFailed := result.Failed()
	if totalFailed > 0 {
		d.Logf(
			"complete WITH FAILURES (%d buckets, %d atomics touched, %d failed, %s elapsed)",
			result.Buckets, result.AtomicsTouched, totalFailed, time.Since(start).Round(time.Millisecond),
		)
	} else {
		d.Logf("complete (%d buckets, %s elapsed)", result.Buckets, time.Since(start).Round(time.Millisecond))
	}
	return result
}
