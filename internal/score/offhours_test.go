package score

import (
	"strings"
	"testing"
	"time"
)

func offHoursCfg(t *testing.T, tz string) OffHours {
	t.Helper()
	o := OffHours{Timezone: tz, StartHour: 20, EndHour: 8, WeekendIsOffHours: true}
	if err := o.validate(); err != nil {
		t.Fatal(err)
	}
	return o
}

func TestIsOffHours(t *testing.T) {
	utc := offHoursCfg(t, "UTC")
	cases := []struct {
		name string
		t    time.Time
		want bool
	}{
		{"weekday afternoon", time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC), false}, // Wednesday
		{"weekday late night", time.Date(2026, 7, 1, 23, 0, 0, 0, time.UTC), true},
		{"weekday early morning", time.Date(2026, 7, 2, 3, 0, 0, 0, time.UTC), true},
		{"boundary start inclusive", time.Date(2026, 7, 1, 20, 0, 0, 0, time.UTC), true},
		{"boundary end exclusive", time.Date(2026, 7, 2, 8, 0, 0, 0, time.UTC), false},
		{"saturday noon", time.Date(2026, 7, 4, 12, 0, 0, 0, time.UTC), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := utc.IsOffHours(tc.t); got != tc.want {
				t.Errorf("IsOffHours(%v) = %v, want %v", tc.t, got, tc.want)
			}
		})
	}

	// Timezone matters: 14:00 UTC Wednesday is 23:00 in Tokyo — off-hours.
	tokyo := offHoursCfg(t, "Asia/Tokyo")
	if !tokyo.IsOffHours(time.Date(2026, 7, 1, 14, 0, 0, 0, time.UTC)) {
		t.Error("14:00 UTC must be off-hours in Asia/Tokyo (23:00 local)")
	}

	// Weekends-only mode: zero-width nightly window.
	weekendOnly := OffHours{Timezone: "UTC", StartHour: 0, EndHour: 0, WeekendIsOffHours: true}
	if err := weekendOnly.validate(); err != nil {
		t.Fatal(err)
	}
	if weekendOnly.IsOffHours(time.Date(2026, 7, 1, 23, 0, 0, 0, time.UTC)) {
		t.Error("zero-width nightly window must not flag weekday nights")
	}
}

func TestOffHoursValidation(t *testing.T) {
	bad := OffHours{Timezone: "Not/AZone", StartHour: 20, EndHour: 8}
	if err := bad.validate(); err == nil || !strings.Contains(err.Error(), "timezone") {
		t.Errorf("bad timezone must fail validation: %v", err)
	}
	empty := OffHours{}
	if err := empty.validate(); err != nil || empty.Timezone != "UTC" {
		t.Errorf("empty block must normalize to defaults: %+v err=%v", empty, err)
	}
}
