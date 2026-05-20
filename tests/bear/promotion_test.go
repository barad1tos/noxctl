package bear_test

import (
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/fastpass"
)

func TestPromoteByCalendar(t *testing.T) {
	now := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local) // Thu

	// Promotion ladder used in tests mirrors the catalog form an
	// operator would write in TOML. PromoteByCalendar's contract is
	// rules-driven now: an empty slice short-circuits, and unknown
	// source tags pass through unchanged.
	rules := []fastpass.PromotionRule{
		{From: "quicknote/daily", To: "quicknote/weekly", Boundary: "day"},
		{From: "quicknote/weekly", To: "quicknote/monthly", Boundary: "week"},
		{From: "quicknote/monthly", To: "quicknote/yearly", Boundary: "month"},
		{From: "quicknote/yearly", To: "quicknote/decadal", Boundary: "year"},
	}

	cases := []struct {
		name           string
		currentTag     string
		created        time.Time
		wantNewTag     string
		wantShouldMove bool
	}{
		{
			name:       "daily atom created today stays",
			currentTag: "quicknote/daily",
			created:    time.Date(2026, 5, 7, 9, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/daily", wantShouldMove: false,
		},
		{
			name:       "daily atom created yesterday → weekly",
			currentTag: "quicknote/daily",
			created:    time.Date(2026, 5, 6, 23, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/weekly", wantShouldMove: true,
		},
		{
			name:       "daily atom from this Monday → weekly (boundary inclusive)",
			currentTag: "quicknote/daily",
			created:    time.Date(2026, 5, 4, 0, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/weekly", wantShouldMove: true,
		},
		{
			name:       "daily atom from prev Sunday → monthly (crossed week)",
			currentTag: "quicknote/daily",
			created:    time.Date(2026, 5, 3, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/monthly", wantShouldMove: true,
		},
		{
			name:       "daily atom from May 1 → monthly (this month, crossed week)",
			currentTag: "quicknote/daily",
			created:    time.Date(2026, 5, 1, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/monthly", wantShouldMove: true,
		},
		{
			name:       "daily atom 8 days ago (last month) → yearly (crossed month)",
			currentTag: "quicknote/daily",
			created:    time.Date(2026, 4, 29, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/yearly", wantShouldMove: true,
		},
		{
			name:       "daily atom from last year → decadal (crossed year)",
			currentTag: "quicknote/daily",
			created:    time.Date(2025, 12, 31, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/decadal", wantShouldMove: true,
		},
		{
			name:       "weekly atom this week stays",
			currentTag: "quicknote/weekly",
			created:    time.Date(2026, 5, 6, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/weekly", wantShouldMove: false,
		},
		{
			name:       "weekly atom from prev Sunday → monthly",
			currentTag: "quicknote/weekly",
			created:    time.Date(2026, 5, 3, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/monthly", wantShouldMove: true,
		},
		{
			name:       "weekly atom last month → yearly (crossed month)",
			currentTag: "quicknote/weekly",
			created:    time.Date(2026, 4, 28, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/yearly", wantShouldMove: true,
		},
		{
			name:       "monthly atom this month stays",
			currentTag: "quicknote/monthly",
			created:    time.Date(2026, 5, 1, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/monthly", wantShouldMove: false,
		},
		{
			name:       "monthly atom prev month this year → yearly",
			currentTag: "quicknote/monthly",
			created:    time.Date(2026, 4, 15, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/yearly", wantShouldMove: true,
		},
		{
			name:       "monthly atom last year → decadal (crossed year)",
			currentTag: "quicknote/monthly",
			created:    time.Date(2025, 12, 15, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/decadal", wantShouldMove: true,
		},
		{
			name:       "yearly atom this year stays",
			currentTag: "quicknote/yearly",
			created:    time.Date(2026, 1, 5, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/yearly", wantShouldMove: false,
		},
		{
			name:       "yearly atom 11 years ago → decadal",
			currentTag: "quicknote/yearly",
			created:    time.Date(2015, 5, 7, 14, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/decadal", wantShouldMove: true,
		},
		{
			name:       "decadal atom is terminal",
			currentTag: "quicknote/decadal",
			created:    time.Date(1995, 1, 1, 0, 0, 0, 0, time.Local),
			wantNewTag: "quicknote/decadal", wantShouldMove: false,
		},
		{
			name:       "non-quicknote atom is left alone",
			currentTag: "library/poetry",
			created:    time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local),
			wantNewTag: "library/poetry", wantShouldMove: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotTag, gotMove := fastpass.PromoteByCalendar(tc.currentTag, tc.created, now, rules)
			if gotTag != tc.wantNewTag || gotMove != tc.wantShouldMove {
				t.Errorf("got (%q, %v), want (%q, %v)", gotTag, gotMove, tc.wantNewTag, tc.wantShouldMove)
			}
		})
	}
}

// TestPromoteByCalendar_EmptyRules — the rules-driven short-circuit:
// an operator with no `[[promotion]]` blocks should see every tag pass
// through unchanged regardless of creation date. Pins the contract
// because empty-rules-disables time-promotion is what makes the
// catalog-driven design opt-in.
func TestPromoteByCalendar_EmptyRules(t *testing.T) {
	now := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local)
	created := time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local) // ancient
	gotTag, gotMove := fastpass.PromoteByCalendar("quicknote/daily", created, now, nil)
	if gotTag != "quicknote/daily" || gotMove {
		t.Errorf("empty rules: got (%q, %v), want (\"quicknote/daily\", false)",
			gotTag, gotMove)
	}
}

// TestPromoteByCalendar_CustomLadder — operator-defined ladder using
// non-quicknote tags chains correctly. Confirms the rules-driven
// promoter is fully decoupled from the legacy quicknote/* hardcode.
func TestPromoteByCalendar_CustomLadder(t *testing.T) {
	now := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local)
	rules := []fastpass.PromotionRule{
		{From: "fleeting/inbox", To: "fleeting/week", Boundary: "day"},
		{From: "fleeting/week", To: "fleeting/archive", Boundary: "week"},
	}
	// Created last month → ladder runs to terminal `fleeting/archive`.
	created := time.Date(2026, 4, 1, 14, 0, 0, 0, time.Local)
	gotTag, gotMove := fastpass.PromoteByCalendar("fleeting/inbox", created, now, rules)
	if gotTag != "fleeting/archive" || !gotMove {
		t.Errorf("custom ladder: got (%q, %v), want (\"fleeting/archive\", true)",
			gotTag, gotMove)
	}
}

// TestPromoteByCalendar_BoundaryParity pins the validator-vs-runtime
// agreement on the boundary string set. `fastpass.ValidPromotionBoundaries`
// is the catalog-level allow-list; the rules-driven promoter routes
// each key through a calendar-start helper. A boundary that passes
// validation but produces a zero `time.Time` at runtime is a silent
// no-op rule — exactly the regression the shared map was introduced
// to prevent. Iterating the map and asserting every key yields a
// non-zero promotion outcome catches future drift in either
// direction.
func TestPromoteByCalendar_BoundaryParity(t *testing.T) {
	// Pick a `created` far enough in the past to predate every
	// supported boundary window; the only valid response for any
	// non-empty key is "promote to target".
	now := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local)
	created := time.Date(2020, 1, 1, 0, 0, 0, 0, time.Local)
	for boundary := range fastpass.ValidPromotionBoundaries {
		rules := []fastpass.PromotionRule{
			{From: "src", To: "dst", Boundary: boundary},
		}
		gotTag, gotMove := fastpass.PromoteByCalendar("src", created, now, rules)
		if gotTag != "dst" || !gotMove {
			t.Errorf("boundary=%q: got (%q, %v), want (\"dst\", true) — "+
				"validator accepts this key but boundaryStart returned zero Time",
				boundary, gotTag, gotMove)
		}
	}
}
