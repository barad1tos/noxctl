package bear_test

import (
	"testing"
	"time"

	"github.com/barad1tos/noxctl/bear/domain"
)

// mkDate builds a local-zone time.Time at hour:00:00 for the given Y/M/D/H.
// Wrapping the verbose `time.Date(..., 0, 0, 0, time.Local)` boilerplate
// keeps each table row terse so dupl doesn't see twin token windows in
// adjacent rows.
func mkDate(year int, month time.Month, day, hour int) time.Time {
	return time.Date(year, month, day, hour, 0, 0, 0, time.Local)
}

// TestCalendarBoundaries exercises every CalendarStartOf* helper through a
// single table. One table avoids per-helper test-function duplication.
func TestCalendarBoundaries(t *testing.T) {
	cases := []struct {
		name string
		fn   func(time.Time) time.Time
		in   time.Time
		want time.Time
	}{
		{
			"StartOfDay strips wall clock",
			domain.CalendarStartOfDay,
			time.Date(2026, 5, 7, 15, 30, 45, 0, time.Local),
			mkDate(2026, 5, 7, 0),
		},
		{
			"StartOfWeek: Wednesday → previous Monday",
			domain.CalendarStartOfWeek, mkDate(2026, 5, 6, 14), mkDate(2026, 5, 4, 0),
		},
		{
			"StartOfWeek: Monday → same day at 00:00",
			domain.CalendarStartOfWeek, mkDate(2026, 5, 4, 14), mkDate(2026, 5, 4, 0),
		},
		{
			"StartOfWeek: Sunday → previous Monday (6 days back)",
			domain.CalendarStartOfWeek, mkDate(2026, 5, 10, 14), mkDate(2026, 5, 4, 0),
		},
		{
			"StartOfMonth strips day and wall clock",
			domain.CalendarStartOfMonth, mkDate(2026, 5, 17, 14), mkDate(2026, 5, 1, 0),
		},
		{
			"StartOfYear strips month, day and wall clock",
			domain.CalendarStartOfYear, mkDate(2026, 5, 17, 14), mkDate(2026, 1, 1, 0),
		},
		{
			"StartOfDecade: 2026 → 2020",
			domain.CalendarStartOfDecade, mkDate(2026, 6, 15, 12), mkDate(2020, 1, 1, 0),
		},
		{
			"StartOfDecade: 2020 → 2020 (start of decade)",
			domain.CalendarStartOfDecade, mkDate(2020, 6, 15, 12), mkDate(2020, 1, 1, 0),
		},
		{
			"StartOfDecade: 2029 → 2020 (end of decade)",
			domain.CalendarStartOfDecade, mkDate(2029, 6, 15, 12), mkDate(2020, 1, 1, 0),
		},
		{
			"StartOfDecade: 2030 → 2030 (next decade boundary)",
			domain.CalendarStartOfDecade, mkDate(2030, 6, 15, 12), mkDate(2030, 1, 1, 0),
		},
		{
			"StartOfDecade: 1999 → 1990",
			domain.CalendarStartOfDecade, mkDate(1999, 6, 15, 12), mkDate(1990, 1, 1, 0),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.fn(tc.in)
			if !got.Equal(tc.want) {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}
