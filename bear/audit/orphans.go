package audit

// orphans.go owns the orphan-family corpus-level detector — a sibling
// scanner to ScanUntracked (untracked.go). It walks bearcli-shaped
// note records and emits one Finding per atom that carries at least
// one stray-family tag (a `#<family>/<sub>` token whose `<family>` is
// NOT in the managed-roots set computed by ManagedRootsFromDomains).
//
// Detection contract:
//   - Fires only for tags of shape `#<family>/<sub>`. Bare top-level
//     tags (`#randomthing`) are out of scope; LintUntracked covers
//     those.
//   - Family extraction uses TopLevelSegment, so depth-3 tags
//     (`#X/Y/Z`) classify by `X`. If `X` is not managed, the atom is
//     an orphan regardless of depth.
//   - Atoms already carrying `#orphans` (or `#orphans/<sub>`) are
//     skipped wholesale — that is the idempotency contract. Match is
//     case-insensitive and trims whitespace (see isOrphansTag), so
//     operator-typed `#Orphans` or `#orphans ` still triggers the
//     skip. `#orphans/duplicate-title` is deliberately not an
//     orphan-family marker; that sibling audit tag must not mask
//     stray-family findings. The apply step issues `bearcli tags add
//     <noteID> orphans` per finding, so re-running the lint sweep on
//     an already-triaged atom must produce zero findings.
//
// Finding shape:
//   - DomainTag is the empty string — orphan-family is a corpus-level
//     concern, sibling to LintUntracked, not scoped to any single
//     managed Domain. SortFindings orders empty DomainTag before
//     non-empty domains so the audit report grouping handles
//     corpus-level findings deterministically without special-casing.
//   - One Finding per atom (not one per stray tag) so the operator
//     sees the full triage context for an atom in one report row.
//     Detail comma-joins multiple strays; the apply step adds a
//     single `#orphans` tag regardless of how many stray-family tags
//     the atom carries.
//   - Fixable is true: the apply path is a single `bearcli tags add`
//     call with no body rewrite.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

// orphansTag / orphansTagPrefix — idempotency markers; see
// isOrphansTag for the case-insensitive match contract.
const (
	orphansTag       = "#orphans"
	orphansTagPrefix = "#orphans/"
)

// AggregateOrphanFamiliesFromJSON is the test seam over
// aggregateOrphanFamilies — accepts a bearcli-shaped JSON payload
// (`[{id,title,tags},...]`) and the managed-roots map
// ManagedRootsFromDomains produces, then runs the pure detector.
// Precedent: AggregateUntrackedFromJSON (untracked.go).
//
// External tests at tests/bear/ build a separate test binary and
// cannot reach in-package unexported symbols, so the seam is exposed
// in production code rather than via an in-package _test.go file.
// Returns nil + wrapped error on parse failure so callers can
// distinguish "input was malformed" from "input had zero findings".
func AggregateOrphanFamiliesFromJSON(jsonBytes []byte, managed map[string]struct{}) ([]Finding, error) {
	notes, err := decodeAuditNotesJSON[domain.AutoTagNote](jsonBytes, "AggregateOrphanFamiliesFromJSON")
	if err != nil {
		return nil, err
	}
	return aggregateOrphanFamilies(notes, managed), nil
}

func decodeAuditNotesJSON[T any](jsonBytes []byte, label string) ([]T, error) {
	var notes []T
	if err := json.Unmarshal(jsonBytes, &notes); err != nil {
		return nil, fmt.Errorf("%s parse: %w", label, err)
	}
	return notes, nil
}

// aggregateOrphanFamilies is the pure-logic core: walks the supplied
// notes, collects stray-family tags per atom via strayFamilyTags, and
// emits one Finding per atom that has at least one stray. Returns the
// findings sorted via SortFindings for deterministic output.
func aggregateOrphanFamilies(notes []domain.AutoTagNote, managed map[string]struct{}) []Finding {
	var findings []Finding
	for _, note := range notes {
		strays := strayFamilyTags(note, managed)
		if len(strays) == 0 {
			continue
		}
		findings = append(findings, Finding{
			NoteID:   note.ID,
			Title:    note.Title,
			Category: LintOrphanFamily,
			Detail:   formatStrayDetail(strays),
			Fixable:  true,
		})
	}
	SortFindings(findings)
	return findings
}

