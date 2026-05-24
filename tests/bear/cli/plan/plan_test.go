package plan_test

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli"
	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/engine"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

// planFake is a read-only bearcli.Backend for the RunPlan orchestration
// tests: every `list` returns empty (no notes, no master), `cat` returns an
// empty note, `show` a stub hash. Against an empty vault, engine.Plan sees no
// existing master for any catalog domain and reports create-drift — the
// "fresh vault, everything will be created" journey.
type planFake struct{}

func (planFake) Run(_ context.Context, args []string, _ string) ([]byte, error) {
	if len(args) > 0 && args[0] == "cat" {
		return []byte(`{"content":""}`), nil
	}
	if len(args) > 0 && args[0] == "show" {
		return []byte(`{"hash":"deadbeef"}`), nil
	}
	return []byte("[]"), nil
}

// planOptionsForTest builds PlanOptions pointing at the real catalog fixture
// with throwaway pin paths (legacy absent → migration is a no-op).
func planOptionsForTest(t *testing.T, args []string, stdout *bytes.Buffer) cli.PlanOptions {
	t.Helper()
	return cli.PlanOptions{
		Color:     engine.ColorNever,
		Output:    "text",
		Args:      args,
		CfgPath:   testutil.CatalogPath(t),
		PinLegacy: filepath.Join(t.TempDir(), "legacy-pins.json"),
		PinTarget: filepath.Join(t.TempDir(), "pins.json"),
		Stdout:    stdout,
		Stderr:    &bytes.Buffer{},
	}
}

func TestErrDriftDetectedSentinelDeclared(t *testing.T) {
	if cli.ErrDriftDetected == nil {
		t.Fatal("cli.ErrDriftDetected sentinel is nil")
	}
	if !errors.Is(cli.ErrDriftDetected, cli.ErrDriftDetected) {
		t.Error("errors.Is(cli.ErrDriftDetected, cli.ErrDriftDetected) = false; want true")
	}
	if !strings.Contains(cli.ErrDriftDetected.Error(), "drift") {
		t.Errorf("cli.ErrDriftDetected.Error() = %q; should mention 'drift'",
			cli.ErrDriftDetected.Error())
	}
}

func TestValidateOutputRejectsUnknownFormat(t *testing.T) {
	if err := cli.ValidateOutput("yaml"); err == nil {
		t.Fatal("ValidateOutput(yaml) expected error; got nil")
	}
	if err := cli.ValidateOutput("text"); err != nil {
		t.Errorf("ValidateOutput(text) unexpected error: %v", err)
	}
	if err := cli.ValidateOutput("json"); err != nil {
		t.Errorf("ValidateOutput(json) unexpected error: %v", err)
	}
}

func TestScopeDomainsRejectsUnknownTag(t *testing.T) {
	_, err := cli.ScopeDomains(nil, "unknown/tag")
	if err == nil {
		t.Fatal("ScopeDomains(nil, unknown) expected error; got nil")
	}
	if !strings.Contains(err.Error(), "unknown tag") {
		t.Errorf("error should mention 'unknown tag'; got %q", err.Error())
	}
}

// TestLoadDomains_NoArgsReturnsFullCatalog — `noxctl plan` without a
// positional arg returns every domain from examples/personal.toml.
// Pins the core load path: pin migration is best-effort (logged
// warning, never blocks), TOML parse must succeed, slice must be
// non-empty.
func TestLoadDomains_NoArgsReturnsFullCatalog(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	domains, err := cli.LoadDomains(
		nil,
		testutil.CatalogPath(t),
		filepath.Join(tmp, "legacy-pins.json"),
		filepath.Join(tmp, "target-pins.json"),
		&stderr,
	)
	if err != nil {
		t.Fatalf("LoadDomains: %v", err)
	}
	if len(domains) == 0 {
		t.Fatal("LoadDomains returned empty slice; expected the full catalog")
	}
	// 31 = 27 leaves + 4 umbrellas (matches testutil's catalog self-test).
	if len(domains) != 31 {
		t.Errorf("len(domains) = %d, want 31", len(domains))
	}
}

