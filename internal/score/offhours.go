package score

import (
	"fmt"
	"time"
)

// Off-hours classification (REQ-NOISE evidence extension, SRE
// assessment): pages that would wake someone are the burden SREs triage
// by. Evidence-only in this version — no score weighting until the
// calibrate workflow provides a factual basis.

// OffHours is the config block defining what counts as off-hours.
type OffHours struct {
	// Timezone is the IANA zone the org's business hours are defined in.
	Timezone string `yaml:"timezone"`
	// StartHour..EndHour (local, 24h) bound the nightly off-hours window;
	// the window wraps midnight (e.g. 20 -> 8).
	StartHour int `yaml:"start_hour"`
	EndHour   int `yaml:"end_hour"`
	// WeekendIsOffHours counts all of Saturday and Sunday as off-hours.
	WeekendIsOffHours bool `yaml:"weekend_is_offhours"`

	loc *time.Location
}

func defaultOffHours() OffHours {
	return OffHours{Timezone: "UTC", StartHour: 20, EndHour: 8, WeekendIsOffHours: true}
}

func (o *OffHours) validate() error {
	if o.Timezone == "" {
		*o = defaultOffHours()
	}
	if o.StartHour < 0 || o.StartHour > 23 || o.EndHour < 0 || o.EndHour > 23 {
		return fmt.Errorf("offhours hours must be 0-23: start %d end %d", o.StartHour, o.EndHour)
	}
	loc, err := time.LoadLocation(o.Timezone)
	if err != nil {
		return fmt.Errorf("offhours timezone %q: %w", o.Timezone, err)
	}
	o.loc = loc
	return nil
}

// IsOffHours reports whether t falls in the configured off-hours window.
// Deterministic given the embedded tzdata (cmd imports time/tzdata so
// binaries do not depend on host zone files).
func (o OffHours) IsOffHours(t time.Time) bool {
	local := t.In(o.loc)
	if o.WeekendIsOffHours {
		switch local.Weekday() {
		case time.Saturday, time.Sunday:
			return true
		}
	}
	h := local.Hour()
	if o.StartHour == o.EndHour {
		return false // zero-width nightly window: weekends only
	}
	if o.StartHour > o.EndHour { // wraps midnight, e.g. 20 -> 8
		return h >= o.StartHour || h < o.EndHour
	}
	return h >= o.StartHour && h < o.EndHour
}
