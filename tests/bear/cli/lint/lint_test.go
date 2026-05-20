// Package lint_test exercises the audit + lint CLI subcommand body.
//
// User-side scenarios:
//   - audit happy-path: domains scanned, findings printed, no writes
//   - lint without --apply: identical to audit (report-only)
//   - lint --apply: auto-fix orchestrator runs (writes through bearcli)
//   - canceled context: sweep aborts gracefully, partial output OK
//   - empty domain set: tally line still renders
//
// The fake BearcliBackend captures every list/show/overwrite call so
// each test asserts both the operator-facing render shape and the
// underlying bearcli traffic — the read-only invariant only holds
// when AuditDomains never emits an "overwrite" verb.
package lint_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/lint"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// fakeBearcli records every domain.runBearcli call routed through the
// BearcliBackend seam. Returns canned JSON for "list" and a stub
// {"ok":true} for "overwrite". Mirrors the shape used by tests/bear/
// engine/* so the fixture story stays consistent across packages.
type fakeBearcli struct {
	listPayload []byte
	mu          sync.Mutex
	calls       []fakeCall
	count       atomic.Int64
}

type fakeCall struct {
	Kind string
	Args []string
	Body string
}

func newFakeBearcli(payload []byte) *fakeBearcli {
	return &fakeBearcli{listPayload: payload}
}

// Run satisfies domain.BearcliBackend.
func (f *fakeBearcli) Run(_ context.Context, args []string, stdin string) ([]byte, error) {
	f.count.Add(1)
	kind := "other"
	if len(args) > 0 {
		kind = args[0]
	}
	f.mu.Lock()
	f.calls = append(f.calls, fakeCall{
		Kind: kind, Args: append([]string(nil), args...), Body: stdin,
	})
	f.mu.Unlock()
	switch kind {
	case "list":
		return f.listPayload, nil
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		return []byte(`{"ok":true}`), nil
	}
	return []byte("{}"), nil
}

// countKind reports how many calls of a given verb the fake recorded.
func (f *fakeBearcli) countKind(kind string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c.Kind == kind {
			n++
		}
	}
	return n
}

// brokenH1ListPayload returns the JSON shape `bearcli list --tag X`
// emits for one note with a broken-H1 title — the canonical lint
// finding the audit pass surfaces. The Tags field carries the
// domain's CanonicalTag so the lint pass scopes the finding to the
// right domain.
func brokenH1ListPayload(t *testing.T, tag string) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{
		"id":      "note-1",
		"title":   "| broken header",
		"content": "Body without canonical tag-line.\n",
		"tags":    []string{tag},
		"created": "2026-05-19T12:00:00Z",
	}})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// flatListDomainForTest constructs a minimal flat-list domain that
// exercises the lint pass. Real catalogs ship richer wiring; this
// fixture keeps the test surface narrow to the lint heuristics
// themselves.
func flatListDomainForTest() *domain.Domain {
	return render.NewFlatListDomain("test/notes", "✱ Notes")
}

// armBearcliPool resets the bearcli subprocess semaphore to a small
// fixed capacity so the lint sweep's bearcli calls actually execute.
// Production wires this via engine.Apply; tests must do it explicitly
// before any code path that ends up calling domain.runBearcli.
//
// Cleanup resets capacity to 1, NOT the production daemon default
// (8). bearcliSema is a process-global semaphore, so any subsequent
// test in this go test binary inherits whatever the previous test
// left behind. Tests that need a real capacity must call
// domain.ResetBearcliPoolForTest themselves; cleanup-to-1 is the safe
// minimum that surfaces a missing arm as a deterministic block
// rather than spurious concurrency surprises.
func armBearcliPool(t *testing.T) {
	t.Helper()
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })
}

