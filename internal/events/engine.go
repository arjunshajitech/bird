// Package events implements the reminder state machine: which occurrences are
// due, deduplication, snoozing, missed-event catch-up and action handling.
//
// The Engine never reads the clock, sleeps or touches the UI. Every method
// takes `now`, so behaviour is deterministic and fully unit-testable. It is not
// safe for concurrent use; the daemon owns it from a single goroutine.
//
// Lifecycle of one occurrence:
//
//	scheduled ──Tick──▶ active ──Resolve(dismiss|open_url|timeout)──▶ done
//	                      │  ▲
//	     Resolve(snooze)  │  │ Tick (snooze due)
//	     Resolve(failed)  ▼  │
//	                    snoozed
package events

import (
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"time"

	"github.com/bird-app/bird/internal/config"
)

const (
	// RetryDelay is how long to wait before re-showing a reminder whose UI
	// could not be displayed (e.g. no graphical session yet).
	RetryDelay = time.Minute
	// MaxRetries bounds automatic re-attempts (about an hour).
	MaxRetries = 60
)

// ErrStale is returned when a response refers to an occurrence that is no
// longer active (already handled, superseded, or from a previous process).
var ErrStale = errors.New("response for an occurrence that is no longer active")

// Trigger is an occurrence that should be presented now.
type Trigger struct {
	Occurrence Occurrence
	Event      config.Event
}

// ResponseKind says how a reminder ended.
type ResponseKind int

const (
	// Chose: the user picked an action button.
	Chose ResponseKind = iota
	// Cancelled: Escape, window closed, or notification dismissed.
	Cancelled
	// TimedOut: a non-persistent reminder expired.
	TimedOut
	// Failed: the UI could not be shown at all.
	Failed
)

func (k ResponseKind) String() string {
	return [...]string{"chose", "cancelled", "timed-out", "failed"}[k]
}

// Response is what the presenter reports back.
type Response struct {
	Kind     ResponseKind
	ActionID string // for Chose
}

// Outcome tells the daemon what to do after a response.
type Outcome struct {
	// Close removes the reminder from screen. If false the reminder stays
	// (open_url with acknowledge: false, or an unknown action id).
	Close bool
	// OpenURL, if set, must be opened in the default browser. Never log it.
	OpenURL string
	// Summary is a log-safe description of the transition.
	Summary string
}

// Upcoming describes the next time an event will show.
type Upcoming struct {
	EventID string
	Title   string
	At      time.Time
	Snoozed bool
}

// Engine is the reminder state machine.
type Engine struct {
	cfg      *config.Config
	events   map[string]*config.Event
	state    *State
	inFlight map[string]bool // event IDs being presented by this process
	dirty    bool
	log      *slog.Logger
}

// NewEngine creates an engine over a validated config and loaded state.
func NewEngine(cfg *config.Config, state *State, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	e := &Engine{state: state, inFlight: map[string]bool{}, log: log}
	e.SetConfig(cfg)
	return e
}

// SetConfig swaps in a new validated configuration. State is keyed by event
// ID, so unchanged events keep their dedup watermark and snoozes across
// reloads. State for removed events is dropped; disabled events lose any
// pending snooze.
func (e *Engine) SetConfig(cfg *config.Config) {
	e.cfg = cfg
	e.events = make(map[string]*config.Event, len(cfg.Events))
	for i := range cfg.Events {
		ev := &cfg.Events[i]
		e.events[ev.ID] = ev
	}
	for id, st := range e.state.Events {
		ev, ok := e.events[id]
		switch {
		case !ok && !e.inFlight[id]:
			delete(e.state.Events, id)
			e.dirty = true
		case ok && !ev.IsEnabled() && st.Snooze != nil:
			st.Snooze = nil
			e.dirty = true
		}
	}
}

// Config returns the active configuration.
func (e *Engine) Config() *config.Config { return e.cfg }

// State exposes the state for persistence.
func (e *Engine) State() *State { return e.state }

// TakeDirty reports whether state changed since the last call and resets the flag.
func (e *Engine) TakeDirty() bool {
	d := e.dirty
	e.dirty = false
	return d
}

func (e *Engine) catchUp() time.Duration { return e.cfg.App.CatchUp() }

// Tick returns the occurrences due at `now` and marks them active. The caller
// must persist State (TakeDirty) before presenting them.
func (e *Engine) Tick(now time.Time) []Trigger {
	var out []Trigger
	for i := range e.cfg.Events {
		ev := &e.cfg.Events[i]
		if !ev.IsEnabled() || e.inFlight[ev.ID] || ev.Compiled == nil {
			continue
		}
		if occ, ok := e.due(ev, now); ok {
			st := e.state.get(ev.ID)
			active := &Active{Occurrence: occ, Since: now}
			if occ.Snoozed && st.Snooze != nil {
				active.Retries = st.Snooze.Retries
				st.Snooze = nil
			}
			st.Active = active
			e.inFlight[ev.ID] = true
			e.dirty = true
			out = append(out, Trigger{Occurrence: occ, Event: *ev})
		}
	}
	return out
}

