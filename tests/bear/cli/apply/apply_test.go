package apply_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/bear/render"
	"github.com/barad1tos/noxctl/bear/state"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

type failingBackend struct{}

func (failingBackend) Run(_ context.Context, _ []string, _ string) ([]byte, error) {
	return nil, errors.New("bearcli unavailable")
}

type bootstrapFailureBackend struct{}

func (bootstrapFailureBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`{}`), nil
	}
	switch args[0] {
	case "list":
		if hasArgValue(args, "--location", "notes") {
			return []byte(`[{"id":"bootstrap-1","title":"Bootstrap Atom","tags":["#test/bootstrap"],"content":"# Bootstrap Atom\nbody\n"}]`), nil
		}
		return []byte(`[]`), nil
	case "show":
		return []byte(`{"hash":"deadbeef"}`), nil
	case "overwrite":
		if len(args) > 1 && args[1] == "bootstrap-1" {
			return nil, errors.New("bootstrap overwrite denied")
		}
		return []byte(`{"ok":true}`), nil
	case "create":
		return []byte(`{"id":"created","title":"created","content":"","tags":[]}`), nil
	case "cat":
		return []byte(`{"id":"created","title":"created","content":"","tags":[]}`), nil
	default:
		return []byte(`{}`), nil
	}
}

func hasArgValue(args []string, flag, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == flag && args[i+1] == value {
			return true
		}
	}
	return false
}

func falsePtr() *bool {
	return new(false)
}

func truePtr() *bool {
	return new(true)
}

type notifyingWriter struct {
	dst    *bytes.Buffer
	needle string
	seen   chan struct{}
	once   sync.Once
}

func (w *notifyingWriter) Write(p []byte) (int, error) {
	n, err := w.dst.Write(p)
	if strings.Contains(string(p), w.needle) {
		w.once.Do(func() { close(w.seen) })
	}
	return n, err
}

func disabledFeatureCatalog() *config.Catalog {
	return &config.Catalog{
		Features: config.Features{
			AutoTagDefault:    falsePtr(),
			CrossDomainMoves:  falsePtr(),
			TimePromotion:     falsePtr(),
			ForeignTagEscape:  falsePtr(),
			DuplicateRegistry: falsePtr(),
			DomainBootstrap:   falsePtr(),
		},
	}
}

func failingDomain() *domain.Domain {
	return &domain.Domain{
		Tag:          "test/failing",
		CanonicalTag: "#test/failing",
		IndexTitle:   "Test Failing",
		ParseMeta: func(_ *domain.Domain, _ string) domain.AtomicMeta {
			return domain.AtomicMeta{}
		},
		RenderMaster: func(_ *domain.Domain, _ map[string][]domain.Note) string {
			return "# Test Failing\n"
		},
	}
}

func TestRunApply_DomainFailureReturnsFailureSentinel(t *testing.T) {
	dir := t.TempDir()
	ctx := bearcli.ContextWithBackend(context.Background(), failingBackend{})
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{failingDomain()},
		Catalog:   disabledFeatureCatalog(),
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if !errors.Is(err, cli.ErrApplyFailures) {
		t.Fatalf("RunApply err = %v, want ErrApplyFailures", err)
	}
	if !strings.Contains(stdout.String(), "failed=1") {
		t.Fatalf("stdout = %q, want failed=1 recap row", stdout.String())
	}
	if !strings.Contains(stderr.String(), "bearcli unavailable") {
		t.Fatalf("stderr = %q, want quiet failure diagnostics", stderr.String())
	}
}

func TestRunApply_QuietReportsDomainBootstrapFailure(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	dir := t.TempDir()
	d := render.NewFlatListDomain("test/bootstrap", "Bootstrap")
	catalog := disabledFeatureCatalog()
	catalog.Features.DomainBootstrap = truePtr()
	ctx := bearcli.ContextWithBackend(context.Background(), bootstrapFailureBackend{})
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{d},
		Catalog:   catalog,
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if !errors.Is(err, cli.ErrApplyFailures) {
		t.Fatalf("RunApply err = %v, want ErrApplyFailures", err)
	}
	if !strings.Contains(stdout.String(), "domain_bootstrap") || !strings.Contains(stdout.String(), "failed=1") {
		t.Fatalf("stdout = %q, want failed domain_bootstrap recap row", stdout.String())
	}
	if !strings.Contains(stderr.String(), "domain-bootstrap") ||
		!strings.Contains(stderr.String(), "bootstrap overwrite denied") {
		t.Fatalf("stderr = %q, want quiet bootstrap failure diagnostics", stderr.String())
	}
}

