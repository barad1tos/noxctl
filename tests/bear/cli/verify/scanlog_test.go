package verify_test

import (
	"strings"
	"testing"

	"github.com/barad1tos/noxctl/bear/cli/verify"
)

// scanLogCase captures one rewind/scan scenario over the daemon log.
// `wantOK=false` covers the "no startup marker" branch (daemon never
// ran); `wantWarnings` is the expected count of LOOP/EMERGENCY/ERROR:
// lines collected from the post-startup window.
type scanLogCase struct {
	name         string
	log          []string
	wantOK       bool
	wantWarnings int
}

// TestScanLogSinceStartup table-drives `ScanDaemonLogForTest` across
// every rewind/scan scenario. Single runner avoids the structurally
// identical "join, scan, assert" block from tripping the `dupl`
// linter per-test.
func TestScanLogSinceStartup(t *testing.T) {
	cases := []scanLogCase{
		{
			name: "CleanRunAfterStart",
			log: []string{
				"2026/05/01 10:00:00 regen-watchd starting",
				"2026/05/01 10:00:01 regen[poetry]: complete (6 buckets, 800ms)",
				"2026/05/01 10:00:02 regen[lyrics]: unchanged",
			},
			wantOK:       true,
			wantWarnings: 0,
		},
		{
			name: "RewindsToLastStart",
			log: []string{
				"2026/05/01 10:00:00 regen-watchd starting",
				"2026/05/01 10:00:01 LOOP detected for note X — old session, ignore",
				"2026/05/01 11:00:00 regen-watchd starting",
				"2026/05/01 11:00:01 regen[poetry]: complete",
			},
			wantOK:       true,
			wantWarnings: 0,
		},
		{
			name: "WarningsAfterLastStart",
			log: []string{
				"2026/05/01 10:00:00 regen-watchd starting",
				"2026/05/01 10:00:01 LOOP detected for note Я — rewrite_count=5",
				"2026/05/01 10:00:02 EMERGENCY DISABLE — 20 stuck notes",
				"2026/05/01 10:00:03 regen[poetry]: ERROR: bearcli timeout",
				"2026/05/01 10:00:04 regen[lyrics]: unchanged",
			},
			wantOK:       true,
			wantWarnings: 3,
		},
		{
			name: "NoStartupMarker",
			log: []string{
				"2026/05/01 10:00:00 some other process line",
				"2026/05/01 10:00:01 another non-startup line",
			},
			wantOK:       false,
			wantWarnings: 0,
		},
		{
			name: "WarningsBeforeStartupIgnored",
			log: []string{
				"2026/05/01 09:00:00 LOOP detected — old process death-rattle",
				"2026/05/01 10:00:00 regen-watchd starting",
				"2026/05/01 10:00:01 regen[poetry]: complete",
			},
			wantOK:       true,
			wantWarnings: 0,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			warnings, ok := verify.ScanDaemonLogForTest(strings.NewReader(strings.Join(c.log, "\n")))
			if ok != c.wantOK {
				t.Errorf("ok = %v, want %v", ok, c.wantOK)
			}
			if got := len(warnings); got != c.wantWarnings {
				t.Errorf("len(warnings) = %d, want %d (warnings=%v)", got, c.wantWarnings, warnings)
			}
		})
	}
}
