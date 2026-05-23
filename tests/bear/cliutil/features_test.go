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

// TestFeaturesFromCatalog_AllFieldsRoundTrip catches field-swap bugs
// in the per-pointer overlay (six near-identical `if cat.Features.X
// != nil { f.X = *cat.Features.X }` blocks). A copy-paste regression
// that wires the wrong source field into one of the slots would
// compile cleanly and pass the single-field DomainBootstrap test.
// Two passes with inverse T/F masks cover all 15 possible field-pair
// swaps — a swap that hides under the first mask (both slots share
// the same value) surfaces under the second.
func TestFeaturesFromCatalog_AllFieldsRoundTrip(t *testing.T) {
	masks := []struct {
		name              string
		autoTagDefault    bool
		crossDomainMoves  bool
		timePromotion     bool
		foreignTagEscape  bool
		duplicateRegistry bool
		domainBootstrap   bool
	}{
		{"mask T,F,T,F,T,F", true, false, true, false, true, false},
		{"mask F,T,F,T,F,T", false, true, false, true, false, true},
	}
	for _, m := range masks {
		t.Run(m.name, func(t *testing.T) {
			in := config.Features{
				AutoTagDefault:    &m.autoTagDefault,
				CrossDomainMoves:  &m.crossDomainMoves,
				TimePromotion:     &m.timePromotion,
				ForeignTagEscape:  &m.foreignTagEscape,
				DuplicateRegistry: &m.duplicateRegistry,
				DomainBootstrap:   &m.domainBootstrap,
			}
			out := cliutil.FeaturesFromCatalog(&config.Catalog{Features: in})
			cases := []struct {
				name string
				got  bool
				want bool
			}{
				{"AutoTagDefault", out.AutoTagDefault, m.autoTagDefault},
				{"CrossDomainMoves", out.CrossDomainMoves, m.crossDomainMoves},
				{"TimePromotion", out.TimePromotion, m.timePromotion},
				{"ForeignTagEscape", out.ForeignTagEscape, m.foreignTagEscape},
				{"DuplicateRegistry", out.DuplicateRegistry, m.duplicateRegistry},
				{"DomainBootstrap", out.DomainBootstrap, m.domainBootstrap},
			}
			for _, tc := range cases {
				if tc.got != tc.want {
					t.Errorf("%s: got %v, want %v", tc.name, tc.got, tc.want)
				}
			}
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
