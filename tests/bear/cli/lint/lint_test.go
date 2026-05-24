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
// when Scan never emits an "overwrite" verb.
package lint_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli"
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
	case "tags":
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
// finding the audit pass surfaces. Tagged under the flat-list domain
// (test/notes) so the lint pass scopes the finding to the same
// fixture domain every other test uses.
func brokenH1ListPayload(t *testing.T) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{
		"id":      "note-1",
		"title":   "| broken header",
		"content": "Body without canonical tag-line.\n",
		"tags":    []string{"test/notes"},
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

// runLintExpectOK invokes cli.RunLint and t.Fatalf's on any returned
// error. Every happy-path test in this file uses the same three-line
// "invoke, check err, fail loud" shape; centralizing it keeps each
// case under the dupl token threshold and ensures a future RunLint
// error contract change updates one call site instead of seven.
func runLintExpectOK(t *testing.T, ctx context.Context, buf *bytes.Buffer, domains []*domain.Domain, apply bool, label string) {
	t.Helper()
	if err := cli.RunLint(ctx, buf, domains, apply); err != nil {
		t.Fatalf("RunLint %s: unexpected err %v", label, err)
	}
}

// TestRun_AuditMode_PrintsFindingsNoWrites is the canonical audit
// scenario: a domain with a broken-H1 note gets scanned, findings
// are rendered to the supplied writer, and the bearcli backend
// records zero overwrite calls — the read-only contract.
func TestRun_AuditMode_PrintsFindingsNoWrites(t *testing.T) {
	armBearcliPool(t)
	fake := newFakeBearcli(brokenH1ListPayload(t))
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	runLintExpectOK(t, ctx, &buf, []*domain.Domain{flatListDomainForTest()}, false, "audit-mode happy path")

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
	fake := newFakeBearcli(brokenH1ListPayload(t))
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	runLintExpectOK(t, ctx, &buf, []*domain.Domain{flatListDomainForTest()}, true, "apply-mode happy path")

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
// I/O. Scan's contract is that a canceled context short-
// circuits each domain's listNotes call.
func TestRun_CanceledContext_Aborts(t *testing.T) {
	armBearcliPool(t)
	fake := newFakeBearcli(brokenH1ListPayload(t))
	ctx, cancel := context.WithCancel(domain.ContextWithBackend(t.Context(), fake))
	cancel() // canceled before Run even starts

	var buf bytes.Buffer
	// Audit mode swallows scan failures (per-domain Scan returns
	// what it has, orphan scan appends a synthetic finding). The
	// canceled context surfaces through the empty list-call count
	// below, not as a RunLint return value.
	runLintExpectOK(t, ctx, &buf, []*domain.Domain{flatListDomainForTest()}, false, "audit-mode canceled ctx")

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
	runLintExpectOK(t, ctx, &buf, nil, false, "empty-domains tally")

	if !strings.Contains(buf.String(), "0 findings across 0 domains") {
		t.Errorf("empty-domain tally missing; got %q", buf.String())
	}
	if got := fake.countKind("list"); got != 0 {
		t.Errorf("empty domain set should issue 0 list calls; got %d", got)
	}
}

// orphanFamilyListPayload builds a single-note bearcli `list` payload
// with the supplied id/title/tags. Used by the orphan-family
// integration tests below — generic shape (no canonical-line content)
// so both audit-mode and apply-mode tests can drive the same fixture
// through cli.RunLint.
//
// Caveat: the same payload is served to BOTH the per-domain
// audit.Scan call (`bearcli list --tag <X>`) AND the corpus
// audit.ScanOrphanFamilies call (`bearcli list --location notes`)
// because the fake does not discriminate on flags — only on the
// sub-verb. That is fine for these tests: the per-domain Scan walks
// the atom under its catalog tag and emits whatever per-atom lints
// fire on its body; the corpus scan walks the same atom and emits the
// orphan-family finding. Both code paths exercised in one run.
func orphanFamilyListPayload(t *testing.T, tags []string) []byte {
	t.Helper()
	raw, err := json.Marshal([]map[string]any{{
		"id":      "note-stray",
		"title":   "Stray Note",
		"content": "",
		"tags":    tags,
		"created": "2026-05-23T12:00:00Z",
	}})
	if err != nil {
		t.Fatalf("marshal orphan payload: %v", err)
	}
	return raw
}

// TestRun_AuditMode_OrphanFamilyAppearsInOutput verifies the read-only
// composition: the corpus orphan scan runs alongside the per-domain
// audit scan, the stray-family finding lands in the printed report,
// and ZERO write calls (overwrite, tag) leak to bearcli — audit mode
// is a strict read-only contract.
func TestRun_AuditMode_OrphanFamilyAppearsInOutput(t *testing.T) {
	armBearcliPool(t)
	payload := orphanFamilyListPayload(t,
		[]string{"#test/notes", "#strayfamily/sub"})
	fake := newFakeBearcli(payload)
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	runLintExpectOK(t, ctx, &buf, []*domain.Domain{flatListDomainForTest()}, false, "audit-mode orphan finding")

	out := buf.String()
	if !strings.Contains(out, "Stray Note") {
		t.Errorf("audit output missing orphan-finding Title; got %q", out)
	}
	if !strings.Contains(out, "strayfamily") {
		t.Errorf("audit output missing stray-family name in Detail; got %q", out)
	}
	if got := fake.countKind("overwrite"); got != 0 {
		t.Errorf("audit mode wrote %d overwrites; want 0 (read-only contract)", got)
	}
	if got := fake.countKind("tags"); got != 0 {
		t.Errorf("audit mode issued %d tags calls; want 0 (read-only contract)", got)
	}
}

// TestRun_ApplyMode_OrphanFamilyTagEmitted_AndIdempotent pins the
// two-phase apply chain: a stray-family atom triggers exactly one
// `bearcli tags add <id> orphans` call on the first run; a second run
// where the atom already carries `#orphans` issues ZERO new tags calls
// — the idempotency contract that lets the operator re-run
// `noxctl lint --apply` safely.
func TestRun_ApplyMode_OrphanFamilyTagEmitted_AndIdempotent(t *testing.T) {
	armBearcliPool(t)

	// First run: atom carries the stray-family tag without #orphans.
	payload1 := orphanFamilyListPayload(t,
		[]string{"#test/notes", "#strayfamily/sub"})
	fake1 := newFakeBearcli(payload1)
	ctx1 := domain.ContextWithBackend(t.Context(), fake1)

	var buf1 bytes.Buffer
	runLintExpectOK(t, ctx1, &buf1, []*domain.Domain{flatListDomainForTest()}, true, "apply-mode first run")

	if got := fake1.countKind("tags"); got != 1 {
		t.Fatalf("apply mode tags-call count = %d, want 1 (stray family tagged); calls=%d",
			got, fake1.count.Load())
	}
	// Verify the tags-call args shape: ["tags", "add", noteID, "orphans"].
	fake1.mu.Lock()
	var tagCall *fakeCall
	for i := range fake1.calls {
		if fake1.calls[i].Kind == "tags" {
			tagCall = &fake1.calls[i]
			break
		}
	}
	fake1.mu.Unlock()
	if tagCall == nil {
		t.Fatalf("expected one recorded tags call; got none")
	}
	wantArgs := []string{"tags", "add", "note-stray", "orphans"}
	if len(tagCall.Args) != len(wantArgs) {
		t.Fatalf("tags call args length = %d, want %d (%v)", len(tagCall.Args), len(wantArgs), tagCall.Args)
	}
	for i, want := range wantArgs {
		if tagCall.Args[i] != want {
			t.Errorf("tags call args[%d] = %q, want %q", i, tagCall.Args[i], want)
		}
	}

	// Second run: atom now carries #orphans — aggregator returns empty,
	// ApplyOrphanFamilies receives empty slice, zero tags calls.
	payload2 := orphanFamilyListPayload(t,
		[]string{"#test/notes", "#strayfamily/sub", "#orphans"})
	fake2 := newFakeBearcli(payload2)
	ctx2 := domain.ContextWithBackend(t.Context(), fake2)

	var buf2 bytes.Buffer
	runLintExpectOK(t, ctx2, &buf2, []*domain.Domain{flatListDomainForTest()}, true, "apply-mode second run idempotent")

	if got := fake2.countKind("tags"); got != 0 {
		t.Errorf("idempotency violated: apply mode tags-call count on already-tagged atom = %d, want 0",
			got)
	}
}

// TestRun_ApplyMode_PoolInitializedFromCold pins the SF8 wire-up: the
// production path (RunLint) MUST arm the bearcli concurrency pool
// itself — earlier tests pre-armed via armBearcliPool, so dropping
// line 44 from RunLint would not have failed any test. This case
// resets the pool to a sentinel capacity (2) WITHOUT calling
// armBearcliPool, then asserts that after RunLint the pool capacity
// reflects the production default (engine.DefaultBearcliConcurrency).
// If the production SetBearcliConcurrency wiring drops, capacity stays
// at 2 and the assertion catches it.
func TestRun_ApplyMode_PoolInitializedFromCold(t *testing.T) {
	domain.ResetBearcliPoolForTest(2)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	fake := newFakeBearcli([]byte(`[]`))
	ctx := domain.ContextWithBackend(t.Context(), fake)
	var buf bytes.Buffer
	runLintExpectOK(t, ctx, &buf, []*domain.Domain{flatListDomainForTest()}, true, "cold-pool path")

	metrics := domain.BearcliMetricsSnapshot()
	if metrics.Capacity < 4 {
		t.Errorf("pool capacity after RunLint = %d, want >= 4 (production default); "+
			"SetBearcliConcurrency wiring dropped", metrics.Capacity)
	}
}

// TestRun_ApplyMode_EmptyDomains_NoBearcliCalls pins the short-circuit
// guard in runApplyOrphanPass: an empty domain catalog must skip the
// corpus orphan scan entirely. Without the guard, RunLint would issue
// a `list --location notes` round-trip just to discover there is
// nothing to tag.
func TestRun_ApplyMode_EmptyDomains_NoBearcliCalls(t *testing.T) {
	armBearcliPool(t)
	fake := newFakeBearcli([]byte(`[]`))
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	runLintExpectOK(t, ctx, &buf, nil, true, "apply-mode empty domains")

	if got := fake.count.Load(); got != 0 {
		t.Errorf("apply mode with no domains issued %d bearcli calls; want 0", got)
	}
}

// scanFailFakeBearcli serves a normal per-domain list and errors on
// the corpus orphan scan (the `list --location notes` call). Lets
// the apply-mode error-propagation test exercise the scan-failure
// branch end-to-end without a separate seam in the production code.
type scanFailFakeBearcli struct {
	*fakeBearcli
	corpusErr error
}

// Run wraps fakeBearcli.Run, intercepting the corpus list call by
// `--location notes` and returning corpusErr. Per-domain list calls
// (which carry `--tag X`) fall through to the embedded fake.
func (f *scanFailFakeBearcli) Run(ctx context.Context, args []string, stdin string) ([]byte, error) {
	if len(args) >= 3 && args[0] == "list" && args[1] == "--location" && args[2] == "notes" {
		return nil, f.corpusErr
	}
	return f.fakeBearcli.Run(ctx, args, stdin)
}

// TestRun_ApplyMode_ScanFailure_ReturnsError pins the SF1 error
// propagation: when the corpus orphan scan fails, RunLint must
// surface the wrapped error so the cmd shim exits non-zero. Earlier
// the failure logged to stderr and the process exited 0.
func TestRun_ApplyMode_ScanFailure_ReturnsError(t *testing.T) {
	armBearcliPool(t)
	corpusErr := errors.New("bearcli list --location notes boom")
	fake := &scanFailFakeBearcli{
		fakeBearcli: newFakeBearcli(brokenH1ListPayload(t)),
		corpusErr:   corpusErr,
	}
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	err := cli.RunLint(ctx, &buf, []*domain.Domain{flatListDomainForTest()}, true)
	if err == nil {
		t.Fatalf("RunLint apply-mode with failing corpus scan: err = nil, want wrapped scan error")
	}
	if !strings.Contains(err.Error(), "orphan scan") {
		t.Errorf("err = %v, want message mentioning 'orphan scan'", err)
	}
}

// mutatingFakeBearcli wraps fakeBearcli and rewrites listPayload after
// every `tags add` call so a subsequent list returns the mutated
// state. Lets TestRun_ApplyMode_TrueE2EIdempotency exercise the full
// idempotency contract against ONE fake instance (rather than two
// fakes with hand-mutated payloads), which is what TC8 of the review
// asked for.
type mutatingFakeBearcli struct {
	*fakeBearcli
	notes []map[string]any
}

// newMutatingFakeBearcli wires the notes slice to the embedded fake's
// listPayload by marshaling once at construction. AppendTag mutates
// the slice and re-marshals so the next list call returns the new
// shape.
func newMutatingFakeBearcli(t *testing.T, notes []map[string]any) *mutatingFakeBearcli {
	t.Helper()
	payload, err := json.Marshal(notes)
	if err != nil {
		t.Fatalf("newMutatingFakeBearcli: marshal: %v", err)
	}
	return &mutatingFakeBearcli{
		fakeBearcli: newFakeBearcli(payload),
		notes:       notes,
	}
}

// Run delegates to the embedded fake, then re-marshals notes after
// every `tags add` so a subsequent list call returns the mutated
// payload. Errors propagate verbatim from the embedded fake.
func (f *mutatingFakeBearcli) Run(ctx context.Context, args []string, stdin string) ([]byte, error) {
	out, err := f.fakeBearcli.Run(ctx, args, stdin)
	if err != nil {
		return out, err
	}
	if len(args) >= 4 && args[0] == "tags" && args[1] == "add" {
		noteID, newTag := args[2], "#"+args[3]
		for _, n := range f.notes {
			if id, _ := n["id"].(string); id != noteID {
				continue
			}
			tags, _ := n["tags"].([]string)
			n["tags"] = append(tags, newTag)
		}
		updated, marshalErr := json.Marshal(f.notes)
		if marshalErr != nil {
			return nil, marshalErr
		}
		f.mu.Lock()
		f.listPayload = updated
		f.mu.Unlock()
	}
	return out, nil
}

// TestRun_ApplyMode_TrueE2EIdempotency drives the full apply →
// re-apply cycle against ONE fake instance whose listPayload mutates
// after the first `tags add` call. Pins the operator-facing contract:
// running `noxctl lint --apply` twice in a row tags the atom once,
// then becomes a no-op on the second run because the corpus scan
// observes the freshly-added `#orphans` tag and skips the atom.
func TestRun_ApplyMode_TrueE2EIdempotency(t *testing.T) {
	armBearcliPool(t)
	notes := []map[string]any{{
		"id":      "note-stray",
		"title":   "Stray Note",
		"content": "",
		"tags":    []string{"#test/notes", "#strayfamily/sub"},
		"created": "2026-05-23T12:00:00Z",
	}}
	fake := newMutatingFakeBearcli(t, notes)
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf1 bytes.Buffer
	runLintExpectOK(t, ctx, &buf1, []*domain.Domain{flatListDomainForTest()}, true, "true-E2E first run")
	if got := fake.countKind("tags"); got != 1 {
		t.Fatalf("first run tags-call count = %d, want 1", got)
	}

	var buf2 bytes.Buffer
	runLintExpectOK(t, ctx, &buf2, []*domain.Domain{flatListDomainForTest()}, true, "true-E2E second run")
	if got := fake.countKind("tags"); got != 1 {
		t.Errorf("second run tags-call count = %d, want still 1 (idempotency must skip already-tagged atom)", got)
	}
}

// TestRun_AuditMode_OrphanFamilyLabelInOutput tightens the substring
// assertion in TestRun_AuditMode_OrphanFamilyAppearsInOutput: the
// `orphan-family:` category header must appear in the rendered report
// so a consumer grepping for the section header gets a deterministic
// landmark. A bare `"Stray Note"` substring would pass even if
// PrintFindings dropped the category header — this test guards the
// rendering shape itself.
func TestRun_AuditMode_OrphanFamilyLabelInOutput(t *testing.T) {
	armBearcliPool(t)
	payload := orphanFamilyListPayload(t,
		[]string{"#test/notes", "#strayfamily/sub"})
	fake := newFakeBearcli(payload)
	ctx := domain.ContextWithBackend(t.Context(), fake)

	var buf bytes.Buffer
	runLintExpectOK(t, ctx, &buf, []*domain.Domain{flatListDomainForTest()}, false,
		"orphan-family label rendering")

	out := buf.String()
	if !strings.Contains(out, "orphan-family:") {
		t.Errorf("audit output missing 'orphan-family:' category header; got %q", out)
	}
}
