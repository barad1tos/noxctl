// Package diag owns the shared diagnostic result model and renderer
// consumed by both `noxctl verify` (vault checks) and `noxctl doctor`
// (environment checks). It is a pure leaf: stdlib only, no engine,
// config, or bearcli imports, so either command can depend on it
// without pulling in the other's machinery.
//
// The model is lifted from the original verify result types and
// extended with three fields verify never used: a StatusWarn severity,
// a per-check Group, and a per-check Remediation. Group and Remediation
// are json:",omitempty"; because verify leaves them zero, verify's JSON
// stays byte-identical after it is refactored onto diag.
package diag

import (
	"fmt"
	"time"
)

// Status is the per-check verdict. JSON-stable string values.
type Status string

const (
	// StatusPass — check ran and the contract holds.
	StatusPass Status = "pass"
	// StatusWarn — check ran and surfaced a non-blocking advisory
	// (e.g. daemon not installed, first run, stale state). NEW
	// relative to verify, which never emits it; doctor-only severity
	// that maps to a non-failing exit code.
	StatusWarn Status = "warn"
	// StatusFail — check ran and the contract is broken (e.g. drift
	// detected, log has warnings, idempotency violated).
	StatusFail Status = "fail"
	// StatusSkipped — check intentionally not run.
	StatusSkipped Status = "skipped"
	// StatusError — check could not run (e.g. bearcli unreachable,
	// config file missing). Distinct from FAIL — signals "can't make
	// a verdict", not "verdict is no".
	StatusError Status = "error"
)

// Check is one named diagnostic result.
//
// Group and Remediation are NEW relative to verify and MUST stay
// json:",omitempty": a Check that leaves them zero serializes EXACTLY
// as verify's old Check did, which is what keeps verify's JSON
// byte-stable after the refactor onto diag.
type Check struct {
	Name        string   `json:"name"`
	Status      Status   `json:"status"`
	Message     string   `json:"message"`
	Details     []string `json:"details,omitempty"`
	Group       string   `json:"group,omitempty"`
	Remediation string   `json:"remediation,omitempty"`
}

// Summary aggregates per-status counts for the JSON output. Warn is
// NEW; the four legacy counters keep their always-emit shape so a
// scripted consumer decoding verify's summary into a struct is
// unaffected by the additive warn:0 field.
type Summary struct {
	Pass    int `json:"pass"`
	Warn    int `json:"warn"`
	Fail    int `json:"fail"`
	Skipped int `json:"skipped"`
	Error   int `json:"error"`
}

// Result is the full diagnostic output — every check plus the summary.
type Result struct {
	SchemaVersion int       `json:"schema_version"`
	StartedAt     time.Time `json:"started_at"`
	CompletedAt   time.Time `json:"completed_at"`
	Checks        []Check   `json:"checks"`
	Summary       Summary   `json:"summary"`
}

// SchemaVersion locks the JSON output schema. Bumping it signals an
// incompatible output change to scripted consumers. Exported so both
// verify and doctor seed Result.SchemaVersion from one source.
const SchemaVersion = 1

// Summarize rolls a slice of checks into per-status counts. Owns the
// status switch so no consumer re-implements counting; the StatusWarn
// arm is the only addition over verify's original loop.
func Summarize(checks []Check) Summary {
	var summary Summary
	for _, check := range checks {
		switch check.Status {
		case StatusPass:
			summary.Pass++
		case StatusWarn:
			summary.Warn++
		case StatusFail:
			summary.Fail++
		case StatusSkipped:
			summary.Skipped++
		case StatusError:
			summary.Error++
		default:
			// An unrecognized status (typo'd literal, a producer using a
			// constant outside the known five) must never vanish from the
			// rollup — that would silently break the total==len(Checks)
			// invariant consumers rely on. Route it to Error (fail-loud)
			// so a malformed Check surfaces instead of disappearing.
			// verify never emits a status outside the four it uses, so this
			// arm is unreachable on the verify path and its output stays
			// byte-identical.
			summary.Error++
		}
	}
	return summary
}

// ValidateOutput rejects values other than "text" and "json". The
// message is identical to verify's so any caller or test matching on
// it keeps working.
func ValidateOutput(output string) error {
	if output != "text" && output != "json" {
		return fmt.Errorf("invalid -o value %q (expected text|json)", output)
	}
	return nil
}
