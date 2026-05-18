// Package verify_test — render JSON shape coverage via RenderForTest.
//
// User-scenario framing: each test pins a field a CI consumer parses
// with jq / unmarshal — `schema_version`, `checks[].status`,
// `summary.fail`, etc. Routes through `verify.RenderForTest` so a
// rename or signature change in the production renderer trips the
// test (the earlier shape tests used a private json.Encoder and
// therefore tested encoding/json — not the renderer).
package verify_test

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// TestRenderJSON_SchemaVersionPinned — CI-consumer contract: any
// scripted parser pinning `.schema_version == 1` MUST keep working
// across patches. Bumping the version is an opt-in breaking change.
func TestRenderJSON_SchemaVersionPinned(t *testing.T) {
	out := renderJSONBytes(t, buildSampleResult(verify.StatusPass))
	var decoded map[string]any
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got, ok := decoded["schema_version"].(float64)
	if !ok {
		t.Fatalf("schema_version field missing or wrong type; got %T", decoded["schema_version"])
	}
	if int(got) != 1 {
		t.Errorf("schema_version = %v, want 1 (bumping is a breaking change for CI consumers)", got)
	}
}

// TestRenderJSON_ChecksArrayShape — pins the fields a CI script picks
// out with `jq '.checks[]'`. Operators iterate this array to surface
// per-check status on dashboards / PR comments / Slack.
func TestRenderJSON_ChecksArrayShape(t *testing.T) {
	out := renderJSONBytes(t, buildSampleResult(verify.StatusFail))
	var decoded struct {
		Checks []struct {
			Name    string   `json:"name"`
			Status  string   `json:"status"`
			Message string   `json:"message"`
			Details []string `json:"details,omitempty"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Checks) != 1 {
		t.Fatalf("len(checks) = %d, want 1", len(decoded.Checks))
	}
	c := decoded.Checks[0]
	if c.Name == "" || c.Status == "" || c.Message == "" {
		t.Errorf("required check field empty; name=%q status=%q msg=%q", c.Name, c.Status, c.Message)
	}
}

// TestRenderJSON_SummaryCounters — pins the per-status aggregate that
// alerting rules key off (e.g., "alert when summary.fail > 0").
func TestRenderJSON_SummaryCounters(t *testing.T) {
	out := renderJSONBytes(t, buildAllFourStatusesResult())
	var decoded struct {
		Summary struct {
			Pass    int `json:"pass"`
			Fail    int `json:"fail"`
			Error   int `json:"error"`
			Skipped int `json:"skipped"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.Summary.Pass != 1 || decoded.Summary.Fail != 1 ||
		decoded.Summary.Error != 1 || decoded.Summary.Skipped != 1 {
		t.Errorf("summary counters = %+v, want all 1", decoded.Summary)
	}
}

// TestRenderJSON_StatusStringsAreStable — locks the four status-string
// literals that JSON consumers branch on. Renaming `"pass"` →
// `"passed"` would silently break every dashboard query in the wild.
func TestRenderJSON_StatusStringsAreStable(t *testing.T) {
	cases := []struct {
		status verify.Status
		want   string
	}{
		{verify.StatusPass, "pass"},
		{verify.StatusFail, "fail"},
		{verify.StatusSkipped, "skipped"},
		{verify.StatusError, "error"},
	}
	for _, c := range cases {
		t.Run(string(c.status), func(t *testing.T) {
			if string(c.status) != c.want {
				t.Errorf("status %v stringifies as %q, want %q (CI consumers branch on this literal)",
					c.status, string(c.status), c.want)
			}
		})
	}
}

// renderJSONBytes drives the production JSON renderer over a Result
// fixture and returns the emitted bytes. Routing through
// `RenderForTest` (not a private json.NewEncoder) means a regression
// in production rendering shows up here, not just in end-to-end tests.
func renderJSONBytes(t *testing.T, r *verify.Result) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := verify.RenderForTest(verify.Options{Output: "json", Stdout: &buf}, r); err != nil {
		t.Fatalf("RenderForTest: %v", err)
	}
	return buf.Bytes()
}

// buildSampleResult constructs a single-check Result for shape tests.
func buildSampleResult(status verify.Status) *verify.Result {
	r := &verify.Result{
		SchemaVersion: 1,
		StartedAt:     time.Now().UTC(),
		CompletedAt:   time.Now().UTC(),
		Checks: []verify.Check{
			{Name: "sample", Status: status, Message: "from-sample", Details: []string{"d1"}},
		},
	}
	for _, c := range r.Checks {
		switch c.Status {
		case verify.StatusPass:
			r.Summary.Pass++
		case verify.StatusFail:
			r.Summary.Fail++
		case verify.StatusSkipped:
			r.Summary.Skipped++
		case verify.StatusError:
			r.Summary.Error++
		}
	}
	return r
}
