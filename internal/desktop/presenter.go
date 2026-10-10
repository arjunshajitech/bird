package desktop

import (
	"context"
	"errors"
	"log/slog"
)

// Choice is what the user did.
type Choice struct {
	ActionID string // set when a button was chosen
	Cancel   bool   // Escape, window closed, notification dismissed
}

// Decide is called by a presenter for every user choice. It returns whether
// the reminder should close (false keeps it on screen, e.g. after opening a
// link that does not acknowledge). It may block briefly.
type Decide func(Choice) (close bool)

// Presenter puts a reminder on screen and reports the user's choices.
//
// Present blocks until the reminder is closed. It returns:
//   - nil when Decide returned close=true;
//   - ctx.Err() when the context ended first (timeout or shutdown); the
//     reminder has been removed from screen;
//   - any other error when the reminder could not be shown or the renderer
//     died before a closing decision. Callers may then try another presenter.
type Presenter interface {
	Name() string
	Present(ctx context.Context, r Reminder, decide Decide) error
}

// ErrUnavailable means the presenter cannot work in this session at all.
var ErrUnavailable = errors.New("presenter unavailable")

// Chain tries presenters in order until one manages to show the reminder.
type Chain struct {
	Presenters []Presenter
	Log        *slog.Logger
}

func (c *Chain) Name() string {
	name := ""
	for i, p := range c.Presenters {
		if i > 0 {
			name += " → "
		}
		name += p.Name()
	}
	return name
}

func (c *Chain) Present(ctx context.Context, r Reminder, decide Decide) error {
	err := ErrUnavailable
	for _, p := range c.Presenters {
		err = p.Present(ctx, r, decide)
		if err == nil || ctx.Err() != nil {
			return err
		}
		if c.Log != nil {
			c.Log.Warn("presenter failed, trying fallback", "presenter", p.Name(), "event", r.EventID, "err", err)
		}
	}
	return err
}