// due decides whether one event has an occurrence to present now.
// Priority: orphaned active reminder > new regular occurrence > due snooze.
// A new occurrence always wins over a snooze of an older one, which is then
// stale (nobody wants yesterday's stand-up reminder on top of today's).
func (e *Engine) due(ev *config.Event, now time.Time) (Occurrence, bool) {
	st := e.state.get(ev.ID)
	log := e.log.With("event", ev.ID)
	var pick *Occurrence

	// An Active entry that this process is not presenting was left behind by
	// a previous run (crash, logout, restart) while unanswered. Re-show it if
	// it is recent; it was never acknowledged, so this is not a duplicate.
	if st.Active != nil {
		if now.Sub(st.Active.Since) <= e.catchUp() {
			occ := st.Active.Occurrence
			pick = &occ
			log.Info("re-showing unanswered reminder from previous run")
		} else {
			log.Info("dropping unanswered reminder from previous run: too old")
		}
		st.Active = nil
		e.dirty = true
	}

	if prev := ev.Compiled.Prev(now); !prev.IsZero() && prev.After(st.Watermark) {
		hadWatermark := !st.Watermark.IsZero()
		st.Watermark = prev
		e.dirty = true
		switch {
		case pick != nil:
			// Re-showing an older occurrence; the newer slot is consumed
			// rather than queued behind it.
			log.Info("occurrence superseded by reminder already being shown", "scheduled", prev)
		case now.Sub(prev) <= e.catchUp():
			pick = &Occurrence{EventID: ev.ID, Scheduled: prev}
		case hadWatermark:
			log.Info("missed occurrence skipped: past catch-up window", "scheduled", prev)
		}
		if pick != nil && st.Snooze != nil && !pick.Snoozed {
			log.Info("pending snooze superseded by newer occurrence")
			st.Snooze = nil
		}
	}

	if pick == nil && st.Snooze != nil && !now.Before(st.Snooze.Until) {
		if now.Sub(st.Snooze.Until) <= e.catchUp() {
			occ := st.Snooze.Occurrence
			occ.Snoozed = true
			pick = &occ
		} else {
			log.Info("snoozed reminder missed: past catch-up window", "due", st.Snooze.Until)
			st.Snooze = nil
			e.dirty = true
		}
	}

	if pick == nil {
		return Occurrence{}, false
	}
	return *pick, true
}

// NextWake returns the earliest instant after `now` at which Tick could
// return something, or the zero time if nothing is scheduled.
func (e *Engine) NextWake(now time.Time) time.Time {
	var next time.Time
	consider := func(t time.Time) {
		if !t.IsZero() && t.After(now) && (next.IsZero() || t.Before(next)) {
			next = t
		}
	}
	for _, u := range e.Upcoming(now) {
		consider(u.At)
	}
	return next
}

// Upcoming lists the next showing of every enabled event, soonest first.
// Events currently on screen are omitted.
func (e *Engine) Upcoming(now time.Time) []Upcoming {
	var out []Upcoming
	for i := range e.cfg.Events {
		ev := &e.cfg.Events[i]
		if !ev.IsEnabled() || e.inFlight[ev.ID] || ev.Compiled == nil {
			continue
		}
		u := Upcoming{EventID: ev.ID, Title: ev.Title, At: ev.Compiled.Next(now)}
		if st := e.state.Events[ev.ID]; st != nil && st.Snooze != nil && st.Snooze.Until.Before(u.At) {
			u.At, u.Snoozed = st.Snooze.Until, true
		}
		if !u.At.IsZero() {
			out = append(out, u)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	return out
}

// Resolve applies the user's (or presenter's) response to an active occurrence.
func (e *Engine) Resolve(occ Occurrence, resp Response, now time.Time) (Outcome, error) {
	st := e.state.Events[occ.EventID]
	if st == nil || st.Active == nil || !st.Active.Occurrence.Same(occ) {
		delete(e.inFlight, occ.EventID)
		return Outcome{Close: true, Summary: "stale"}, ErrStale
	}
	ev, ok := e.events[occ.EventID]
	if !ok {
		// Event removed from config while on screen.
		e.finish(occ.EventID, st)
		delete(e.state.Events, occ.EventID)
		return Outcome{Close: true, Summary: "event no longer configured"}, nil
	}

	switch resp.Kind {
	case TimedOut:
		e.finish(occ.EventID, st)
		return Outcome{Close: true, Summary: "timed out"}, nil

	case Failed:
		retries := st.Active.Retries + 1
		e.finish(occ.EventID, st)
		if retries > MaxRetries {
			return Outcome{Close: true, Summary: "dropped: display unavailable"}, nil
		}
		st.Snooze = &Snooze{Occurrence: occ, Until: now.Add(RetryDelay), Retries: retries}
		return Outcome{Close: true, Summary: fmt.Sprintf("display failed, retry %d in %s", retries, RetryDelay)}, nil

	case Cancelled:
		id := ev.CancelAction()
		if id == "" {
			e.finish(occ.EventID, st)
			return Outcome{Close: true, Summary: "closed"}, nil
		}
		resp = Response{Kind: Chose, ActionID: id}
	}

	action, ok := ev.Action(resp.ActionID)
	if !ok {
		return Outcome{Close: false, Summary: "unknown action"}, fmt.Errorf("event %s has no action %q", ev.ID, resp.ActionID)
	}
	switch action.Action {
	case config.ActionDismiss:
		e.finish(occ.EventID, st)
		return Outcome{Close: true, Summary: "dismissed via " + action.ID}, nil
	case config.ActionSnooze:
		e.finish(occ.EventID, st)
		until := now.Add(time.Duration(action.Minutes) * time.Minute)
		st.Snooze = &Snooze{Occurrence: Occurrence{EventID: occ.EventID, Scheduled: occ.Scheduled}, Until: until}
		return Outcome{Close: true, Summary: "snoozed until " + until.Format("15:04")}, nil
	case config.ActionOpenURL:
		out := Outcome{OpenURL: action.URL, Summary: "opened link via " + action.ID}
		if action.AcknowledgesOnOpen() {
			e.finish(occ.EventID, st)
			out.Close = true
		}
		return out, nil
	}
	return Outcome{}, fmt.Errorf("event %s action %s: unsupported kind %q", ev.ID, action.ID, action.Action)
}

func (e *Engine) finish(id string, st *EventState) {
	st.Active = nil
	delete(e.inFlight, id)
	e.dirty = true
}
