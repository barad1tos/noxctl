// Package engine parity — D-03 dual-path render diff.
//
// Sibling of plan.go's planSinglePath. Reachable via the Plan dispatcher
// when ConfigSource == ConfigSourceBoth. Different inputs (two slices
// paired by Tag), same output type (*PlanResult) so RenderText /
// RenderJSON keep working unchanged.
//
// Wire schema is stable: per-pair rows fold into the existing DomainPlan
// shape with Status=StatusParityMismatch on byte-divergence. Daily plist
// cron + parity-check consume engine.PlanResult JSON
// without modification.
//
// Layering: stdlib + bear (read-only). Never imports bear/config or
// registry — cmd/noxctl/plan.go resolves both slices and feeds them in.
package engine

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

// planParity walks pairs of (TOML domain, hardcoded domain) by Tag,
// renders both halves via SnapshotDomainRenderInputs + d.RenderMaster,
// compares via bear.EqualIgnoringNewNoteLink (the same idempotency-safe
// helper engine.Plan uses), and reports per-domain match/mismatch.
//
// Output schema: PlanResult with each DomainPlan.Status set to
// StatusClean (match) or StatusParityMismatch (differ). Tags present
// in only one slice surface as StatusError + a PlanError row. Untracked
// scan is skipped — parity mode is a self-comparison, not a Bear-state
// comparison.
//
// gocognit budget: this function delegates per-pair work to
// computeParityDelta to stay under the threshold (Pitfall 10).
// The pairing-loop body itself reuses recordParityPair for the missing-
// half/dispatch boilerplate — keeps planParity at ~12 statements.
func planParity(ctx context.Context, opts PlanOpts) (*PlanResult, error) {
	if opts.Stderr == nil {
		opts.Stderr = os.Stderr
	}
	result := newEmptyPlanResult(len(opts.Domains))

	tomlByTag := IndexByTag(opts.Domains)
	hardByTag := IndexByTag(opts.HardcodedRef)
	pairTags := UnionSortedTags(tomlByTag, hardByTag)

	for _, tag := range pairTags {
		if ctx.Err() != nil {
			result.Interrupted = true
			break
		}
		recordParityPair(ctx, result, tag, tomlByTag, hardByTag, opts.Verbose)
	}

	result.CompletedAt = time.Now().UTC()
	result.Summary = computeSummary(result)
	return result, nil
}

// recordParityPair handles one (tag, toml?, hard?) tuple. Extracted from
// planParity to keep the dispatcher's branch shape simple. Mutates the
// passed *PlanResult in place — same pattern as planSinglePath's
// loop body which appends to result.Domains / result.Errors directly.
func recordParityPair(
	ctx context.Context,
	result *PlanResult,
	tag string,
	tomlByTag, hardByTag map[string]*bear.Domain,
	verbose bool,
) {
	toml, hasToml := tomlByTag[tag]
	hard, hasHard := hardByTag[tag]
	switch {
	case !hasToml:
		appendMissingHalfRow(result, tag,
			fmt.Sprintf("parity: tag %q in hardcoded but not in TOML", tag))
	case !hasHard:
		appendMissingHalfRow(result, tag,
			fmt.Sprintf("parity: tag %q in TOML but not in hardcoded", tag))
	default:
		dp, err := computeParityDelta(ctx, hard, toml, verbose)
		if err != nil {
			result.Errors = append(result.Errors, PlanError{Tag: tag, Msg: err.Error()})
			dp = DomainPlan{Tag: tag, Status: StatusError, Changes: make([]Diff, 0)}
		}
		result.Domains = append(result.Domains, dp)
	}
}

// appendMissingHalfRow records the (PlanError, DomainPlan) pair the
// parity loop emits when one slice is missing a tag the other slice
// has. Extracted to keep recordParityPair below the dupl threshold —
// the !hasToml and !hasHard branches were near-identical 8-line blocks.
func appendMissingHalfRow(result *PlanResult, tag, msg string) {
	result.Errors = append(result.Errors, PlanError{Tag: tag, Msg: msg})
	result.Domains = append(result.Domains, DomainPlan{
		Tag: tag, Status: StatusError, Changes: make([]Diff, 0),
	})
}

// computeParityDelta renders both halves of a (hard, toml) pair via
// SnapshotDomainRenderInputs and compares via the pure helper
// parityDeltaFromMasters. Mirrors plan.go's computeDomainDelta shape
// but over a Domain pair instead of (Domain, Bear-state).
//
// I/O is concentrated here; parityDeltaFromMasters is the pure tail
// that test code can drive directly without bearcli.
func computeParityDelta(ctx context.Context, hard, toml *bear.Domain, verbose bool) (DomainPlan, error) {
	dp := DomainPlan{Tag: toml.Tag, Status: StatusClean, Changes: make([]Diff, 0)}

	inputsHard, err := bear.SnapshotDomainRenderInputs(ctx, hard)
	if err != nil {
		return dp, fmt.Errorf("parity(%s) hardcoded inputs: %w", toml.Tag, err)
	}
	inputsToml, err := bear.SnapshotDomainRenderInputs(ctx, toml)
	if err != nil {
		return dp, fmt.Errorf("parity(%s) toml inputs: %w", toml.Tag, err)
	}

	hardMaster := hard.RenderMaster(hard, inputsHard.Groups)
	tomlMaster := toml.RenderMaster(toml, inputsToml.Groups)
	return parityDeltaFromMasters(toml, hardMaster, tomlMaster, verbose), nil
}

