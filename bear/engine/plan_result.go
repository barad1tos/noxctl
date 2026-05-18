// Package engine plan_result — JSON-stable schema for `noxctl plan`.
//
// deliverable. Mirrors bear/engine/apply_result.go in
// style: types live here, no behavior; behavior lives in plan.go and
// diff.go. The JSON wire shape is locked at SchemaVersion=1 per
// CONTEXT D-05 (integer schema versioning, full-shape always emitted —
// empty-drift marshals `[]`, never `null`, per RESEARCH Pitfall 6).
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

// status constants and the D-03 parity-mismatch
// addition. Pre-Phase-4, the three legacy values were inline string
// literals across plan.go / diff.go / external tests; keeps
// those literals untouched (touching the existing strings would ripple
// into RenderText / RenderJSON / external test fixtures) and only
// surfaces the new constant for callers of planParity.
const (
	StatusClean          = "clean"           // no drift, no parity mismatch
	StatusDrift          = "drift"           // single-path drift vs Bear state
	StatusError          = "error"           // per-domain failure
	StatusParityMismatch = "parity-mismatch" //  D-03 — only when ConfigSource=Both
)

// DomainPlan groups one domain's per-note changes plus a status flag.
// Changes is initialized to make([]Diff, 0) at construction time so the
// JSON shape is `"changes": []` rather than `"changes": null` for clean
// domains (RESEARCH Pitfall 6).
type DomainPlan struct {
	Tag     string `json:"tag"`
	Status  string `json:"status"`  // StatusClean | StatusDrift | StatusError | StatusParityMismatch
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

// UntrackedReport is the residue section of plan output (CONTEXT D-02).
// emits a zero-value report (TagFamilies as an initialized
// empty slice, TotalNotes=0) — wires bear.ScanUntracked to
// populate it.
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
	DomainsTotal int `json:"domains_total"`
	DomainsClean int `json:"domains_clean"`
	DomainsDrift int `json:"domains_drift"`
	DomainsError int `json:"domains_error"`
	// DomainsParityMismatch — D-03 addition. Counts DomainPlan
	// rows whose Status == StatusParityMismatch (only emitted when the
	// caller invokes engine.Plan with ConfigSource=ConfigSourceBoth).
	// `omitempty` keeps the legacy single-path JSON output byte-equal
	// to — clean+drift+error PlanResults marshal without this
	// field (Pitfall 6 backwards compatibility).
	DomainsParityMismatch int `json:"domains_parity_mismatch,omitempty"`
	ChangesTotal          int `json:"changes_total"`
	UntrackedFamilies     int `json:"untracked_families"`
}

// PlanResult is the complete return payload of engine.Plan. Stable
// across patch versions per PLAN-05; SchemaVersion bumps only on
// breaking changes (additive evolution allowed inside SchemaVersion=1).
//
// Slice fields are initialized via make(..., 0) at construction time in
// Plan so JSON marshaling produces `[]` rather than `null` (RESEARCH
// Pitfall 6 — the most common JSON-stability bug in Go).
type PlanResult struct {
	SchemaVersion int             `json:"schema_version"` // = 1 in
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
// methods). Callers (cmd/noxctl/plan.go in) use this to map
// to exit code 2.
func (r *PlanResult) HasDrift() bool {
	if r == nil {
		return false
	}
	return r.Summary.DomainsDrift > 0
}
