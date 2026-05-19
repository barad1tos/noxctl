// Package engine plan_result — JSON-stable schema for `noxctl plan`.
//
// Mirrors bear/engine/apply_result.go in style: types live here, no
// behavior; behavior lives in plan.go and diff.go. The JSON wire shape
// is locked at SchemaVersion=1: integer schema versioning, full-shape
// always emitted — empty-drift marshals `[]`, never `null`.
package engine

import "time"

// DiffKind enumerates the change classes plan emits. Wire values are
// lowercase ASCII so machine consumers (jq, downstream tooling) can
// switch over them without case folding. Locked by TestDiffKindWireValues.
type DiffKind string

const (
	// DiffReplace — existing note will be overwritten on apply.
	DiffReplace DiffKind = "replace"
	// DiffCreate — note does not exist yet; apply will create it.
	DiffCreate DiffKind = "create"
	// DiffNoop — captured for completeness; transient/error paths use this
	// so the diff can carry a Summary without claiming a real change.
	DiffNoop DiffKind = "noop"
)

// Diff captures a single per-note change preview. Detail is populated
// only when the caller passes Verbose=true through PlanOpts → RenderText
// — the JSON path always emits the field as either an array or omits it
// (omitempty) so empty-drift output stays compact.
type Diff struct {
	Kind    DiffKind `json:"kind"`
	Target  string   `json:"target"`           // "master" | "hub" | "atom"
	Title   string   `json:"title"`            // bear note title affected
	Summary string   `json:"summary"`          // single-line human summary
	Detail  []string `json:"detail,omitempty"` // populated only when verbose
}

// Wire values for DomainPlan.Status. Stable lowercase ASCII so jq
// and downstream tooling can switch on them without case folding;
// changes to these literals are a JSON-schema bump.
const (
	StatusClean = "clean" // no drift
	StatusDrift = "drift" // drift vs Bear state
	StatusError = "error" // per-domain failure
)

// DomainPlan groups one domain's per-note changes plus a status flag.
// Changes is initialized to make([]Diff, 0) at construction time so the
// JSON shape is `"changes": []` rather than `"changes": null` for clean
// domains.
type DomainPlan struct {
	Tag     string `json:"tag"`
	Status  string `json:"status"`  // StatusClean | StatusDrift | StatusError
	Changes []Diff `json:"changes"` // make([]Diff, 0) — never null
}

// UntrackedFamily is one row of the residue report. NoteCount is the
// count of notes carrying the exact tag string Tag (not the top-level
// segment) — matches the granularity downstream tooling reports as
// "claude/sessions: 47 notes".
type UntrackedFamily struct {
	Tag       string `json:"tag"`
	NoteCount int    `json:"note_count"`
}

// UntrackedReport is the residue section of plan output. The result
// always carries a zero-value report (TagFamilies as an initialized
// empty slice, TotalNotes=0) when bear.ScanUntracked finds nothing.
type UntrackedReport struct {
	TagFamilies []UntrackedFamily `json:"tag_families"` // make(..., 0) — never null
	TotalNotes  int               `json:"total_notes"`
}

// PlanError records one per-domain or corpus-level (Tag="") failure.
// Apply-style log-and-continue: per-domain errors collect here while
// the iteration walks every remaining domain.
type PlanError struct {
	Tag string `json:"tag"` // "" for corpus-level (residue) errors
	Msg string `json:"msg"`
}

// PlanSummary is the footer-counts block. RenderText reads from it for
// the "Plan: N domains drift, M changes, K errors" closing line; JSON
// consumers query individual fields directly.
type PlanSummary struct {
	DomainsTotal      int `json:"domains_total"`
	DomainsClean      int `json:"domains_clean"`
	DomainsDrift      int `json:"domains_drift"`
	DomainsError      int `json:"domains_error"`
	ChangesTotal      int `json:"changes_total"`
	UntrackedFamilies int `json:"untracked_families"`
}

// PlanResult is the complete return payload of engine.Plan. Stable
// across patch versions; SchemaVersion bumps only on breaking changes
// (additive evolution allowed inside SchemaVersion=1).
//
// Slice fields are initialized via make(..., 0) at construction time in
// Plan so JSON marshaling produces `[]` rather than `null` — the most
// common JSON-stability bug in Go.
type PlanResult struct {
	SchemaVersion int             `json:"schema_version"` // = 1
	StartedAt     time.Time       `json:"started_at"`
	CompletedAt   time.Time       `json:"completed_at"`
	Domains       []DomainPlan    `json:"domains"` // make(..., 0)
	Untracked     UntrackedReport `json:"untracked"`
	Errors        []PlanError     `json:"errors"` // make(..., 0)
	Interrupted   bool            `json:"interrupted"`
	Summary       PlanSummary     `json:"summary"`
}

// HasDrift reports whether the plan found any drift across all domains.
// Nil-safe: a nil receiver returns false (mirrors bear/pins.go nil-safe
// methods). Callers (cmd/noxctl/plan.go) use this to map to exit
// code 2.
func (r *PlanResult) HasDrift() bool {
	if r == nil {
		return false
	}
	return r.Summary.DomainsDrift > 0
}
