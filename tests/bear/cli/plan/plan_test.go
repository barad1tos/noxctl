package plan_test

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/cli/plan"
	"github.com/barad1tos/noxctl/bear/engine"
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
	_, err := plan.ScopeDomains(nil, "unknown/tag", "noxctl.toml")
	if err == nil {
		t.Fatal("ScopeDomains(nil, unknown) expected error; got nil")
	}
	if !strings.Contains(err.Error(), "unknown tag") {
		t.Errorf("error should mention 'unknown tag'; got %q", err.Error())
	}
}

// TestScopeDomainsSourceLabel locks WR-03: the "unknown tag" error
// message names the catalog source the user requested via
// --config-source, not a hardcoded "noxctl.toml" string. The CLI
// boundary translation is ConfigSourceLabel; this test exercises it
// end-to-end via ScopeDomains for each ConfigSource value.
func TestScopeDomainsSourceLabel(t *testing.T) {
	fakeDomain := &bear.Domain{Tag: "library/poetry"}
	domains := []*bear.Domain{fakeDomain}

	cases := []struct {
		name       string
		src        engine.ConfigSource
		wantPhrase string
	}{
		{"toml-default", engine.ConfigSourceTOML, "noxctl.toml"},
		{"hardcoded-bridge", engine.ConfigSourceHardcoded, "hardcoded registry"},
		{"both-parity", engine.ConfigSourceBoth, "any catalog"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			label := plan.ConfigSourceLabel(tc.src)
			_, err := plan.ScopeDomains(domains, "nonexistent/tag", label)
			if err == nil {
				t.Fatal("expected error for unknown tag")
			}
			if !strings.Contains(err.Error(), tc.wantPhrase) {
				t.Errorf("err %q must contain %q for src=%v",
					err.Error(), tc.wantPhrase, tc.src)
			}
		})
	}
}

func TestLoadDomainsByConfigSourceBothLoadsBothSlices(t *testing.T) {
	var stderr bytes.Buffer
	toml, hard, err := plan.LoadDomainsByConfigSource(
		engine.ConfigSourceBoth, nil,
		"../../../../examples/roman.toml",
		"/dev/null/legacy-pins.json", // bogus — MigratePins ignores missing files
		"/dev/null/target-pins.json",
		&stderr,
	)
	if err != nil {
		t.Fatalf("LoadDomainsByConfigSource: %v", err)
	}
	if len(toml) == 0 {
		t.Error("Both mode: toml slice is empty; want >0 from examples/roman.toml")
	}
	if len(hard) != 31 {
		t.Errorf("Both mode: hard slice len = %d, want 31 (registry.All)", len(hard))
	}
}

func TestLoadDomainsByConfigSourceBothScopesArgs(t *testing.T) {
	var stderr bytes.Buffer
	toml, hard, err := plan.LoadDomainsByConfigSource(
		engine.ConfigSourceBoth, []string{"library/poetry"},
		"../../../../examples/roman.toml",
		"/dev/null/legacy-pins.json",
		"/dev/null/target-pins.json",
		&stderr,
	)
	if err != nil {
		t.Fatalf("LoadDomainsByConfigSource: %v", err)
	}
	if len(toml) != 1 || toml[0].Tag != "library/poetry" {
		t.Errorf("toml after scoping: got len=%d, first tag=%q; want 1 entry tag=library/poetry",
			len(toml), tagOrEmpty(toml))
	}
	if len(hard) != 1 || hard[0].Tag != "library/poetry" {
		t.Errorf("hard after scoping: got len=%d, first tag=%q; want 1 entry tag=library/poetry",
			len(hard), tagOrEmpty(hard))
	}
}

// tagOrEmpty returns the first domain's Tag for diagnostic messages.
// Defensive against empty slices so the surrounding Errorf never
// dereferences a missing index when the test fixture itself misbehaves.
func tagOrEmpty(ds []*bear.Domain) string {
	if len(ds) == 0 {
		return ""
	}
	return ds[0].Tag
}

func TestPickPrimary(t *testing.T) {
	tomlDomains := []*bear.Domain{{Tag: "toml-only"}}
	hardDomains := []*bear.Domain{{Tag: "hard-only"}}
	cases := []struct {
		name    string
		src     engine.ConfigSource
		want    string
		wantNil bool
	}{
		{"toml-picks-toml", engine.ConfigSourceTOML, "toml-only", false},
		{"hardcoded-picks-hard", engine.ConfigSourceHardcoded, "hard-only", false},
		{"both-picks-toml", engine.ConfigSourceBoth, "toml-only", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := plan.PickPrimary(tc.src, tomlDomains, hardDomains)
			if len(got) != 1 || got[0].Tag != tc.want {
				t.Errorf("PickPrimary(%v) = %v, want [{Tag:%s}]", tc.src, got, tc.want)
			}
		})
	}
}

func TestPickRef(t *testing.T) {
	hardDomains := []*bear.Domain{{Tag: "hard-only"}}
	cases := []struct {
		name    string
		src     engine.ConfigSource
		wantNil bool
	}{
		{"toml-ref-nil", engine.ConfigSourceTOML, true},
		{"hardcoded-ref-nil", engine.ConfigSourceHardcoded, true},
		{"both-ref-hard", engine.ConfigSourceBoth, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := plan.PickRef(tc.src, hardDomains)
			if tc.wantNil && got != nil {
				t.Errorf("PickRef(%v) = %v, want nil", tc.src, got)
			}
			if !tc.wantNil && (len(got) != 1 || got[0].Tag != "hard-only") {
				t.Errorf("PickRef(%v) = %v, want hardDomains", tc.src, got)
			}
		})
	}
}

func TestHasParityMismatch(t *testing.T) {
	cases := []struct {
		name string
		r    *engine.PlanResult
		want bool
	}{
		{"nil result", nil, false},
		{"empty domains", &engine.PlanResult{}, false},
		{"only-clean", &engine.PlanResult{
			Domains: []engine.DomainPlan{{Tag: "a", Status: engine.StatusClean}},
		}, false},
		{"has-parity-mismatch", &engine.PlanResult{
			Domains: []engine.DomainPlan{
				{Tag: "a", Status: engine.StatusClean},
				{Tag: "b", Status: engine.StatusParityMismatch},
			},
		}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := plan.HasParityMismatch(tc.r); got != tc.want {
				t.Errorf("HasParityMismatch = %v, want %v", got, tc.want)
			}
		})
	}
}
