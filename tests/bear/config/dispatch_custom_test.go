package config_test

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/custom"
)

// stanzaForCustomLyrics is the canonical "happy path" stanza shape
// for the closed custom blueprint. Mirrors examples/roman.toml's
// library/lyrics primitive surface so the round-trip test stays
// directly comparable. Uses Go 1.26 `new(literal)` for ASCII pointers
// and bear.T for every user-facing Cyrillic value (cyrillic-lint
// I18N-04 — no raw Cyrillic literals in bear/...).
func stanzaForCustomLyrics() config.Stanza {
	idx := bear.T("library.lyrics.index")
	unknown := bear.T("library.lyrics.unknown")
	h2 := bear.T("library.lyrics.h2-prefix")
	return config.Stanza{
		Tag:                  "library/lyrics",
		IndexTitle:           idx,
		Blueprint:            "custom",
		Renderer:             new("lyrics"),
		UnknownBucket:        &unknown,
		HubH2Prefix:          &h2,
		LegacyAuthorFallback: new(true),
		StripLegacyAuthorH2:  new(true),
	}
}

// TestDispatchCustomRoundTrip — given a well-formed custom stanza
// the dispatcher returns a Domain whose RenderMaster pointer equals
// the production lyrics renderer registered under Lookup("lyrics").
//
// Pointer equality is the strongest available "renderer identity"
// check for closure-typed fields — bytes.Equal of rendered output
// would also work but requires a fixture corpus that this test
// does not load.
func TestDispatchCustomRoundTrip(t *testing.T) {
	s := stanzaForCustomLyrics()
	d, err := config.Dispatch(s, nil)
	if err != nil {
		t.Fatalf("Dispatch(custom lyrics): unexpected error: %v", err)
	}
	if d == nil {
		t.Fatal("Dispatch(custom lyrics): nil Domain")
	}
	if d.Tag != s.Tag {
		t.Errorf("Tag = %q, want %q", d.Tag, s.Tag)
	}
	if d.IndexTitle != s.IndexTitle {
		t.Errorf("IndexTitle = %q, want %q", d.IndexTitle, s.IndexTitle)
	}
	if d.RenderMaster == nil {
		t.Fatal("Dispatch(custom lyrics): nil RenderMaster (Apply did not stamp it)")
	}

	// Reach the registered Apply func directly and stamp a probe Domain.
	c, err := custom.Lookup("lyrics")
	if err != nil {
		t.Fatalf("custom.Lookup(\"lyrics\"): %v", err)
	}
	wantPtr := reflect.ValueOf(d.RenderMaster).Pointer()
	probe := *d
	probe.RenderMaster = nil
	c.Apply(&probe)
	gotPtr := reflect.ValueOf(probe.RenderMaster).Pointer()
	if wantPtr != gotPtr {
		t.Errorf("RenderMaster pointer mismatch: dispatch=%x apply=%x", wantPtr, gotPtr)
	}
}

// TestDispatchCustomUnknownRenderer locks: an unknown name
// surfaces a wrapped custom.ErrUnknownRenderer reachable via
// errors.Is, never a string-match. This is the schema's safety net
// against typos in [[domain]].renderer = "lirycs".
func TestDispatchCustomUnknownRenderer(t *testing.T) {
	s := stanzaForCustomLyrics()
	s.Renderer = new("definitely_not_a_real_renderer")
	_, err := config.Dispatch(s, nil)
	if err == nil {
		t.Fatal("Dispatch(custom unknown): want error, got nil")
	}
	if !errors.Is(err, custom.ErrUnknownRenderer) {
		t.Errorf("err = %v, want errors.Is(err, custom.ErrUnknownRenderer)", err)
	}
}