// strayFamilyTags returns the stray-family tags on a note — preserving
// the leading `#` prefix so Detail messages stay operator-friendly
// (matches what the user sees on the Bear chip). Returns nil when the
// note is already tagged `#orphans` (idempotency skip) or when no
// stray-family tag is present.
//
// Extracted from aggregateOrphanFamilies to keep both functions under
// gocognit ≤15. The helper owns the per-tag classification; the
// aggregator owns the per-note iteration.
func strayFamilyTags(note domain.AutoTagNote, managed map[string]struct{}) []string {
	var strays []string
	for _, tag := range note.Tags {
		if tag == "" {
			continue
		}
		if isDuplicateTitleAuditTag(tag) {
			continue
		}
		if isOrphansTag(tag) {
			return nil // idempotency skip — already triaged
		}
		stripped := strings.TrimPrefix(tag, "#")
		if !strings.Contains(stripped, "/") {
			continue // bare top-level out of scope (LintUntracked handles)
		}
		if _, ok := managed[domain.TopLevelSegment(tag)]; ok {
			continue // managed family — not orphan
		}
		strays = append(strays, tag)
	}
	return strays
}

// isOrphansTag reports whether tag is the idempotency marker, ignoring
// case and surrounding whitespace. Bear preserves operator-typed case
// on tag chips, so `#Orphans` or `#orphans ` would slip past a byte-
// exact compare and cause the apply pass to re-tag an already-triaged
// atom. Normalizing here keeps both write-side (apply) and read-side
// (scan) consistent.
func isOrphansTag(tag string) bool {
	normalized := normalizeAuditTag(tag)
	return normalized == orphansTag || strings.HasPrefix(normalized, orphansTagPrefix)
}

func isDuplicateTitleAuditTag(tag string) bool {
	return normalizeAuditTag(tag) == "#"+duplicateTitleTag
}

func normalizeAuditTag(tag string) string {
	return strings.TrimSpace(strings.ToLower(tag))
}

// formatStrayDetail composes the operator-facing Detail message for an
// orphan-family Finding. Comma-joins all stray tags and family names so
// one report row captures full triage context. The trailing phrase
// `tag-as-orphans candidate` is the apply-hint — ApplyOrphanFamilies
// adds `#orphans` to the note via bearcli when --apply is set.
func formatStrayDetail(strays []string) string {
	families := uniqueFamilies(strays)
	joinedStrays := strings.Join(strays, ", ")
	if len(families) == 1 {
		return fmt.Sprintf("%s — family %q not in catalog (tag-as-orphans candidate)",
			joinedStrays, families[0])
	}
	return fmt.Sprintf("%s — families %s not in catalog (tag-as-orphans candidate)",
		joinedStrays, quoteJoin(families))
}

// uniqueFamilies extracts the de-duplicated set of top-level family
// segments from a stray-tag slice, preserving first-seen order so the
// Detail message stays stable across runs (Go map iteration is not
// stable; this helper avoids that pitfall).
func uniqueFamilies(strays []string) []string {
	seen := make(map[string]struct{}, len(strays))
	out := make([]string, 0, len(strays))
	for _, tag := range strays {
		fam := domain.TopLevelSegment(tag)
		if _, ok := seen[fam]; ok {
			continue
		}
		seen[fam] = struct{}{}
		out = append(out, fam)
	}
	return out
}

// quoteJoin renders a string slice as a comma-separated list of
// double-quoted items (e.g. `"quick notes", "scratch"`). Used by the
// multi-family Detail formatter so each family name shows up verbatim
// in the operator report.
func quoteJoin(items []string) string {
	parts := make([]string, len(items))
	for i, item := range items {
		parts[i] = fmt.Sprintf("%q", item)
	}
	return strings.Join(parts, ", ")
}

// ScanOrphanFamilies is the corpus-level read-only scan. It issues one
// `bearcli list --location notes` call (mirroring ScanUntracked's call
// shape exactly), derives the managed-roots set from the supplied
// domain catalog, and runs the pure aggregateOrphanFamilies detector
// over the result.
//
// Returns (nil, wrapped error) on bearcli failure or JSON parse
// failure — corpus-level scans cannot partially succeed the way the
// per-domain Scan can (no per-domain fallback to fall back to). The
// caller (cli.RunLint) is responsible for log-and-continue on error
// so the per-domain findings still render even when the corpus scan
// fails.
//
// Read-only: never writes to bearcli; never mutates inputs. The
// mutation pass lives in ApplyOrphanFamilies.
func ScanOrphanFamilies(ctx context.Context, domains []*domain.Domain) ([]Finding, error) {
	managed := ManagedRootsFromDomains(domains)

	out, err := bearcli.Run(ctx,
		[]string{
			"list", "--location", "notes",
			bearcli.FlagFormat, bearcli.FormatJSON,
			bearcli.FlagFields, "id,title,tags",
		},
		"")
	if err != nil {
		return nil, fmt.Errorf("ScanOrphanFamilies list: %w", err)
	}

	var notes []domain.AutoTagNote
	if parseErr := json.Unmarshal(out, &notes); parseErr != nil {
		return nil, fmt.Errorf("ScanOrphanFamilies parse: %w", parseErr)
	}

	return aggregateOrphanFamilies(notes, managed), nil
}

