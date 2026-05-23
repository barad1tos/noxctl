// Package bear_test — factory wiring lock for Domain.Buckets.
//
// Plan 12-02 wires the Buckets whitelist slot (added in Plan 12-01) into
// the two sub-tag preserving factories: NewGroupedVerticalDomain and
// NewHubRoutedSubTagDomain. Without this wiring, computeTagOverrides has
// no whitelist to consult, and every sidebar drag would be ignored.
//
// Lives in its own file (rather than alongside TestComputeTagOverrides)
// so the bear/render import stays local to the factory assertion and
// the algorithm-shape tests keep their narrow import surface.
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
// defensive copy is performed inside the factory (lines 85/139 of
// grouped.go / subtag.go) so the resulting field is independent from the
// caller's slice — slice-identity is intentionally NOT asserted to avoid
// over-constraining the implementation.
//
//cyrillic:permit
func TestFactoryPopulatesBuckets(t *testing.T) {
	groupedBuckets := []string{"tasks", "development"}
	hubRoutedBuckets := []string{"sessions", "memory"}
	cases := []factoryBucketsCase{
		{
			name:    "grouped_vertical",
			domain:  render.NewGroupedVerticalDomain("work", "* Робота", "інше", groupedBuckets),
			buckets: groupedBuckets,
		},
		{
			name:    "hub_routed_with_subtag",
			domain:  render.NewHubRoutedSubTagDomain("claude", "* Claude", "general", hubRoutedBuckets),
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
}
