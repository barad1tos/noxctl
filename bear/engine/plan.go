// Package engine plan — read-only "what would Apply do" preview.
//
// Sibling of engine.Apply, explicitly NOT a dryRun flag
// (CONTEXT D-05 + RESEARCH Pattern 1: pre-pass set is structurally
// different; threading dryRun through every pre-pass would not match
// plan's actual behavior — plan does not invoke foreign-tag escape,
// auto-tag, cross-domain, or time-promotion).
//
// Layering: stdlib + bear (read-only). engine.Plan never imports
// bear/config and never calls overwriteWithRetry / state.Save /
// AcquireApply (PLAN-03 / PLAN-06 hard contract — verified by grep
// gate in 03-VALIDATION.md).
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// ConfigSource selects which domain catalog `noxctl plan` reads.
// ConfigSourceTOML is the post-Phase-4 default and the only value users
// will see after the bridge window closes; the other two are explicit
// opt-ins for the bridge window (D-01, D-04).
//
// The zero value (== ConfigSourceTOML) preserves Phase-03 behavior for
// every existing caller — engine.Plan without an explicit ConfigSource
// dispatches into planSinglePath verbatim.
type ConfigSource int

const (
	// ConfigSourceTOML — default. Reads opts.Domains as the TOML-loaded
	// catalog and runs the Phase-03 single-path drift compare.
	ConfigSourceTOML ConfigSource = iota
	// ConfigSourceHardcoded — explicit opt-in. Reads opts.Domains as the
	// hardcoded `registry.All` slice and runs the same single-path
	// drift compare. Bridge-window only; post-Phase-4 the CLI rejects
	// this value with a friendly "removed" message.
	ConfigSourceHardcoded
	// ConfigSourceBoth — explicit opt-in. Pairs opts.Domains (TOML)
	// against opts.HardcodedRef (hardcoded) by Tag and renders both
	// halves in-memory; mismatches surface as Status=StatusParityMismatch.
	// Bridge-window only.
	ConfigSourceBoth
)

// String makes ConfigSource log/marshal friendly. Unknown int values
// surface as "ConfigSource(N)" — same shape as Stringer-generated enums.
func (c ConfigSource) String() string {
	switch c {
	case ConfigSourceTOML:
		return "toml"
	case ConfigSourceHardcoded:
		return "hardcoded"
	case ConfigSourceBoth:
		return "both"
	}
	return fmt.Sprintf("ConfigSource(%d)", int(c))
}

// ParseConfigSource validates a --config-source flag value. Mirrors
// ParseColorMode (bear/engine/diff.go) — returns a wrapped error with
// the rejected value in the message. Empty string maps to TOML, the
// post-Phase-4 default. NEVER silently defaults on unknown values
// (RESEARCH Pitfall 8).
func ParseConfigSource(s string) (ConfigSource, error) {
	switch s {
	case "", "toml":
		return ConfigSourceTOML, nil
	case "hardcoded":
		return ConfigSourceHardcoded, nil
	case "both":
		return ConfigSourceBoth, nil
	}
	return ConfigSourceTOML, fmt.Errorf("invalid --config-source value %q (expected toml|hardcoded|both)", s)
}

// PlanOpts bundles inputs to engine.Plan. Mirrors ApplyOpts shape but
// drops every write-path knob (Features.AcquireFlock, etc.) — plan is
// read-only by construction.
type PlanOpts struct {
	// Domains is the closed-catalog set of domains to evaluate.
	// Iteration order is preserved into PlanResult.Domains; RenderText
	// applies its own alphabetical sort on top.
	//
	// When ConfigSource=ConfigSourceBoth, Domains carries the TOML half
	// and HardcodedRef carries the hardcoded half — the planParity loop
	// pairs them by Tag.
	Domains []*bear.Domain

	// StatePath is optional and READ-ONLY. wires it for
	// header lines like "applied_config_hash=…"; does not
	// dereference it.
	StatePath string

	// Verbose populates Diff.Detail with before/after strings and
	// emits a per-domain trace line to Stderr.
	Verbose bool

	// Stderr is the verbose-trace target. Defaults to os.Stderr when
	// nil. Tests inject a bytes.Buffer here.
	Stderr io.Writer

	// ConfigSource selects single-path render (TOML or Hardcoded) vs
	// parity render (Both). Default zero-value is ConfigSourceTOML —
	// preserves callers' behavior. D-04.
	ConfigSource ConfigSource

	// HardcodedRef carries the hardcoded *bear.Domain slice when
	// ConfigSource == ConfigSourceBoth. Pairs are matched by Tag.
	// nil/empty when ConfigSource != Both — planSinglePath does not
	// read this field.
	HardcodedRef []*bear.Domain
}

