// Package cliutil_test exercises the catalog-to-engine feature stamp
// and the daemon-toml override layer that lives at the cmd/noxctl
// boundary. These functions are the only seam between bear/config/
// (TOML schema) and bear/engine/ (runtime contracts); a regression
// here silently disables fast-passes in the running daemon — exactly
// the failure shape the package was extracted to make testable.
package cliutil_test

import (
	"testing"

	"github.com/barad1tos/noxctl/bear/cliutil"
	"github.com/barad1tos/noxctl/bear/config"
	"github.com/barad1tos/noxctl/bear/engine"
)

// TestFeaturesFromCatalog_DefaultsAllOnForNilCatalog locks the
// ship-default contract: a nil catalog yields every feature ON.
// Operators with no Features stanza must get the full fast-pass suite
// running without explicit opt-in.
func TestFeaturesFromCatalog_DefaultsAllOnForNilCatalog(t *testing.T) {
	f := cliutil.FeaturesFromCatalog(nil)
	if !f.AutoTagDefault || !f.CrossDomainMoves || !f.TimePromotion ||
		!f.ForeignTagEscape || !f.DuplicateRegistry || !f.DomainBootstrap {
		t.Errorf("nil catalog must yield all-ON Features; got %+v", f)
	}
}

func TestDailyDefaultTagFromCatalog_NilAndConfigured(t *testing.T) {
	if got := cliutil.DailyDefaultTagFromCatalog(nil); got != "" {
		t.Fatalf("nil catalog daily default = %q, want empty", got)
	}
	catalog := &config.Catalog{}
	catalog.Meta.DailyDefaultTag = "quicknote/daily"
	if got := cliutil.DailyDefaultTagFromCatalog(catalog); got != "quicknote/daily" {
		t.Fatalf("configured daily default = %q, want quicknote/daily", got)
	}
}

// TestFeaturesFromCatalog_DomainBootstrapOverlay locks the four
// catalog states for DomainBootstrap (nil catalog / omitted pointer /
// explicit true / explicit false). Field-swap coverage for the other
// five Features fields lives in TestFeaturesFromCatalog_AllFieldsRoundTrip.
func TestFeaturesFromCatalog_DomainBootstrapOverlay(t *testing.T) {
	cases := []struct {
		name string
		cat  *config.Catalog
		want bool
	}{
		{
			name: "empty catalog features → default on",
			cat:  &config.Catalog{},
			want: true,
		},
		{
			name: "explicit nil pointer → default on",
			cat:  &config.Catalog{Features: config.Features{DomainBootstrap: nil}},
			want: true,
		},
		{
			name: "explicit true → on",
			cat:  &config.Catalog{Features: config.Features{DomainBootstrap: new(true)}},
			want: true,
		},
		{
			name: "explicit false → off",
			cat:  &config.Catalog{Features: config.Features{DomainBootstrap: new(false)}},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cliutil.FeaturesFromCatalog(tc.cat).DomainBootstrap
			if got != tc.want {
				t.Errorf("DomainBootstrap=%v, want %v", got, tc.want)
			}
		})
	}
}

// TestResolveFeatures_DomainBootstrapPrecedence locks the operator
// kill-switch contract: env / daemon-toml override of DomainBootstrap
// wins over the catalog setting. Drift here turns the
// `REGEN_DOMAIN_BOOTSTRAP=off` / `daemon.toml [daemon].domain_bootstrap`
// override into a no-op without surfacing in tests.
func TestResolveFeatures_DomainBootstrapPrecedence(t *testing.T) {
	cases := []struct {
		name         string
		catalogOn    *bool // nil = omitted, else explicit
		daemonSource string
		daemonValue  bool
		want         bool
	}{
		{
			name:         "catalog on, daemon default → on (catalog wins because no operator override)",
			catalogOn:    new(true),
			daemonSource: config.SourceDefault,
			daemonValue:  true,
			want:         true,
		},
		{
			name:         "catalog off, daemon default → off (catalog wins because no operator override)",
			catalogOn:    new(false),
			daemonSource: config.SourceDefault,
			daemonValue:  true,
			want:         false,
		},
		{
			name:         "catalog off, daemon-toml on → on (operator override wins)",
			catalogOn:    new(false),
			daemonSource: config.SourceFile,
			daemonValue:  true,
			want:         true,
		},
		{
			name:         "catalog on, env off → off (env wins)",
			catalogOn:    new(true),
			daemonSource: config.SourceEnv,
			daemonValue:  false,
			want:         false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := &config.Catalog{Features: config.Features{DomainBootstrap: tc.catalogOn}}
			dc := config.DaemonConfig{
				DomainBootstrap: tc.daemonValue,
				Sources:         map[string]string{"DomainBootstrap": tc.daemonSource},
			}
			got := cliutil.ResolveFeatures(cat, dc).DomainBootstrap
			if got != tc.want {
				t.Errorf("ResolveFeatures DomainBootstrap=%v, want %v", got, tc.want)
			}
		})
	}
}

// fieldOp captures the getter/setter pair for one engine.Features
// field so the round-trip test can drive every slot through a
// uniform interface without reflection.
type fieldOp struct {
	name string
	get  func(engine.Features) bool
	set  func(*config.Features, *bool)
}

