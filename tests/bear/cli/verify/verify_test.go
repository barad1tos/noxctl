// Package verify_test — hermetic unit coverage for `noxctl verify`.
//
// The vault-bound checks (plan-parity, apply-idempotency) need a real
// bearcli + a live Bear database; those are exercised end-to-end by
// the operator-side `scripts/ship-gate.sh` invocation, not in CI.
// What we CAN test hermetically:
//
//   - `scanLogSinceStartup` — pure string parsing over an in-memory
//     reader; rewind-to-last-startup semantics, warn detection.
//   - `ValidateOutput` — flag whitelist.
//   - `Result.Summary` finalize math + verdict glyph mapping.
//   - JSON render schema stability (Result.SchemaVersion).
package verify_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// TestValidateOutput pins the supported -o whitelist. Adding a new
// output format must update this test in the same change.
func TestValidateOutput(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "Text", input: "text"},
		{name: "JSON", input: "json"},
		{name: "Empty", input: "", wantErr: true},
		{name: "Yaml", input: "yaml", wantErr: true},
		{name: "Uppercase", input: "TEXT", wantErr: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := verify.ValidateOutput(c.input)
			gotErr := err != nil
			if gotErr != c.wantErr {
				t.Errorf("ValidateOutput(%q) err = %v, want err? %v", c.input, err, c.wantErr)
			}
		})
	}
}

// TestJSONResultStable asserts that the JSON output carries the
// schema_version field. Scripted consumers should pin against this
// integer; a non-backward-compatible change to the result shape MUST
// bump the constant.
func TestJSONResultStable(t *testing.T) {
	r := verify.Result{
		SchemaVersion: 1,
		Checks: []verify.Check{
			{Name: "plan-parity", Status: verify.StatusPass, Message: "ok"},
		},
		Summary: verify.Summary{Pass: 1},
	}
	var buf bytes.Buffer
	if err := json.NewEncoder(&buf).Encode(r); err != nil {
		t.Fatalf("encode: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, `"schema_version": 1`) && !strings.Contains(out, `"schema_version":1`) {
		t.Errorf("schema_version field missing or unexpected; got:\n%s", out)
	}
	if !strings.Contains(out, `"plan-parity"`) {
		t.Errorf("check name field missing; got:\n%s", out)
	}
}
