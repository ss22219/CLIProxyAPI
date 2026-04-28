package kiro

import (
	"regexp"
	"time"
)

// currentTimeRe matches "Current time: <ISO8601>" patterns injected by the framework.
var currentTimeRe = regexp.MustCompile(`Current time:\s*(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2}))`)

// FakeWorkTime rewrites "Current time: ..." timestamps in text to fall within
// Mon-Fri 09:00-18:00 UTC. This prevents Kiro from refusing to work outside
// business hours. Enabled by default.
func FakeWorkTime(text string) string {
	return currentTimeRe.ReplaceAllStringFunc(text, func(match string) string {
		sub := currentTimeRe.FindStringSubmatch(match)
		if len(sub) < 2 {
			return match
		}
		t, err := time.Parse(time.RFC3339Nano, sub[1])
		if err != nil {
			t, err = time.Parse("2006-01-02T15:04:05Z", sub[1])
			if err != nil {
				return match
			}
		}
		t = MapToWorkTime(t)
		return "Current time: " + t.Format(time.RFC3339Nano)
	})
}

// MapToWorkTime shifts a time to the nearest weekday and clamps hours to 09-17.
func MapToWorkTime(t time.Time) time.Time {
	// Shift weekend to Monday
	switch t.Weekday() {
	case time.Saturday:
		t = t.AddDate(0, 0, 2)
	case time.Sunday:
		t = t.AddDate(0, 0, 1)
	}
	// Clamp hour to 09:00-17:59
	h := t.Hour()
	if h < 9 {
		t = time.Date(t.Year(), t.Month(), t.Day(), 9, t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	} else if h >= 18 {
		t = time.Date(t.Year(), t.Month(), t.Day(), 17, t.Minute(), t.Second(), t.Nanosecond(), t.Location())
	}
	return t
}
