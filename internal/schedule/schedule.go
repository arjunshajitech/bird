// Package schedule computes recurring event occurrences in a time zone.
//
// It is deliberately free of I/O and wall-clock access: every function takes
// the reference instant as an argument, which makes the calculations
// deterministic and easy to test.
package schedule

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Schedule yields the occurrences of a recurring event.
//
// New schedule kinds (interval, cron, one-off dates, ...) only need to
// implement this interface and be registered in New.
type Schedule interface {
	// Next returns the first occurrence strictly after t.
	// It returns the zero time if there is none.
	Next(t time.Time) time.Time
	// Prev returns the latest occurrence at or before t.
	// It returns the zero time if there is none.
	Prev(t time.Time) time.Time
	// String describes the schedule for humans (logs, --check-config).
	String() string
}

// Kinds understood by New.
const (
	KindDaily  = "daily"
	KindWeekly = "weekly"
)

// New builds a schedule from its configuration fields.
func New(kind, hhmm string, days []string, loc *time.Location) (Schedule, error) {
	if loc == nil {
		return nil, fmt.Errorf("time zone is required")
	}
	if kind != KindDaily && kind != KindWeekly {
		if kind == "" {
			return nil, fmt.Errorf("type is required (one of: %s, %s)", KindDaily, KindWeekly)
		}
		return nil, fmt.Errorf("unknown type %q (one of: %s, %s)", kind, KindDaily, KindWeekly)
	}
	hour, minute, err := ParseClock(hhmm)
	if err != nil {
		return nil, err
	}
	switch kind {
	case KindDaily:
		if len(days) > 0 {
			return nil, fmt.Errorf("days is only valid for weekly schedules")
		}
		return newDayTime(hour, minute, allDays(), loc), nil
	case KindWeekly:
		set, err := ParseDays(days)
		if err != nil {
			return nil, err
		}
		return newDayTime(hour, minute, set, loc), nil
	}
	panic("unreachable")
}

// ParseClock parses a strict 24-hour "HH:MM" value.
func ParseClock(s string) (hour, minute int, err error) {
	if len(s) != 5 || s[2] != ':' {
		return 0, 0, fmt.Errorf("time %q must use 24-hour HH:MM format", s)
	}
	hour, errH := strconv.Atoi(s[:2])
	minute, errM := strconv.Atoi(s[3:])
	if errH != nil || errM != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, 0, fmt.Errorf("time %q must use 24-hour HH:MM format (00:00-23:59)", s)
	}
	return hour, minute, nil
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// ParseDays parses weekday names ("mon" or "monday", case-insensitive).
func ParseDays(days []string) ([7]bool, error) {
	var set [7]bool
	if len(days) == 0 {
		return set, fmt.Errorf("days must list at least one weekday")
	}
	for _, d := range days {
		wd, ok := dayNames[strings.ToLower(strings.TrimSpace(d))]
		if !ok {
			return set, fmt.Errorf("unknown weekday %q (use mon, tue, wed, thu, fri, sat, sun)", d)
		}
		if set[wd] {
			return set, fmt.Errorf("weekday %q is listed more than once", d)
		}
		set[wd] = true
	}
	return set, nil
}

func allDays() [7]bool { return [7]bool{true, true, true, true, true, true, true} }

// dayTime fires at a wall-clock time on a set of weekdays.
// A daily schedule is a dayTime with every weekday enabled.
type dayTime struct {
	hour, minute int
	days         [7]bool
	loc          *time.Location
}

func newDayTime(hour, minute int, days [7]bool, loc *time.Location) *dayTime {
	return &dayTime{hour: hour, minute: minute, days: days, loc: loc}
}

// searchDays bounds the scan; a weekly schedule always has an occurrence
// within 7 days, the extra day covers DST shifts across midnight.
const searchDays = 8

// at returns the occurrence on the calendar day that is offset days away from
// the date (y, m, d), and whether that day is enabled.
//
// Wall-clock times that do not exist (spring-forward gap) are moved forward by
// the size of the gap, so a 02:30 reminder fires at 03:30 that day rather than
// early. For ambiguous times (fall-back) one instant is chosen, so the
// reminder fires once, never twice.
func (s *dayTime) at(y int, m time.Month, d, offset int) (time.Time, bool) {
	// Weekday is taken from noon, which is never affected by DST transitions.
	noon := time.Date(y, m, d+offset, 12, 0, 0, 0, s.loc)
	if !s.days[noon.Weekday()] {
		return time.Time{}, false
	}
	c := time.Date(noon.Year(), noon.Month(), noon.Day(), s.hour, s.minute, 0, 0, s.loc)
	if c.Hour() != s.hour || c.Minute() != s.minute {
		// time.Date resolved a non-existent time using the post-transition
		// offset, which lands before the gap. Shift by the gap size instead.
		_, before := c.Zone()
		_, after := c.Add(3 * time.Hour).Zone()
		if gap := time.Duration(after-before) * time.Second; gap > 0 {
			c = c.Add(gap)
		}
	}
	return c, true
}

func (s *dayTime) Next(t time.Time) time.Time {
	y, m, d := t.In(s.loc).Date()
	for i := 0; i <= searchDays; i++ {
		if c, ok := s.at(y, m, d, i); ok && c.After(t) {
			return c
		}
	}
	return time.Time{}
}

func (s *dayTime) Prev(t time.Time) time.Time {
	y, m, d := t.In(s.loc).Date()
	for i := 0; i <= searchDays; i++ {
		if c, ok := s.at(y, m, d, -i); ok && !c.After(t) {
			return c
		}
	}
	return time.Time{}
}

func (s *dayTime) String() string {
	clock := fmt.Sprintf("%02d:%02d", s.hour, s.minute)
	if s.days == allDays() {
		return "daily at " + clock
	}
	var names []string
	for wd := time.Sunday; wd <= time.Saturday; wd++ {
		if s.days[wd] {
			names = append(names, strings.ToLower(wd.String()[:3]))
		}
	}
	return fmt.Sprintf("weekly at %s on %s", clock, strings.Join(names, ","))
}
