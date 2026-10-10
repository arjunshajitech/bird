package config

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const validYAML = `
app:
  timezone: Europe/Berlin
events:
  - id: daily-standup
    title: "Stand-up meeting"
    message: "Stop coding."
    schedule:
      type: weekly
      time: "11:00"
      days: [mon, tue, wed, thu, fri]
    actions:
      - id: join
        label: "Join meeting"
        action: open_url
        url: "https://meet.example.com/abc?pwd=SECRET"
      - id: snooze
        label: "Remind me in 5 minutes"
        action: snooze
        minutes: 5
  - id: lunch
    title: Lunch
    schedule:
      type: daily
      time: "13:00"
`

func parse(t *testing.T, y string) (*Config, error) {
	t.Helper()
	return Parse(strings.NewReader(y))
}

func mustParse(t *testing.T, y string) *Config {
	t.Helper()
	c, err := parse(t, y)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return c
}

func TestValidConfigAndDefaults(t *testing.T) {
	c := mustParse(t, validYAML)

	if c.Location.String() != "Europe/Berlin" {
		t.Errorf("location = %v", c.Location)
	}
	if c.App.Name != DefaultAppName || c.App.CatchUp() != DefaultCatchUpWindow {
		t.Errorf("app defaults not applied: %+v", c.App)
	}
	if !c.App.TrayEnabled() || !c.App.StartsMinimized() {
		t.Errorf("tray/start_minimized should default to true")
	}
	if c.Appearance.Character != "bird" || c.Appearance.AnimationSpeed != "normal" ||
		c.Appearance.Renderer != RendererAuto || !c.Appearance.OnTop() || !c.Appearance.SoundOn() {
		t.Errorf("appearance defaults not applied: %+v", c.Appearance)
	}
	if len(c.Events) != 2 {
		t.Fatalf("got %d events", len(c.Events))
	}
	standup := c.Events[0]
	if !standup.IsEnabled() || !standup.IsPersistent() || standup.Animation.Type != AnimationFlyAcross {
		t.Errorf("event defaults not applied: %+v", standup)
	}
	if standup.Compiled == nil || standup.Compiled.String() != "weekly at 11:00 on mon,tue,wed,thu,fri" {
		t.Errorf("schedule not compiled: %v", standup.Compiled)
	}
	if standup.CancelAction() != "snooze" {
		t.Errorf("cancel action = %q, want snooze", standup.CancelAction())
	}

	lunch := c.Events[1]
	if len(lunch.Actions) != 1 || lunch.Actions[0].Action != ActionDismiss {
		t.Errorf("lunch should get a default dismiss action, got %+v", lunch.Actions)
	}
	if lunch.CancelAction() != DefaultDismissActionID {
		t.Errorf("lunch cancel = %q", lunch.CancelAction())
	}
	if lunch.Timeout.D() != DefaultTimeout {
		t.Errorf("timeout default = %v", lunch.Timeout.D())
	}
}

func TestLocalTimezone(t *testing.T) {
	c := mustParse(t, `
app: {timezone: Local}
events: []
`)
	if c.Location != time.Local {
		t.Errorf("Local should map to time.Local")
	}
}

func TestCatchUpZeroIsKept(t *testing.T) {
	c := mustParse(t, "app: {catch_up_window: 0s}\nevents: []\n")
	if c.App.CatchUp() != 0 {
		t.Errorf("explicit 0s catch-up was overridden: %v", c.App.CatchUp())
	}
}

func TestSnoozeMinutesDefault(t *testing.T) {
	c := mustParse(t, `
events:
  - id: a
    title: A
    schedule: {type: daily, time: "09:00"}
    actions:
      - {id: later, label: Later, action: snooze}
`)
	if got := c.Events[0].Actions[0].Minutes; got != DefaultSnoozeMinutes {
		t.Errorf("minutes = %d, want %d", got, DefaultSnoozeMinutes)
	}
}