// allFieldOps returns the closed catalog of engine.Features fields.
// Keep the slice in the same declaration order as the struct so a
// future field addition surfaces here when the test fails.
//
//nolint:dupl // table-driven row pattern — each row binds one field to its getter/setter pair
func allFieldOps() []fieldOp {
	return []fieldOp{
		{
			"AutoTagDefault", func(f engine.Features) bool { return f.AutoTagDefault },
			func(c *config.Features, p *bool) { c.AutoTagDefault = p },
		},
		{
			"CrossDomainMoves", func(f engine.Features) bool { return f.CrossDomainMoves },
			func(c *config.Features, p *bool) { c.CrossDomainMoves = p },
		},
		{
			"TimePromotion", func(f engine.Features) bool { return f.TimePromotion },
			func(c *config.Features, p *bool) { c.TimePromotion = p },
		},
		{
			"ForeignTagEscape", func(f engine.Features) bool { return f.ForeignTagEscape },
			func(c *config.Features, p *bool) { c.ForeignTagEscape = p },
		},
		{
			"DuplicateRegistry", func(f engine.Features) bool { return f.DuplicateRegistry },
			func(c *config.Features, p *bool) { c.DuplicateRegistry = p },
		},
		{
			"DomainBootstrap", func(f engine.Features) bool { return f.DomainBootstrap },
			func(c *config.Features, p *bool) { c.DomainBootstrap = p },
		},
	}
}

// assertOnlyOneFieldLit drives one round-trip pass: light up exactly
// the field at `litIdx` with `true`, leave the rest at `false`, run
// FeaturesFromCatalog, assert every output slot mirrors the expected
// pattern. Split out of the test so the parent stays under gocognit.
func assertOnlyOneFieldLit(t *testing.T, fields []fieldOp, litIdx int) {
	t.Helper()
	tr, fa := true, false
	var in config.Features
	for j, fld := range fields {
		if j == litIdx {
			fld.set(&in, &tr)
		} else {
			fld.set(&in, &fa)
		}
	}
	out := cliutil.FeaturesFromCatalog(&config.Catalog{Features: in})
	for j, fld := range fields {
		want := j == litIdx
		if got := fld.get(out); got != want {
			t.Errorf("only %s lit: slot %s got %v, want %v", fields[litIdx].name, fld.name, got, want)
		}
	}
}

// TestFeaturesFromCatalog_AllFieldsRoundTrip catches field-swap bugs
// in the per-pointer overlay (six near-identical `if cat.Features.X
// != nil { f.X = *cat.Features.X }` blocks). Each sub-test lights up
// exactly one field with `true` and leaves the other five at `false`,
// then asserts the output mirrors the input. Because exactly one slot
// carries a distinguishing value, ANY copy-paste regression that
// reads from the wrong source slot or writes into the wrong
// destination slot surfaces — the "lit" position is unique across
// the six fields, so equality with the expected pattern is a complete
// swap-detection signal. Provably covers all 15 field-pair swap
// combinations, single-direction wire-ups, and constant-source
// regressions.
func TestFeaturesFromCatalog_AllFieldsRoundTrip(t *testing.T) {
	fields := allFieldOps()
	for i, lit := range fields {
		t.Run("only_"+lit.name+"_true", func(t *testing.T) {
			assertOnlyOneFieldLit(t, fields, i)
		})
	}
}

// TestResolveFeatures_NilSourcesMap pins the defensive ok-guard:
// a DaemonConfig with no Sources map (e.g. a test fixture or future
// caller that constructs a partial DaemonConfig) MUST leave the
// catalog value intact rather than silently overriding with dc's
// zero-value DomainBootstrap. Without the guard, `nil_map["..."]`
// returns `""`, which compares not-equal to SourceDefault, and the
// override branch fires inappropriately.
func TestResolveFeatures_NilSourcesMap(t *testing.T) {
	cases := []struct {
		name    string
		sources map[string]string
		want    bool // catalog value should win
	}{
		{"nil Sources", nil, true},
		{"empty Sources", map[string]string{}, true},
		{"Sources missing the DomainBootstrap key", map[string]string{"Unrelated": config.SourceFile}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cat := &config.Catalog{Features: config.Features{DomainBootstrap: new(true)}}
			dc := config.DaemonConfig{
				DomainBootstrap: false, // would clobber catalog if override fires
				Sources:         tc.sources,
			}
			got := cliutil.ResolveFeatures(cat, dc).DomainBootstrap
			if got != tc.want {
				t.Errorf("ResolveFeatures DomainBootstrap=%v, want %v (catalog must win when Sources lacks the key)",
					got, tc.want)
			}
		})
	}
}

// TestResolveFeatures_CatalogOmittedDaemonOverride pins the canonical
// operator workflow: "I never touched [features] in the catalog, but
// set domain_bootstrap = false in daemon.toml". The catalog field is
// nil-pointer → FeaturesFromCatalog stamps the default true → daemon
// override flips it to false.
func TestResolveFeatures_CatalogOmittedDaemonOverride(t *testing.T) {
	cat := &config.Catalog{Features: config.Features{DomainBootstrap: nil}}
	dc := config.DaemonConfig{
		DomainBootstrap: false,
		Sources:         map[string]string{"DomainBootstrap": config.SourceFile},
	}
	got := cliutil.ResolveFeatures(cat, dc).DomainBootstrap
	if got != false {
		t.Errorf("ResolveFeatures DomainBootstrap=%v, want false (daemon-toml override of catalog-omitted field)", got)
	}
}
