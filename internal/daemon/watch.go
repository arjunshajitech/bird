package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

const reloadDebounce = 300 * time.Millisecond

// watchConfig signals `out` when the config file changes.
//
// It watches the parent directory, not the file: editors commonly save by
// writing a temp file and renaming it over the original, which would silently
// orphan a watch on the file itself. Bursts of events are debounced into one
// reload.
func watchConfig(ctx context.Context, path string, out chan<- struct{}, log *slog.Logger) error {
	dir, name := filepath.Dir(path), filepath.Base(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	if err := w.Add(dir); err != nil {
		w.Close()
		return err
	}
	go func() {
		defer w.Close()
		debounce := time.NewTimer(time.Hour)
		debounce.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case ev, ok := <-w.Events:
				if !ok {
					return
				}
				if filepath.Base(ev.Name) == name && ev.Op&(fsnotify.Write|fsnotify.Create|fsnotify.Rename|fsnotify.Remove) != 0 {
					debounce.Reset(reloadDebounce)
				}
			case err, ok := <-w.Errors:
				if !ok {
					return
				}
				log.Warn("config watcher error", "err", err)
			case <-debounce.C:
				select {
				case out <- struct{}{}:
				default: // a reload is already pending
				}
			}
		}
	}()
	return nil
}