// Plan walks opts.Domains and returns a *PlanResult. Plan dispatches
// between planSinglePath (TOML or Hardcoded — single-path render) and
// planParity (Both — dual-path render with byte-equality compare).
//
// The dispatcher is deliberately tiny so gocognit stays well under the
// threshold; the work happens in the two helpers (RESEARCH Pitfall 10).
func Plan(ctx context.Context, opts PlanOpts) (*PlanResult, error) {
	if opts.ConfigSource == ConfigSourceBoth {
		return planParity(ctx, opts)
	}
	return planSinglePath(ctx, opts)
}

// planSinglePath holds the verbatim Plan body. Behavior is
// byte-identical to the pre-Phase-04 implementation; this is a refactor
// for the parity-branch split (RESEARCH Pitfall 10), NOT a behavior
// change. Reachable via the dispatcher when ConfigSource is TOML or
// Hardcoded — for ConfigSource=Hardcoded, the caller (cmd/noxctl/plan.go)
// has resolved opts.Domains from registry.All before invoking Plan.
//
// Read-only: never invokes overwriteWithRetry, never writes state.json,
// never acquires flock. Single-pass per CONTEXT D-06.
//
// Per-domain failures collect into PlanResult.Errors and continue
// (mirrors apply.go log-and-continue pattern). Context cancellation
// mid-iteration sets PlanResult.Interrupted=true and stops at the
// next domain boundary; partial results are still returned.
func planSinglePath(ctx context.Context, opts PlanOpts) (*PlanResult, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	seedDuplicateRegistry(ctx, opts.Domains, opts.Stderr)
	result := newEmptyPlanResult(len(opts.Domains))
	for _, d := range opts.Domains {
		if ctx.Err() != nil {
			result.Interrupted = true
			break
		}
		dp, err := computeDomainDelta(ctx, d, opts.Verbose)
		if err != nil {
			result.Errors = append(result.Errors, PlanError{Tag: d.Tag, Msg: err.Error()})
			dp = DomainPlan{Tag: d.Tag, Status: StatusError, Changes: make([]Diff, 0)}
		}
		result.Domains = append(result.Domains, dp)
		if opts.Verbose {
			_, _ = fmt.Fprintf(opts.Stderr, "▶ %s — %s (%d change(s))\n", d.Tag, dp.Status, len(dp.Changes))
		}
	}
	// Residue scan — corpus-level, NOT per-domain (CONTEXT D-02). Failure
	// is non-fatal: log into Errors with empty Tag (corpus-scope) and
	// continue. Skipped on zero-domain TOML (every tag would be untracked
	// trivially — invalid config per schema D-09) and on
	// interrupt (don't add bearcli round-trip on top of a canceled ctx).
	if !result.Interrupted && len(opts.Domains) > 0 {
		if scan, scanErr := bear.ScanUntracked(ctx, opts.Domains); scanErr != nil {
			result.Errors = append(result.Errors, PlanError{Tag: "", Msg: scanErr.Error()})
		} else {
			result.Untracked = translateUntracked(scan)
		}
	}
	result.CompletedAt = time.Now().UTC()
	result.Summary = computeSummary(result)
	return result, nil
}

// translateUntracked converts bear.UntrackedReport (residue scanner's
// output type, declared in bear/residue.go) into the engine-side
// UntrackedReport (declared in bear/engine/plan_result.go). Boundary
// translation pattern — same shape as cmd/noxctl/preflight.go's
// featuresFromCatalog.
//
// The two types carry IDENTICAL JSON tags by construction (
// residue + plan_result agree on tag/note_count/tag_families/
// total_notes). The translation exists to avoid an import cycle
// (bear/engine imports bear; bear cannot import bear/engine).
func translateUntracked(b bear.UntrackedReport) UntrackedReport {
	fams := make([]UntrackedFamily, len(b.TagFamilies))
	for i, f := range b.TagFamilies {
		fams[i] = UntrackedFamily{Tag: f.Tag, NoteCount: f.NoteCount}
	}
	return UntrackedReport{TagFamilies: fams, TotalNotes: b.TotalNotes}
}

// seedDuplicateRegistry primes `Domain.Duplicates` on every domain so
// `AtomicWikilink` emits the `[Title](bear://x-callback-url/open-note?id=X)`
// disambiguation form for cross-corpus duplicate titles. Without it
// every duplicate atom renders as plain `[[Title]]` here and the vault
// read (which has the URL form, written by `apply`) surfaces as false
// drift. Build-failure is non-fatal: log to stderr and fall back to
// plain wikilinks (matches the apply-side log-and-continue pattern at
// `bear/engine/apply.go`).
func seedDuplicateRegistry(ctx context.Context, domains []*bear.Domain, stderr io.Writer) {
	registry, err := bear.BuildDuplicateRegistry(ctx, domains)
	if err != nil {
		_, _ = fmt.Fprintf(stderr,
			"duplicates: registry build failed: %v (continuing with plain wikilinks)\n", err)
		return
	}
	for _, d := range domains {
		d.Duplicates = registry
	}
}

