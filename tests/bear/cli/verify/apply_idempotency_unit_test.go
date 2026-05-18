// Package verify_test — direct coverage of checkApplyIdempotency
// against in-memory domain fixtures, bypassing the catalog-load and
// daemon-log layers.
//
// User-scenario framing: each test mimics what the operator sees from
// the apply-idempotency check given a specific backend behavior, with
// the per-domain fixture coming from `config.Load` over a temp TOML
// (just enough to exercise the real engine.Apply pipeline). The check
// is driven directly through `verify.CheckApplyIdempotencyForTest`
// so the assertions stay focused on the twin-apply contract.
package verify_test

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
)

// loadFixtureDomains returns the parsed domain slice from a minimal
// TOML catalog. Caller-supplied wrapper so a test failure inside
// config.Load doesn't masquerade as an apply-idempotency failure.
func loadFixtureDomains(t *testing.T) []*bear.Domain {
	t.Helper()
	domains, _, err := config.Load(writeMinimalCatalog(t))
	if err != nil {
		t.Fatalf("config.Load(minimal): %v", err)
	}
	return domains
}

// applyOpts builds an ApplyOpts template rooted at t.TempDir() —
// matches what `cmd/noxctl/verify.go::buildVerifyApplyTemplate` does
// in production. AutoTagDefault is forcibly OFF because the minimal
// catalog has no `quicknote/daily` domain — leaving it on triggers
// `ApplyDailyDefaultTag: dailyDomain is nil`, marking pre-pass Failed
// and routing every test through the `first.AnyFailed()` ERROR
// branch instead of exercising the PASS / per-domain branches.
func applyOpts(t *testing.T) engine.ApplyOpts {
	t.Helper()
	dir := t.TempDir()
	pins, _ := bear.LoadPinRegistry(filepath.Join(dir, "pins.json"))
	features := engine.AllFeaturesOn()
	features.AutoTagDefault = false
	return engine.ApplyOpts{
		Pins:      pins,
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, "lock"),
		Features:  features,
	}
}

// TestRunApplyOnce_BenignBackend_Returns — drive runApplyOnce directly
// through the test seam. Pins the contract that a clean apply pass
// returns a non-nil ApplyResult and never errors, so the verify check
// can branch on AnyFailed / Domain stats without a nil-deref guard.
func TestRunApplyOnce_BenignBackend_Returns(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx := ctxWithBenignBackend(t)
	res, err := verify.RunApplyOnceForTest(ctx,
		verify.Options{ApplyOpts: applyOpts(t)},
		domains)
	if err != nil {
		t.Fatalf("runApplyOnce err = %v, want nil", err)
	}
	if res == nil {
		t.Fatalf("res = nil, want non-nil ApplyResult")
	}
	if res.Domains == nil {
		t.Errorf("res.Domains = nil — verify check would panic on AnyFailed/stat aggregation")
	}
}

// TestCheckApplyIdempotency_OperatorOmitsApplyOptsLock_FirstApplyFails
// — Sourcery iter-#3 regression: with `--with-apply` set but
// `Options.ApplyOpts.LockPath` empty, engine.Apply fails at
// AcquireApply. Check surfaces StatusError + a "first apply failed"
// hint so the operator knows the issue is infrastructure, not drift.
func TestCheckApplyIdempotency_OperatorOmitsApplyOptsLock_FirstApplyFails(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx := ctxWithBenignBackend(t)
	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		// ApplyOpts deliberately at zero value.
	}, domains)
	if got.Status != verify.StatusError {
		t.Errorf("status = %v, want StatusError (infrastructure failure)", got.Status)
	}
	if !strings.Contains(got.Message, "first apply failed") {
		t.Errorf("expected 'first apply failed' hint; got: %q", got.Message)
	}
}