func TestRunApply_WarnsWhenPreviousApplyWasInProgress(t *testing.T) {
	dir := t.TempDir()
	statePath := filepath.Join(dir, "state.json")
	startedAt := time.Date(2026, 5, 27, 7, 30, 0, 0, time.UTC)
	if err := (&state.State{
		Version:    state.SchemaVersion,
		InProgress: state.InProgress{Verb: "apply", StartedAt: startedAt},
	}).Save(statePath); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer

	err := cli.RunApply(context.Background(), cli.ApplyOptions{
		Catalog:   disabledFeatureCatalog(),
		StatePath: statePath,
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("RunApply: %v", err)
	}
	if !strings.Contains(stderr.String(), "resuming after interrupted apply") {
		t.Fatalf("stderr = %q, want interrupted apply warning", stderr.String())
	}
}

// TestRunApply_WarnsWhenPinRegistryFailsToLoad pins that an unreadable pin
// registry is surfaced to the operator rather than silently dropped. PinTarget
// points at a directory, so the registry read fails with a non-NotExist error
// (a missing file is legitimately silent; an unreadable one must warn). apply
// must still proceed with no pins. Reverting to the silent discard form
// regresses this — the operator would lose pins with no trace.
func TestRunApply_WarnsWhenPinRegistryFailsToLoad(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	err := cli.RunApply(context.Background(), cli.ApplyOptions{
		Catalog:   disabledFeatureCatalog(),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		PinTarget: dir, // a directory, not a file — the read fails
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("RunApply must proceed despite a pin-load failure, got: %v", err)
	}
	if got := stderr.String(); !strings.Contains(got, "pin registry") || !strings.Contains(got, "failed to load") {
		t.Fatalf("stderr = %q, want a pin-registry load-failure warning", got)
	}
}

// TestRunApply_RejectsNegativeConcurrency pins that an invalid --concurrency is
// refused at the apply boundary rather than silently coerced. A negative value
// is a user typo; apply must error with a clear message, not run with garbage.
func TestRunApply_RejectsNegativeConcurrency(t *testing.T) {
	dir := t.TempDir()
	var stdout, stderr bytes.Buffer

	err := cli.RunApply(context.Background(), cli.ApplyOptions{
		Catalog:     disabledFeatureCatalog(),
		StatePath:   filepath.Join(dir, "state.json"),
		LockPath:    filepath.Join(dir, ".lock"),
		Concurrency: -1,
		Quiet:       true,
		Stdout:      &stdout,
		Stderr:      &stderr,
	})
	if err == nil || !strings.Contains(err.Error(), "concurrency") {
		t.Fatalf("RunApply(--concurrency=-1) err = %v, want an error mentioning concurrency", err)
	}
}

// TestRunApply_QuietSuppressesCycleTelemetry pins that `noxctl apply -q`
// stays quiet on successful runs. The engine keeps daemon telemetry enabled by
// default, but the one-shot CLI boundary must not leak the success-only
// `regen cycle:` line through the process-global logger when quiet is set.
func TestRunApply_QuietSuppressesCycleTelemetry(t *testing.T) {
	bearcli.ResetPoolForTest(1)
	t.Cleanup(func() { bearcli.ResetPoolForTest(1) })

	dir := t.TempDir()
	d := render.NewFlatListDomain("test/quiet", "Quiet")
	catalog := disabledFeatureCatalog()
	catalog.Features.AutoTagDefault = truePtr()
	catalog.Meta.DailyDefaultTag = d.Tag
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{
		"": {
			{ID: "untagged-1", Title: "Loose", Content: "# Loose\nbody\n"},
		},
		d.Tag: {{ID: "atom-1", Title: "Atom", Content: "# Atom\nbody\n", Tags: []string{"#test/quiet"}}},
	})
	ctx := bearcli.ContextWithBackend(context.Background(), backend)

	var logBuf bytes.Buffer
	restoreOutput := log.Writer()
	restoreFlags := log.Flags()
	log.SetOutput(&logBuf)
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(restoreOutput)
		log.SetFlags(restoreFlags)
	})

	var stdout, stderr bytes.Buffer
	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{d},
		Catalog:   catalog,
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("RunApply quiet success: %v", err)
	}
	if logBuf.Len() != 0 {
		t.Fatalf("log = %q, want no quiet success logs", logBuf.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no quiet success stdout", stdout.String())
	}
	if stderr.Len() != 0 {
		t.Fatalf("stderr = %q, want no quiet success stderr", stderr.String())
	}
}