// ParityDeltaFromMastersForTest is the exported test seam for the pure
// decision step of computeParityDelta. Production code reaches it via
// computeParityDelta; tests in tests/bear/engine/plan_parity_test.go
// drive it directly with synthetic strings (no bearcli).
//
// Exported (rather than living in an in-package _test.go) per the same
// rationale recorded in bear/engine/export_test.go: external tests at
// tests/bear/engine/ cannot bridge unexported symbols across the
// directory gap, so test seams that the external suite depends on
// surface as production exports with a `ForTest` suffix.
//
// Match comparison uses bear.EqualIgnoringNewNoteLink (non-strict
// flavor) — both hardcoded and TOML masters come from the same
// post-Task-2 codegen so both URL shapes are fresh; the non-strict
// variant avoids false positives if a stale Bear-side master sneaks in
// via a snapshot path. Without the strip the [Нова нотатка] timestamp
// drift would trigger a false parity mismatch every cycle.
func ParityDeltaFromMastersForTest(toml *bear.Domain, hardMaster, tomlMaster string, verbose bool) DomainPlan {
	return parityDeltaFromMasters(toml, hardMaster, tomlMaster, verbose)
}

// parityDeltaFromMasters is the unexported worker that planParity's
// I/O wrapper computeParityDelta routes through. Pure: no I/O, no
// bearcli. Production reaches it via computeParityDelta only; tests
// reach it via ParityDeltaFromMastersForTest above.
func parityDeltaFromMasters(toml *bear.Domain, hardMaster, tomlMaster string, verbose bool) DomainPlan {
	dp := DomainPlan{Tag: toml.Tag, Status: StatusClean, Changes: make([]Diff, 0)}
	if bear.EqualIgnoringNewNoteLink(hardMaster, tomlMaster) {
		return dp
	}
	diff := Diff{
		Kind:   DiffReplace,
		Target: "master",
		Title:  toml.IndexTitle,
		Summary: fmt.Sprintf(
			"parity: master differs (%d hardcoded bytes vs %d toml bytes)",
			len(hardMaster), len(tomlMaster)),
	}
	if verbose {
		diff.Detail = FirstDivergentLines(hardMaster, tomlMaster, 5)
	} else {
		diff.Detail = []string{FirstDivergentLine(hardMaster, tomlMaster)}
	}
	dp.Changes = append(dp.Changes, diff)
	dp.Status = StatusParityMismatch
	return dp
}

// IndexByTag builds a map keyed by Domain.Tag. Pure helper; nil-input safe.
func IndexByTag(ds []*bear.Domain) map[string]*bear.Domain {
	out := make(map[string]*bear.Domain, len(ds))
	for _, d := range ds {
		if d == nil {
			continue
		}
		out[d.Tag] = d
	}
	return out
}

// UnionSortedTags returns sorted unique keys present in either map. Pure;
// the sort makes the parity row order deterministic — important for
// tests and for daily JSON output stability (ordering helps diff tools).
func UnionSortedTags(a, b map[string]*bear.Domain) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FirstDivergentLine returns the first line where a and b differ,
// formatted as `line N: hardcoded=%q toml=%q`. Used in non-verbose
// mode for a 1-line summary. Plain ASCII (Pitfall 3 — never ANSI in
// JSON detail strings).
func FirstDivergentLine(a, b string) string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	n := min(len(aLines), len(bLines))
	for i := range n {
		if aLines[i] != bLines[i] {
			return fmt.Sprintf("line %d: hardcoded=%q toml=%q", i+1, aLines[i], bLines[i])
		}
	}
	if len(aLines) == len(bLines) {
		// Equal byte content with our slicing; falls through if the
		// caller invoked us on already-equal strings (defensive — the
		// production caller guards via EqualIgnoringNewNoteLink first).
		return "no divergent line found"
	}
	return fmt.Sprintf("trailing length differs: hardcoded=%d lines, toml=%d lines",
		len(aLines), len(bLines))
}

// FirstDivergentLines returns up to maxLines surrounding lines of
// per-side context centered on the first divergence. Verbose mode
// helper; emits one entry per side (`hardcoded[i]: %q`, `toml[i]: %q`)
// so RenderText can indent each at the standard four-space depth.
func FirstDivergentLines(a, b string, maxLines int) []string {
	aLines := strings.Split(a, "\n")
	bLines := strings.Split(b, "\n")
	n := min(len(aLines), len(bLines))
	first := -1
	for i := range n {
		if aLines[i] != bLines[i] {
			first = i
			break
		}
	}
	if first == -1 {
		// Length mismatch but no in-range line differs — emit the
		// trailing length report as the single diagnostic.
		return []string{fmt.Sprintf(
			"trailing length differs: hardcoded=%d lines, toml=%d lines",
			len(aLines), len(bLines))}
	}
	end := min(first+maxLines, n)
	out := make([]string, 0, (end-first)*2)
	for i := first; i < end; i++ {
		out = append(out,
			fmt.Sprintf("hardcoded[%d]: %q", i+1, aLines[i]),
			fmt.Sprintf("toml[%d]: %q", i+1, bLines[i]),
		)
	}
	return out
}