// TestCheckApplyIdempotency_OperatorRunsWithBenignBackend_OutcomeRecorded
// — drive the full twin-apply path with the benign backend and a
// populated ApplyOpts. The outcome (PASS / FAIL / ERROR) depends on
// engine.Apply's behavior with empty bearcli responses; what matters
// is that the check completes and produces a status + non-empty
// message. Pins that the twin-apply orchestration runs end-to-end
// without panic when given a hermetic backend.
func TestCheckApplyIdempotency_OperatorRunsWithBenignBackend_OutcomeRecorded(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx := ctxWithBenignBackend(t)
	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		ApplyOpts: applyOpts(t),
	}, domains)
	// Status must be one of the four — never empty.
	switch got.Status {
	case verify.StatusPass, verify.StatusFail, verify.StatusError:
		// expected
	case verify.StatusSkipped:
		t.Errorf("twin-apply must never report Skipped from within "+
			"checkApplyIdempotency — Skipped is the WithApply=false "+
			"caller-level branch; got: %+v", got)
	default:
		t.Errorf("unexpected status: %+v", got)
	}
	if got.Message == "" {
		t.Errorf("status %v but empty message — operator gets no signal", got.Status)
	}
	if got.Name != "apply-idempotency" {
		t.Errorf("check name = %q, want 'apply-idempotency'", got.Name)
	}
}

// failingBearcliBackend returns an error on every bearcli call. From
// the operator's POV: bearcli outage / Bear not running. engine.Apply
// surfaces the failures via DomainCounts.Failed > 0; checkApplyIdempotency
// then routes through the `first.AnyFailed()` StatusError branch.
type failingBearcliBackend struct{}

func (failingBearcliBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	return nil, errSimulatedBearcliOutage
}

// errSimulatedBearcliOutage is a sentinel for the failing-backend
// path — gives test-output a recognizable string when it bubbles up.
var errSimulatedBearcliOutage = simulatedErr("bearcli simulated outage")

type simulatedErr string

func (e simulatedErr) Error() string { return string(e) }

// TestCheckApplyIdempotency_OperatorWithFailingBackend_FirstPassReportsFailures
// — bearcli outage during the first apply pass. engine.Apply
// completes (no orchestrator-level error) but per-domain RunRegen
// failures push DomainCounts.Failed > 0; checkApplyIdempotency
// surfaces StatusError + the "first apply pass reported per-domain
// failures" message so the operator routes to the daemon log.
func TestCheckApplyIdempotency_OperatorWithFailingBackend_FirstPassReportsFailures(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx := bear.ContextWithBackend(t.Context(), failingBearcliBackend{})
	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		ApplyOpts: applyOpts(t),
	}, domains)
	// Outcome must be StatusError (not Fail, not Pass) — bearcli outage
	// is infrastructure-class, not idempotency-class.
	if got.Status != verify.StatusError {
		t.Errorf("status = %v, want StatusError (bearcli outage classified as infrastructure)",
			got.Status)
	}
	// Either of the two error messages is acceptable — the engine
	// might surface as a Plan-level err OR via per-domain Failed counts.
	wantOneOf := []string{
		"first apply failed",
		"first apply pass reported per-domain failures",
	}
	matched := false
	for _, w := range wantOneOf {
		if strings.Contains(got.Message, w) {
			matched = true
			break
		}
	}
	if !matched {
		t.Errorf("expected one of %v in message; got: %q", wantOneOf, got.Message)
	}
}

// TestCheckApplyIdempotency_OperatorCancelsMidCycle_SurfacesError —
// SIGINT mid-cycle: ctx canceled before pass-1 finishes. engine.Apply
// returns with res.Interrupted=true; runApplyOnce wraps that as
// "apply interrupted" error. Check surfaces StatusError so the verdict
// line shows ERROR.
func TestCheckApplyIdempotency_OperatorCancelsMidCycle_SurfacesError(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx, cancel := context.WithCancel(ctxWithBenignBackend(t))
	cancel() // pre-cancel so pass-1 sees ctx.Done() immediately.
	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		ApplyOpts: applyOpts(t),
	}, domains)
	if got.Status != verify.StatusError {
		t.Errorf("status = %v, want StatusError on canceled ctx", got.Status)
	}
}
