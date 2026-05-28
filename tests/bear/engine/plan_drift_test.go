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
	"errors"
	"strings"
	"testing"
	"testing/synctest"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/regen"
	"github.com/barad1tos/noxctl/bear/render"
)

// planAtomRow is the single work atom every drift scenario stages. Its body
// canonical (via canonicalAtomBody) puts it in the "інше" UnknownBucket; its
// tags array carries #work/tasks. The drift assertions don't depend on the
// bucket name — each test varies only the master state.
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

type catEmptyIDFailsBackend struct {
	*fakeWorkBackend
}

func (b catEmptyIDFailsBackend) Run(ctx context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) >= 2 && args[0] == "cat" && args[1] == "" {
		return nil, errors.New("cat empty note ID")
	}
	return b.fakeWorkBackend.Run(ctx, args, stdin)
}

type duplicateRegistryFailurePlanBackend struct {
	*fakeWorkBackend
}

func (b duplicateRegistryFailurePlanBackend) Run(ctx context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list" && args[1] == "--location" && args[2] == "notes" {
		return nil, errors.New("duplicate registry corpus read failed")
	}
	return b.fakeWorkBackend.Run(ctx, args, stdin)
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

func firstHubDiff(dp engine.DomainPlan) (engine.Diff, bool) {
	for _, d := range dp.Changes {
		if d.Target == "hub" {
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
		ctx := bearcli.ContextWithBackend(context.Background(), catEmptyIDFailsBackend{fakeWorkBackend: fake})

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

func TestPlan_RecordsError_WhenDuplicateRegistryFails(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		fake.StageList(t, []map[string]any{planAtomRow()})
		fake.StageNote(t, atomNoteID, planAtomRow())
		ctx := bearcli.ContextWithBackend(context.Background(),
			duplicateRegistryFailurePlanBackend{fakeWorkBackend: fake})

		res, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{buildWorkDomainForIntegration()}})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if len(res.Errors) == 0 {
			t.Fatal("PlanResult.Errors is empty after duplicate registry failure; JSON consumers need this error")
		}
		if !strings.Contains(res.Errors[0].Msg, "duplicate registry corpus read failed") {
			t.Fatalf("PlanResult.Errors[0] = %+v, want duplicate registry failure", res.Errors[0])
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
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

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
		// The operator reads the Summary line in `noxctl plan` output; pin
		// that it names the master change (not just that a Diff exists).
		if !strings.Contains(diff.Summary, "master changed") {
			t.Errorf("replace diff summary = %q, want it to mention \"master changed\"", diff.Summary)
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
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

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
		ctx := bearcli.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()

		// Render the master exactly as apply would, then stage it as the
		// current Bear master so Plan should see zero drift.
		inputs, err := regen.SnapshotDomainRenderInputs(ctx, d)
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

func TestPlan_ReportsClean_WhenDuplicateMasterUsesURLLinks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		first := duplicatePlanAtomRow("plan-dup-a", "#work | [[✱ Робота]] | tasks")
		second := duplicatePlanAtomRow("plan-dup-b", "#work | [[✱ Робота]] | tasks")
		fake.StageList(t, []map[string]any{first, second})
		fake.StageNote(t, "plan-dup-a", first)
		fake.StageNote(t, "plan-dup-b", second)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)
		d := buildWorkDomainForIntegration()
		registry, err := regen.BuildCorpusDuplicateRegistry(ctx)
		if err != nil {
			t.Fatalf("BuildCorpusDuplicateRegistry: %v", err)
		}
		d.Duplicates = registry

		inputs, err := regen.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}
		rendered := d.RenderMaster(d, inputs.Groups)
		if !strings.Contains(rendered, "id=plan-dup-a") || !strings.Contains(rendered, "id=plan-dup-b") {
			t.Fatalf("rendered master lacks duplicate URL links:\n%s", rendered)
		}
		masterRow := map[string]any{"id": masterNoteID, "title": "✱ Робота", "tags": []string{"#work"}, "content": rendered}
		fake.StageList(t, []map[string]any{first, second, masterRow})
		fake.StageNote(t, masterNoteID, masterRow)

		res, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{d}, Verbose: true})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if res.HasDrift() {
			t.Fatalf("Plan reported duplicate URL-link false drift; want clean. domain=%+v", res.Domains[0])
		}
	})
}

