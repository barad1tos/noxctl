// Package bear_test — factory wiring lock for Domain.Buckets.
//
// The two sub-tag preserving factories (NewGroupedVerticalDomain and
// NewHubRoutedSubTagDomain) populate Domain.Buckets with the whitelist
// computeTagOverrides consults. Without that wiring the override layer
// has no whitelist and every sidebar drag is silently ignored.
//
// Factory wiring is conceptually independent from algorithm shape; two
// failure modes, two test files.
//
//cyrillic:permit
package bear_test

import (
	"slices"
	"testing"

	"github.com/barad1tos/noxctl/bear/domain"
	"github.com/barad1tos/noxctl/bear/render"
)

// factoryBucketsCase pairs one constructed *Domain with the bucket slice
// the factory was given. Flat shape avoids closure literals in the case
// table — those tripped dupl when written as two near-identical t.Run
// builders.
type factoryBucketsCase struct {
	name    string
	domain  *domain.Domain
	buckets []string
}

// TestFactoryPopulatesBuckets locks both sub-tag preserving factories to
// copy their `buckets []string` argument into Domain.Buckets. The
// defensive copy is performed inside the factory bodies (at the top of
// NewGroupedVerticalDomain / NewHubRoutedSubTagDomain) so the resulting
// field is independent from the caller's slice — slice-identity is
// intentionally NOT asserted to avoid over-constraining the
// implementation.
//
//cyrillic:permit
func TestFactoryPopulatesBuckets(t *testing.T) {
	groupedBuckets := []string{"tasks", "development"}
	hubRoutedBuckets := []string{"sessions", "memory"}
	cases := []factoryBucketsCase{
		{
			name:    "grouped_vertical",
			domain:  render.NewGroupedVerticalDomain("work", "✱ Робота", "інше", groupedBuckets),
			buckets: groupedBuckets,
		},
		{
			name:    "hub_routed_with_subtag",
			domain:  render.NewHubRoutedSubTagDomain("claude", "✱ Claude", "general", hubRoutedBuckets),
			buckets: hubRoutedBuckets,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if !slices.Equal(tc.domain.Buckets, tc.buckets) {
				t.Errorf("%s Buckets = %v, want %v", tc.name, tc.domain.Buckets, tc.buckets)
			}
		})
	}

	// Defensive-copy guard: mutating the caller's source slice after the
	// factory returns must NOT propagate into Domain.Buckets. Without the
	// `append([]string(nil), buckets...)` copy at the top of each factory
	// the field would alias the caller's slice and downstream writes would
	// silently shift the whitelist.
	t.Run("grouped_vertical_defensive_copy", func(t *testing.T) {
		src := []string{"tasks", "development"}
		d := render.NewGroupedVerticalDomain("work", "✱ Робота", "інше", src)
		src[0] = "MUTATED"
		if d.Buckets[0] != "tasks" {
			t.Errorf("Buckets[0] = %q after caller mutated source; want %q (factory must defensive-copy)",
				d.Buckets[0], "tasks")
		}
	})
}
