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
// catalog states for DomainBootstrap. The other five flags follow the
// same pointer-overlay pattern; pinning DomainBootstrap is sufficient
// regression coverage because the loop structure is identical for
// each field and any drift in the overlay shape would surface here.
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
