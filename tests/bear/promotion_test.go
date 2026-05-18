package bear_test

import (
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear"
)

func TestPromoteByCalendar(t *testing.T) {
	now := time.Date(2026, 5, 7, 14, 0, 0, 0, time.Local) // Thu

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
			gotTag, gotMove := bear.PromoteByCalendar(tc.currentTag, tc.created, now)
			if gotTag != tc.wantNewTag || gotMove != tc.wantShouldMove {
				t.Errorf("got (%q, %v), want (%q, %v)", gotTag, gotMove, tc.wantNewTag, tc.wantShouldMove)
			}
		})
	}
}