// ApplyOrphanFamilies issues one `bearcli tags add <noteID> orphans`
// call per finding (via bearcli.AddTag). Log-and-continue on per-atom
// failure: partial tagging is strictly better than abort because
// orphan-family triage is an operator-facing workflow — the operator
// would rather see "tagged 47, failed 3 (see log)" than "aborted at
// the first failure, 0 tagged".
//
// Honors ctx.Err at the top of each iteration so SIGINT response time
// is bounded by at most one bearcli call (bearcli.Timeout=10s) instead
// of the full sweep duration — matches the cancellation pattern used
// by audit.Scan and LintApplyDomains. The third return surfaces
// ctx.Err on cancellation so the caller can distinguish "ran clean"
// from "Ctrl-C mid-loop". Per-atom tagging failures still come back
// via the failed counter without populating err, EXCEPT when
// failures reach batchAbortThreshold without any prior success —
// the loop then aborts with ErrApplyAllFailed so a bearcli verb-
// rename or permissions regression cannot silently turn a sweep
// into a no-op. A single success at any point disarms the guard
// for the remainder of the sweep.
//
// Defensive Category filter: skips findings whose Category is not
// LintOrphanFamily. Belt-and-suspenders — the caller should pre-filter
// by passing only ScanOrphanFamilies output, but mis-filtering at the
// call site must NOT silently apply `#orphans` to unrelated findings.
func ApplyOrphanFamilies(ctx context.Context, findings []Finding) (tagged, failed int, err error) {
	for _, f := range findings {
		if ctxErr := domain.CheckCtx(ctx); ctxErr != nil {
			return tagged, failed, fmt.Errorf("ApplyOrphanFamilies canceled after %d/%d: %w",
				tagged+failed, len(findings), ctxErr)
		}
		if f.Category != LintOrphanFamily {
			continue
		}
		taggedDelta, failedDelta, tagErr := applyFindingTag(ctx, f, orphanTagApply, tagged, failed)
		tagged += taggedDelta
		failed += failedDelta
		if tagErr != nil {
			return tagged, failed, tagErr
		}
	}
	return tagged, failed, nil
}

type tagApplyLabels struct {
	tag          string
	failLabel    string
	successLabel string
	abortDetail  string
}

var orphanTagApply = tagApplyLabels{
	tag:          "orphans",
	failLabel:    "orphan-tag",
	successLabel: "orphan-tagged",
	abortDetail:  "initial attempts failed; bearcli verb drift or permissions issue",
}

func applyFindingTag(
	ctx context.Context,
	finding Finding,
	labels tagApplyLabels,
	tagged int,
	failed int,
) (taggedDelta int, failedDelta int, err error) {
	if tagErr := bearcli.AddTag(ctx, finding.NoteID, labels.tag); tagErr != nil {
		log.Printf("audit: %s %s (id=%s) failed: %v", labels.failLabel, finding.Title, finding.NoteID, tagErr)
		nextFailed := failed + 1
		if shouldAbortOnTotalFailure(tagged, nextFailed) {
			return 0, 1, fmt.Errorf(
				"%w: %d/%d %s",
				ErrApplyAllFailed, nextFailed, batchAbortThreshold, labels.abortDetail,
			)
		}
		return 0, 1, nil
	}
	log.Printf("audit: %s: %s (id=%s)", labels.successLabel, finding.Title, finding.NoteID)
	return 1, 0, nil
}

// batchAbortThreshold is the count of consecutive starting failures
// that ApplyOrphanFamilies will tolerate before aborting. Set low
// enough to fail fast on a global bearcli regression, high enough to
// absorb the occasional single-atom transient (e.g. a note the user
// just trashed mid-sweep).
const batchAbortThreshold = 3

// ErrApplyAllFailed is returned by ApplyOrphanFamilies when bearcli
// AddTag fails on every attempt without a single success, reaching
// batchAbortThreshold consecutive failures. The sentinel lets callers
// distinguish a true regression (bearcli verb rename, permissions
// drift) from a sweep that produced mixed results; without it a
// 100%-failure run would surface as "tagged=0 failed=N" with exit 0.
var ErrApplyAllFailed = errors.New("ApplyOrphanFamilies: total failure batch")

// shouldAbortOnTotalFailure reports whether the abort condition is met:
// no success yet AND at least batchAbortThreshold failures observed.
func shouldAbortOnTotalFailure(tagged, failed int) bool {
	return tagged == 0 && failed >= batchAbortThreshold
}
