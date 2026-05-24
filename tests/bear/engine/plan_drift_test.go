// Package engine_test — drift-detection locks for `noxctl plan`.
//
// plan_test.go covers the PlanResult schema, RenderText/RenderJSON, and the
// zero-domain path, but explicitly defers computeDomainDelta-with-real-domains
// to "smoke" — leaving the core delta logic (master read → render → compare,
// create-vs-replace, the read-only contract) untested at the engine boundary.
// This file closes that gap by driving engine.Plan with a real grouped-vertical
// domain and the shared fakeWorkBackend, asserting the drift verdict an
// operator sees when they run `noxctl plan`.
//
//cyrillic:permit
package engine_test

import (
	"context"
	"testing"
	"testing/synctest"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

// planAtomRow is the single work atom every drift scenario stages: canonical
// under "tasks", carrying only its own tags (no drag gesture). Reused so each
// test varies only the master state.
//
//cyrillic:permit
func planAtomRow() map[string]any {
	return map[string]any{
		"id":      atomNoteID,
		"title":   "Нова нотатка",
		"tags":    []string{"#work", "#work/tasks"},
		"content": canonicalAtomBody(),
	}
}

// firstMasterDiff returns the first master-targeted Diff in a domain plan, or
// (zero, false) when none exists.
func firstMasterDiff(dp engine.DomainPlan) (engine.Diff, bool) {
	for _, d := range dp.Changes {
		if d.Target == "master" {
			return d, true
		}
	}
	return engine.Diff{}, false
}

// stageMasterWithBody primes the fake with the work atom plus a master note
// carrying masterBody — the shared list+note staging the drift-replace,
// read-only, and clean scenarios all need (varying only the master body).
//
//cyrillic:permit
func stageMasterWithBody(t *testing.T, fake *fakeWorkBackend, masterBody string) {
	t.Helper()
	masterRow := map[string]any{"id": masterNoteID, "title": "✱ Робота", "tags": []string{"#work"}, "content": masterBody}
	fake.StageList(t, []map[string]any{planAtomRow(), masterRow})
	fake.StageNote(t, atomNoteID, planAtomRow())
	fake.StageNote(t, masterNoteID, masterRow)
}

// TestPlan_ReportsDriftCreate_WhenMasterMissing pins the create path: a domain
// whose master note does not yet exist in Bear must surface as drift with a
// create diff. User-facing bug if this regresses: `noxctl plan` shows a brand
// new domain as clean and the operator never learns apply will create a master.
//
//cyrillic:permit
func TestPlan_ReportsDriftCreate_WhenMasterMissing(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		// List the atom but NO master note → FetchMasterContent finds
		// nothing → empty current master → create drift.
		fake.StageList(t, []map[string]any{planAtomRow()})
		fake.StageNote(t, atomNoteID, planAtomRow())
		ctx := domain.ContextWithBackend(context.Background(), fake)

		res, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{buildWorkDomainForIntegration()}})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if !res.HasDrift() {
			t.Fatalf("Plan reported no drift for a missing master; want drift. summary=%+v", res.Summary)
		}
		diff, ok := firstMasterDiff(res.Domains[0])
		if !ok {
			t.Fatalf("no master diff recorded; domain plan=%+v", res.Domains[0])
		}
		if diff.Kind != engine.DiffCreate {
			t.Errorf("master diff kind = %q, want %q (missing master is a create)", diff.Kind, engine.DiffCreate)
		}
	})
}

// TestPlan_ReportsDriftReplace_WhenMasterStale pins the replace path: a domain
// whose master exists but holds stale content must surface as drift with a
// replace diff naming the bucket count. User-facing bug if this regresses: a
// master that drifted from the catalog shows clean and apply silently never
// reconciles it.
//
//cyrillic:permit
func TestPlan_ReportsDriftReplace_WhenMasterStale(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		stageMasterWithBody(t, fake, "# ✱ Робота\n\nстарий вміст що не збігається з рендером\n")
		ctx := domain.ContextWithBackend(context.Background(), fake)

		res, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{buildWorkDomainForIntegration()}})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if !res.HasDrift() {
			t.Fatalf("Plan reported no drift for a stale master; want drift. summary=%+v", res.Summary)
		}
		diff, ok := firstMasterDiff(res.Domains[0])
		if !ok {
			t.Fatalf("no master diff recorded; domain plan=%+v", res.Domains[0])
		}
		if diff.Kind != engine.DiffReplace {
			t.Errorf("master diff kind = %q, want %q (stale master is a replace)", diff.Kind, engine.DiffReplace)
		}
	})
}

// TestPlan_IssuesZeroOverwrites_ReadOnlyContract pins the defining contract of
// `noxctl plan`: it previews changes but NEVER mutates Bear. Even with a
// drifted master staged, zero overwrite calls must reach bearcli. User-facing
// bug if this regresses: `noxctl plan` silently writes to the vault — the one
// thing a preview command must never do.
//
//cyrillic:permit
func TestPlan_IssuesZeroOverwrites_ReadOnlyContract(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		stageMasterWithBody(t, fake, "# ✱ Робота\n\nстарий вміст\n")
		ctx := domain.ContextWithBackend(context.Background(), fake)

		if _, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{buildWorkDomainForIntegration()}}); err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if n := fake.OverwriteCount(); n != 0 {
			t.Errorf("Plan issued %d bearcli overwrite(s); want 0 (read-only contract)", n)
		}
	})
}

// TestPlan_ReportsClean_WhenMasterMatchesRender pins the no-false-drift
// contract: when the vault master already equals what apply would render,
// Plan must report clean. User-facing bug if this regresses: every plan run
// shows phantom drift on an in-sync vault and the operator loses trust in the
// signal. The desired master is computed from the same render path apply uses,
// then staged as the current master so the two halves match.
//
//cyrillic:permit
func TestPlan_ReportsClean_WhenMasterMatchesRender(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		fake.StageList(t, []map[string]any{planAtomRow()})
		fake.StageNote(t, atomNoteID, planAtomRow())
		ctx := domain.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		// Render the master exactly as apply would, then stage it as the
		// current Bear master so Plan should see zero drift.
		inputs, err := domain.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}
		rendered := d.RenderMaster(d, inputs.Groups)
		stageMasterWithBody(t, fake, rendered)

		res, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{d}})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if res.HasDrift() {
			t.Errorf("Plan reported drift on an in-sync master; want clean. domain=%+v", res.Domains[0])
		}
	})
}
