package desktop

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/bird-app/bird/internal/platform"
)

// TestMain lets the test binary double as a fake bird-overlay process.
func TestMain(m *testing.M) {
	if script := os.Getenv("BIRD_FAKE_OVERLAY"); script != "" {
		os.Exit(fakeOverlay(script))
	}
	os.Exit(m.Run())
}

// fakeOverlay implements the overlay side of the protocol with scripted
// behaviour.
func fakeOverlay(script string) int {
	in := bufio.NewScanner(os.Stdin)
	out := os.NewFile(ProtocolFD, "proto")
	send := func(m OverlayMsg) { b, _ := json.Marshal(m); fmt.Fprintf(out, "%s\n", b) }
	fmt.Println("noise on stdout must not break the protocol")

	if !in.Scan() {
		return 2
	}
	var show DaemonMsg
	if json.Unmarshal(in.Bytes(), &show) != nil || show.Type != MsgShow || show.Reminder == nil {
		return 3
	}
	switch script {
	case "crash":
		return 1
	case "hang": // never becomes ready
		time.Sleep(time.Minute)
		return 0
	}
	send(OverlayMsg{Type: MsgReady})
	switch script {
	case "choose":
		send(OverlayMsg{Type: MsgAction, Action: show.Reminder.Actions[0].ID})
	case "keep-then-cancel":
		send(OverlayMsg{Type: MsgAction, Action: "join"})
		if !in.Scan() || !json.Valid(in.Bytes()) {
			return 4
		}
		var m DaemonMsg
		_ = json.Unmarshal(in.Bytes(), &m)
		if m.Type != MsgKeep {
			return 5
		}
		send(OverlayMsg{Type: MsgCancel})
	case "wait":
		// Do nothing; the daemon will close us.
	}
	for in.Scan() {
		var m DaemonMsg
		if json.Unmarshal(in.Bytes(), &m) == nil && m.Type == MsgClose {
			return 0
		}
	}
	return 0 // stdin EOF: daemon gone
}

func fakePresenter(t *testing.T, script string) *Overlay {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("BIRD_FAKE_OVERLAY", script)
	return &Overlay{
		Path:    exe,
		Session: platform.Session{Type: "x11", Display: ":99"},
		Log:     slog.New(slog.DiscardHandler),
	}
}

var testReminder = Reminder{
	EventID: "standup", Title: "Stand-up", Persistent: true,
	Actions: []Button{{ID: "join", Label: "Join", Kind: "open_url"}, {ID: "snooze", Label: "Later", Kind: "snooze"}},
}

func TestOverlayChoose(t *testing.T) {
	o := fakePresenter(t, "choose")
	var got []Choice
	err := o.Present(context.Background(), testReminder, func(c Choice) bool { got = append(got, c); return true })
	if err != nil {
		t.Fatalf("Present: %v", err)
	}
	if len(got) != 1 || got[0].ActionID != "join" {
		t.Errorf("choices = %+v", got)
	}
}

func TestOverlayKeepThenCancel(t *testing.T) {
	o := fakePresenter(t, "keep-then-cancel")
	var got []Choice
	err := o.Present(context.Background(), testReminder, func(c Choice) bool {
		got = append(got, c)
		return c.Cancel // keep open after "join", close on cancel
	})
	if err != nil {
		t.Fatalf("Present: %v", err)
	}
	if len(got) != 2 || got[0].ActionID != "join" || !got[1].Cancel {
		t.Errorf("choices = %+v", got)
	}
}

func TestOverlayCrashIsAnError(t *testing.T) {
	o := fakePresenter(t, "crash")
	err := o.Present(context.Background(), testReminder, func(Choice) bool { t.Error("unexpected decision"); return true })
	if err == nil {
		t.Fatal("want error for a crashed overlay")
	}
}

func TestOverlayContextCancelClosesIt(t *testing.T) {
	o := fakePresenter(t, "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := o.Present(ctx, testReminder, func(Choice) bool { return true })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want deadline exceeded", err)
	}
	if time.Since(start) > 3*time.Second {
		t.Error("overlay did not exit promptly on close")
	}
}

func TestOverlayUnavailableWithoutDisplay(t *testing.T) {
	o := &Overlay{Path: "/bin/true", Log: slog.New(slog.DiscardHandler)}
	if err := o.Present(context.Background(), testReminder, nil); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

type stubPresenter struct {
	name string
	err  error
	n    int
}

func (s *stubPresenter) Name() string { return s.name }
func (s *stubPresenter) Present(context.Context, Reminder, Decide) error {
	s.n++
	return s.err
}

func TestChainFallsBack(t *testing.T) {
	a := &stubPresenter{name: "a", err: ErrUnavailable}
	b := &stubPresenter{name: "b"}
	c := &Chain{Presenters: []Presenter{a, b}}
	if err := c.Present(context.Background(), testReminder, nil); err != nil {
		t.Fatal(err)
	}
	if a.n != 1 || b.n != 1 {
		t.Errorf("calls a=%d b=%d", a.n, b.n)
	}
	if c.Name() != "a → b" {
		t.Errorf("name = %q", c.Name())
	}
}

func TestChainStopsOnContextEnd(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	a := &stubPresenter{name: "a", err: context.Canceled}
	b := &stubPresenter{name: "b"}
	c := &Chain{Presenters: []Presenter{a, b}}
	if err := c.Present(ctx, testReminder, nil); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v", err)
	}
	if b.n != 0 {
		t.Error("fallback must not run after the context ended")
	}
}
