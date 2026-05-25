// Package config_test — master_section dispatch validation coverage.
//
// User-scenario framing: every test mimics what an operator hits
// when they author or edit a `[[domain.master_section]]` block in
// TOML and reload the catalog. The validator rejects bad shapes at
// load time with a message that names the offending stanza, section
// index, and section title — operators copy-paste that message
// straight into a fix.
package config_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/domain"
)

// masterSectionStanza returns a hub-routed stanza pre-filled with
// the required fields, ready for a single MasterSection-shaped
// override per test.
func masterSectionStanza(sections []config.StanzaMasterSection) config.Stanza {
	return config.Stanza{
		Tag:            "library/test",
		IndexTitle:     "✱ Test",
		Blueprint:      "hub-routed",
		UnknownBucket:  new("Unknown"),
		HubH2Prefix:    new("Books"),
		MasterSections: &sections,
	}
}

// TestDispatch_MasterSection_EmptyBlockRejected — the silent-master
// trap: `master_section = []` (block declared but no sections)
// previously bypassed the nil-guard, ran the sectioned renderer
// over zero sections, and emitted a blank master. Validator now
// rejects this loudly so the operator sees the mistake at load.
func TestDispatch_MasterSection_EmptyBlockRejected(t *testing.T) {
	stanza := masterSectionStanza(nil)
	empty := []config.StanzaMasterSection{}
	stanza.MasterSections = &empty
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch with empty master_section block returned nil error; expected rejection")
	}
	if !strings.Contains(err.Error(), "master_section block") {
		t.Errorf("err = %q; should explain the empty-block trap", err.Error())
	}
}

// TestDispatch_MasterSection_MissingTitleRejected — every section
// must carry a non-empty Title. Empty headers would render as `(N)`
// with no leading text, which is operator-confusing.
func TestDispatch_MasterSection_MissingTitleRejected(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Buckets: []string{"a"}}, // Title left empty
	})
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch with empty Title returned nil error")
	}
	if !strings.Contains(err.Error(), "missing required `title`") {
		t.Errorf("err = %q; want 'missing required title'", err.Error())
	}
	if !strings.Contains(err.Error(), "master_section[0]") {
		t.Errorf("err = %q; want section index in message", err.Error())
	}
}

// TestDispatch_MasterSection_BucketsAndScriptConflict — Buckets and
// Script are mutually exclusive selection rules. Operators who set
// both have a logic error; the validator surfaces it before the
// renderer silently picks one path.
func TestDispatch_MasterSection_BucketsAndScriptConflict(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Title: "Bad", Buckets: []string{"a"}, Script: "latin"},
	})
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch with buckets+script returned nil error")
	}
	if !strings.Contains(err.Error(), "pick exactly one selection rule") {
		t.Errorf("err = %q; want 'pick exactly one selection rule'", err.Error())
	}
}

// TestDispatch_MasterSection_UnknownScriptRejected — typos in the
// `script` field need to fail loudly with the accepted set in the
// message so the operator copy-pastes the correction.
func TestDispatch_MasterSection_UnknownScriptRejected(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Title: "X", Script: "cyrillic"}, // valid-looking but not accepted
	})
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch with unknown script returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown script") {
		t.Errorf("err = %q; want 'unknown script'", err.Error())
	}
	if !strings.Contains(err.Error(), "latin|non-latin") {
		t.Errorf("err = %q; want accepted set 'latin|non-latin' in message", err.Error())
	}
}

// TestDispatch_MasterSection_UnknownCountModeRejected — same loud
// rejection for count_mode typos.
func TestDispatch_MasterSection_UnknownCountModeRejected(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Title: "X", CountMode: "rows"},
	})
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch with unknown count_mode returned nil error")
	}
	if !strings.Contains(err.Error(), "unknown count_mode") {
		t.Errorf("err = %q; want 'unknown count_mode'", err.Error())
	}
}

// TestDispatch_MasterSection_EmptyCountModeAccepted — empty string
// defaults to "notes" at apply time. The validator allows it so
// operators don't have to spell the default at every section.
func TestDispatch_MasterSection_EmptyCountModeAccepted(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Title: "Defaults", CountMode: ""},
	})
	d, err := config.Dispatch(stanza, nil)
	if err != nil {
		t.Fatalf("empty count_mode rejected: %v", err)
	}
	if len(d.MasterSections) != 1 {
		t.Fatalf("len = %d, want 1 (empty count_mode should still produce one section)", len(d.MasterSections))
	}
	if d.MasterSections[0].CountMode != domain.CountModeNotes {
		t.Errorf("default CountMode = %v, want CountModeNotes", d.MasterSections[0].CountMode)
	}
}

