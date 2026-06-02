// Package diag_test — external coverage of the diag result model.
//
// User-scenario framing: diag is the shared diagnostic model consumed
// by both `verify` (vault checks) and `doctor` (environment checks).
// These tests pin the model's three load-bearing contracts: Summarize
// counts every status (including the new warn arm), ValidateOutput
// gates the -o flag, and the new Group/Remediation fields stay
// json:",omitempty" so verify's existing JSON stays byte-identical.
package diag_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/diag"
)

// TestSummarize_CountsEveryStatus — one check per status (plus a second
// pass) must roll up into the matching Summary counter, including the
// NEW warn arm that verify never emitted.
func TestSummarize_CountsEveryStatus(t *testing.T) {
	checks := []diag.Check{
		{Name: "a", Status: diag.StatusPass},
		{Name: "b", Status: diag.StatusPass},
		{Name: "c", Status: diag.StatusWarn},
		{Name: "d", Status: diag.StatusFail},
		{Name: "e", Status: diag.StatusSkipped},
		{Name: "f", Status: diag.StatusError},
	}
	got := diag.Summarize(checks)
	want := diag.Summary{Pass: 2, Warn: 1, Fail: 1, Skipped: 1, Error: 1}
	if got != want {
		t.Errorf("Summarize() = %+v, want %+v", got, want)
	}
}

// TestSummarize_UnknownStatusRoutesToError — a Check carrying a status
// outside the known five must never vanish from the rollup. Summarize
// routes it to the Error bucket (fail-loud) so total==len(Checks) holds
// even for a malformed Check.
func TestSummarize_UnknownStatusRoutesToError(t *testing.T) {
	checks := []diag.Check{
		{Name: "a", Status: diag.StatusPass},
		{Name: "b", Status: diag.Status("bogus")},
	}
	got := diag.Summarize(checks)
	if got.Error != 1 {
		t.Errorf("unknown status not routed to Error: got %+v", got)
	}
	total := got.Pass + got.Warn + got.Fail + got.Skipped + got.Error
	if total != len(checks) {
		t.Errorf("summary total %d != check count %d (a check vanished)", total, len(checks))
	}
}

// TestSummarize_EmptyIsZero — no checks yields a zero Summary, the
// resting state doctor reports on a clean environment with nothing run.
func TestSummarize_EmptyIsZero(t *testing.T) {
	got := diag.Summarize(nil)
	if (got != diag.Summary{}) {
		t.Errorf("Summarize(nil) = %+v, want zero Summary", got)
	}
}

// TestStatusStringsAreStable — locks the five status-string literals
// JSON consumers branch on. The four legacy literals match verify's;
// warn is the new fifth.
func TestStatusStringsAreStable(t *testing.T) {
	cases := []struct {
		status diag.Status
		want   string
	}{
		{diag.StatusPass, "pass"},
		{diag.StatusWarn, "warn"},
		{diag.StatusFail, "fail"},
		{diag.StatusSkipped, "skipped"},
		{diag.StatusError, "error"},
	}
	for _, c := range cases {
		t.Run(c.want, func(t *testing.T) {
			if string(c.status) != c.want {
				t.Errorf("status %v stringifies as %q, want %q", c.status, string(c.status), c.want)
			}
		})
	}
}

// TestValidateOutput_AcceptsTextAndJSON — the two legal -o values pass.
func TestValidateOutput_AcceptsTextAndJSON(t *testing.T) {
	for _, output := range []string{"text", "json"} {
		if err := diag.ValidateOutput(output); err != nil {
			t.Errorf("ValidateOutput(%q) = %v, want nil", output, err)
		}
	}
}

// TestValidateOutput_RejectsOther — anything else is rejected with the
// identical message verify used (CI scripts may match on it).
func TestValidateOutput_RejectsOther(t *testing.T) {
	err := diag.ValidateOutput("yaml")
	if err == nil {
		t.Fatalf("ValidateOutput(\"yaml\") = nil, want error")
	}
	want := `invalid -o value "yaml" (expected text|json)`
	if err.Error() != want {
		t.Errorf("ValidateOutput error = %q, want %q", err.Error(), want)
	}
}

// TestSchemaVersionIsOne — the exported schema constant stays 1; a bump
// is an opt-in breaking change for scripted consumers.
func TestSchemaVersionIsOne(t *testing.T) {
	if diag.SchemaVersion != 1 {
		t.Errorf("diag.SchemaVersion = %d, want 1", diag.SchemaVersion)
	}
}

// TestCheck_GroupRemediationOmitempty — a Check that leaves Group and
// Remediation zero MUST serialize WITHOUT those keys, so verify's old
// Check JSON stays byte-identical. This is the byte-stability invariant.
func TestCheck_GroupRemediationOmitempty(t *testing.T) {
	out, err := json.Marshal(diag.Check{Name: "x", Status: diag.StatusPass, Message: "ok"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, forbidden := range []string{"group", "remediation"} {
		if strings.Contains(got, forbidden) {
			t.Errorf("zero-Group/Remediation Check emitted %q key; got %s", forbidden, got)
		}
	}
}

// TestCheck_GroupRemediationEmittedWhenSet — when set, the new fields
// appear so doctor's grouped, remediated checks round-trip through JSON.
func TestCheck_GroupRemediationEmittedWhenSet(t *testing.T) {
	out, err := json.Marshal(diag.Check{
		Name:        "system.bear-app",
		Status:      diag.StatusError,
		Message:     "missing",
		Group:       "System",
		Remediation: "install Bear",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, want := range []string{`"group":"System"`, `"remediation":"install Bear"`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in JSON; got %s", want, got)
		}
	}
}

// TestSummary_WarnFieldIsEmittedWhenNonZero — the Summary carries a
// new warn counter, but only non-zero warnings should serialize so
// verify's zero-warn JSON remains byte-stable.
func TestSummary_WarnFieldIsEmittedWhenNonZero(t *testing.T) {
	out, err := json.Marshal(diag.Summary{Pass: 1, Warn: 2, Fail: 0, Skipped: 0, Error: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := string(out)
	for _, want := range []string{`"pass":1`, `"warn":2`, `"fail":0`, `"skipped":0`, `"error":0`} {
		if !strings.Contains(got, want) {
			t.Errorf("expected %q in summary JSON; got %s", want, got)
		}
	}
}

// TestSummary_WarnZeroOmitsKey pins verify JSON compatibility: verify
// never emits warn-status checks, so the shared Summary must not add a
// new "warn":0 key to verify -o json.
func TestSummary_WarnZeroOmitsKey(t *testing.T) {
	out, err := json.Marshal(diag.Summary{Pass: 1, Fail: 0, Skipped: 0, Error: 0})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(out), `"warn"`) {
		t.Errorf("zero warn summary emitted warn key; got %s", out)
	}
}
