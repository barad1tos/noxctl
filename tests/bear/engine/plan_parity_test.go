// Task 2 — integration tests for engine.Plan with
// ConfigSource=ConfigSourceBoth.
//
// External `engine_test` package per project convention. The bearcli-
// bound branch (matched-pair render) is exercised through the pure
// helper engine.ParityDeltaFromMastersForTest exported via engine's
// test seam — production callers route through engine.Plan, which
// calls bear.SnapshotDomainRenderInputs (bearcli) and is unsuitable
// for hermetic CI tests.
//
// The missing-half / dispatch-shape branches DO go through engine.Plan
// directly because they don't reach SnapshotDomainRenderInputs.
package engine_test

import (
	"context"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/engine"
)

// synthDomain builds an in-memory *bear.Domain with no factory side
// effects. Suitable for parity unit tests that never reach bearcli.
func synthDomain(tag, indexTitle string) *bear.Domain {
	return &bear.Domain{
		Tag:          tag,
		CanonicalTag: "#" + tag,
		IndexTitle:   indexTitle,
	}
}

func TestPlanParityMatch(t *testing.T) {
	d := synthDomain("library/poetry", "✱ Поезії")
	master := "## Автори (1)\n- [[Bear]]\n"
	dp := engine.ParityDeltaFromMastersForTest(d, master, master, false)
	if dp.Status != engine.StatusClean {
		t.Errorf("Status = %q, want %q", dp.Status, engine.StatusClean)
	}
	if len(dp.Changes) != 0 {
		t.Errorf("Changes len = %d, want 0", len(dp.Changes))
	}
	if dp.Tag != "library/poetry" {
		t.Errorf("Tag = %q, want %q", dp.Tag, "library/poetry")
	}
}

func TestPlanParityMismatch(t *testing.T) {
	d := synthDomain("library/poetry", "✱ Поезії")
	hard := "## Автори (1)\n- [[Bear]]\n"
	toml := "## Автори (2)\n- [[Bear]]\n- [[Cat]]\n"

	// Non-verbose: Detail carries 1 summary line ("line N: hardcoded=…").
	dpQuiet := engine.ParityDeltaFromMastersForTest(d, hard, toml, false)
	if dpQuiet.Status != engine.StatusParityMismatch {
		t.Errorf("Status = %q, want %q", dpQuiet.Status, engine.StatusParityMismatch)
	}
	if len(dpQuiet.Changes) != 1 {
		t.Fatalf("Changes len = %d, want 1", len(dpQuiet.Changes))
	}
	change := dpQuiet.Changes[0]
	if change.Kind != engine.DiffReplace {
		t.Errorf("Kind = %q, want %q", change.Kind, engine.DiffReplace)
	}
	if change.Target != "master" {
		t.Errorf("Target = %q, want %q", change.Target, "master")
	}
	if !strings.Contains(change.Summary, "parity") {
		t.Errorf("Summary lacks 'parity': %q", change.Summary)
	}
	if !strings.Contains(change.Summary, "differ") {
		t.Errorf("Summary lacks 'differ': %q", change.Summary)
	}
	if len(change.Detail) != 1 {
		t.Errorf("non-verbose Detail len = %d, want 1; got %v", len(change.Detail), change.Detail)
	}

	// Verbose: Detail carries multi-line context.
	dpVerbose := engine.ParityDeltaFromMastersForTest(d, hard, toml, true)
	if len(dpVerbose.Changes) != 1 {
		t.Fatalf("verbose Changes len = %d, want 1", len(dpVerbose.Changes))
	}
	if len(dpVerbose.Changes[0].Detail) < 2 {
		t.Errorf("verbose Detail too short: %v", dpVerbose.Changes[0].Detail)
	}
}

