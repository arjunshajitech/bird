package schedule

import (
	"testing"
	"time"
)

func mustLoc(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Skipf("time zone %s unavailable: %v", name, err)
	}
	return loc
}

func mustNew(t *testing.T, kind, hhmm string, days []string, loc *time.Location) Schedule {
	t.Helper()
	s, err := New(kind, hhmm, days, loc)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return s
}

func TestDailyNextPrev(t *testing.T) {
	loc := time.UTC
	s := mustNew(t, KindDaily, "13:00", nil, loc)

	before := time.Date(2026, 3, 10, 12, 59, 0, 0, loc)
	exact := time.Date(2026, 3, 10, 13, 0, 0, 0, loc)
	after := time.Date(2026, 3, 10, 13, 1, 0, 0, loc)

	if got := s.Next(before); !got.Equal(exact) {
		t.Errorf("Next(before) = %v, want %v", got, exact)
	}
	// Next is strictly after.
	if got, want := s.Next(exact), exact.AddDate(0, 0, 1); !got.Equal(want) {
		t.Errorf("Next(exact) = %v, want %v", got, want)
	}
	// Prev is at-or-before.
	if got := s.Prev(exact); !got.Equal(exact) {
		t.Errorf("Prev(exact) = %v, want %v", got, exact)
	}
	if got := s.Prev(after); !got.Equal(exact) {
		t.Errorf("Prev(after) = %v, want %v", got, exact)
	}
	if got, want := s.Prev(before), exact.AddDate(0, 0, -1); !got.Equal(want) {
		t.Errorf("Prev(before) = %v, want %v", got, want)
	}
}

func TestWeekdaysOnly(t *testing.T) {
	loc := time.UTC
	s := mustNew(t, KindWeekly, "11:00", []string{"mon", "tue", "wed", "thu", "fri"}, loc)

	// 2026-03-13 is a Friday.
	fri := time.Date(2026, 3, 13, 11, 30, 0, 0, loc)
	if fri.Weekday() != time.Friday {
		t.Fatalf("fixture is %v, want Friday", fri.Weekday())
	}
	wantMon := time.Date(2026, 3, 16, 11, 0, 0, 0, loc)
	if got := s.Next(fri); !got.Equal(wantMon) {
		t.Errorf("Next(Fri 11:30) = %v, want Monday %v", got, wantMon)
	}
	sun := time.Date(2026, 3, 15, 18, 0, 0, 0, loc)
	wantFri := time.Date(2026, 3, 13, 11, 0, 0, 0, loc)
	if got := s.Prev(sun); !got.Equal(wantFri) {
		t.Errorf("Prev(Sun) = %v, want Friday %v", got, wantFri)
	}
}

func TestSingleWeekday(t *testing.T) {
	loc := time.UTC
	s := mustNew(t, KindWeekly, "09:15", []string{"Wednesday"}, loc)
	// Wednesday 2026-03-11 09:15 exactly -> next is one week later.
	wed := time.Date(2026, 3, 11, 9, 15, 0, 0, loc)
	if got, want := s.Next(wed), wed.AddDate(0, 0, 7); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
	if got := s.Prev(wed.Add(-time.Second)); !got.Equal(wed.AddDate(0, 0, -7)) {
		t.Errorf("Prev = %v, want previous Wednesday", got)
	}
}

