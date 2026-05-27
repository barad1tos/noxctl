// Package verify_test — pure-helper coverage for the apply-idempotency
// arithmetic (nonIdempotentDomains alphabetical-sort contract +
// sumApplyField aggregation) driven through the ForTest exports.
package verify_test

import (
	"reflect"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
	"github.com/barad1tos/noxctl/bear/engine"
)

// TestNonIdempotentDomains_SortedAlphabetically — when pass-2 writes
// across multiple domains, the Details list MUST come back sorted.
// Go's map iteration is randomized; the operator's paste-into-issue
// workflow demands a stable order so diffs across runs are signal
// (a domain joined/left the list) not noise (random reshuffle).
func TestNonIdempotentDomains_SortedAlphabetically(t *testing.T) {
	res := &engine.ApplyResult{
		PrePasses: map[string]engine.PrePassCounts{
			"foreign_tag": {Changed: 2},
		},
		Domains: map[string]engine.DomainCounts{
			"library/poetry":  {Changed: 1},
			"llm/agents":      {Created: 1},
			"it/vendors":      {Created: 1, Changed: 2},
			"library/quotes":  {Created: 1},
			"personal/daily":  {Unchanged: 1}, // clean — must be omitted
			"personal/weekly": {Failed: 1},    // failed — surfaced via AnyFailed, omitted here
		},
	}
	got := verify.NonIdempotentDomainsForTest(res)
	want := []string{
		"foreign_tag (pre-pass changed=2)",
		"it/vendors (created=1 changed=2)",
		"library/poetry (created=0 changed=1)",
		"library/quotes (created=1 changed=0)",
		"llm/agents (created=1 changed=0)",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("\n got: %v\nwant: %v", got, want)
	}
}

// TestNonIdempotentDomains_EmptyOnCleanResult — happy path: every
// domain reports zero Created/Changed → empty slice (not nil, since
// the production code initializes via `make`). The verify check uses
// the length to gate PASS vs FAIL.
func TestNonIdempotentDomains_EmptyOnCleanResult(t *testing.T) {
	res := &engine.ApplyResult{
		Domains: map[string]engine.DomainCounts{
			"library/poetry": {Unchanged: 1},
			"llm/agents":     {Unchanged: 1},
		},
	}
	got := verify.NonIdempotentDomainsForTest(res)
	if len(got) != 0 {
		t.Errorf("clean second pass must produce empty list; got: %v", got)
	}
}

// TestSumApplyField_AggregatesAcrossDomains — the PASS message reads
// pass-1 stats by calling sumApplyField four times (one per field).
// Test the aggregation over a 3-domain fixture so a future refactor
// that flips the loop direction or skips zero-valued entries gets
// caught immediately.
func TestSumApplyField_AggregatesAcrossDomains(t *testing.T) {
	res := &engine.ApplyResult{
		Domains: map[string]engine.DomainCounts{
			"library/poetry": {Created: 1, Changed: 2, Unchanged: 3, Failed: 0},
			"llm/agents":     {Created: 0, Changed: 1, Unchanged: 5, Failed: 1},
			"it/vendors":     {Created: 2, Changed: 0, Unchanged: 4, Failed: 0},
		},
	}
	cases := []struct {
		name string
		pick func(engine.DomainCounts) int
		want int
	}{
		{"Created", func(d engine.DomainCounts) int { return d.Created }, 3},
		{"Changed", func(d engine.DomainCounts) int { return d.Changed }, 3},
		{"Unchanged", func(d engine.DomainCounts) int { return d.Unchanged }, 12},
		{"Failed", func(d engine.DomainCounts) int { return d.Failed }, 1},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := verify.SumApplyFieldForTest(res, c.pick)
			if got != c.want {
				t.Errorf("sum(%s) = %d, want %d", c.name, got, c.want)
			}
		})
	}
}

// TestSumApplyField_EmptyResult — empty domain map → 0 regardless of
// picker. The pass message would render "0 created / 0 changed / 0
// unchanged / 0 failed across 0 domains" — operator reads that as
// "verify ran but had nothing to do" (vs missing — "verify didn't
// run at all").
func TestSumApplyField_EmptyResult(t *testing.T) {
	res := &engine.ApplyResult{Domains: map[string]engine.DomainCounts{}}
	got := verify.SumApplyFieldForTest(res, func(d engine.DomainCounts) int { return d.Created })
	if got != 0 {
		t.Errorf("sum over empty result = %d, want 0", got)
	}
}
