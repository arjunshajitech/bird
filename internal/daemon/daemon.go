// Package daemon runs Bird in the background: it owns the event engine,
// persists state, watches the configuration and hands due reminders to the
// presenter.
//
// Concurrency model: one goroutine (the loop in Run) owns the Engine and all
// state; everything else talks to it through channels. There are no locks
// around scheduling logic.
package daemon

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/bird-app/bird/internal/assets"
	"github.com/bird-app/bird/internal/config"
	"github.com/bird-app/bird/internal/desktop"
	"github.com/bird-app/bird/internal/events"
	"github.com/bird-app/bird/internal/platform"
)

// maxSleep caps how long the loop sleeps between wall-clock checks.
//
// Go timers run on the monotonic clock, which stops while the machine is
// suspended. A single timer armed for 11:00 at 09:00 would fire hours late
// after a laptop sleeps through 11:00. Re-checking the wall clock at least
// this often bounds that error (and handles NTP/manual clock changes) at a
// negligible cost: one cheap computation every 30 seconds, no busy loop.
const maxSleep = 30 * time.Second

// Options configure a Daemon.
type Options struct {
	ConfigPath string
	StateDir   string
	Session    platform.Session
	Log        *slog.Logger
	Now        func() time.Time // injectable clock; defaults to time.Now
	// Presenter overrides presenter selection (tests, --renderer flags).
	Presenter desktop.Presenter
	// OpenURL overrides how links are opened (tests).
	OpenURL func(ctx context.Context, url string) error
	// NoTray disables the tray regardless of configuration.
	NoTray bool
}

type response struct {
	occ   events.Occurrence
	resp  events.Response
	reply chan bool
}

// Daemon is the long-running reminder service.
type Daemon struct {
	opt       Options
	log       *slog.Logger
	engine    *events.Engine
	statePath string
	dispatch  *dispatcher
	responses chan response
	reloads   chan struct{}
	previews  chan string
	quit      chan struct{}
	tray      *tray
	cfgErr    error // last configuration error, shown in the tray
}

// Run starts the daemon and blocks until ctx is cancelled or the user quits
// from the tray. It returns an error only for unrecoverable startup problems.
func Run(ctx context.Context, opt Options) error {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.OpenURL == nil {
		opt.OpenURL = platform.Open
	}
	log := opt.Log

	release, err := platform.Lock(filepath.Join(platform.RuntimeDir(), "bird.lock"))
	if err != nil {
		return err
	}
	defer release()

	d := &Daemon{
		opt:       opt,
		log:       log,
		statePath: filepath.Join(opt.StateDir, "state.json"),
		responses: make(chan response),
		reloads:   make(chan struct{}, 1),
		previews:  make(chan string, 1),
		quit:      make(chan struct{}),
	}

	state, err := events.LoadState(d.statePath)
	if err != nil {
		if state == nil {
			return fmt.Errorf("load state: %w", err)
		}
		log.Warn("starting with empty state", "err", err)
	}

	// An invalid or missing config must not stop the daemon: it would
	// crash-loop under systemd. Run with no events and wait for a fix.
	cfg, err := config.Load(opt.ConfigPath)
	if err != nil {
		d.cfgErr = err
		log.Error("configuration not loaded; waiting for a valid file", "path", opt.ConfigPath, "err", err)
		cfg = emptyConfig()
	}
	d.engine = events.NewEngine(cfg, state, log)

	presenter := opt.Presenter
	if presenter == nil {
		presenter = d.selectPresenter(cfg)
	}
	d.dispatch = newDispatcher(presenter, d.requestResolve, log)

	log.Info("bird started", "session", opt.Session.String(), "presenter", presenter.Name(),
		"config", opt.ConfigPath, "state", d.statePath, "events", len(cfg.Events))

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := watchConfig(runCtx, opt.ConfigPath, d.reloads, log); err != nil {
		log.Warn("config hot reload unavailable; use SIGHUP, the tray or a restart", "err", err)
	}
	// SIGHUP reloads too (`systemctl --user reload bird`).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for {
			select {
			case <-runCtx.Done():
				return
			case <-hup:
				select {
				case d.reloads <- struct{}{}:
				default:
				}
			}
		}
	}()
	if !opt.NoTray && cfg.App.TrayEnabled() && opt.Session.HasDisplay() {
		d.tray = startTray(d, log)
		defer d.tray.stop()
	}
	if d.cfgErr != nil {
		d.notifyConfigError(runCtx)
	} else if !cfg.App.StartsMinimized() {
		go d.notifyStarted(runCtx, cfg)
	}

	dispatchDone := make(chan struct{})
	go func() { d.dispatch.run(runCtx); close(dispatchDone) }()

	d.loop(runCtx)

	cancel()
	<-dispatchDone // overlays are told to close before we persist and exit
	d.save()
	log.Info("bird stopped")
	return nil
}

