package server

import "time"

// timeUTC builds a UTC timestamp; test-fixture helper.
func timeUTC(y int, mo time.Month, d, h, mi int) time.Time {
	return time.Date(y, mo, d, h, mi, 0, 0, time.UTC)
}

const timeHour = time.Hour

// timeUTCWeekday returns the weekday (Sunday=0) of a YYYY-MM-DD key.
func timeUTCWeekday(day string) int {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return -1
	}
	return int(t.Weekday())
}