func TestPlan_ReportsHubDrift_WhenDuplicateHubNeedsURLLinks(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		d := buildHubPlanDomain()
		first := hubPlanAtomRow("hub-dup-a")
		second := hubPlanAtomRow("hub-dup-b")
		fake.StageList(t, []map[string]any{first, second})
		fake.StageNote(t, "hub-dup-a", first)
		fake.StageNote(t, "hub-dup-b", second)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		inputs, err := regen.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}
		master := d.RenderMaster(d, inputs.Groups)
		plainHub := "# Bucket\n#test/hub | [[Index]]\n---\n## Items (2)\n- [[Same Title]]\n- [[Same Title]]\n"
		masterRow := hubPlanMasterRow(master)
		hubRow := hubPlanHubRow(plainHub)
		fake.StageList(t, []map[string]any{first, second, hubRow, masterRow})
		fake.StageNote(t, "hub-bucket", hubRow)
		fake.StageNote(t, "hub-master", masterRow)

		res, err := engine.Plan(ctx, engine.PlanOpts{Domains: []*domain.Domain{d}, Verbose: true})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if !res.HasDrift() {
			t.Fatalf("Plan reported no drift for stale duplicate hub links; want drift")
		}
		diff, ok := firstHubDiff(res.Domains[0])
		if !ok {
			t.Fatalf("no hub diff recorded; domain plan=%+v", res.Domains[0])
		}
		if diff.Kind != engine.DiffReplace || diff.Title != "Bucket" {
			t.Fatalf("hub diff = %+v, want replace for Bucket", diff)
		}
	})
}

func TestPlan_FeatureDisabledKeepsPlainDuplicateHubLinksClean(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resetPoolForApply(t)
		fake := newFakeWorkBackend()
		d := buildHubPlanDomain()
		first := hubPlanAtomRow("hub-feature-a")
		second := hubPlanAtomRow("hub-feature-b")
		fake.StageList(t, []map[string]any{first, second})
		fake.StageNote(t, "hub-feature-a", first)
		fake.StageNote(t, "hub-feature-b", second)
		ctx := bearcli.ContextWithBackend(context.Background(), fake)

		inputs, err := regen.SnapshotDomainRenderInputs(ctx, d)
		if err != nil {
			t.Fatalf("SnapshotDomainRenderInputs: %v", err)
		}
		master := d.RenderMaster(d, inputs.Groups)
		plainHub := d.RenderHub(d, "Bucket", inputs.Groups["Bucket"], nil)
		registry, err := regen.BuildCorpusDuplicateRegistry(ctx)
		if err != nil {
			t.Fatalf("BuildCorpusDuplicateRegistry: %v", err)
		}
		d.Duplicates = registry

		masterRow := hubPlanMasterRow(master)
		hubRow := hubPlanHubRow(plainHub)
		fake.StageList(t, []map[string]any{first, second, hubRow, masterRow})
		fake.StageNote(t, "hub-bucket", hubRow)
		fake.StageNote(t, "hub-master", masterRow)
		features := engine.AllFeaturesOn()
		features.DuplicateRegistry = false

		res, err := engine.Plan(ctx, engine.PlanOpts{
			Domains:  []*domain.Domain{d},
			Features: &features,
			Verbose:  true,
		})
		if err != nil {
			t.Fatalf("Plan: %v", err)
		}
		if res.HasDrift() {
			t.Fatalf("Plan reported drift with duplicate registry disabled; want clean. domain=%+v", res.Domains[0])
		}
	})
}

func duplicatePlanAtomRow(id, canonicalLine string) map[string]any {
	return map[string]any{
		"id":      id,
		"title":   "Same Title",
		"tags":    []string{"#work", "#work/tasks"},
		"content": "# Same Title\n" + canonicalLine + "\n---\n",
	}
}

func buildHubPlanDomain() *domain.Domain {
	return render.NewHubRoutedDomain("test/hub", "Index", "Inbox", "Items", render.DefaultRenderMaster3Tier)
}

func hubPlanAtomRow(id string) map[string]any {
	return map[string]any{
		"id":      id,
		"title":   "Same Title",
		"tags":    []string{"#test/hub"},
		"content": "# Same Title\n#test/hub | [[Bucket]]\n---\n",
	}
}

func hubPlanHubRow(content string) map[string]any {
	return map[string]any{
		"id":      "hub-bucket",
		"title":   "Bucket",
		"tags":    []string{"#test/hub"},
		"content": content,
	}
}

func hubPlanMasterRow(content string) map[string]any {
	return map[string]any{
		"id":      "hub-master",
		"title":   "Index",
		"tags":    []string{"#test/hub"},
		"content": content,
	}
}
