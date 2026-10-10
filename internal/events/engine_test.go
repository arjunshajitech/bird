package events

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/bird-app/bird/internal/config"
)

const baseYAML = `
app:
  timezone: UTC
  catch_up_window: 15m
events:
  - id: standup
    title: Stand-up
    schedule: {type: weekly, time: "11:00", days: [mon, tue, wed, thu, fri]}
    actions:
      - {id: join, label: Join, action: open_url, url: "https://meet.example.com/x"}
      - {id: snooze, label: Later, action: snooze, minutes: 5}
  - id: lunch
    title: Lunch
    schedule: {type: daily, time: "13:00"}
`

// Monday 2026-03-16.
func at(day, hour, min int) time.Time { return time.Date(2026, 3, day, hour, min, 0, 0, time.UTC) }

func cfg(t *testing.T, y string) *config.Config {
	t.Helper()
	c, err := config.Parse(strings.NewReader(y))
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	return c
}

func newEngine(t *testing.T, y string) *Engine {
	t.Helper()
	return NewEngine(cfg(t, y), NewState(), nil)
}

func ids(trs []Trigger) []string {
	var out []string
	for _, tr := range trs {
		out = append(out, tr.Occurrence.EventID)
	}
	return out
}

func expectTriggers(t *testing.T, got []Trigger, want ...string) {
	t.Helper()
	if g := strings.Join(ids(got), ","); g != strings.Join(want, ",") {
		t.Fatalf("triggers = [%s], want [%s]", g, strings.Join(want, ","))
	}
}

func mustResolve(t *testing.T, e *Engine, occ Occurrence, r Response, now time.Time) Outcome {
	t.Helper()
	out, err := e.Resolve(occ, r, now)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	return out
}

func TestFiresAtScheduledTimeOnly(t *testing.T) {
	e := newEngine(t, baseYAML)
	// Fresh state at 10:59: the previous occurrence (Fri 11:00) is days old,
	// so nothing fires and nothing is reported as missed.
	expectTriggers(t, e.Tick(at(16, 10, 59)))
	if got, want := e.NextWake(at(16, 10, 59)), at(16, 11, 0); !got.Equal(want) {
		t.Fatalf("NextWake = %v, want %v", got, want)
	}
	trs := e.Tick(at(16, 11, 0))
	expectTriggers(t, trs, "standup")
	if !trs[0].Occurrence.Scheduled.Equal(at(16, 11, 0)) || trs[0].Occurrence.Snoozed {
		t.Errorf("occurrence = %+v", trs[0].Occurrence)
	}
	if !e.TakeDirty() {
		t.Error("state must be dirty after a trigger (it is persisted before presenting)")
	}
}

func TestNoDuplicateForSameOccurrence(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	expectTriggers(t, trs, "standup")
	// While on screen: no re-trigger.
	expectTriggers(t, e.Tick(at(16, 11, 1)))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 2))
	// After acknowledgement: the same occurrence never fires again.
	expectTriggers(t, e.Tick(at(16, 11, 3)))
	expectTriggers(t, e.Tick(at(16, 11, 14)))
}

func TestNoDuplicateAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 1))
	if err := SaveState(path, e.State()); err != nil {
		t.Fatal(err)
	}

	st, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	restarted := NewEngine(cfg(t, baseYAML), st, nil)
	expectTriggers(t, restarted.Tick(at(16, 11, 2)))
	// The next day still fires.
	expectTriggers(t, restarted.Tick(at(17, 11, 0)), "standup")
}

func TestUnansweredReminderReshownAfterQuickRestart(t *testing.T) {
	e := newEngine(t, baseYAML)
	expectTriggers(t, e.Tick(at(16, 11, 0)), "standup")

	// Process dies with the reminder on screen; restart 3 minutes later.
	restarted := NewEngine(cfg(t, baseYAML), e.State(), nil)
	trs := restarted.Tick(at(16, 11, 3))
	expectTriggers(t, trs, "standup")
	if !trs[0].Occurrence.Scheduled.Equal(at(16, 11, 0)) {
		t.Errorf("re-shown occurrence = %+v", trs[0].Occurrence)
	}
	// Only once.
	expectTriggers(t, restarted.Tick(at(16, 11, 4)))
}

