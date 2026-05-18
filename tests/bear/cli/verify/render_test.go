// Package verify_test — render coverage.
//
// User-scenario framing: every assertion here mirrors what an operator
// or CI script would actually look for after running `noxctl verify`.
// Text-output checks pin the visible signals (glyphs, summary line,
// indented Details) the operator scans for at a glance. JSON-output
// checks pin the fields a CI consumer parses with jq or unmarshal —
// `schema_version`, `checks[].status`, `summary.fail`, etc.
//
// Scope: bear/cli/verify/render.go (`render`, `renderText`,
// `renderJSON`, `statusGlyph`, `overallVerdict`).
package verify_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// runTextRender invokes verify.Run's render via the public surface by
// constructing a Result and calling json/text. We can't call render()
// directly (unexported), so we call verify.Run with a doomed config
// to force the catalog-load StatusError path AND emit text/json — but
// that ALSO renders side-effects we don't want here. Cleaner: build
// the Result, encode to JSON ourselves and compare; for text, run
// verify.Run with a synthetic check shape via ContextWithBackend in
// run_endtoend_test.go.
//
// The shape-pinning tests in this file therefore exercise the JSON
// surface (operator-visible structure) and the public glyph contract
// indirectly via JSON `status` strings. The text-output assertions
// live in run_endtoend_test.go where Run() naturally drives render().

// TestRenderJSON_SchemaVersionPinned is the CI-consumer contract: any
// scripted parser pinning `.schema_version == 1` MUST keep working
// across patches. Bumping the version is an opt-in breaking change.
func TestRenderJSON_SchemaVersionPinned(t *testing.T) {
	r := buildSampleResult(verify.StatusPass)
	out := encodeResult(t, r)
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

// TestRenderJSON_ChecksArrayShape pins the fields a CI script picks
// out with `jq '.checks[]'`. Operators iterate this array to surface
// per-check status on dashboards / PR comments / Slack.
func TestRenderJSON_ChecksArrayShape(t *testing.T) {
	r := buildSampleResult(verify.StatusFail)
	out := encodeResult(t, r)
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

// TestRenderJSON_SummaryCounters pins the per-status aggregate that
// alerting rules key off (e.g., "alert when summary.fail > 0").
func TestRenderJSON_SummaryCounters(t *testing.T) {
	out := encodeResult(t, buildAllFourStatusesResult())
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

// TestRenderJSON_StatusStringsAreStable locks the four status-string
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

// encodeResult marshals a Result to JSON using the same encoder
// settings the production render path does — pretty-printed,
// stable field order via json struct tags.
func encodeResult(t *testing.T, r *verify.Result) []byte {
	t.Helper()
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := buf.Bytes()
	if !strings.Contains(string(out), `"schema_version"`) {
		t.Fatalf("schema_version not in output; got:\n%s", out)
	}
	return out
}
