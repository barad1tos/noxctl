// Package engine plan — read-only "what would Apply do" preview.
//
// Sibling of engine.Apply, explicitly NOT a dryRun flag: the pre-pass
// set is structurally different; threading dryRun through every
// pre-pass would not match plan's actual behavior — plan does not
// invoke foreign-tag escape, auto-tag, cross-domain, or
// time-promotion.
//
// Layering: stdlib + bear (read-only). engine.Plan never imports
// bear/config and never calls overwriteWithRetry / state.Save /
// AcquireApply.
package engine

import (
	"context"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// PlanOpts bundles inputs to engine.Plan. Mirrors ApplyOpts shape but
// drops every write-path knob (Features.AcquireFlock, etc.) — plan is
// read-only by construction.
type PlanOpts struct {
	// Domains is the closed-catalog set of domains to evaluate.
	// Iteration order is preserved into PlanResult.Domains; RenderText
	// applies its own alphabetical sort on top.
	Domains []*bear.Domain

	// StatePath is optional and READ-ONLY. Wired for header lines like
	// "applied_config_hash=…"; never dereferenced.
	StatePath string

	// Verbose populates Diff.Detail with before/after strings and
	// emits a per-domain trace line to Stderr.
	Verbose bool

	// Stderr is the verbose-trace target. Defaults to os.Stderr when
	// nil. Tests inject a bytes.Buffer here.
	Stderr io.Writer
}

// Plan walks opts.Domains and returns a *PlanResult.
func Plan(ctx context.Context, opts PlanOpts) (*PlanResult, error) {
	return planSinglePath(ctx, opts)
}

// planSinglePath walks opts.Domains once and produces a PlanResult.
// Read-only: never invokes overwriteWithRetry, never writes state.json,
// never acquires flock.
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
	// Residue scan — corpus-level, NOT per-domain. Failure is non-fatal:
	// log into Errors with empty Tag (corpus-scope) and continue. Skipped
	// on zero-domain TOML (every tag would be untracked trivially —
	// invalid config) and on interrupt (don't add bearcli round-trip on
	// top of a canceled ctx).
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
		// Defense in depth: planSinglePath already defaults nil
		// stderr to os.Stderr, but callers landing here through
		// future refactors might not. Fall back rather than panic
		// inside fmt.Fprintf.
		if stderr == nil {
			stderr = os.Stderr
		}
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
// surfaces as a real diff). Mirrors bear/regen_writes.go::upsertMasterIndex
// with overwriteWithRetry calls replaced by Diff{} appends.
//
// Hub layer is summary-only — full per-hub diff fidelity requires
// re-rendering each hub via d.RenderHub, which needs helpers
// (parseHubBulletIdentifiers) currently unexported. The master-level
// diff is the user-visible signal we design around; per-hub fidelity
// is a documented gap.
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
	// the vault read so the bytes are idempotency-hash stable. The
	// renderer emits them. Strip the desired side too so both halves
	// are URL-free before the strict comparator's URL count check —
	// otherwise every domain surfaces as false drift (1 vs 0 URLs in
	// the header).
	desiredAuto = bear.StripNewNoteURLsFromBody(desiredAuto)
	// Preserve the curator zone (below `## ✱ Куратор`) before
	// comparing. `upsertMasterIndex` in `bear/regen_writes.go` composes
	// the final write as `desiredAuto + "\n" + manual`; plan MUST mirror
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

// newEmptyPlanResult builds the JSON-stable empty *PlanResult shape:
// slice fields initialized via make so they marshal as `[]` instead of
// `null`. SchemaVersion=1 is pinned here so any future bump lives in
// exactly one place.
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
		}
		s.ChangesTotal += len(dp.Changes)
	}
	s.UntrackedFamilies = len(r.Untracked.TagFamilies)
	return s
}
