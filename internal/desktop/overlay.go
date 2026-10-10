package desktop

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/bird-app/bird/internal/platform"
)

// OverlayBinary is the name of the overlay executable.
const OverlayBinary = "bird-overlay"

const (
	readyTimeout = 20 * time.Second // WebKit cold start on a slow machine
	exitTimeout  = 5 * time.Second  // fly-away animation plus teardown
)

// FindOverlay locates the overlay binary: $BIRD_OVERLAY, next to the running
// executable, then $PATH.
func FindOverlay() (string, error) {
	if p := os.Getenv("BIRD_OVERLAY"); p != "" {
		return p, nil
	}
	if exe, err := os.Executable(); err == nil {
		p := filepath.Join(filepath.Dir(exe), OverlayBinary)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	return exec.LookPath(OverlayBinary)
}

// Overlay renders reminders with the animated bird-overlay process.
type Overlay struct {
	Path    string
	Session platform.Session
	Log     *slog.Logger
}

func (o *Overlay) Name() string { return "overlay" }

func (o *Overlay) Present(ctx context.Context, r Reminder, decide Decide) error {
	if !o.Session.HasDisplay() {
		return fmt.Errorf("%w: no DISPLAY or WAYLAND_DISPLAY", ErrUnavailable)
	}
	if o.Path == "" {
		return fmt.Errorf("%w: %s not found", ErrUnavailable, OverlayBinary)
	}

	fromOverlay, protoW, err := os.Pipe()
	if err != nil {
		return err
	}
	defer fromOverlay.Close()

	cmd := exec.Command(o.Path)
	cmd.Env = o.env()
	cmd.ExtraFiles = []*os.File{protoW} // becomes fd 3 (ProtocolFD)
	logw := &lineLogger{log: o.Log, event: r.EventID}
	cmd.Stdout, cmd.Stderr = logw, logw
	stdin, err := cmd.StdinPipe()
	if err != nil {
		protoW.Close()
		return err
	}
	if err := cmd.Start(); err != nil {
		protoW.Close()
		return fmt.Errorf("%w: start overlay: %v", ErrUnavailable, err)
	}
	protoW.Close() // the child holds its own copy; EOF arrives when it exits

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	send := func(m DaemonMsg) error {
		b, _ := json.Marshal(m)
		_, err := stdin.Write(append(b, '\n'))
		return err
	}
	// finish asks the overlay to fly away, then makes sure it is gone.
	finish := func() {
		_ = send(DaemonMsg{Type: MsgClose})
		select {
		case <-exited:
		case <-time.After(exitTimeout):
			_ = cmd.Process.Kill()
			<-exited
		}
		stdin.Close()
	}

	if err := send(DaemonMsg{Type: MsgShow, Reminder: &r}); err != nil {
		_ = cmd.Process.Kill()
		<-exited
		return fmt.Errorf("send reminder to overlay: %w", err)
	}

	msgs := make(chan OverlayMsg)
	done := make(chan struct{})
	defer close(done)
	go readMessages(fromOverlay, msgs, done)

	ready := false
	readyTimer := time.NewTimer(readyTimeout)
	defer readyTimer.Stop()

	for {
		select {
		case <-ctx.Done():
			finish()
			return ctx.Err()

		case <-readyTimer.C:
			if !ready {
				_ = cmd.Process.Kill()
				<-exited
				return fmt.Errorf("overlay did not become ready within %s", readyTimeout)
			}

		case m, ok := <-msgs:
			if !ok {
				// Protocol pipe closed: the overlay exited without a
				// closing decision (crash, killed, display lost).
				err := <-exited
				stdin.Close()
				if err == nil {
					err = errors.New("exited without a decision")
				}
				return fmt.Errorf("overlay: %w", err)
			}
			var choice Choice
			switch m.Type {
			case MsgReady:
				ready = true
				continue
			case MsgAction:
				choice = Choice{ActionID: m.Action}
			case MsgCancel:
				choice = Choice{Cancel: true}
			default:
				o.Log.Debug("ignoring unknown overlay message", "type", m.Type)
				continue
			}
			if decide(choice) {
				finish()
				return nil
			}
			_ = send(DaemonMsg{Type: MsgKeep})
		}
	}
}

// env prepares the overlay's environment. On a Wayland session with
// XWayland available, the overlay is run through XWayland: native Wayland
// does not let clients position their own windows or keep them on top.
func (o *Overlay) env() []string {
	env := os.Environ()
	if o.Session.IsWayland() && o.Session.Display != "" && os.Getenv("GDK_BACKEND") == "" {
		env = append(env, "GDK_BACKEND=x11")
	}
	return env
}

func readMessages(r io.Reader, out chan<- OverlayMsg, done <-chan struct{}) {
	defer close(out)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 4096), 64*1024)
	for sc.Scan() {
		var m OverlayMsg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		select {
		case out <- m:
		case <-done:
			return
		}
	}
}

// lineLogger forwards the overlay's stdout/stderr (GTK/WebKit warnings) to
// the debug log, line by line, with a cap so a noisy library cannot flood it.
type lineLogger struct {
	log   *slog.Logger
	event string
	buf   []byte
	lines int
}

const maxOverlayLogLines = 50

func (l *lineLogger) Write(p []byte) (int, error) {
	l.buf = append(l.buf, p...)
	for {
		i := bytes.IndexByte(l.buf, '\n')
		if i < 0 {
			if len(l.buf) > 4096 {
				l.buf = l.buf[:0]
			}
			return len(p), nil
		}
		line := string(l.buf[:i])
		l.buf = l.buf[i+1:]
		if l.lines++; l.lines <= maxOverlayLogLines && line != "" {
			l.log.Debug("overlay output", "event", l.event, "line", line)
		}
	}
}
