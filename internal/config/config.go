// Package config loads, defaults and validates the Bird YAML configuration.
//
// Validation collects every problem it finds (not just the first) and reports
// each one with a path such as `events[0] (daily-standup).actions[1] (join).url`
// so users can fix a file in one pass. URLs are never echoed in errors, because
// meeting links frequently embed secrets.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/bird-app/bird/internal/schedule"
)

// Action kinds. This is a closed allowlist; Bird never runs shell commands.
const (
	ActionDismiss = "dismiss"
	ActionSnooze  = "snooze"
	ActionOpenURL = "open_url"
)

// Renderer choices for appearance.renderer.
const (
	RendererAuto         = "auto"
	RendererOverlay      = "overlay"
	RendererNotification = "notification"
)

// Animation types for events[].animation.type.
const (
	AnimationFlyAcross = "fly-across"
	AnimationNone      = "none"
)

// Defaults, documented in docs/CONFIGURATION.md.
const (
	DefaultAppName         = "Bird"
	DefaultTimezone        = "Local"
	DefaultCatchUpWindow   = 15 * time.Minute
	DefaultTimeout         = 60 * time.Second
	DefaultSnoozeMinutes   = 5
	DefaultCharacter       = "bird"
	DefaultAnimationSpeed  = "normal"
	DefaultStopAt          = "center"
	MaxActionsPerEvent     = 4
	MaxSnoozeMinutes       = 24 * 60
	maxTitleLen            = 120
	maxMessageLen          = 500
	maxLabelLen            = 40
	DefaultDismissActionID = "dismiss"
)

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)

// Config is the root of config.yaml.
type Config struct {
	App        App        `yaml:"app"`
	Appearance Appearance `yaml:"appearance"`
	Events     []Event    `yaml:"events"`

	// Location is the resolved app.timezone.
	Location *time.Location `yaml:"-"`
}

type App struct {
	Name           string    `yaml:"name"`
	Timezone       string    `yaml:"timezone"`
	StartMinimized *bool     `yaml:"start_minimized"`
	CatchUpWindow  *Duration `yaml:"catch_up_window"`
	Tray           *bool     `yaml:"tray"`
}

type Appearance struct {
	Character      string `yaml:"character"`
	AlwaysOnTop    *bool  `yaml:"always_on_top"`
	AnimationSpeed string `yaml:"animation_speed"`
	SoundEnabled   *bool  `yaml:"sound_enabled"`
	Renderer       string `yaml:"renderer"`
}

type Event struct {
	ID         string    `yaml:"id"`
	Enabled    *bool     `yaml:"enabled"`
	Title      string    `yaml:"title"`
	Message    string    `yaml:"message"`
	Schedule   Schedule  `yaml:"schedule"`
	Animation  Animation `yaml:"animation"`
	Persistent *bool     `yaml:"persistent"`
	Timeout    Duration  `yaml:"timeout"`
	Sound      *bool     `yaml:"sound"`
	Actions    []Action  `yaml:"actions"`

	// Compiled is the validated schedule.
	Compiled schedule.Schedule `yaml:"-"`
}

type Schedule struct {
	Type string   `yaml:"type"`
	Time string   `yaml:"time"`
	Days []string `yaml:"days"`
}

type Animation struct {
	Type   string `yaml:"type"`
	StopAt string `yaml:"stop_at"`
}

type Action struct {
	ID     string `yaml:"id"`
	Label  string `yaml:"label"`
	Action string `yaml:"action"`
	// URL is required for open_url. Never log it.
	URL string `yaml:"url"`
	// Minutes is the snooze delay.
	Minutes int `yaml:"minutes"`
	// Acknowledge controls whether open_url also closes the reminder.
	Acknowledge *bool `yaml:"acknowledge"`
}

// IsEnabled reports whether the event is active (default true).
func (e *Event) IsEnabled() bool { return boolOr(e.Enabled, true) }

// IsPersistent reports whether the reminder stays until acted on (default true).
func (e *Event) IsPersistent() bool { return boolOr(e.Persistent, true) }

// SoundOn resolves the per-event override against the global setting.
func (e *Event) SoundOn(a Appearance) bool { return boolOr(e.Sound, boolOr(a.SoundEnabled, true)) }

// Action returns the action with the given id.
func (e *Event) Action(id string) (Action, bool) {
	for _, a := range e.Actions {
		if a.ID == id {
			return a, true
		}
	}
	return Action{}, false
}

// CancelAction is what Escape / closing the window does: the first snooze
// action if there is one (an accidental key press must not lose a reminder),
// otherwise the first dismiss action. The empty string means "acknowledge".
func (e *Event) CancelAction() string {
	for _, kind := range []string{ActionSnooze, ActionDismiss} {
		for _, a := range e.Actions {
			if a.Action == kind {
				return a.ID
			}
		}
	}
	return ""
}

// AcknowledgesOnOpen reports whether open_url closes the reminder (default true).
func (a Action) AcknowledgesOnOpen() bool { return boolOr(a.Acknowledge, true) }

