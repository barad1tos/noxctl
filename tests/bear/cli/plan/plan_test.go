package plan_test

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/plan"
	"github.com/barad1tos/noxctl/tests/bear/testutil"
)

func TestErrDriftDetectedSentinelDeclared(t *testing.T) {
	if plan.ErrDriftDetected == nil {
		t.Fatal("plan.ErrDriftDetected sentinel is nil")
	}
	if !errors.Is(plan.ErrDriftDetected, plan.ErrDriftDetected) {
		t.Error("errors.Is(plan.ErrDriftDetected, plan.ErrDriftDetected) = false; want true")
	}
	if !strings.Contains(plan.ErrDriftDetected.Error(), "drift") {
		t.Errorf("plan.ErrDriftDetected.Error() = %q; should mention 'drift'",
			plan.ErrDriftDetected.Error())
	}
}

func TestValidateOutputRejectsUnknownFormat(t *testing.T) {
	if err := plan.ValidateOutput("yaml"); err == nil {
		t.Fatal("ValidateOutput(yaml) expected error; got nil")
	}
	if err := plan.ValidateOutput("text"); err != nil {
		t.Errorf("ValidateOutput(text) unexpected error: %v", err)
	}
	if err := plan.ValidateOutput("json"); err != nil {
		t.Errorf("ValidateOutput(json) unexpected error: %v", err)
	}
}

func TestScopeDomainsRejectsUnknownTag(t *testing.T) {
	_, err := plan.ScopeDomains(nil, "unknown/tag")
	if err == nil {
		t.Fatal("ScopeDomains(nil, unknown) expected error; got nil")
	}
	if !strings.Contains(err.Error(), "unknown tag") {
		t.Errorf("error should mention 'unknown tag'; got %q", err.Error())
	}
}

// TestLoadDomains_NoArgsReturnsFullCatalog — `noxctl plan` без позиційного
// arg повертає всі домени з examples/personal.toml. Pin'ить core load path:
// pin migration is best-effort (logged warning, never blocks), TOML parse
// must succeed, slice must be non-empty.
func TestLoadDomains_NoArgsReturnsFullCatalog(t *testing.T) {
	tmp := t.TempDir()
	var stderr bytes.Buffer
	domains, err := plan.LoadDomains(
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
	domains, err := plan.LoadDomains(
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
	_, err := plan.LoadDomains(
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