func TestInvalidConfigs(t *testing.T) {
	event := func(body string) string {
		return "events:\n  - id: ev\n    title: T\n    schedule: {type: daily, time: \"09:00\"}\n" + body
	}
	cases := []struct {
		name string
		yaml string
		want string // substring of the error
	}{
		{"empty file", "", "empty"},
		{"malformed yaml", "events: [ {id: a", "invalid YAML"},
		{"unknown key", "events:\n  - id: a\n    titel: x\n", "titel"},
		{"bad timezone", "app: {timezone: Mars/Olympus}\nevents: []\n", "app.timezone"},
		{"bad duration", "app: {catch_up_window: soon}\nevents: []\n", "invalid duration"},
		{"bad speed", "appearance: {animation_speed: ludicrous}\nevents: []\n", "appearance.animation_speed"},
		{"bad character", "appearance: {character: dragon}\nevents: []\n", "appearance.character"},
		{"missing id", "events:\n  - title: T\n    schedule: {type: daily, time: \"09:00\"}\n", "events[0].id: is required"},
		{"bad id", "events:\n  - id: Has Spaces\n    title: T\n    schedule: {type: daily, time: \"09:00\"}\n", "events[0] (Has Spaces).id"},
		{"missing title", "events:\n  - id: a\n    schedule: {type: daily, time: \"09:00\"}\n", "events[0] (a).title: is required"},
		{"missing schedule", "events:\n  - id: a\n    title: T\n", "events[0] (a).schedule: type is required"},
		{"invalid time", "events:\n  - id: a\n    title: T\n    schedule: {type: daily, time: \"25:00\"}\n", "events[0] (a).schedule: time \"25:00\""},
		{"unquoted-style time", "events:\n  - id: a\n    title: T\n    schedule: {type: daily, time: \"9am\"}\n", "HH:MM"},
		{"invalid weekday", "events:\n  - id: a\n    title: T\n    schedule: {type: weekly, time: \"09:00\", days: [mon, xyz]}\n", "unknown weekday \"xyz\""},
		{"weekly without days", "events:\n  - id: a\n    title: T\n    schedule: {type: weekly, time: \"09:00\"}\n", "at least one weekday"},
		{"unknown action", event("    actions:\n      - {id: x, label: X, action: run_shell}\n"), "unknown action \"run_shell\""},
		{"missing action kind", event("    actions:\n      - {id: x, label: X}\n"), "action: is required"},
		{"missing label", event("    actions:\n      - {id: x, action: dismiss}\n"), "(x).label: is required"},
		{"duplicate action id", event("    actions:\n      - {id: x, label: X, action: dismiss}\n      - {id: x, label: Y, action: dismiss}\n"), "duplicate action id"},
		{"snooze too long", event("    actions:\n      - {id: s, label: S, action: snooze, minutes: 99999}\n"), "minutes: must be between"},
		{"snooze negative", event("    actions:\n      - {id: s, label: S, action: snooze, minutes: -1}\n"), "minutes: must be between"},
		{"open_url no url", event("    actions:\n      - {id: o, label: O, action: open_url}\n"), "url: is required"},
		{"open_url file scheme", event("    actions:\n      - {id: o, label: O, action: open_url, url: \"file:///etc/passwd\"}\n"), "must use http or https"},
		{"open_url javascript", event("    actions:\n      - {id: o, label: O, action: open_url, url: \"javascript:alert(1)\"}\n"), "must use http or https"},
		{"open_url no host", event("    actions:\n      - {id: o, label: O, action: open_url, url: \"https://\"}\n"), "must include a host"},
		{"open_url option injection", event("    actions:\n      - {id: o, label: O, action: open_url, url: \"--help\"}\n"), "start with '-'"},
		{"dismiss with url", event("    actions:\n      - {id: d, label: D, action: dismiss, url: \"https://x.y\"}\n"), "dismiss does not take"},
		{"too many actions", event("    actions:\n" + strings.Repeat("", 0) +
			"      - {id: a1, label: A, action: dismiss}\n      - {id: a2, label: A, action: dismiss}\n" +
			"      - {id: a3, label: A, action: dismiss}\n      - {id: a4, label: A, action: dismiss}\n" +
			"      - {id: a5, label: A, action: dismiss}\n"), "at most 4 actions"},
		{"bad animation", event("    animation: {type: teleport}\n"), "animation.type"},
		{"timeout too short", event("    persistent: false\n    timeout: 1s\n"), "timeout: must be between"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parse(t, tc.yaml)
			if err == nil {
				t.Fatalf("expected an error containing %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not contain %q", err, tc.want)
			}
		})
	}
}

func TestDuplicateEventIDs(t *testing.T) {
	_, err := parse(t, `
events:
  - {id: same, title: A, schedule: {type: daily, time: "09:00"}}
  - {id: same, title: B, schedule: {type: daily, time: "10:00"}}
`)
	if err == nil || !strings.Contains(err.Error(), `events[1] (same).id: duplicate id "same" (also used by events[0])`) {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAllProblemsReported(t *testing.T) {
	_, err := parse(t, `
app: {timezone: Nowhere/Land}
events:
  - {id: a, schedule: {type: daily, time: "09:00"}}
  - {id: b, title: B, schedule: {type: hourly, time: "09:00"}}
`)
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("want *ValidationError, got %v", err)
	}
	if len(ve.Problems) != 3 {
		t.Errorf("want 3 problems, got %d: %v", len(ve.Problems), ve.Problems)
	}
}

func TestErrorsNeverContainURLs(t *testing.T) {
	secret := "https://meet.example.com/room?pwd=TOPSECRET with space"
	_, err := parse(t, "events:\n  - id: a\n    title: T\n    schedule: {type: daily, time: \"09:00\"}\n"+
		"    actions:\n      - {id: o, label: O, action: open_url, url: \""+secret+"\"}\n")
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), "TOPSECRET") {
		t.Errorf("error leaks the URL: %v", err)
	}
}

func TestExampleConfigIsValid(t *testing.T) {
	c, err := Load("../../config.example.yaml")
	if err != nil {
		t.Fatalf("config.example.yaml must stay valid: %v", err)
	}
	if len(c.Events) == 0 {
		t.Error("example should contain events")
	}
}