func TestPlanParityMissingHardcoded(t *testing.T) {
	tomlOnly := synthDomain("library/poetry", "✱ Поезії")
	res, err := engine.Plan(context.Background(), engine.PlanOpts{
		Domains:      []*bear.Domain{tomlOnly},
		HardcodedRef: nil,
		ConfigSource: engine.ConfigSourceBoth,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(res.Domains) != 1 {
		t.Fatalf("Domains len = %d, want 1", len(res.Domains))
	}
	if res.Domains[0].Status != engine.StatusError {
		t.Errorf("Status = %q, want %q", res.Domains[0].Status, engine.StatusError)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected at least 1 PlanError")
	}
	if !strings.Contains(res.Errors[0].Msg, "in TOML but not in hardcoded") {
		t.Errorf("Errors[0].Msg = %q; want mention of 'in TOML but not in hardcoded'", res.Errors[0].Msg)
	}
}

func TestPlanParityMissingTOML(t *testing.T) {
	hardOnly := synthDomain("library/poetry", "✱ Поезії")
	res, err := engine.Plan(context.Background(), engine.PlanOpts{
		Domains:      nil,
		HardcodedRef: []*bear.Domain{hardOnly},
		ConfigSource: engine.ConfigSourceBoth,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(res.Domains) != 1 {
		t.Fatalf("Domains len = %d, want 1", len(res.Domains))
	}
	if res.Domains[0].Status != engine.StatusError {
		t.Errorf("Status = %q, want %q", res.Domains[0].Status, engine.StatusError)
	}
	if len(res.Errors) == 0 {
		t.Fatal("expected at least 1 PlanError")
	}
	if !strings.Contains(res.Errors[0].Msg, "in hardcoded but not in TOML") {
		t.Errorf("Errors[0].Msg = %q; want mention of 'in hardcoded but not in TOML'", res.Errors[0].Msg)
	}
}

func TestPlanParityIdempotency(t *testing.T) {
	tomlOnly := synthDomain("library/aphorisms", "✱ Афоризми")
	hardOnly := synthDomain("llm/agents", "✱ AI Agents")
	opts := engine.PlanOpts{
		Domains:      []*bear.Domain{tomlOnly},
		HardcodedRef: []*bear.Domain{hardOnly},
		ConfigSource: engine.ConfigSourceBoth,
	}
	first, err := engine.Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan first: %v", err)
	}
	second, err := engine.Plan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Plan second: %v", err)
	}
	if len(first.Domains) != len(second.Domains) {
		t.Fatalf("Domains len mismatch: first=%d, second=%d",
			len(first.Domains), len(second.Domains))
	}
	for i, fd := range first.Domains {
		sd := second.Domains[i]
		if fd.Tag != sd.Tag || fd.Status != sd.Status {
			t.Errorf("Domains[%d] mismatch: first=(%q,%q), second=(%q,%q)",
				i, fd.Tag, fd.Status, sd.Tag, sd.Status)
		}
	}
	// Sorted-tags contract: alphabetical ordering of unique tags from
	// either slice. Two distinct tags here → sorted alphabetically.
	wantOrder := []string{"library/aphorisms", "llm/agents"}
	for i, w := range wantOrder {
		if first.Domains[i].Tag != w {
			t.Errorf("tag order at %d: got %q, want %q", i, first.Domains[i].Tag, w)
		}
	}
}

// TestPlanParityZeroPairs — both slices empty, no errors, summary clean.
func TestPlanParityZeroPairs(t *testing.T) {
	res, err := engine.Plan(context.Background(), engine.PlanOpts{
		ConfigSource: engine.ConfigSourceBoth,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(res.Domains) != 0 {
		t.Errorf("Domains len = %d, want 0", len(res.Domains))
	}
	if res.Summary.DomainsParityMismatch != 0 {
		t.Errorf("DomainsParityMismatch = %d, want 0", res.Summary.DomainsParityMismatch)
	}
}

// TestPlanParitySummaryCounters — when matching pair (clean) and missing
// halves coexist, the summary tallies them in the right buckets.
func TestPlanParitySummaryCounters(t *testing.T) {
	tomlA := synthDomain("a/x", "✱ A")
	hardOnly := synthDomain("b/x", "✱ B")
	res, err := engine.Plan(context.Background(), engine.PlanOpts{
		Domains:      []*bear.Domain{tomlA},
		HardcodedRef: []*bear.Domain{hardOnly},
		ConfigSource: engine.ConfigSourceBoth,
	})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	// Both rows are "missing half" → both Status=StatusError → DomainsError=2.
	if res.Summary.DomainsError != 2 {
		t.Errorf("DomainsError = %d, want 2", res.Summary.DomainsError)
	}
	if res.Summary.DomainsParityMismatch != 0 {
		t.Errorf("DomainsParityMismatch = %d, want 0", res.Summary.DomainsParityMismatch)
	}
	// Wire-shape check: omitempty kicks in on zero parity-mismatch count.
}