// TestDispatchCustomMissingRenderer — Renderer is required when
// Blueprint = "custom"; the dispatch validator must reject the
// stanza with a message naming the missing field, NOT silently
// fall back to a default renderer.
func TestDispatchCustomMissingRenderer(t *testing.T) {
	s := stanzaForCustomLyrics()
	s.Renderer = nil
	_, err := config.Dispatch(s, nil)
	if err == nil {
		t.Fatal("Dispatch(custom without renderer): want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "renderer") {
		t.Errorf("err must mention 'renderer': %q", msg)
	}
	if !strings.Contains(msg, "custom") {
		t.Errorf("err must mention 'custom' blueprint: %q", msg)
	}
}

// stanzaForCustomAgents is the canonical "happy path" stanza shape
// for blueprint="custom" renderer="agents". Mirrors examples/roman.toml's
// llm/agents primitive surface after the WR-01 fix (no legacy flag
// keys — the renderer enforces them, codegen short-circuits, dispatch
// rejects TOML overrides).
func stanzaForCustomAgents() config.Stanza {
	idx := bear.T("llm.agents.index")
	// "uncategorized" is the literal value llm/agents uses for its
	// unknown bucket (ASCII identifier; not a translated string).
	unknown := "uncategorized"
	h2 := bear.T("llm.agents.h2-prefix")
	return config.Stanza{
		Tag:           "llm/agents",
		IndexTitle:    idx,
		Blueprint:     "custom",
		Renderer:      new("agents"),
		UnknownBucket: &unknown,
		HubH2Prefix:   &h2,
	}
}

// TestDispatchCustomAgentsRejectsLegacyFlagOverride locks WR-01: TOML
// stanza with blueprint="custom" renderer="agents" + a legacy-flag
// override must fail loudly at validate-time (rejectCustomFlagConflicts),
// not silently corrupt the agents Domain after applyHubRoutedOptionals
// overrides the renderer-enforced false → user-supplied true. The error
// message must name both the renderer and the forbidden field so the
// operator can locate the offending stanza.
//
// Table-driven across the two forbidden fields. The mutate closure
// stamps exactly one *bool override onto the canonical agents stanza
// per row; wantField is the toml-tag substring the dispatcher must
// surface in the rejection message.
func TestDispatchCustomAgentsRejectsLegacyFlagOverride(t *testing.T) {
	cases := []struct {
		name      string
		mutate    func(s *config.Stanza)
		wantField string
	}{
		{
			name:      "LegacyAuthorFallback override",
			mutate:    func(s *config.Stanza) { s.LegacyAuthorFallback = new(true) },
			wantField: "legacy_author_fallback",
		},
		{
			name:      "StripLegacyAuthorH2 override",
			mutate:    func(s *config.Stanza) { s.StripLegacyAuthorH2 = new(true) },
			wantField: "strip_legacy_author_h2",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := stanzaForCustomAgents()
			tc.mutate(&s)
			_, err := config.Dispatch(s, nil)
			if err == nil {
				t.Fatalf("Dispatch(agents+%s): want error, got nil", tc.wantField)
			}
			msg := err.Error()
			if !strings.Contains(msg, tc.wantField) {
				t.Errorf("err must mention forbidden field %q: %q", tc.wantField, msg)
			}
			if !strings.Contains(msg, "agents") {
				t.Errorf("err must mention renderer name 'agents': %q", msg)
			}
		})
	}
}

// TestDispatchCustomAgentsPreservesInvariantsWithoutOverride locks the
// happy path: a stanza WITHOUT the forbidden legacy-flag keys
// dispatches cleanly, and the resulting *bear.Domain still carries the
// agents-enforced LegacyAuthorFallback=false / StripLegacyAuthorH2=false.
// Ensures the guard doesn't accidentally reject the canonical shape.
func TestDispatchCustomAgentsPreservesInvariantsWithoutOverride(t *testing.T) {
	s := stanzaForCustomAgents()
	d, err := config.Dispatch(s, nil)
	if err != nil {
		t.Fatalf("Dispatch(agents canonical): unexpected error: %v", err)
	}
	if d == nil {
		t.Fatal("Dispatch(agents canonical): nil Domain")
	}
	if d.LegacyAuthorFallback {
		t.Errorf("agents must enforce LegacyAuthorFallback=false; got true")
	}
	if d.StripLegacyAuthorH2 {
		t.Errorf("agents must enforce StripLegacyAuthorH2=false; got true")
	}
}

// TestDispatchRendererOnNonCustomRejected — placing renderer on any
// of the 6 declarative blueprints is a misconfiguration the
// validateBlueprintFields allow-list catches; surface lyrics on
// hub-routed and confirm dispatch refuses with both "renderer" and
// the offending blueprint name in the message.
func TestDispatchRendererOnNonCustomRejected(t *testing.T) {
	idx := bear.T("library.lyrics.index")
	unknown := bear.T("library.lyrics.unknown")
	h2 := bear.T("library.lyrics.h2-prefix")
	s := config.Stanza{
		Tag:           "library/lyrics",
		IndexTitle:    idx,
		Blueprint:     "hub-routed",
		UnknownBucket: &unknown,
		HubH2Prefix:   &h2,
		Renderer:      new("lyrics"), // not allowed on hub-routed
	}
	_, err := config.Dispatch(s, nil)
	if err == nil {
		t.Fatal("Dispatch(hub-routed with renderer): want error, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "renderer") {
		t.Errorf("err must mention 'renderer': %q", msg)
	}
	if !strings.Contains(msg, "hub-routed") {
		t.Errorf("err must mention 'hub-routed' blueprint: %q", msg)
	}
}