// TestDispatch_MasterSection_OffByOneIndex — the index in the error
// message must match the section's position, not a wrong off-by-one.
// Pin this so future loop-index regressions get caught immediately.
func TestDispatch_MasterSection_OffByOneIndex(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Title: "Good"},
		{Title: "Bad", Script: "klingon"}, // section index 1
	})
	_, err := config.Dispatch(stanza, nil)
	if err == nil {
		t.Fatal("Dispatch with bad section[1] returned nil error")
	}
	if !strings.Contains(err.Error(), "master_section[1]") {
		t.Errorf("err = %q; want section index 1 (not 0)", err.Error())
	}
}

// TestDispatch_MasterSection_NilKeepsDefaultRenderer — the no-op
// path: hub-routed stanza without master_section keeps the default
// 3-tier renderer (NOT silently swapped to the sectioned one).
func TestDispatch_MasterSection_NilKeepsDefaultRenderer(t *testing.T) {
	stanza := masterSectionStanza(nil)
	stanza.MasterSections = nil
	d, err := config.Dispatch(stanza, nil)
	if err != nil {
		t.Fatalf("hub-routed without master_section: %v", err)
	}
	if len(d.MasterSections) != 0 {
		t.Errorf("d.MasterSections = %v, want empty (nil stanza ⇒ unset on Domain)", d.MasterSections)
	}
	// Sanity check: RenderMaster is wired (not nil) — the default
	// 3-tier renderer should be in place.
	if d.RenderMaster == nil {
		t.Error("d.RenderMaster is nil; default 3-tier renderer should be wired")
	}
}

// TestDispatch_MasterSection_PopulatesDomainFields — the happy path:
// every section field round-trips from TOML stanza onto the Domain.
// Covers all three ShowBulletCounts paths in showBulletCountsDefault:
// nil (default true), &true (explicit true), &false (explicit false).
func TestDispatch_MasterSection_PopulatesDomainFields(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{
			Title:            "Languages",
			Buckets:          []string{"go", "rust"},
			CountMode:        "buckets",
			ShowBulletCounts: new(false),
		},
		{
			Title:            "Explicit",
			Buckets:          []string{"swift"},
			ShowBulletCounts: new(true),
		},
		{
			Title:  "Other",
			Script: "non-latin",
			// ShowBulletCounts unset — defaults to true.
		},
	})
	d, err := config.Dispatch(stanza, nil)
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if len(d.MasterSections) != 3 {
		t.Fatalf("len = %d, want 3", len(d.MasterSections))
	}
	first := d.MasterSections[0]
	if first.Title != "Languages" {
		t.Errorf("section[0].Title = %q, want Languages", first.Title)
	}
	if first.CountMode != domain.CountModeBuckets {
		t.Errorf("section[0].CountMode = %v, want CountModeBuckets", first.CountMode)
	}
	if first.ShowBulletCounts {
		t.Errorf("section[0].ShowBulletCounts = true, want false (explicit &false override)")
	}
	if got := strings.Join(first.Buckets, ","); got != "go,rust" {
		t.Errorf("section[0].Buckets = %q, want go,rust", got)
	}
	if !d.MasterSections[1].ShowBulletCounts {
		t.Errorf("section[1].ShowBulletCounts = false; want true (explicit &true)")
	}
	if !d.MasterSections[2].ShowBulletCounts {
		t.Errorf("section[2].ShowBulletCounts = false; want true (nil default)")
	}
	if d.MasterSections[2].Script != "non-latin" {
		t.Errorf("section[2].Script = %q, want non-latin", d.MasterSections[2].Script)
	}
}

// TestApplyMasterSections_NoAppendOnRepeat — apply twice on the SAME
// *domain.Domain instance and assert the second call assigns (not
// appends). Dispatch can't catch this because it constructs a fresh
// Domain each call; the regression would only manifest when
// applyMasterSections accidentally grows d.MasterSections in place.
// Uses config.ApplyMasterSectionsForTest test seam to reach the
// private apply path directly.
func TestApplyMasterSections_NoAppendOnRepeat(t *testing.T) {
	stanza := masterSectionStanza([]config.StanzaMasterSection{
		{Title: "Pinned", Buckets: []string{"go", "rust"}},
	})
	d := &domain.Domain{Tag: "test"}
	config.ApplyMasterSectionsForTest(d, stanza)
	if len(d.MasterSections) != 1 {
		t.Fatalf("first apply len = %d, want 1", len(d.MasterSections))
	}
	config.ApplyMasterSectionsForTest(d, stanza)
	if len(d.MasterSections) != 1 {
		t.Errorf("second apply len = %d, want 1 (apply must assign, not append)",
			len(d.MasterSections))
	}
}
