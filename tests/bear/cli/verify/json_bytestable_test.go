// Package verify_test — byte-stability snapshot for verify's JSON.
//
// This file is the machine-checked form of the project invariant that
// `noxctl verify -o json` output must stay byte-identical after the
// shared result model was lifted into the diag leaf package. The
// snapshot renders a FIXED verify.Result (timestamps zeroed) through the
// production renderer (verify.RenderForTest -> diag.RenderJSON) and
// compares the emitted bytes against a frozen reference literal.
//
// If a future change makes verify emit a "group" or "remediation" key
// (both omitempty fields verify deliberately leaves zero), renames a
// summary counter, or otherwise reshapes the JSON, the frozen-byte
// assertion below FAILS the build — which is the intended, correct
// behavior: verify's JSON shape is a contract, not an implementation
// detail.
package verify_test

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// frozenVerifyJSON is the locked, pre-refactor verify JSON shape for the
// canonical four-status fixture below, with started_at/completed_at
// zeroed so the snapshot is deterministic. The trailing newline is the
// one json.Encoder.Encode always appends. Regenerating this literal is a
// deliberate act: it means verify's output contract changed.
const frozenVerifyJSON = `{
  "schema_version": 1,
  "started_at": "0001-01-01T00:00:00Z",
  "completed_at": "0001-01-01T00:00:00Z",
  "checks": [
    {
      "name": "plan-parity",
      "status": "pass",
      "message": "no drift"
    },
    {
      "name": "daemon-log",
      "status": "fail",
      "message": "2 warning(s)",
      "details": [
        "LOOP detected at 12:04",
        "ERROR: render failed"
      ]
    },
    {
      "name": "apply-idempotency",
      "status": "skipped",
      "message": "opt-in via --with-apply"
    },
    {
      "name": "catalog-load",
      "status": "error",
      "message": "config missing"
    }
  ],
  "summary": {
    "pass": 1,
    "fail": 1,
    "skipped": 1,
    "error": 1
  }
}
`

// buildByteStableResult is the canonical fixture the snapshot freezes:
// one check per legacy status (pass/fail/skipped/error), the fail check
// carrying a Details array (so the optional "details" key is exercised),
// the matching four-counter Summary, and zeroed StartedAt/CompletedAt so
// the rendered bytes are deterministic. It sets neither Group nor
// Remediation — verify never does — so those omitempty keys must be
// absent from the output.
func buildByteStableResult() *verify.Result {
	return &verify.Result{
		SchemaVersion: 1,
		// StartedAt / CompletedAt left as the zero time.Time on purpose.
		Checks: []verify.Check{
			{Name: "plan-parity", Status: verify.StatusPass, Message: "no drift"},
			{
				Name:    "daemon-log",
				Status:  verify.StatusFail,
				Message: "2 warning(s)",
				Details: []string{"LOOP detected at 12:04", "ERROR: render failed"},
			},
			{Name: "apply-idempotency", Status: verify.StatusSkipped, Message: "opt-in via --with-apply"},
			{Name: "catalog-load", Status: verify.StatusError, Message: "config missing"},
		},
		Summary: verify.Summary{Pass: 1, Fail: 1, Skipped: 1, Error: 1},
	}
}

// renderByteStableJSON drives the production JSON renderer over the
// canonical fixture and returns the emitted bytes.
func renderByteStableJSON(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	opts := verify.Options{Output: "json", Stdout: &buf}
	if err := verify.RenderForTest(opts, buildByteStableResult()); err != nil {
		t.Fatalf("RenderForTest: %v", err)
	}
	return buf.Bytes()
}

// TestVerifyJSONByteStable is the strongest form of the byte-stability
// invariant: the timestamp-zeroed verify JSON must equal the frozen
// reference literal byte-for-byte. A drift in field order, indentation,
// key names, or an accidental group/remediation emission breaks this.
func TestVerifyJSONByteStable(t *testing.T) {
	got := renderByteStableJSON(t)
	if string(got) != frozenVerifyJSON {
		t.Errorf("verify JSON drifted from the frozen reference\n--- got ---\n%s\n--- want ---\n%s",
			got, frozenVerifyJSON)
	}
}

// TestVerifyJSONHasNoGroupOrRemediationKeys pins the omitempty contract
// at the per-check object level: because verify never sets Group or
// Remediation, neither key may appear in any check object. This is the
// targeted assertion behind the byte snapshot — a future change emitting
// either key from verify would (correctly) trip both this test and the
// frozen-byte test.
func TestVerifyJSONHasNoGroupOrRemediationKeys(t *testing.T) {
	var decoded struct {
		Checks []map[string]json.RawMessage `json:"checks"`
	}
	if err := json.Unmarshal(renderByteStableJSON(t), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(decoded.Checks) != 4 {
		t.Fatalf("len(checks) = %d, want 4", len(decoded.Checks))
	}
	for i, check := range decoded.Checks {
		if _, ok := check["group"]; ok {
			t.Errorf("checks[%d] carries a %q key; verify must never emit it", i, "group")
		}
		if _, ok := check["remediation"]; ok {
			t.Errorf("checks[%d] carries a %q key; verify must never emit it", i, "remediation")
		}
	}
}

// TestVerifyJSONSchemaAndLegacySummaryCounters pins schema_version==1
// and asserts the four legacy summary counters BY NAME (pass/fail/
// skipped/error). It also rejects a zero-valued summary.warn key:
// verify never emitted warn before diag, so adding "warn":0 would not
// be byte-stable.
func TestVerifyJSONSchemaAndLegacySummaryCounters(t *testing.T) {
	var decoded struct {
		SchemaVersion int `json:"schema_version"`
		Summary       struct {
			Pass    int `json:"pass"`
			Fail    int `json:"fail"`
			Skipped int `json:"skipped"`
			Error   int `json:"error"`
		} `json:"summary"`
	}
	if err := json.Unmarshal(renderByteStableJSON(t), &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded.SchemaVersion != 1 {
		t.Errorf("schema_version = %d, want 1", decoded.SchemaVersion)
	}
	if decoded.Summary.Pass != 1 || decoded.Summary.Fail != 1 ||
		decoded.Summary.Skipped != 1 || decoded.Summary.Error != 1 {
		t.Errorf("legacy summary counters = %+v, want each == 1", decoded.Summary)
	}

	var raw struct {
		Summary map[string]json.RawMessage `json:"summary"`
	}
	if err := json.Unmarshal(renderByteStableJSON(t), &raw); err != nil {
		t.Fatalf("unmarshal raw summary: %v", err)
	}
	if _, ok := raw.Summary["warn"]; ok {
		t.Errorf("verify JSON emitted summary.warn despite zero warn count: %v", raw.Summary)
	}
}