// computeDomainDelta returns one domain's plan entry by reading
// current Bear state (FetchMasterContent) and rendering desired state
// via bear.SnapshotDomainRenderInputs + d.RenderMaster, comparing via
// bear.EqualIgnoringNewNoteLinkStrict (master flavor — URL-shape drift
// surfaces as a real diff). Mirrors bear/core.go::upsertMasterIndex
// with overwriteWithRetry calls replaced by Diff{} appends.
//
// Hub layer is summary-only per CONTEXT D-01 — full per-hub diff
// fidelity requires re-rendering each hub via d.RenderHub, which needs
// helpers (parseHubBulletIdentifiers) currently unexported. The
// master-level diff is the user-visible signal CONTEXT D-01 designs
// around; per-hub fidelity is a documented gap in 03-02-SUMMARY.md.
func computeDomainDelta(ctx context.Context, d *bear.Domain, verbose bool) (DomainPlan, error) {
	dp := DomainPlan{Tag: d.Tag, Status: StatusClean, Changes: make([]Diff, 0)}

	inputs, err := bear.SnapshotDomainRenderInputs(ctx, d)
	if err != nil {
		return dp, fmt.Errorf("computeDomainDelta(%s) inputs: %w", d.Tag, err)
	}
	desiredAuto := d.RenderMaster(d, inputs.Groups)
	currentMaster, err := bear.FetchMasterContent(ctx, d)
	if err != nil {
		return dp, fmt.Errorf("computeDomainDelta(%s) master read: %w", d.Tag, err)
	}
	// `FetchMasterContent` pre-strips `bear://` new-note URLs from
	// the vault read (`bear/snapshot.go::FetchMasterContent` → D-07
	// idempotency-hash convention). The renderer emits them. Strip
	// the desired side too so both halves are URL-free before the
	// strict comparator's URL count check — otherwise every domain
	// surfaces as false drift (1 vs 0 URLs in the header).
	desiredAuto = bear.StripNewNoteURLsFromBody(desiredAuto)
	// Preserve the curator zone (below `## ✱ Куратор`) before
	// comparing. `upsertMasterIndex` in `bear/core.go` composes the
	// final write as `desiredAuto + "\n" + manual`; plan MUST mirror
	// that or every master with a curator zone surfaces as false
	// drift here.
	_, manual := bear.SplitMarker(currentMaster)
	desiredMaster := desiredAuto
	if manual != "" {
		desiredMaster = desiredAuto + "\n" + manual
	}
	switch {
	case currentMaster == "":
		dp.Changes = append(dp.Changes, Diff{
			Kind:    DiffCreate,
			Target:  "master",
			Title:   d.IndexTitle,
			Summary: fmt.Sprintf("master ✱ %s will be created", d.IndexTitle),
		})
		dp.Status = StatusDrift
	case !bear.EqualIgnoringNewNoteLinkStrict(desiredMaster, currentMaster):
		diff := Diff{
			Kind:    DiffReplace,
			Target:  "master",
			Title:   d.IndexTitle,
			Summary: fmt.Sprintf("master changed (%d bucket(s))", len(inputs.Groups)),
		}
		if verbose {
			diff.Detail = []string{
				"before:", currentMaster,
				"after:", desiredMaster,
			}
		}
		dp.Changes = append(dp.Changes, diff)
		dp.Status = StatusDrift
	}
	return dp, nil
}

// newEmptyPlanResult builds the JSON-stable empty *PlanResult shape
// (slice fields initialized via make so they marshal as `[]`, not
// `null` — RESEARCH Pitfall 6). Shared between planSinglePath and
// planParity; extracting kept the dupl gate happy and keeps the
// SchemaVersion=1 promise in one place.
func newEmptyPlanResult(capDomains int) *PlanResult {
	return &PlanResult{
		SchemaVersion: 1,
		StartedAt:     time.Now().UTC(),
		Domains:       make([]DomainPlan, 0, capDomains),
		Untracked:     UntrackedReport{TagFamilies: make([]UntrackedFamily, 0)},
		Errors:        make([]PlanError, 0),
	}
}

// computeSummary aggregates per-domain status into the footer-counts
// block. Iteration order doesn't matter — this is pure aggregation.
func computeSummary(r *PlanResult) PlanSummary {
	s := PlanSummary{DomainsTotal: len(r.Domains)}
	for _, dp := range r.Domains {
		switch dp.Status {
		case StatusClean:
			s.DomainsClean++
		case StatusDrift:
			s.DomainsDrift++
		case StatusError:
			s.DomainsError++
		case StatusParityMismatch:
			s.DomainsParityMismatch++
		}
		s.ChangesTotal += len(dp.Changes)
	}
	s.UntrackedFamilies = len(r.Untracked.TagFamilies)
	return s
}