func (d *Daemon) loop(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	lastStatus := ""
	for {
		now := d.opt.Now()
		triggers := d.engine.Tick(now)
		d.save() // persist before presenting: a crash must not cause a duplicate
		for _, tr := range triggers {
			d.dispatch.enqueue(job{trigger: tr, reminder: reminderFor(d.engine.Config(), tr)})
		}
		if s := d.status(now); s != lastStatus {
			lastStatus = s
			d.tray.setStatus(s)
		}

		sleep := maxSleep
		if next := d.engine.NextWake(now); !next.IsZero() && next.Sub(now) < sleep {
			sleep = next.Sub(now)
		}
		timer.Reset(sleep)

		select {
		case <-ctx.Done():
			return
		case <-d.quit:
			return
		case <-timer.C:
		case <-d.reloads:
			d.reload(ctx)
		case id := <-d.previews:
			d.preview(id)
		case r := <-d.responses:
			r.reply <- d.handleResponse(ctx, r.occ, r.resp)
		}
		// Go >= 1.23 timers: Reset at the top of the loop discards any
		// stale expiry, so no manual draining is needed.
	}
}

// requestResolve is called from the dispatcher goroutine and waits for the
// loop to apply the response.
func (d *Daemon) requestResolve(ctx context.Context, occ events.Occurrence, r events.Response) bool {
	reply := make(chan bool, 1)
	select {
	case d.responses <- response{occ: occ, resp: r, reply: reply}:
	case <-ctx.Done():
		return true
	}
	select {
	case close := <-reply:
		return close
	case <-ctx.Done():
		return true
	}
}

func (d *Daemon) handleResponse(ctx context.Context, occ events.Occurrence, r events.Response) bool {
	log := d.log.With("event", occ.EventID, "response", r.Kind.String())
	if r.ActionID != "" {
		log = log.With("action", r.ActionID)
	}
	out, err := d.engine.Resolve(occ, r, d.opt.Now())
	d.save()
	switch {
	case errors.Is(err, events.ErrStale):
		log.Debug("ignored stale response")
	case err != nil:
		log.Warn("could not apply response", "err", err)
	default:
		log.Info("reminder " + out.Summary)
	}
	if out.OpenURL != "" {
		go func(url string) {
			if err := d.opt.OpenURL(ctx, url); err != nil {
				// The URL itself is never logged.
				log.Error("could not open link", "err", err)
			}
		}(out.OpenURL)
	}
	return out.Close
}

func (d *Daemon) reload(ctx context.Context) {
	cfg, err := config.Load(d.opt.ConfigPath)
	if err != nil {
		d.cfgErr = err
		d.log.Error("configuration reload rejected; keeping previous configuration", "err", err)
		d.notifyConfigError(ctx)
		return
	}
	d.cfgErr = nil
	if d.opt.Presenter == nil && cfg.Appearance.Renderer != d.engine.Config().Appearance.Renderer {
		d.dispatch.setPresenter(d.selectPresenter(cfg))
	}
	d.engine.SetConfig(cfg)
	d.save()
	d.log.Info("configuration reloaded", "events", len(cfg.Events))
}

// preview shows an event's reminder without touching its state. An empty id
// means the next upcoming event.
func (d *Daemon) preview(id string) {
	cfg := d.engine.Config()
	if id == "" {
		up := d.engine.Upcoming(d.opt.Now())
		if len(up) == 0 {
			return
		}
		id = up[0].EventID
	}
	for _, ev := range cfg.Events {
		if ev.ID == id {
			tr := events.Trigger{Occurrence: events.Occurrence{EventID: ev.ID, Scheduled: d.opt.Now()}, Event: ev}
			d.dispatch.enqueue(job{trigger: tr, reminder: reminderFor(cfg, tr), preview: true})
			return
		}
	}
}

