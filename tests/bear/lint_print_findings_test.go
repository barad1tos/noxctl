// Package bear_test — coverage for the audit/lint reporter.
//
// PrintFindings is the user-facing formatter consumed by both
// `noxctl audit` and `noxctl lint` (the report-only path). It must
// group findings by domain, then category, and stamp a final tally
// line — exact shape the operator copy-pastes into vault triage
// notes. The function regressed into orphan status after
// cmd/regen-watchd/ was deleted; this test pins the contract so the
// new CLI wrappers do not drift from the expected output.
package bear_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
)

// TestPrintFindings_GroupedShape locks the exact human-readable
// layout: blank line + `[<tag>]` header per domain, two-space indent
// + `<category>:` per category, four-space indent + `<title> — <detail>`
// per finding, optional `(fixable)` suffix, blank line + tally.
func TestPrintFindings_GroupedShape(t *testing.T) {
	findings := []bear.Finding{
		{
			DomainTag: "library/poetry", NoteID: "x1", Title: "Бридке каченя",
			Category: bear.LintBrokenH1, Detail: "title starts with `|`", Fixable: true,
		},
		{
			DomainTag: "library/poetry", NoteID: "x2", Title: "Шекспір",
			Category: bear.LintBrokenH1, Detail: "title starts with `*`", Fixable: true,
		},
		{
			DomainTag: "library/poetry", NoteID: "x3", Title: "Скитальці",
			Category: bear.LintUnsafeTitle, Detail: "contains `]`", Fixable: false,
		},
		{
			DomainTag: "llm/agents", NoteID: "x4", Title: "Helper",
			Category: bear.LintMalformedCanonical, Detail: "no recognized bucket", Fixable: true,
		},
	}
	var buf bytes.Buffer
	bear.PrintFindings(&buf, findings, 2)
	got := buf.String()

	wants := []string{
		"[library/poetry]",
		"  broken-h1:",
		"    Бридке каченя — title starts with `|`  (fixable)",
		"    Шекспір — title starts with `*`  (fixable)",
		"  unsafe-title:",
		"    Скитальці — contains `]`",
		"[llm/agents]",
		"  malformed-canonical:",
		"    Helper — no recognized bucket  (fixable)",
		"4 findings across 2 domains",
	}
	for _, want := range wants {
		if !strings.Contains(got, want) {
			t.Errorf("PrintFindings output missing %q\n--- full output ---\n%s", want, got)
		}
	}
}

// TestPrintFindings_EmptyTally — the zero-findings case still emits
// the tally so an automated triage runner can grep for the count
// regardless of whether anything fired.
func TestPrintFindings_EmptyTally(t *testing.T) {
	var buf bytes.Buffer
	bear.PrintFindings(&buf, nil, 5)
	got := buf.String()
	if !strings.Contains(got, "0 findings across 5 domains") {
		t.Errorf("empty-findings tally missing; got %q", got)
	}
}

// TestPrintFindings_FixableSuffix — the `(fixable)` suffix only
// appears on Fixable=true rows. The suffix is the operator's signal
// that `noxctl lint --apply` will auto-resolve the row; mis-stamping
// it on a non-fixable finding misleads the operator into a no-op
// apply run.
func TestPrintFindings_FixableSuffix(t *testing.T) {
	findings := []bear.Finding{
		{DomainTag: "d", Title: "fix-me", Category: bear.LintBrokenH1, Detail: "x", Fixable: true},
		{DomainTag: "d", Title: "review-me", Category: bear.LintBrokenH1, Detail: "y", Fixable: false},
	}
	var buf bytes.Buffer
	bear.PrintFindings(&buf, findings, 1)
	got := buf.String()
	if !strings.Contains(got, "fix-me — x  (fixable)") {
		t.Errorf("fixable row missing suffix; got %q", got)
	}
	if strings.Contains(got, "review-me — y  (fixable)") {
		t.Errorf("non-fixable row got the suffix; got %q", got)
	}
}