func TestUnansweredReminderDroppedAfterLongRestart(t *testing.T) {
	e := newEngine(t, baseYAML)
	expectTriggers(t, e.Tick(at(16, 11, 0)), "standup")
	restarted := NewEngine(cfg(t, baseYAML), e.State(), nil)
	expectTriggers(t, restarted.Tick(at(16, 12, 30)))
	if restarted.State().Events["standup"].Active != nil {
		t.Error("stale active entry should be cleared")
	}
}

func TestMissedEventPolicy(t *testing.T) {
	t.Run("within catch-up window fires once", func(t *testing.T) {
		e := newEngine(t, baseYAML)
		trs := e.Tick(at(16, 11, 10)) // started 10 min late
		expectTriggers(t, trs, "standup")
		if !trs[0].Occurrence.Scheduled.Equal(at(16, 11, 0)) {
			t.Errorf("scheduled = %v", trs[0].Occurrence.Scheduled)
		}
	})
	t.Run("past catch-up window is skipped", func(t *testing.T) {
		e := newEngine(t, baseYAML)
		expectTriggers(t, e.Tick(at(16, 11, 16)))
		// And it is consumed: it does not come back later.
		expectTriggers(t, e.Tick(at(16, 11, 17)))
	})
	t.Run("long shutdown shows nothing old", func(t *testing.T) {
		e := newEngine(t, baseYAML)
		e.Tick(at(16, 11, 0))
		e.inFlight = map[string]bool{}
		e.State().Events["standup"].Active = nil
		// Machine off for three days; each missed occurrence must not replay.
		expectTriggers(t, e.Tick(at(19, 15, 0)))
	})
	t.Run("zero window disables catch-up", func(t *testing.T) {
		e := newEngine(t, strings.Replace(baseYAML, "catch_up_window: 15m", "catch_up_window: 0s", 1))
		expectTriggers(t, e.Tick(at(16, 11, 0)), "standup") // exactly on time still fires
		e2 := newEngine(t, strings.Replace(baseYAML, "catch_up_window: 15m", "catch_up_window: 0s", 1))
		expectTriggers(t, e2.Tick(at(16, 11, 1)))
	})
}

func TestWeekdayOnlyEventSkipsWeekend(t *testing.T) {
	e := newEngine(t, baseYAML)
	// Saturday 2026-03-21: only lunch fires.
	expectTriggers(t, e.Tick(at(21, 11, 0)))
	expectTriggers(t, e.Tick(at(21, 13, 0)), "lunch")
}

func TestSnooze(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	out := mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 1))
	if !out.Close || out.OpenURL != "" {
		t.Fatalf("outcome = %+v", out)
	}
	if got, want := e.NextWake(at(16, 11, 1)), at(16, 11, 6); !got.Equal(want) {
		t.Fatalf("NextWake = %v, want snooze at %v", got, want)
	}
	expectTriggers(t, e.Tick(at(16, 11, 5)))

	trs = e.Tick(at(16, 11, 6))
	expectTriggers(t, trs, "standup")
	occ := trs[0].Occurrence
	if !occ.Snoozed || !occ.Scheduled.Equal(at(16, 11, 0)) {
		t.Fatalf("snoozed occurrence = %+v, want original slot with Snoozed", occ)
	}
	// Snoozing again works; then acknowledging ends it.
	mustResolve(t, e, occ, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 7))
	trs = e.Tick(at(16, 11, 12))
	expectTriggers(t, trs, "standup")
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 13))
	expectTriggers(t, e.Tick(at(16, 11, 30)))

	// The regular schedule is unaffected by snoozing: next day at 11:00 sharp.
	if got, want := e.NextWake(at(16, 14, 0)), at(17, 11, 0); !got.Equal(want) {
		t.Errorf("NextWake = %v, want %v", got, want)
	}
}

func TestSnoozeSurvivesRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 0))
	if err := SaveState(path, e.State()); err != nil {
		t.Fatal(err)
	}
	st, _ := LoadState(path)
	restarted := NewEngine(cfg(t, baseYAML), st, nil)
	expectTriggers(t, restarted.Tick(at(16, 11, 5)), "standup")
}

func TestStaleSnoozeSupersededByNewOccurrence(t *testing.T) {
	y := strings.Replace(baseYAML, "minutes: 5", "minutes: 1440", 1)
	e := newEngine(t, y)
	trs := e.Tick(at(16, 11, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 0))
	// Next day's occurrence arrives before the 24h snooze is due.
	trs = e.Tick(at(17, 11, 0))
	expectTriggers(t, trs, "standup")
	if trs[0].Occurrence.Snoozed {
		t.Errorf("want the new regular occurrence, got the snooze")
	}
	if e.State().Events["standup"].Snooze != nil {
		t.Error("older snooze should be cleared by the newer occurrence")
	}
}

func TestOpenURLAcknowledge(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	out := mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 1))
	if !out.Close || out.OpenURL != "https://meet.example.com/x" {
		t.Errorf("outcome = %+v", out)
	}
	if strings.Contains(out.Summary, "meet.example.com") {
		t.Error("summary must not contain the URL")
	}
}

func TestOpenURLWithoutAcknowledgeKeepsReminder(t *testing.T) {
	e := newEngine(t, strings.Replace(baseYAML, `url: "https://meet.example.com/x"}`,
		`url: "https://meet.example.com/x", acknowledge: false}`, 1))
	trs := e.Tick(at(16, 11, 0))
	out := mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 1))
	if out.Close || out.OpenURL == "" {
		t.Fatalf("outcome = %+v, want URL opened and reminder kept", out)
	}
	// Still active: no duplicate, and a later dismissal is accepted.
	expectTriggers(t, e.Tick(at(16, 11, 2)))
	out = mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 3))
	if !out.Close {
		t.Error("snooze should close")
	}
}

func TestCancelPrefersSnooze(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	out := mustResolve(t, e, trs[0].Occurrence, Response{Kind: Cancelled}, at(16, 11, 1))
	if !out.Close || e.State().Events["standup"].Snooze == nil {
		t.Errorf("Escape should snooze when a snooze action exists: %+v", out)
	}
}

func TestCancelWithoutSnoozeDismisses(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 13, 0))
	expectTriggers(t, trs, "lunch")
	out := mustResolve(t, e, trs[0].Occurrence, Response{Kind: Cancelled}, at(16, 13, 1))
	if !out.Close || e.State().Events["lunch"].Snooze != nil {
		t.Errorf("outcome = %+v", out)
	}
}

func TestTimeoutAcknowledges(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 13, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: TimedOut}, at(16, 13, 1))
	expectTriggers(t, e.Tick(at(16, 13, 2)))
}

func TestFailedDisplayRetries(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 13, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Failed}, at(16, 13, 0))
	expectTriggers(t, e.Tick(at(16, 13, 0)))
	trs = e.Tick(at(16, 13, 1))
	expectTriggers(t, trs, "lunch")

	// Keep failing: retries are bounded.
	now := at(16, 13, 1)
	attempts := 1
	for len(trs) > 0 && attempts < MaxRetries+5 {
		mustResolve(t, e, trs[0].Occurrence, Response{Kind: Failed}, now)
		now = now.Add(RetryDelay)
		trs = e.Tick(now)
		attempts++
	}
	if attempts != MaxRetries+1 {
		t.Errorf("attempts = %d, want %d", attempts, MaxRetries+1)
	}
	if st := e.State().Events["lunch"]; st.Snooze != nil || st.Active != nil {
		t.Errorf("retries should stop after %d attempts: %+v", MaxRetries, st)
	}
}