func TestTimeZoneIsRespected(t *testing.T) {
	kolkata := mustLoc(t, "Asia/Kolkata") // UTC+05:30
	s := mustNew(t, KindDaily, "11:00", nil, kolkata)
	now := time.Date(2026, 3, 10, 0, 0, 0, 0, time.UTC) // 05:30 in Kolkata
	want := time.Date(2026, 3, 10, 5, 30, 0, 0, time.UTC)
	if got := s.Next(now); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestWeekdayEvaluatedInScheduleZone(t *testing.T) {
	// Monday 01:00 in Tokyo is still Sunday in UTC. A Monday-only schedule
	// must use Tokyo's calendar.
	tokyo := mustLoc(t, "Asia/Tokyo")
	s := mustNew(t, KindWeekly, "01:00", []string{"mon"}, tokyo)
	now := time.Date(2026, 3, 15, 15, 0, 0, 0, time.UTC) // Sun 15:00 UTC = Mon 00:00 JST
	want := time.Date(2026, 3, 16, 1, 0, 0, 0, tokyo)
	if got := s.Next(now); !got.Equal(want) {
		t.Errorf("Next = %v, want %v", got, want)
	}
}

func TestDSTSpringForwardGap(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-03-08: clocks jump from 02:00 to 03:00 in New York.
	s := mustNew(t, KindDaily, "02:30", nil, ny)
	now := time.Date(2026, 3, 8, 0, 0, 0, 0, ny)
	got := s.Next(now)
	if got.Year() != 2026 || got.Month() != 3 || got.Day() != 8 {
		t.Fatalf("Next = %v, want an occurrence on the transition day", got)
	}
	if h, m, _ := got.Clock(); h != 3 || m != 30 {
		t.Errorf("non-existent 02:30 resolved to %02d:%02d, want 03:30", h, m)
	}
	// Exactly one occurrence that day: the following one is on the next day.
	if next := s.Next(got); next.Day() != 9 {
		t.Errorf("following occurrence = %v, want March 9", next)
	}
}

func TestDSTFallBackFiresOnce(t *testing.T) {
	ny := mustLoc(t, "America/New_York")
	// 2026-11-01: 01:00-02:00 happens twice in New York.
	s := mustNew(t, KindDaily, "01:30", nil, ny)
	start := time.Date(2026, 11, 1, 0, 0, 0, 0, ny)
	first := s.Next(start)
	second := s.Next(first)
	if first.Day() != 1 {
		t.Fatalf("first = %v, want Nov 1", first)
	}
	if second.Day() != 2 {
		t.Errorf("second = %v, want Nov 2 (ambiguous hour must fire once)", second)
	}
	// Occurrences stay at 01:30 wall-clock time across the transition.
	if h, m, _ := second.Clock(); h != 1 || m != 30 {
		t.Errorf("second at %02d:%02d, want 01:30", h, m)
	}
}

func TestDailyAcrossDSTKeepsWallClock(t *testing.T) {
	berlin := mustLoc(t, "Europe/Berlin")
	s := mustNew(t, KindDaily, "11:00", nil, berlin)
	// Walk across the 2026-03-29 transition.
	t0 := time.Date(2026, 3, 27, 12, 0, 0, 0, berlin)
	for i := 0; i < 5; i++ {
		t0 = s.Next(t0)
		if h, m, _ := t0.Clock(); h != 11 || m != 0 {
			t.Fatalf("occurrence %d at %v, want 11:00 local", i, t0)
		}
	}
}

func TestNewValidation(t *testing.T) {
	cases := []struct {
		name string
		kind string
		time string
		days []string
	}{
		{"empty kind", "", "11:00", nil},
		{"unknown kind", "monthly", "11:00", nil},
		{"12h clock", KindDaily, "11:00 AM", nil},
		{"single digit hour", KindDaily, "9:00", nil},
		{"hour 24", KindDaily, "24:00", nil},
		{"minute 60", KindDaily, "10:60", nil},
		{"days on daily", KindDaily, "10:00", []string{"mon"}},
		{"weekly without days", KindWeekly, "10:00", nil},
		{"bad weekday", KindWeekly, "10:00", []string{"funday"}},
		{"duplicate weekday", KindWeekly, "10:00", []string{"mon", "Monday"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(tc.kind, tc.time, tc.days, time.UTC); err == nil {
				t.Errorf("expected error")
			}
		})
	}
}

func TestString(t *testing.T) {
	if got := mustNew(t, KindDaily, "07:05", nil, time.UTC).String(); got != "daily at 07:05" {
		t.Errorf("got %q", got)
	}
	got := mustNew(t, KindWeekly, "11:00", []string{"fri", "mon"}, time.UTC).String()
	if got != "weekly at 11:00 on mon,fri" {
		t.Errorf("got %q", got)
	}
}
