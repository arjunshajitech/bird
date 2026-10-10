package daemon

import (
	"context"
	"errors"
	"log/slog"
	"sync"

	"github.com/bird-app/bird/internal/config"
	"github.com/bird-app/bird/internal/desktop"
	"github.com/bird-app/bird/internal/events"
)

// job is one reminder waiting to be shown.
type job struct {
	trigger  events.Trigger
	reminder desktop.Reminder
	preview  bool // tray "preview": no state changes, no actions performed
}

// dispatcher shows reminders one at a time, in order. Reminders are modal-ish
// and centred, so stacking several on top of each other would be unreadable.
//
// Enqueue never blocks, so the scheduler loop can never deadlock against a
// presenter that is waiting for the loop to resolve a choice.
type dispatcher struct {
	presenterMu sync.Mutex
	presenter   desktop.Presenter
	resolve     func(ctx context.Context, occ events.Occurrence, r events.Response) (close bool)
	log         *slog.Logger

	mu     sync.Mutex
	queue  []job
	queued map[string]bool // event IDs queued or showing
	wake   chan struct{}
}

func newDispatcher(p desktop.Presenter, resolve func(context.Context, events.Occurrence, events.Response) bool, log *slog.Logger) *dispatcher {
	return &dispatcher{presenter: p, resolve: resolve, log: log, queued: map[string]bool{}, wake: make(chan struct{}, 1)}
}

// setPresenter swaps the renderer (appearance.renderer changed on reload).
// The reminder currently on screen, if any, keeps its presenter.
func (d *dispatcher) setPresenter(p desktop.Presenter) {
	d.presenterMu.Lock()
	d.presenter = p
	d.presenterMu.Unlock()
}

func (d *dispatcher) enqueue(j job) {
	d.mu.Lock()
	id := j.trigger.Occurrence.EventID
	if j.preview && d.queued[id] {
		d.mu.Unlock()
		return
	}
	d.queued[id] = true
	d.queue = append(d.queue, j)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *dispatcher) next() (job, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return job{}, false
	}
	j := d.queue[0]
	d.queue = d.queue[1:]
	return j, true
}

func (d *dispatcher) done(id string) {
	d.mu.Lock()
	delete(d.queued, id)
	d.mu.Unlock()
}

// run processes the queue until ctx ends. Reminders still queued or on
// screen at shutdown stay "active" in the persisted state and are re-shown on
// the next start if still recent.
func (d *dispatcher) run(ctx context.Context) {
	for {
		j, ok := d.next()
		if !ok {
			select {
			case <-ctx.Done():
				return
			case <-d.wake:
				continue
			}
		}
		d.show(ctx, j)
		d.done(j.trigger.Occurrence.EventID)
		if ctx.Err() != nil {
			return
		}
	}
}

func (d *dispatcher) show(ctx context.Context, j job) {
	occ := j.trigger.Occurrence
	ev := j.trigger.Event
	log := d.log.With("event", occ.EventID)

	showCtx, cancel := ctx, context.CancelFunc(func() {})
	if !ev.IsPersistent() {
		showCtx, cancel = context.WithTimeout(ctx, ev.Timeout.D())
	}
	defer cancel()

	decide := func(c desktop.Choice) bool {
		if j.preview {
			return true
		}
		r := events.Response{Kind: events.Chose, ActionID: c.ActionID}
		if c.Cancel {
			r = events.Response{Kind: events.Cancelled}
		}
		return d.resolve(ctx, occ, r)
	}

	d.presenterMu.Lock()
	presenter := d.presenter
	d.presenterMu.Unlock()

	log.Info("showing reminder", "via", presenter.Name(), "snoozed", occ.Snoozed, "preview", j.preview)
	err := presenter.Present(showCtx, j.reminder, decide)
	switch {
	case err == nil, j.preview:
		if err != nil && ctx.Err() == nil {
			log.Warn("preview failed", "err", err)
		}
	case ctx.Err() != nil:
		// Shutting down: leave the occurrence active for the next run.
	case errors.Is(err, context.DeadlineExceeded):
		d.resolve(ctx, occ, events.Response{Kind: events.TimedOut})
	default:
		log.Error("could not show reminder", "err", err)
		d.resolve(ctx, occ, events.Response{Kind: events.Failed})
	}
}

// reminderFor builds the renderer's view of a trigger.
func reminderFor(cfg *config.Config, tr events.Trigger) desktop.Reminder {
	ev := tr.Event
	r := desktop.Reminder{
		EventID:     ev.ID,
		Title:       ev.Title,
		Message:     ev.Message,
		Snoozed:     tr.Occurrence.Snoozed,
		Persistent:  ev.IsPersistent(),
		Character:   cfg.Appearance.Character,
		Sound:       ev.SoundOn(cfg.Appearance),
		AlwaysOnTop: cfg.Appearance.OnTop(),
		Speed:       cfg.Appearance.AnimationSpeed,
		Animation:   ev.Animation.Type,
		AppName:     cfg.App.Name,
	}
	for _, a := range ev.Actions {
		r.Actions = append(r.Actions, desktop.Button{ID: a.ID, Label: a.Label, Kind: a.Action})
	}
	return r
}