// CatchUp is how late a missed reminder may still be shown (0 = never).
func (a App) CatchUp() time.Duration {
	if a.CatchUpWindow == nil {
		return DefaultCatchUpWindow
	}
	return a.CatchUpWindow.D()
}

func (a App) StartsMinimized() bool { return boolOr(a.StartMinimized, true) }
func (a App) TrayEnabled() bool     { return boolOr(a.Tray, true) }
func (a Appearance) OnTop() bool    { return boolOr(a.AlwaysOnTop, true) }
func (a Appearance) SoundOn() bool  { return boolOr(a.SoundEnabled, true) }
func boolOr(p *bool, def bool) bool {
	if p == nil {
		return def
	}
	return *p
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Parse(f)
}

// Parse decodes and validates a configuration. Unknown keys are rejected so
// that typos ("titel:") surface as errors rather than silently ignored fields.
func Parse(r io.Reader) (*Config, error) {
	data, err := io.ReadAll(io.LimitReader(r, 1<<20))
	if err != nil {
		return nil, err
	}
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var c Config
	if err := dec.Decode(&c); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, errors.New("configuration is empty")
		}
		return nil, fmt.Errorf("invalid YAML: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	if c.App.Name == "" {
		c.App.Name = DefaultAppName
	}
	if c.App.Timezone == "" {
		c.App.Timezone = DefaultTimezone
	}
	if c.App.CatchUpWindow == nil {
		d := Duration(DefaultCatchUpWindow)
		c.App.CatchUpWindow = &d
	}
	if c.Appearance.Character == "" {
		c.Appearance.Character = DefaultCharacter
	}
	if c.Appearance.AnimationSpeed == "" {
		c.Appearance.AnimationSpeed = DefaultAnimationSpeed
	}
	if c.Appearance.Renderer == "" {
		c.Appearance.Renderer = RendererAuto
	}
	for i := range c.Events {
		e := &c.Events[i]
		if e.Animation.Type == "" {
			e.Animation.Type = AnimationFlyAcross
		}
		if e.Animation.StopAt == "" {
			e.Animation.StopAt = DefaultStopAt
		}
		if e.Timeout == 0 {
			e.Timeout = Duration(DefaultTimeout)
		}
		if len(e.Actions) == 0 {
			e.Actions = []Action{{ID: DefaultDismissActionID, Label: "Got it", Action: ActionDismiss}}
		}
		for j := range e.Actions {
			a := &e.Actions[j]
			if a.Action == ActionSnooze && a.Minutes == 0 {
				a.Minutes = DefaultSnoozeMinutes
			}
		}
	}
}

// ValidationError lists every problem found in a configuration.
type ValidationError struct{ Problems []string }

func (v *ValidationError) Error() string {
	if len(v.Problems) == 1 {
		return "invalid configuration: " + v.Problems[0]
	}
	return "invalid configuration:\n  - " + strings.Join(v.Problems, "\n  - ")
}

type problems []string

func (p *problems) addf(format string, args ...any) { *p = append(*p, fmt.Sprintf(format, args...)) }

func oneOf(v string, allowed ...string) bool {
	for _, a := range allowed {
		if v == a {
			return true
		}
	}
	return false
}

func (c *Config) validate() error {
	var p problems

	loc, err := LoadLocation(c.App.Timezone)
	if err != nil {
		p.addf("app.timezone: %v", err)
	} else {
		c.Location = loc
	}
	if d := c.App.CatchUp(); d < 0 || d > 24*time.Hour {
		p.addf("app.catch_up_window: must be between 0s and 24h")
	}
	if !oneOf(c.Appearance.Character, "bird") {
		p.addf("appearance.character: unknown character %q (available: bird)", c.Appearance.Character)
	}
	if !oneOf(c.Appearance.AnimationSpeed, "slow", "normal", "fast") {
		p.addf("appearance.animation_speed: must be slow, normal or fast")
	}
	if !oneOf(c.Appearance.Renderer, RendererAuto, RendererOverlay, RendererNotification) {
		p.addf("appearance.renderer: must be auto, overlay or notification")
	}

	seen := map[string]int{}
	for i := range c.Events {
		e := &c.Events[i]
		path := eventPath(i, e.ID)
		switch {
		case e.ID == "":
			p.addf("%s.id: is required", path)
		case !idPattern.MatchString(e.ID):
			p.addf("%s.id: must be lowercase letters, digits, '-' or '_' (max 64 chars)", path)
		default:
			if prev, dup := seen[e.ID]; dup {
				p.addf("%s.id: duplicate id %q (also used by events[%d])", path, e.ID, prev)
			}
			seen[e.ID] = i
		}
		validateEvent(&p, path, e, c.Location)
	}

	if len(p) > 0 {
		return &ValidationError{Problems: p}
	}
	return nil
}

func eventPath(i int, id string) string {
	if id == "" {
		return fmt.Sprintf("events[%d]", i)
	}
	return fmt.Sprintf("events[%d] (%s)", i, id)
}

