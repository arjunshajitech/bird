package daemon

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/bird-app/bird/internal/desktop"
	"github.com/bird-app/bird/internal/events"
)

type fakePresenter struct {
	mu     sync.Mutex
	shown  []desktop.Reminder
	choice desktop.Choice
	seen   chan string
}

func (f *fakePresenter) Name() string { return "fake" }
func (f *fakePresenter) Present(ctx context.Context, r desktop.Reminder, decide desktop.Decide) error {
	f.mu.Lock()
	f.shown = append(f.shown, r)
	f.mu.Unlock()
	decide(f.choice)
	f.seen <- r.EventID
	return nil
}

const daemonYAML = `
app: {timezone: UTC, tray: false}
events:
  - id: standup
    title: Stand-up
    schedule: {type: daily, time: "11:00"}
    actions:
      - {id: join, label: Join, action: open_url, url: "https://meet.example.com/x"}
      - {id: snooze, label: Later, action: snooze, minutes: 5}
`

func setup(t *testing.T) (cfgPath, stateDir string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	cfgPath = filepath.Join(dir, "config", "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cfgPath, []byte(daemonYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	return cfgPath, filepath.Join(dir, "state")
}

func start(t *testing.T, opt Options) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, opt) }()
	return func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Errorf("Run: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("daemon did not stop")
		}
	}
}

func waitShown(t *testing.T, f *fakePresenter) string {
	t.Helper()
	select {
	case id := <-f.seen:
		return id
	case <-time.After(5 * time.Second):
		t.Fatal("reminder was not shown")
		return ""
	}
}

func TestDaemonShowsAndPersistsSnooze(t *testing.T) {
	cfgPath, stateDir := setup(t)
	now := time.Date(2026, 3, 16, 11, 0, 0, 0, time.UTC)
	fp := &fakePresenter{choice: desktop.Choice{ActionID: "snooze"}, seen: make(chan string, 4)}
	stop := start(t, Options{
		ConfigPath: cfgPath, StateDir: stateDir, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return now }, Presenter: fp, NoTray: true,
	})
	if id := waitShown(t, fp); id != "standup" {
		t.Fatalf("shown %q", id)
	}
	stop()

	st, err := events.LoadState(filepath.Join(stateDir, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	es := st.Events["standup"]
	if es == nil || es.Snooze == nil || !es.Snooze.Until.Equal(now.Add(5*time.Minute)) {
		t.Fatalf("persisted state = %+v", es)
	}
	if fp.shown[0].Title != "Stand-up" || len(fp.shown[0].Actions) != 2 {
		t.Errorf("reminder = %+v", fp.shown[0])
	}
}

func TestDaemonOpensURLAndDoesNotRefireAfterRestart(t *testing.T) {
	cfgPath, stateDir := setup(t)
	now := time.Date(2026, 3, 16, 11, 0, 0, 0, time.UTC)
	opened := make(chan string, 1)
	opt := Options{
		ConfigPath: cfgPath, StateDir: stateDir, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return now }, NoTray: true,
		OpenURL: func(_ context.Context, u string) error { opened <- u; return nil },
	}
	fp := &fakePresenter{choice: desktop.Choice{ActionID: "join"}, seen: make(chan string, 4)}
	opt.Presenter = fp
	stop := start(t, opt)
	waitShown(t, fp)
	select {
	case u := <-opened:
		if u != "https://meet.example.com/x" {
			t.Errorf("opened %q", u)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("URL not opened")
	}
	stop()

	// Restart a minute later: the acknowledged occurrence must not fire again.
	now = now.Add(time.Minute)
	fp2 := &fakePresenter{choice: desktop.Choice{ActionID: "join"}, seen: make(chan string, 4)}
	opt.Presenter = fp2
	stop = start(t, opt)
	select {
	case id := <-fp2.seen:
		t.Fatalf("duplicate reminder after restart: %s", id)
	case <-time.After(300 * time.Millisecond):
	}
	stop()
}

func TestDaemonRejectsSecondInstance(t *testing.T) {
	cfgPath, stateDir := setup(t)
	fp := &fakePresenter{seen: make(chan string, 4)}
	opt := Options{ConfigPath: cfgPath, StateDir: stateDir, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC) }, Presenter: fp, NoTray: true}
	stop := start(t, opt)
	defer stop()
	time.Sleep(100 * time.Millisecond)
	if err := Run(context.Background(), opt); err == nil {
		t.Error("second instance should fail to start")
	}
}

func TestDaemonSurvivesInvalidConfigAndReloads(t *testing.T) {
	cfgPath, stateDir := setup(t)
	if err := os.WriteFile(cfgPath, []byte("events: [ {broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 3, 16, 11, 0, 0, 0, time.UTC)
	fp := &fakePresenter{choice: desktop.Choice{ActionID: "snooze"}, seen: make(chan string, 4)}
	stop := start(t, Options{
		ConfigPath: cfgPath, StateDir: stateDir, Log: slog.New(slog.DiscardHandler),
		Now: func() time.Time { return now }, Presenter: fp, NoTray: true,
	})
	defer stop()

	// Fix the file: hot reload picks it up and the due reminder fires.
	time.Sleep(100 * time.Millisecond)
	if err := os.WriteFile(cfgPath, []byte(daemonYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if id := waitShown(t, fp); id != "standup" {
		t.Fatalf("shown %q", id)
	}
}
