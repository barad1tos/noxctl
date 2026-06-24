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
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
)

// loadFixtureDomains returns the parsed domain slice from a minimal
// TOML catalog. Caller-supplied wrapper so a test failure inside
// config.Load doesn't masquerade as an apply-idempotency failure.
func loadFixtureDomains(t *testing.T) []*domain.Domain {
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
	pins, _ := domain.LoadPinRegistry(filepath.Join(dir, "pins.json"))
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
	if res.Domains == nil {
		t.Errorf("res.Domains = nil — verify check would panic on AnyFailed/stat aggregation")
	}
}

// TestCheckApplyIdempotency_OperatorOmitsApplyOptsLock_FirstApplyFails
// — regression-pin: with `--with-apply` set but
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

// TestCheckApplyIdempotency_OperatorOnCleanVault_PassesWithSecondPassClean
// — pins the happy-path operator outcome (gate accepted) the way
// ship-gate.sh asserts it against the real vault.
func TestCheckApplyIdempotency_OperatorOnCleanVault_PassesWithSecondPassClean(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx := ctxWithBenignBackend(t)
	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		ApplyOpts: applyOpts(t),
	}, domains)
	if got.Status != verify.StatusPass {
		t.Fatalf("status = %v, want StatusPass; message=%q", got.Status, got.Message)
	}
	if got.Name != "apply-idempotency" {
		t.Errorf("check name = %q, want 'apply-idempotency'", got.Name)
	}
	for _, want := range []string{
		"second pass clean",
		"pass-1 stats:",
		"created",
		"changed",
		"unchanged",
		"failed",
		"across",
	} {
		if !strings.Contains(got.Message, want) {
			t.Errorf("expected %q in PASS message; got: %q", want, got.Message)
		}
	}
}

// failingBearcliBackend returns the supplied error on every bearcli
// call — used to simulate bearcli outage / Bear not running.
// engine.Apply completes (no orchestrator-level error) but per-domain
// RunRegen failures push DomainCounts.Failed > 0; checkApplyIdempotency
// then routes through the `first.AnyFailed()` StatusError branch.
type failingBearcliBackend struct {
	err error
}

func (b failingBearcliBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	return nil, b.err
}

type secondPassFailureAndCreateBackend struct {
	mu         sync.Mutex
	createSeen bool
}

func (b *secondPassFailureAndCreateBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte("{}"), nil
	}
	switch args[0] {
	case "list":
		if listLocation(args) == "notes" && b.hasCreated() {
			return nil, errors.New("second pass duplicate registry failure")
		}
		return []byte("[]"), nil
	case "cat":
		return []byte(`{"id":"master-1","title":"✱ Test","content":"","hash":"deadbeef","tags":[]}`), nil
	case "show":
		return []byte(`{"hash":"deadbeef","content":""}`), nil
	case "create":
		b.mu.Lock()
		b.createSeen = true
		b.mu.Unlock()
		return []byte(`{"id":"master-1","title":"✱ Test"}`), nil
	case "overwrite":
		return []byte(`{"ok":true}`), nil
	}
	return nil, errors.New("secondPassFailureAndCreateBackend: unhandled bearcli call")
}

func (b *secondPassFailureAndCreateBackend) hasCreated() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.createSeen
}

func listLocation(args []string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == "--location" {
			return args[i+1]
		}
	}
	return ""
}

type simulatedErr string

func (e simulatedErr) Error() string { return string(e) }

// TestCheckApplyIdempotency_OperatorWithFailingBackend_FirstPassReportsFailures
// — bearcli outage during the first apply pass. Pins the
// `first.AnyFailed()` per-domain-failure branch (distinct from the
// AcquireApply-error branch covered by
// _OperatorOmitsApplyOptsLock_FirstApplyFails).
func TestCheckApplyIdempotency_OperatorWithFailingBackend_FirstPassReportsFailures(t *testing.T) {
	domains := loadFixtureDomains(t)
	backend := failingBearcliBackend{err: simulatedErr("bearcli simulated outage")}
	ctx := bearcli.ContextWithBackend(t.Context(), backend)
	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		ApplyOpts: applyOpts(t),
	}, domains)
	if got.Status != verify.StatusError {
		t.Errorf("status = %v, want StatusError (bearcli outage classified as infrastructure)",
			got.Status)
	}
	const want = "first apply pass reported per-domain failures"
	if !strings.Contains(got.Message, want) {
		t.Errorf("expected %q in message; got: %q", want, got.Message)
	}
}

func TestCheckApplyIdempotency_OperatorWithSecondPassFailure_ReturnsRuntimeErrorBeforeWriteDrift(t *testing.T) {
	domains := loadFixtureDomains(t)
	ctx := bearcli.ContextWithBackend(t.Context(), &secondPassFailureAndCreateBackend{})

	got := verify.CheckApplyIdempotencyForTest(ctx, verify.Options{
		ApplyOpts: applyOpts(t),
	}, domains)
	if got.Status != verify.StatusError {
		t.Fatalf("status = %v, want StatusError for second-pass runtime failure", got.Status)
	}
	if !strings.Contains(got.Message, "second apply pass reported apply failures") {
		t.Fatalf("message = %q, want second-pass runtime failure classification", got.Message)
	}
	if strings.Contains(got.Message, "wrote") {
		t.Fatalf("message = %q, want runtime failure before write-drift classification", got.Message)
	}
}

// TestCheckApplyIdempotency_OperatorCancelsMidCycle_SurfacesError —
// SIGINT mid-cycle: ctx canceled before pass-1 finishes. engine.Apply
// either returns with res.Interrupted=true (runApplyOnce wraps that as
// "apply interrupted" error) OR per-domain failures show up because
// the canceled-ctx bearcli calls error out (first.AnyFailed branch).
// Either path surfaces StatusError so the verdict line shows ERROR.
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