// TestRunApply_QuietStillReportsHeldLockWait pins quiet-mode's boundary:
// success noise is suppressed, but a blocked apply still tells the operator why
// the command is waiting instead of looking hung.
func TestRunApply_QuietStillReportsHeldLockWait(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	release, err := engine.AcquireApply(context.Background(), lockPath, false, io.Discard)
	if err != nil {
		t.Fatalf("AcquireApply first lock: %v", err)
	}
	var releaseOnce sync.Once
	releaseLock := func() { releaseOnce.Do(release) }
	defer releaseLock()

	var stderr bytes.Buffer
	stderrSignal := &notifyingWriter{
		dst:    &stderr,
		needle: "waiting for lock",
		seen:   make(chan struct{}),
	}
	done := make(chan error, 1)
	go func() {
		done <- cli.RunApply(context.Background(), cli.ApplyOptions{
			Catalog:   disabledFeatureCatalog(),
			PinTarget: filepath.Join(dir, "pins.json"),
			StatePath: filepath.Join(dir, "state.json"),
			LockPath:  lockPath,
			Quiet:     true,
			Stdout:    io.Discard,
			Stderr:    stderrSignal,
		})
	}()

	select {
	case <-stderrSignal.seen:
	case waitErr := <-done:
		t.Fatalf("RunApply completed before lock advisory: err=%v stderr=%q", waitErr, stderr.String())
	case <-time.After(2 * time.Second):
		releaseLock()
		t.Fatalf("timed out waiting for lock advisory; stderr=%q", stderr.String())
	}
	releaseLock()
	if waitErr := <-done; waitErr != nil {
		t.Fatalf("RunApply after lock release: %v", waitErr)
	}
	if !strings.Contains(stderr.String(), "waiting for lock") {
		t.Fatalf("stderr = %q, want held-lock wait advisory", stderr.String())
	}
}

// TestRunApply_BenchSkipsSummaryOnEarlyLockError pins that --bench reports only
// completed apply cycles. A fail-fast lock error returns before engine metrics
// are populated, so printing a zero-valued BENCH block would mislead operators.
func TestRunApply_BenchSkipsSummaryOnEarlyLockError(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, ".lock")
	release, err := engine.AcquireApply(context.Background(), lockPath, false, io.Discard)
	if err != nil {
		t.Fatalf("AcquireApply first lock: %v", err)
	}
	defer release()

	var stdout, stderr bytes.Buffer
	err = cli.RunApply(context.Background(), cli.ApplyOptions{
		Catalog:     disabledFeatureCatalog(),
		PinTarget:   filepath.Join(dir, "pins.json"),
		StatePath:   filepath.Join(dir, "state.json"),
		LockPath:    lockPath,
		NoWait:      true,
		Stdout:      &stdout,
		Stderr:      &stderr,
		Bench:       true,
		Concurrency: 4,
	})
	if err == nil || !strings.Contains(err.Error(), "lock") {
		t.Fatalf("RunApply bench no-wait err = %v, want lock error", err)
	}
	if strings.Contains(stdout.String(), "BENCH") || strings.Contains(stdout.String(), "capacity=0") {
		t.Fatalf("stdout = %q, want no bench summary for early lock error", stdout.String())
	}
}

// TestRunApplySweep_AbortsOnIterationError pins that a sweep stops at the first
// failing apply and surfaces that error, rather than charging on to the next
// concurrency value and masking the failure. The operator gets the real error
// from value 4 and value 8 is never attempted.
func TestRunApplySweep_AbortsOnIterationError(t *testing.T) {
	dir := t.TempDir()
	ctx := bearcli.ContextWithBackend(context.Background(), failingBackend{})
	var stdout, stderr bytes.Buffer
	runs := 0
	optsFor := func(_ int) cli.ApplyOptions {
		runs++
		return cli.ApplyOptions{
			Domains:   []*domain.Domain{failingDomain()},
			Catalog:   disabledFeatureCatalog(),
			PinTarget: filepath.Join(dir, "pins.json"),
			StatePath: filepath.Join(dir, "state.json"),
			LockPath:  filepath.Join(dir, ".lock"),
			Quiet:     true,
			Stdout:    &stdout,
			Stderr:    &stderr,
		}
	}

	err := cli.RunApplySweep(ctx, []int{4, 8}, optsFor)
	if !errors.Is(err, cli.ErrApplyFailures) {
		t.Fatalf("RunApplySweep err = %v, want ErrApplyFailures from the first iteration", err)
	}
	if runs != 1 {
		t.Fatalf("optsFor called %d times, want 1 (sweep must abort after the first failing value, not run value 8)", runs)
	}
}

