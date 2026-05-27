package audit

// duplicates.go owns the corpus-level duplicate-title detector. It is a
// sibling to orphans.go: both scan all Bear notes and emit operator-facing
// triage findings that `noxctl lint --apply` can tag without rewriting note
// bodies.

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/barad1tos/noxctl/bear/domain"
)

const duplicateTitleTag = "orphans/duplicate-title"

// AggregateDuplicateTitlesFromJSON is the test seam over
// aggregateDuplicateTitles. It accepts the `bearcli list --location notes`
// JSON shape with id, title, and tags.
func AggregateDuplicateTitlesFromJSON(jsonBytes []byte) ([]Finding, error) {
	notes, err := decodeAuditNotesJSON[domain.Note](jsonBytes, "AggregateDuplicateTitlesFromJSON")
	if err != nil {
		return nil, err
	}
	return aggregateDuplicateTitles(notes, nil), nil
}

// ScanDuplicateTitles walks the full Bear notes corpus and returns one finding
// for each untriaged note whose title is shared by at least one other note.
func ScanDuplicateTitles(ctx context.Context, domains []*domain.Domain) ([]Finding, error) {
	notes, err := domain.ListCorpusNotes(ctx)
	if err != nil {
		return nil, fmt.Errorf("ScanDuplicateTitles list: %w", err)
	}
	return aggregateDuplicateTitles(notes, domains), nil
}

func aggregateDuplicateTitles(notes []domain.Note, domains []*domain.Domain) []Finding {
	groups := duplicateTitleGroups(notes)
	var findings []Finding
	for _, group := range groups {
		detail := formatDuplicateTitleDetail(group)
		for _, note := range group {
			if hasDuplicateTitleTag(note.Tags) {
				continue
			}
			findings = append(findings, Finding{
				NoteID:   note.ID,
				Title:    note.Title,
				Category: LintDuplicateTitle,
				Detail:   detail,
				Fixable:  !isManagedAuxNote(note, domains),
			})
		}
	}
	SortFindings(findings)
	return findings
}

func duplicateTitleGroups(notes []domain.Note) [][]domain.Note {
	byTitle := make(map[string][]domain.Note)
	for _, note := range notes {
		title := strings.TrimSpace(note.Title)
		if title == "" {
			continue
		}
		byTitle[title] = append(byTitle[title], note)
	}
	groups := make([][]domain.Note, 0)
	for _, group := range byTitle {
		if len(group) > 1 {
			slices.SortFunc(group, compareDuplicateNotes)
			groups = append(groups, group)
		}
	}
	slices.SortFunc(groups, func(a, b []domain.Note) int {
		return strings.Compare(a[0].Title, b[0].Title)
	})
	return groups
}

func compareDuplicateNotes(a, b domain.Note) int {
	if a.Title != b.Title {
		return strings.Compare(a.Title, b.Title)
	}
	return strings.Compare(a.ID, b.ID)
}

func hasDuplicateTitleTag(tags []string) bool {
	return slices.ContainsFunc(tags, func(tag string) bool {
		normalized := strings.TrimPrefix(strings.ToLower(strings.TrimSpace(tag)), "#")
		return normalized == duplicateTitleTag
	})
}

func isManagedAuxNote(note domain.Note, domains []*domain.Domain) bool {
	for _, d := range domains {
		if d == nil || !hasDomainTag(note.Tags, d.Tag) {
			continue
		}
		if domain.IsAuxNote(d, note) {
			return true
		}
	}
	return false
}

func hasDomainTag(tags []string, domainTag string) bool {
	for _, tag := range tags {
		normalized := strings.TrimPrefix(strings.TrimSpace(tag), "#")
		if normalized == domainTag || strings.HasPrefix(normalized, domainTag+"/") {
			return true
		}
	}
	return false
}

func formatDuplicateTitleDetail(group []domain.Note) string {
	parts := make([]string, 0, len(group))
	for _, note := range group {
		parts = append(parts, fmt.Sprintf("%s tags=%s", note.ID, formatTags(note.Tags)))
	}
	return fmt.Sprintf("%q shared by %d notes: %s; tag-as-duplicate-title candidate",
		group[0].Title, len(group), strings.Join(parts, "; "))
}

func formatTags(tags []string) string {
	if len(tags) == 0 {
		return "[]"
	}
	return "[" + strings.Join(tags, ", ") + "]"
}

// ApplyDuplicateTitles tags duplicate-title findings with
// `#orphans/duplicate-title`. It is intentionally additive: note renaming is
// left to the operator.
func ApplyDuplicateTitles(ctx context.Context, findings []Finding) (tagged, failed int, err error) {
	for _, finding := range findings {
		if ctxErr := domain.CheckCtx(ctx); ctxErr != nil {
			return tagged, failed, fmt.Errorf("ApplyDuplicateTitles canceled after %d/%d: %w",
				tagged+failed, len(findings), ctxErr)
		}
		if finding.Category != LintDuplicateTitle || !finding.Fixable {
			continue
		}
		taggedDelta, failedDelta, tagErr := applyFindingTag(ctx, finding, duplicateTitleTagApply, tagged, failed)
		tagged += taggedDelta
		failed += failedDelta
		if tagErr != nil {
			return tagged, failed, tagErr
		}
	}
	return tagged, failed, nil
}

var duplicateTitleTagApply = tagApplyLabels{
	tag:          duplicateTitleTag,
	failLabel:    "duplicate-title-tag",
	successLabel: "duplicate-title-tagged",
	abortDetail:  "initial duplicate-title tag attempts failed",
}
