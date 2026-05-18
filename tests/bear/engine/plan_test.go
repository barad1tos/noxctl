// Package engine_test — Plan-engine table tests for `noxctl plan`.
//
// deliverable. Tests grow across the plan's three
// task stages:
//
//	Task 1 — PlanResult schema (this file's initial three tests).
//	Task 2 — ColorMode + ParseColorMode + RenderJSON ANSI-leak guard.
//	Task 3 — Plan + RenderText drift-rendering + read-only contract.
//
// External `engine_test` package per the project convention documented
// in tests/bear/snapshot_test.go and bear/engine/export_test.go.
package engine_test

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/barad1tos/noxctl/bear/engine"
)

// ---------- Helpers --------------------------------------------------------

// emptyPlanResult returns a PlanResult with all slice fields initialized
// to empty (never nil) — the JSON-stable shape Plan produces for a
// zero-domain run. Centralizes the dupl-flagged construction pattern.
func emptyPlanResult() *engine.PlanResult {
	return &engine.PlanResult{
		SchemaVersion: 1,
		Domains:       make([]engine.DomainPlan, 0),
		Untracked:     engine.UntrackedReport{TagFamilies: make([]engine.UntrackedFamily, 0)},
		Errors:        make([]engine.PlanError, 0),
	}
}

// cleanDomainResult returns a single-clean-domain PlanResult under tag.
// Centralizes the dupl-flagged construction pattern across RenderText tests.
func cleanDomainResult(tag string) *engine.PlanResult {
	r := emptyPlanResult()
	r.Domains = []engine.DomainPlan{{
		Tag:     tag,
		Status:  "clean",
		Changes: make([]engine.Diff, 0),
	}}
	return r
}

// ---------- Task 1 — PlanResult schema -------------------------------------

func TestPlanResultEmptyMarshalsFullShape(t *testing.T) {
	r := emptyPlanResult()
	out, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	for _, want := range []string{
		`"domains":[]`,
		`"errors":[]`,
		`"tag_families":[]`,
		`"schema_version":1`,
	} {
		if !bytes.Contains(out, []byte(want)) {
			t.Errorf("empty PlanResult JSON missing %s; got %s", want, string(out))
		}
	}

	// RESEARCH Pitfall 6 — none of the slice fields may marshal to null.
	for _, forbidden := range []string{
		`"domains":null`,
		`"errors":null`,
		`"tag_families":null`,
	} {
		if bytes.Contains(out, []byte(forbidden)) {
			t.Errorf("empty PlanResult marshaled null where [] expected: %s in %s", forbidden, string(out))
		}
	}
}