// TestLoadDomains_SingleTagArgScopes — positional arg narrows the slice
// to one domain. The exact tag must round-trip — no silent drop or
// substring match.
func TestLoadDomains_SingleTagArgScopes(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	const wantTag = "library/poetry"
	domains, err := cli.LoadDomains(
		[]string{wantTag},
		testutil.CatalogPath(t),
		filepath.Join(tmp, "legacy-pins.json"),
		filepath.Join(tmp, "target-pins.json"),
		&stderr,
	)
	if err != nil {
		t.Fatalf("LoadDomains(%q): %v", wantTag, err)
	}
	if len(domains) != 1 {
		t.Fatalf("len(domains) = %d, want 1 when scoping by tag", len(domains))
	}
	if domains[0].Tag != wantTag {
		t.Errorf("scoped domain Tag = %q, want %q", domains[0].Tag, wantTag)
	}
}

// TestLoadDomains_UnknownTagSurfacesError — operator typo'd a tag.
// LoadDomains must reject with the "unknown tag" message from
// ScopeDomains so the operator gets a friendly hint instead of a
// crash deep inside the engine.
func TestLoadDomains_UnknownTagSurfacesError(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	_, err := cli.LoadDomains(
		[]string{"library/no-such-tag"},
		testutil.CatalogPath(t),
		filepath.Join(tmp, "legacy-pins.json"),
		filepath.Join(tmp, "target-pins.json"),
		&stderr,
	)
	if err == nil {
		t.Fatal("LoadDomains with bogus tag returned nil error; expected unknown-tag rejection")
	}
	if !strings.Contains(err.Error(), "unknown tag") {
		t.Errorf("error should mention 'unknown tag'; got %q", err.Error())
	}
}

// TestRunPlan_ReportsDriftAndExitsTwo_OnFreshVault drives the full `noxctl
// plan` orchestrator: against an empty vault (no masters exist yet), every
// catalog domain shows create-drift, so RunPlan renders the plan and returns
// ErrDriftDetected (the exit-2 contract). User-facing bug if this regresses:
// `noxctl plan` on a fresh vault reports clean / exit 0 and CI gates that
// branch on exit 2 never fire.
func TestRunPlan_ReportsDriftAndExitsTwo_OnFreshVault(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	ctx := domain.ContextWithBackend(context.Background(), planFake{})
	var stdout bytes.Buffer

	err := cli.RunPlan(ctx, planOptionsForTest(t, nil, &stdout))
	if !errors.Is(err, cli.ErrDriftDetected) {
		t.Fatalf("RunPlan on a fresh vault: err = %v, want ErrDriftDetected", err)
	}
	if stdout.Len() == 0 {
		t.Errorf("RunPlan rendered nothing; operator must see the plan output")
	}
}

// TestRunPlan_RejectsUnknownTag drives RunPlan with a positional tag absent
// from the catalog: it must fail loudly (unknown-tag error) rather than run a
// silent zero-domain plan. User-facing bug if this regresses: a typo'd
// `noxctl plan library/poetri` silently plans nothing and reports clean.
func TestRunPlan_RejectsUnknownTag(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	ctx := domain.ContextWithBackend(context.Background(), planFake{})
	var stdout bytes.Buffer

	err := cli.RunPlan(ctx, planOptionsForTest(t, []string{"library/poetri"}, &stdout))
	if err == nil {
		t.Fatalf("RunPlan with an unknown tag returned nil; want an unknown-tag rejection")
	}
	if !strings.Contains(err.Error(), "unknown tag") {
		t.Errorf("error should mention 'unknown tag'; got %q", err.Error())
	}
}

// TestRunPlan_EmitsJSON_WhenOutputJSON drives the `-o json` path: RunPlan must
// render machine-readable JSON (the documented shape CI tooling consumes)
// instead of the colored text report. User-facing bug if this regresses:
// `noxctl plan -o json | jq` breaks because the command emitted text.
func TestRunPlan_EmitsJSON_WhenOutputJSON(t *testing.T) {
	domain.ResetBearcliPoolForTest(4)
	t.Cleanup(func() { domain.ResetBearcliPoolForTest(1) })

	ctx := domain.ContextWithBackend(context.Background(), planFake{})
	var stdout bytes.Buffer
	opts := planOptionsForTest(t, nil, &stdout)
	opts.Output = "json"

	err := cli.RunPlan(ctx, opts)
	if !errors.Is(err, cli.ErrDriftDetected) {
		t.Fatalf("RunPlan -o json on a fresh vault: err = %v, want ErrDriftDetected", err)
	}
	out := stdout.String()
	if !strings.Contains(out, `"schema_version"`) || !strings.Contains(out, `"domains"`) {
		t.Errorf("`-o json` output is not the documented JSON shape; got:\n%s", out)
	}
}
