package bear

import "time"

// CalendarStartOfDay returns t at 00:00:00 in t's timezone — the start of the
// calendar day that contains t. Used by time-promotion to test whether an
// atom's creation date predates the current calendar day.
func CalendarStartOfDay(t time.Time) time.Time {
	year, month, day := t.Date()
	return time.Date(year, month, day, 0, 0, 0, 0, t.Location())
}

// CalendarStartOfWeek returns the most recent Monday at 00:00:00 in t's
// timezone — start of the ISO 8601 week containing t. Sunday counts as the
// last day of the previous week, so a Sunday t returns the Monday 6 days
// before it.
func CalendarStartOfWeek(t time.Time) time.Time {
	weekday := int(t.Weekday()) // Sunday=0, Monday=1, ..., Saturday=6
	if weekday == 0 {
		weekday = 7 // treat Sunday as day 7 of the week
	}
	daysBack := weekday - 1
	monday := t.AddDate(0, 0, -daysBack)
	return CalendarStartOfDay(monday)
}

// CalendarStartOfMonth returns the 1st of t's month at 00:00:00 in t's
// timezone.
func CalendarStartOfMonth(t time.Time) time.Time {
	year, month, _ := t.Date()
	return time.Date(year, month, 1, 0, 0, 0, 0, t.Location())
}

// CalendarStartOfYear returns January 1st of t's year at 00:00:00 in t's
// timezone.
func CalendarStartOfYear(t time.Time) time.Time {
	return time.Date(t.Year(), time.January, 1, 0, 0, 0, 0, t.Location())
}

// CalendarStartOfDecade returns January 1st of the decade containing t at
// 00:00:00 in t's timezone. Decade is computed as `(year/10)*10` — 2026
// belongs to the 2020s, so 2020-01-01 is returned.
func CalendarStartOfDecade(t time.Time) time.Time {
	decadeYear := (t.Year() / 10) * 10
	return time.Date(decadeYear, time.January, 1, 0, 0, 0, 0, t.Location())
}