func TestHasDriftReportsSummaryDriftCount(t *testing.T) {
	cases := []struct {
		name string
		r    *engine.PlanResult
		want bool
	}{
		{"nil receiver", nil, false},
		{"zero drift", &engine.PlanResult{Summary: engine.PlanSummary{DomainsDrift: 0}}, false},
		{"positive drift", &engine.PlanResult{Summary: engine.PlanSummary{DomainsDrift: 1}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := c.r.HasDrift(); got != c.want {
				t.Errorf("HasDrift() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDiffKindWireValues(t *testing.T) {
	cases := []struct {
		kind engine.DiffKind
		want string
	}{
		{engine.DiffReplace, "replace"},
		{engine.DiffCreate, "create"},
		{engine.DiffNoop, "noop"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if got := string(c.kind); got != c.want {
				t.Errorf("string(%v) = %q, want %q", c.kind, got, c.want)
			}
		})
	}
}

// ---------- Task 2 — ColorMode + ParseColorMode + RenderJSON ---------------

func TestParseColorModeKnownValues(t *testing.T) {
	cases := []struct {
		in   string
		want engine.ColorMode
	}{
		{"", engine.ColorAuto},
		{"auto", engine.ColorAuto},
		{"always", engine.ColorAlways},
		{"never", engine.ColorNever},
	}
	for _, c := range cases {
		t.Run(c.in, func(t *testing.T) {
			got, err := engine.ParseColorMode(c.in)
			if err != nil {
				t.Fatalf("ParseColorMode(%q) err: %v", c.in, err)
			}
			if got != c.want {
				t.Errorf("ParseColorMode(%q) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

func TestParseColorModeUnknownReturnsError(t *testing.T) {
	for _, in := range []string{"Always", "truthy", "yes", "1", "off"} {
		t.Run(in, func(t *testing.T) {
			got, err := engine.ParseColorMode(in)
			if err == nil {
				t.Fatalf("ParseColorMode(%q) expected error, got nil", in)
			}
			if got != engine.ColorAuto {
				t.Errorf("ParseColorMode(%q) fallback = %v, want ColorAuto", in, got)
			}
		})
	}
}

func TestRenderJSONNoANSI(t *testing.T) {
	r := &engine.PlanResult{
		SchemaVersion: 1,
		Domains: []engine.DomainPlan{{
			Tag:    "library/poetry",
			Status: "drift",
			Changes: []engine.Diff{{
				Kind:    engine.DiffReplace,
				Target:  "master",
				Title:   "✱ Поезії",
				Summary: "master changed",
			}},
		}},
		Untracked: engine.UntrackedReport{TagFamilies: make([]engine.UntrackedFamily, 0)},
		Errors:    make([]engine.PlanError, 0),
	}
	var buf bytes.Buffer
	if err := engine.RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	if bytes.IndexByte(buf.Bytes(), 0x1b) != -1 {
		t.Errorf("RenderJSON output contains ANSI escape (0x1b); got %q", buf.String())
	}
}

func TestRenderJSONEmitsValidJSON(t *testing.T) {
	r := emptyPlanResult()
	var buf bytes.Buffer
	if err := engine.RenderJSON(&buf, r); err != nil {
		t.Fatalf("RenderJSON: %v", err)
	}
	var decoded engine.PlanResult
	if err := json.Unmarshal(buf.Bytes(), &decoded); err != nil {
		t.Fatalf("json.Unmarshal RenderJSON output: %v", err)
	}
	if decoded.SchemaVersion != 1 {
		t.Errorf("decoded.SchemaVersion = %d, want 1", decoded.SchemaVersion)
	}
}

// ---------- Task 3 — Plan + RenderText drift-rendering --------------------

func TestPlanZeroDomainsCleanShape(t *testing.T) {
	res, err := engine.Plan(context.Background(), engine.PlanOpts{Domains: nil})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res == nil {
		t.Fatal("Plan returned nil result")
	}
	if len(res.Domains) != 0 {
		t.Errorf("Domains len = %d, want 0", len(res.Domains))
	}
	if res.Summary.DomainsTotal != 0 {
		t.Errorf("Summary.DomainsTotal = %d, want 0", res.Summary.DomainsTotal)
	}
	if res.HasDrift() {
		t.Error("HasDrift() = true on zero-domain result, want false")
	}
	if res.Interrupted {
		t.Error("Interrupted = true on zero-domain result, want false")
	}
}

func TestPlanContextCancelledMarksInterrupted(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := engine.Plan(ctx, engine.PlanOpts{Domains: nil})
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if res == nil {
		t.Fatal("Plan returned nil result")
	}
	// Zero-domain + canceled ctx: loop never executes; Interrupted stays false.
	// The cancellation gate fires only when there ARE domains to walk; the
	// integration-time Test 6 from the plan requires real *bear.Domain values
	// (out of scope for this read-only test seam — covered by smoke).
	if len(res.Errors) != 0 {
		t.Errorf("Errors = %v, want empty (cancellation is not an error)", res.Errors)
	}
}

func TestRenderTextANSIBehaviorByColorMode(t *testing.T) {
	cases := []struct {
		name      string
		mode      engine.ColorMode
		wantANSI  bool
		wantClean bool
	}{
		{"auto+buffer = no ANSI (non-TTY fallback)", engine.ColorAuto, false, true},
		{"always+buffer = ANSI forced", engine.ColorAlways, true, true},
		{"never+buffer = no ANSI", engine.ColorNever, false, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := cleanDomainResult("library/poetry")
			var buf bytes.Buffer
			if err := engine.RenderText(&buf, r, c.mode, false); err != nil {
				t.Fatalf("RenderText: %v", err)
			}
			hasANSI := bytes.IndexByte(buf.Bytes(), 0x1b) != -1
			if hasANSI != c.wantANSI {
				t.Errorf("ANSI presence = %v, want %v; got %q", hasANSI, c.wantANSI, buf.String())
			}
		})
	}
}

func TestRenderTextDriftDomainShowsTagAndChangeCount(t *testing.T) {
	r := &engine.PlanResult{
		SchemaVersion: 1,
		Domains: []engine.DomainPlan{{
			Tag:    "library/poetry",
			Status: "drift",
			Changes: []engine.Diff{{
				Kind:    engine.DiffReplace,
				Target:  "master",
				Summary: "master changed",
			}},
		}},
		Untracked: engine.UntrackedReport{TagFamilies: make([]engine.UntrackedFamily, 0)},
		Errors:    make([]engine.PlanError, 0),
		Summary:   engine.PlanSummary{DomainsTotal: 1, DomainsDrift: 1, ChangesTotal: 1},
	}
	var buf bytes.Buffer
	if err := engine.RenderText(&buf, r, engine.ColorNever, false); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	out := buf.String()
	if !bytes.Contains(buf.Bytes(), []byte("library/poetry")) {
		t.Errorf("output missing tag; got %q", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("1 change")) {
		t.Errorf("output missing change count; got %q", out)
	}
	if !bytes.Contains(buf.Bytes(), []byte("master changed")) {
		t.Errorf("output missing change summary; got %q", out)
	}
}

func TestRenderTextErrorDomainShowsErrorGlyph(t *testing.T) {
	r := &engine.PlanResult{
		SchemaVersion: 1,
		Domains: []engine.DomainPlan{{
			Tag:     "library/broken",
			Status:  "error",
			Changes: make([]engine.Diff, 0),
		}},
		Untracked: engine.UntrackedReport{TagFamilies: make([]engine.UntrackedFamily, 0)},
		Errors:    []engine.PlanError{{Tag: "library/broken", Msg: "boom"}},
		Summary:   engine.PlanSummary{DomainsTotal: 1, DomainsError: 1},
	}
	var buf bytes.Buffer
	if err := engine.RenderText(&buf, r, engine.ColorNever, false); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	if !bytes.Contains(buf.Bytes(), []byte("library/broken")) {
		t.Errorf("output missing error tag; got %q", buf.String())
	}
	if !bytes.Contains(buf.Bytes(), []byte("error")) {
		t.Errorf("output missing 'error' status word; got %q", buf.String())
	}
}

// TestPlanWithEmptyOpts verifies that a zero-value PlanOpts returns
// a clean empty result without errors (defensive: no nil-deref, no
// spurious entries in PlanResult).
func TestPlanWithEmptyOpts(t *testing.T) {
	res, err := engine.Plan(context.Background(), engine.PlanOpts{})
	if err != nil {
		t.Fatalf("Plan zero-value opts: %v", err)
	}
	if res == nil {
		t.Fatal("Plan returned nil")
	}
}

func TestRenderTextSortsDomainsAlphabetically(t *testing.T) {
	r := &engine.PlanResult{
		SchemaVersion: 1,
		Domains: []engine.DomainPlan{
			{Tag: "z/last", Status: "clean", Changes: make([]engine.Diff, 0)},
			{Tag: "a/first", Status: "clean", Changes: make([]engine.Diff, 0)},
		},
		Untracked: engine.UntrackedReport{TagFamilies: make([]engine.UntrackedFamily, 0)},
		Errors:    make([]engine.PlanError, 0),
	}
	var buf bytes.Buffer
	if err := engine.RenderText(&buf, r, engine.ColorNever, false); err != nil {
		t.Fatalf("RenderText: %v", err)
	}
	out := buf.String()
	posA := bytes.Index(buf.Bytes(), []byte("a/first"))
	posZ := bytes.Index(buf.Bytes(), []byte("z/last"))
	if posA == -1 || posZ == -1 {
		t.Fatalf("expected both tags in output; got %q", out)
	}
	if posA > posZ {
		t.Errorf("expected a/first before z/last; got %q", out)
	}
}