func validateEvent(p *problems, path string, e *Event, loc *time.Location) {
	if strings.TrimSpace(e.Title) == "" {
		p.addf("%s.title: is required", path)
	} else if len(e.Title) > maxTitleLen {
		p.addf("%s.title: must be at most %d characters", path, maxTitleLen)
	}
	if len(e.Message) > maxMessageLen {
		p.addf("%s.message: must be at most %d characters", path, maxMessageLen)
	}
	// With an invalid app.timezone, still validate the schedule's shape
	// (against UTC) so every problem is reported in one pass.
	zone := loc
	if zone == nil {
		zone = time.UTC
	}
	s, err := schedule.New(e.Schedule.Type, e.Schedule.Time, e.Schedule.Days, zone)
	if err != nil {
		p.addf("%s.schedule: %v", path, err)
	} else if loc != nil {
		e.Compiled = s
	}
	if !oneOf(e.Animation.Type, AnimationFlyAcross, AnimationNone) {
		p.addf("%s.animation.type: must be %s or %s", path, AnimationFlyAcross, AnimationNone)
	}
	if !oneOf(e.Animation.StopAt, DefaultStopAt) {
		p.addf("%s.animation.stop_at: must be %s", path, DefaultStopAt)
	}
	if d := e.Timeout.D(); d < 5*time.Second || d > 24*time.Hour {
		p.addf("%s.timeout: must be between 5s and 24h", path)
	}
	if len(e.Actions) > MaxActionsPerEvent {
		p.addf("%s.actions: at most %d actions are allowed", path, MaxActionsPerEvent)
	}
	ids := map[string]bool{}
	for j := range e.Actions {
		validateAction(p, fmt.Sprintf("%s.actions[%d]", path, j), &e.Actions[j], ids)
	}
}

func validateAction(p *problems, path string, a *Action, ids map[string]bool) {
	if a.ID != "" {
		path = fmt.Sprintf("%s (%s)", path, a.ID)
	}
	switch {
	case a.ID == "":
		p.addf("%s.id: is required", path)
	case !idPattern.MatchString(a.ID):
		p.addf("%s.id: must be lowercase letters, digits, '-' or '_'", path)
	case ids[a.ID]:
		p.addf("%s.id: duplicate action id %q in this event", path, a.ID)
	default:
		ids[a.ID] = true
	}
	if strings.TrimSpace(a.Label) == "" {
		p.addf("%s.label: is required", path)
	} else if len(a.Label) > maxLabelLen {
		p.addf("%s.label: must be at most %d characters", path, maxLabelLen)
	}

	switch a.Action {
	case ActionDismiss:
		if a.URL != "" || a.Minutes != 0 || a.Acknowledge != nil {
			p.addf("%s: dismiss does not take url, minutes or acknowledge", path)
		}
	case ActionSnooze:
		if a.Minutes < 1 || a.Minutes > MaxSnoozeMinutes {
			p.addf("%s.minutes: must be between 1 and %d", path, MaxSnoozeMinutes)
		}
		if a.URL != "" || a.Acknowledge != nil {
			p.addf("%s: snooze does not take url or acknowledge", path)
		}
	case ActionOpenURL:
		if err := ValidateURL(a.URL); err != nil {
			p.addf("%s.url: %v", path, err)
		}
		if a.Minutes != 0 {
			p.addf("%s: open_url does not take minutes", path)
		}
	case "":
		p.addf("%s.action: is required (dismiss, snooze or open_url)", path)
	default:
		p.addf("%s.action: unknown action %q (allowed: dismiss, snooze, open_url)", path, a.Action)
	}
}

// ValidateURL accepts only absolute http(s) URLs with a host. The error never
// contains the URL itself.
func ValidateURL(raw string) error {
	if strings.TrimSpace(raw) == "" {
		return errors.New("is required for open_url")
	}
	if strings.ContainsAny(raw, " \t\r\n") || strings.HasPrefix(raw, "-") {
		return errors.New("must not contain whitespace or start with '-'")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return errors.New("is not a valid URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("must use http or https")
	}
	if u.Host == "" {
		return errors.New("must include a host")
	}
	return nil
}

// LoadLocation resolves "Local" (or "") and IANA names.
func LoadLocation(name string) (*time.Location, error) {
	if name == "" || strings.EqualFold(name, "local") {
		return time.Local, nil
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return nil, fmt.Errorf("unknown time zone %q (use Local or an IANA name like Europe/Berlin)", name)
	}
	return loc, nil
}

// Duration is a time.Duration that unmarshals from strings like "15m".
type Duration time.Duration

func (d Duration) D() time.Duration { return time.Duration(d) }

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return fmt.Errorf("line %d: duration must be a string like \"15m\" or \"90s\"", n.Line)
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("line %d: invalid duration %q (examples: 90s, 15m, 1h)", n.Line, s)
	}
	*d = Duration(v)
	return nil
}
