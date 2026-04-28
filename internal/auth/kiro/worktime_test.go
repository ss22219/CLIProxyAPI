package kiro

import (
	"testing"
	"time"
)

func TestMapToWorkTime_WeekdayInHours(t *testing.T) {
	// Wednesday 14:30 — already work hours, unchanged
	in := time.Date(2025, 7, 16, 14, 30, 0, 0, time.UTC)
	got := MapToWorkTime(in)
	if got != in {
		t.Errorf("expected unchanged, got %v", got)
	}
}

func TestMapToWorkTime_WeekdayBefore9(t *testing.T) {
	// Tuesday 03:15 → clamped to 09:15
	in := time.Date(2025, 7, 15, 3, 15, 45, 0, time.UTC)
	got := MapToWorkTime(in)
	if got.Hour() != 9 || got.Minute() != 15 {
		t.Errorf("expected 09:15, got %02d:%02d", got.Hour(), got.Minute())
	}
	if got.Weekday() != time.Tuesday {
		t.Errorf("expected Tuesday, got %v", got.Weekday())
	}
}

func TestMapToWorkTime_WeekdayAfter18(t *testing.T) {
	// Thursday 22:00 → clamped to 17:00
	in := time.Date(2025, 7, 17, 22, 0, 0, 0, time.UTC)
	got := MapToWorkTime(in)
	if got.Hour() != 17 {
		t.Errorf("expected hour 17, got %d", got.Hour())
	}
}

func TestMapToWorkTime_Saturday(t *testing.T) {
	// Saturday → Monday
	in := time.Date(2025, 7, 19, 10, 0, 0, 0, time.UTC)
	got := MapToWorkTime(in)
	if got.Weekday() != time.Monday {
		t.Errorf("expected Monday, got %v", got.Weekday())
	}
	if got.Hour() != 10 {
		t.Errorf("expected hour 10, got %d", got.Hour())
	}
}

func TestMapToWorkTime_Sunday(t *testing.T) {
	// Sunday → Monday
	in := time.Date(2025, 7, 20, 11, 0, 0, 0, time.UTC)
	got := MapToWorkTime(in)
	if got.Weekday() != time.Monday {
		t.Errorf("expected Monday, got %v", got.Weekday())
	}
}

func TestMapToWorkTime_SaturdayLateNight(t *testing.T) {
	// Saturday 23:00 → Monday 17:00 (shifted + clamped)
	in := time.Date(2025, 7, 19, 23, 0, 0, 0, time.UTC)
	got := MapToWorkTime(in)
	if got.Weekday() != time.Monday {
		t.Errorf("expected Monday, got %v", got.Weekday())
	}
	if got.Hour() != 17 {
		t.Errorf("expected hour 17, got %d", got.Hour())
	}
}

func TestFakeWorkTime_RewritesTimestamp(t *testing.T) {
	// Sunday 02:00 UTC
	input := "Current time: 2025-07-20T02:00:00Z"
	got := FakeWorkTime(input)
	// Should be Monday 09:xx
	if got == input {
		t.Error("expected timestamp to be rewritten")
	}
	if got == "" {
		t.Error("expected non-empty result")
	}
}

func TestFakeWorkTime_NoMatch(t *testing.T) {
	input := "Hello world, no timestamp here"
	got := FakeWorkTime(input)
	if got != input {
		t.Errorf("expected unchanged, got %q", got)
	}
}

func TestFakeWorkTime_WorkHoursUnchanged(t *testing.T) {
	// Wednesday 10:30 — already work hours
	input := "Current time: 2025-07-16T10:30:00Z"
	got := FakeWorkTime(input)
	if got != input {
		t.Errorf("expected unchanged for work hours, got %q", got)
	}
}