type promotionOverwriteFailBackend struct{}

func (promotionOverwriteFailBackend) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) == 0 {
		return []byte(`[]`), nil
	}
	switch args[0] {
	case "list":
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--tag" && args[i+1] == "test/daily" {
				return []byte(`[` +
					`{"id":"atom-1","title":"Aged","tags":["#test/daily"],` +
					`"content":"# Aged\n#test/daily | [[Daily]]\n---\nbody\n",` +
					`"created":"2020-01-01T00:00:00Z"}` +
					`]`), nil
			}
		}
		return []byte(`[]`), nil
	case "create", "cat", "show":
		return []byte(`{"id":"created","title":"created","content":"","hash":"h","tags":[]}`), nil
	case "overwrite":
		return nil, errors.New("overwrite failed")
	default:
		return []byte(`[]`), nil
	}
}

func timePromotionCatalog() *config.Catalog {
	catalog := disabledFeatureCatalog()
	catalog.Features.TimePromotion = truePtr()
	catalog.Promotions = []config.Promotion{
		{From: "test/daily", To: "test/weekly", Boundary: "day"},
	}
	return catalog
}

func missingPromotionTargetCatalog() *config.Catalog {
	catalog := disabledFeatureCatalog()
	catalog.Features.TimePromotion = truePtr()
	catalog.Promotions = []config.Promotion{
		{From: "test/daily", To: "test/missing", Boundary: "day"},
	}
	return catalog
}

func TestRunApply_PrePassWriteFailureReturnsFailureSentinel(t *testing.T) {
	dir := t.TempDir()
	daily := render.NewFlatListDomain("test/daily", "Daily")
	weekly := render.NewFlatListDomain("test/weekly", "Weekly")
	ctx := bearcli.ContextWithBackend(context.Background(), promotionOverwriteFailBackend{})
	var stdout bytes.Buffer

	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{daily, weekly},
		Catalog:   timePromotionCatalog(),
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
	})
	if !errors.Is(err, cli.ErrApplyFailures) {
		t.Fatalf("RunApply err = %v, want ErrApplyFailures", err)
	}
	if !strings.Contains(stdout.String(), "time_promotion") || !strings.Contains(stdout.String(), "failed=1") {
		t.Fatalf("stdout = %q, want time_promotion failed=1 recap row", stdout.String())
	}
}

func TestRunApply_QuietReportsNonFatalPrePassDiagnostic(t *testing.T) {
	dir := t.TempDir()
	daily := render.NewFlatListDomain("test/daily", "Daily")
	backend := testutil.NewRecordingBackend(map[string][]domain.Note{
		daily.Tag: {{
			ID:      "aged-1",
			Title:   "Aged",
			Tags:    []string{"#test/daily"},
			Content: "# Aged\n#test/daily | [[Daily]]\n---\nbody\n",
			Created: time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC),
		}},
	})
	ctx := bearcli.ContextWithBackend(context.Background(), backend)
	var stdout, stderr bytes.Buffer

	err := cli.RunApply(ctx, cli.ApplyOptions{
		Domains:   []*domain.Domain{daily},
		Catalog:   missingPromotionTargetCatalog(),
		PinTarget: filepath.Join(dir, "pins.json"),
		StatePath: filepath.Join(dir, "state.json"),
		LockPath:  filepath.Join(dir, ".lock"),
		Quiet:     true,
		Stdout:    &stdout,
		Stderr:    &stderr,
	})
	if err != nil {
		t.Fatalf("RunApply quiet success with warning: %v", err)
	}
	if stdout.Len() != 0 {
		t.Fatalf("stdout = %q, want no quiet success stdout", stdout.String())
	}
	if !strings.Contains(stderr.String(), "no domain registered") ||
		!strings.Contains(stderr.String(), "test/missing") {
		t.Fatalf("stderr = %q, want missing promotion target warning", stderr.String())
	}
}