func TestStaleResponseRejected(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	occ := trs[0].Occurrence
	mustResolve(t, e, occ, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 1))
	// A second click (or a reconnecting UI) for the same occurrence.
	if _, err := e.Resolve(occ, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 2)); !errors.Is(err, ErrStale) {
		t.Errorf("err = %v, want ErrStale", err)
	}
	if e.State().Events["standup"].Snooze != nil {
		t.Error("stale response must not change state")
	}
}

func TestUnknownActionKeepsReminder(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	out, err := e.Resolve(trs[0].Occurrence, Response{Kind: Chose, ActionID: "nope"}, at(16, 11, 1))
	if err == nil || out.Close {
		t.Errorf("want error and reminder kept, got %+v, %v", out, err)
	}
}

func TestReloadKeepsDedupAndPrunes(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "join"}, at(16, 11, 1))

	// Reload with the same events plus an edit to lunch's title.
	e.SetConfig(cfg(t, strings.Replace(baseYAML, "title: Lunch", "title: Food", 1)))
	expectTriggers(t, e.Tick(at(16, 11, 2)))

	// Remove lunch; its state is dropped.
	e.Tick(at(16, 13, 0))
	e.inFlight = map[string]bool{}
	e.SetConfig(cfg(t, strings.SplitN(baseYAML, "  - id: lunch", 2)[0]))
	if _, ok := e.State().Events["lunch"]; ok {
		t.Error("state for removed event should be pruned")
	}
}

func TestResponseForEventRemovedWhileShowing(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 13, 0))
	e.SetConfig(cfg(t, strings.SplitN(baseYAML, "  - id: lunch", 2)[0]))
	out := mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "dismiss"}, at(16, 13, 1))
	if !out.Close {
		t.Error("should close")
	}
}

func TestDisablingEventClearsSnooze(t *testing.T) {
	e := newEngine(t, baseYAML)
	trs := e.Tick(at(16, 11, 0))
	mustResolve(t, e, trs[0].Occurrence, Response{Kind: Chose, ActionID: "snooze"}, at(16, 11, 0))
	e.SetConfig(cfg(t, strings.Replace(baseYAML, "  - id: standup\n", "  - id: standup\n    enabled: false\n", 1)))
	expectTriggers(t, e.Tick(at(16, 11, 5)))
	if e.State().Events["standup"].Snooze != nil {
		t.Error("snooze should be cleared for a disabled event")
	}
}

func TestUpcomingOrder(t *testing.T) {
	e := newEngine(t, baseYAML)
	up := e.Upcoming(at(16, 10, 0))
	if len(up) != 2 || up[0].EventID != "standup" || up[1].EventID != "lunch" {
		t.Errorf("upcoming = %+v", up)
	}
}

func TestDSTOccurrenceFiresOnce(t *testing.T) {
	y := `
app: {timezone: America/New_York}
events:
  - {id: night, title: Night, schedule: {type: daily, time: "01:30"}}
`
	c, err := config.Parse(strings.NewReader(y))
	if err != nil {
		t.Skipf("tzdata unavailable: %v", err)
	}
	e := NewEngine(c, NewState(), nil)
	ny := c.Location
	// Walk through the fall-back night minute by minute; 01:30 happens twice
	// on the wall clock but must trigger exactly once.
	count := 0
	for now := time.Date(2026, 11, 1, 0, 0, 0, 0, ny); now.Before(time.Date(2026, 11, 1, 4, 0, 0, 0, ny)); now = now.Add(time.Minute) {
		for _, tr := range e.Tick(now) {
			count++
			mustResolve(t, e, tr.Occurrence, Response{Kind: Chose, ActionID: "dismiss"}, now)
		}
	}
	if count != 1 {
		t.Errorf("fired %d times during DST fall-back, want 1", count)
	}
}

func TestCorruptStateIsMovedAside(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	if err := writeFile(path, "{not json"); err != nil {
		t.Fatal(err)
	}
	st, err := LoadState(path)
	if err == nil || st == nil || len(st.Events) != 0 {
		t.Fatalf("want empty state and error, got %v, %v", st, err)
	}
	if !fileExists(path + ".corrupt") {
		t.Error("corrupt file should be preserved")
	}
}
