package audit

// duplicates.go owns the corpus-level duplicate-title detector. It is a
// sibling to orphans.go: both scan all Bear notes and emit operator-facing
// triage findings that `noxctl lint --apply` can tag without rewriting note
// bodies.

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/barad1tos/noxctl/bear/bearcli"
	"github.com/barad1tos/noxctl/bear/domain"
)

const duplicateTitleTag = "orphans/duplicate-title"

// AggregateDuplicateTitlesFromJSON is the test seam over
// aggregateDuplicateTitles. It accepts the `bearcli list --location notes`
// JSON shape with id, title, and tags.
func AggregateDuplicateTitlesFromJSON(jsonBytes []byte) ([]Finding, error) {
	var notes []domain.AutoTagNote
	if err := json.Unmarshal(jsonBytes, &notes); err != nil {
		return nil, fmt.Errorf("AggregateDuplicateTitlesFromJSON parse: %w", err)
	}
	return aggregateDuplicateTitles(notes), nil
}

// ScanDuplicateTitles walks the full Bear notes corpus and returns one finding
// for each untriaged note whose title is shared by at least one other note.
func ScanDuplicateTitles(ctx context.Context) ([]Finding, error) {
	out, err := bearcli.Run(ctx,
		[]string{
			"list", "--location", "notes",
			bearcli.FlagFormat, bearcli.FormatJSON,
			bearcli.FlagFields, "id,title,tags",
		},
		"")
	if err != nil {
		return nil, fmt.Errorf("ScanDuplicateTitles list: %w", err)
	}
	return AggregateDuplicateTitlesFromJSON(out)
}

func aggregateDuplicateTitles(notes []domain.AutoTagNote) []Finding {
	groups := duplicateTitleGroups(notes)
	var findings []Finding
	for _, group := range groups {
		detail := formatDuplicateTitleDetail(group)
		for _, note := range group {
			if hasOrphansTag(note.Tags) {
				continue
			}
			findings = append(findings, Finding{
				NoteID:   note.ID,
				Title:    note.Title,
				Category: LintDuplicateTitle,
				Detail:   detail,
				Fixable:  true,
			})
		}
	}
	SortFindings(findings)
	return findings
}

func duplicateTitleGroups(notes []domain.AutoTagNote) [][]domain.AutoTagNote {
	byTitle := make(map[string][]domain.AutoTagNote)
	for _, note := range notes {
		title := strings.TrimSpace(note.Title)
		if title == "" {
			continue
		}
		byTitle[title] = append(byTitle[title], note)
	}
	groups := make([][]domain.AutoTagNote, 0)
	for _, group := range byTitle {
		if len(group) > 1 {
			groups = append(groups, group)
		}
	}
	return groups
}

func hasOrphansTag(tags []string) bool {
	return slices.ContainsFunc(tags, isOrphansTag)
}

func formatDuplicateTitleDetail(group []domain.AutoTagNote) string {
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