func (d *Daemon) save() {
	if !d.engine.TakeDirty() {
		return
	}
	if err := events.SaveState(d.statePath, d.engine.State()); err != nil {
		d.log.Error("could not save state", "path", d.statePath, "err", err)
	}
}

func (d *Daemon) status(now time.Time) string {
	if d.cfgErr != nil {
		return "Configuration error — see logs"
	}
	up := d.engine.Upcoming(now)
	if len(up) == 0 {
		return "No upcoming reminders"
	}
	when := up[0].At.Format("15:04")
	if y1, m1, d1 := up[0].At.Date(); y1 != now.Year() || m1 != now.Month() || d1 != now.Day() {
		when = up[0].At.Format("Mon 15:04")
	}
	s := fmt.Sprintf("Next: %s at %s", up[0].Title, when)
	if up[0].Snoozed {
		s += " (snoozed)"
	}
	return s
}

// selectPresenter picks renderers according to appearance.renderer.
func (d *Daemon) selectPresenter(cfg *config.Config) desktop.Presenter {
	notify := &desktop.Notification{IconPath: assets.IconFile(platform.CacheDir()), Log: d.log}
	if cfg.Appearance.Renderer == config.RendererNotification {
		return notify
	}
	path, err := desktop.FindOverlay()
	if err != nil {
		d.log.Warn("bird-overlay not found; animated reminders disabled", "err", err)
	}
	overlay := &desktop.Overlay{Path: path, Session: d.opt.Session, Log: d.log}
	if cfg.Appearance.Renderer == config.RendererOverlay {
		return overlay
	}
	return &desktop.Chain{Presenters: []desktop.Presenter{overlay, notify}, Log: d.log}
}

func (d *Daemon) notifyConfigError(ctx context.Context) {
	msg := "Bird could not load its configuration. Run `bird --check-config` for details."
	if errors.Is(d.cfgErr, os.ErrNotExist) {
		msg = "No configuration found at " + d.opt.ConfigPath + ". Copy config.example.yaml there."
	}
	go d.notifyOnce(ctx, "Bird configuration problem", msg)
}

func (d *Daemon) notifyStarted(ctx context.Context, cfg *config.Config) {
	d.notifyOnce(ctx, cfg.App.Name+" is running", fmt.Sprintf("%d reminder(s) scheduled.", len(cfg.Events)))
}

// notifyOnce shows a plain informational notification (no actions).
func (d *Daemon) notifyOnce(ctx context.Context, title, msg string) {
	if !d.opt.Session.HasDisplay() {
		return
	}
	n := &desktop.Notification{IconPath: assets.IconFile(platform.CacheDir())}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_ = n.Present(ctx, desktop.Reminder{AppName: "Bird", Title: title, Message: msg}, func(desktop.Choice) bool { return true })
}

func emptyConfig() *config.Config {
	c := &config.Config{Location: time.Local}
	c.App.Name = config.DefaultAppName
	return c
}

// PreviewCancelled is returned by Preview when the user pressed Escape or
// closed the window.
const PreviewCancelled = "(cancelled)"

// Preview shows one event's reminder immediately, without the daemon and
// without touching state or performing actions. It returns the chosen
// action ID, PreviewCancelled, or "" if ctx ended first. Used by `bird --preview`.
func Preview(ctx context.Context, opt Options, id string) (string, error) {
	cfg, err := config.Load(opt.ConfigPath)
	if err != nil {
		return "", err
	}
	var ev *config.Event
	for i := range cfg.Events {
		if cfg.Events[i].ID == id {
			ev = &cfg.Events[i]
		}
	}
	if ev == nil {
		return "", fmt.Errorf("no event with id %q in %s", id, opt.ConfigPath)
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	d := &Daemon{opt: opt, log: opt.Log}
	presenter := opt.Presenter
	if presenter == nil {
		presenter = d.selectPresenter(cfg)
	}
	tr := events.Trigger{Occurrence: events.Occurrence{EventID: ev.ID, Scheduled: opt.Now()}, Event: *ev}
	chosen := ""
	err = presenter.Present(ctx, reminderFor(cfg, tr), func(c desktop.Choice) bool {
		chosen = c.ActionID
		if c.Cancel {
			chosen = PreviewCancelled
		}
		return true
	})
	return chosen, err
}