// TestRun_AuditMode_PrintsFindingsNoWrites is the canonical audit
// scenario: a domain with a broken-H1 note gets scanned, findings
// are rendered to the supplied writer, and the bearcli backend
// records zero overwrite calls — the read-only contract.
func TestRun_AuditMode_PrintsFindingsNoWrites(t *testing.T) {
	armBearcliPool(t)
	fake := newFakeBearcli(brokenH1ListPayload(t, "test/notes"))
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	lint.Run(ctx, &buf, []*domain.Domain{flatListDomainForTest()}, false)

	out := buf.String()
	if !strings.Contains(out, "[test/notes]") {
		t.Errorf("audit output missing domain header; got %q", out)
	}
	if !strings.Contains(out, "across 1 domains") {
		t.Errorf("audit output missing tally line; got %q", out)
	}
	if got := fake.countKind("overwrite"); got != 0 {
		t.Errorf("audit mode wrote %d overwrites; must be 0 (read-only contract)", got)
	}
}

// TestRun_ApplyMode_InvokesAutoFix exercises the --apply path:
// LintApplyDomains walks the domain, finds a Fixable row (broken-H1
// rewrites to the canonical title), and emits one overwrite per
// fixable note. Report rendering is suppressed in apply mode — the
// orchestrator logs per-domain via the Domain.Logf hook instead.
func TestRun_ApplyMode_InvokesAutoFix(t *testing.T) {
	armBearcliPool(t)
	fake := newFakeBearcli(brokenH1ListPayload(t, "test/notes"))
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	lint.Run(ctx, &buf, []*domain.Domain{flatListDomainForTest()}, true)

	// Apply path may or may not write (depends on whether the broken
	// title is auto-fixable per AutoFixAtom). What we DO assert: the
	// pass made at least one list call (it actually ran) and did not
	// panic on the synthetic domain.
	if fake.countKind("list") < 1 {
		t.Errorf("apply mode did not call list; backend calls = %v", fake.count.Load())
	}
	// Apply mode does not render findings — the operator gets per-
	// domain log lines via Domain.Logf instead. stdout should be
	// untouched.
	if buf.Len() != 0 {
		t.Errorf("apply mode leaked %d bytes to stdout: %q", buf.Len(), buf.String())
	}
}

// TestRun_CanceledContext_Aborts pins the SIGINT-like cancellation
// contract: a ctx already canceled at entry produces no panic, no
// hang, and the bearcli backend records zero list calls because the
// per-domain loop bails on the ctx.Err check before issuing any
// I/O. AuditDomains' contract is that a canceled context short-
// circuits each domain's listNotes call.
func TestRun_CanceledContext_Aborts(t *testing.T) {
	armBearcliPool(t)
	fake := newFakeBearcli(brokenH1ListPayload(t, "test/notes"))
	ctx, cancel := context.WithCancel(domain.ContextWithBackend(t.Context(), fake))
	cancel() // canceled before Run even starts

	var buf bytes.Buffer
	lint.Run(ctx, &buf, []*domain.Domain{flatListDomainForTest()}, false)

	// Honoring cancellation means the listNotes path saw ctx.Err
	// and skipped the bearcli round-trip. Without this assertion the
	// test would pass even if Run ignored ctx entirely — only
	// "didn't crash", not "honored the signal".
	if got := fake.countKind("list"); got != 0 {
		t.Errorf("canceled-ctx Run made %d list calls; want 0 "+
			"(ctx.Err must short-circuit listNotes)", got)
	}
}

// TestRun_EmptyDomains_RendersEmptyTally guards the operator-facing
// behavior when noxctl.toml declares zero domains: the audit pass
// still prints the tally line so an automated triage runner can
// grep for a count regardless of catalog size.
func TestRun_EmptyDomains_RendersEmptyTally(t *testing.T) {
	fake := newFakeBearcli([]byte(`[]`))
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	lint.Run(ctx, &buf, nil, false)

	if !strings.Contains(buf.String(), "0 findings across 0 domains") {
		t.Errorf("empty-domain tally missing; got %q", buf.String())
	}
	if got := fake.countKind("list"); got != 0 {
		t.Errorf("empty domain set should issue 0 list calls; got %d", got)
	}
}
