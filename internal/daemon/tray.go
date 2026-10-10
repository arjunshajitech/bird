package daemon

import (
	"context"
	"log/slog"
	"sync"

	"fyne.io/systray"

	"github.com/bird-app/bird/internal/assets"
	"github.com/bird-app/bird/internal/platform"
)

// tray is the optional StatusNotifierItem (AppIndicator) icon and menu.
// It talks to the D-Bus tray host directly (no GTK), so it adds no native
// dependencies to the daemon. Without a tray host (e.g. stock GNOME without
// the AppIndicator extension) it is simply invisible.
//
// All methods are safe on a nil *tray.
type tray struct {
	end      func()
	statusMu sync.Mutex
	status   *systray.MenuItem
	pending  string
}

func startTray(d *Daemon, log *slog.Logger) *tray {
	t := &tray{}
	start, end := systray.RunWithExternalLoop(func() {
		systray.SetIcon(assets.PNG(64))
		systray.SetTitle("Bird")
		systray.SetTooltip("Bird reminders")

		t.statusMu.Lock()
		t.status = systray.AddMenuItem("Starting…", "")
		t.status.Disable()
		if t.pending != "" {
			t.status.SetTitle(t.pending)
		}
		t.statusMu.Unlock()
		systray.AddSeparator()
		preview := systray.AddMenuItem("Preview next reminder", "Show the next reminder now without changing its schedule")
		openCfg := systray.AddMenuItem("Open configuration", d.opt.ConfigPath)
		reload := systray.AddMenuItem("Reload configuration", "")
		systray.AddSeparator()
		quit := systray.AddMenuItem("Quit Bird", "")

		go func() {
			for {
				select {
				case <-preview.ClickedCh:
					select {
					case d.previews <- "": // "" = the next upcoming event
					default:
					}
				case <-openCfg.ClickedCh:
					go func() {
						if err := platform.Open(context.Background(), d.opt.ConfigPath); err != nil {
							log.Warn("could not open configuration", "err", err)
						}
					}()
				case <-reload.ClickedCh:
					select {
					case d.reloads <- struct{}{}:
					default:
					}
				case <-quit.ClickedCh:
					close(d.quit)
					return
				}
			}
		}()
	}, func() {})
	start()
	t.end = end
	return t
}

func (t *tray) setStatus(s string) {
	if t == nil {
		return
	}
	t.statusMu.Lock()
	defer t.statusMu.Unlock()
	if t.status == nil {
		t.pending = s
		return
	}
	t.status.SetTitle(s)
	systray.SetTooltip("Bird — " + s)
}

func (t *tray) stop() {
	if t == nil || t.end == nil {
		return
	}
	t.end()
}
