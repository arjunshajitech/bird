//go:build desktop

// Command bird-overlay renders one reminder as an animated, transparent,
// borderless window. It is started by the bird daemon (see
// internal/desktop/protocol.go) and exits when the reminder is closed or the
// daemon goes away.
//
// Run `bird-overlay --demo` to see the UI without the daemon.
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/logger"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
	"github.com/wailsapp/wails/v2/pkg/options/linux"
	"github.com/wailsapp/wails/v2/pkg/runtime"

	"github.com/bird-app/bird/frontend"
	"github.com/bird-app/bird/internal/assets"
	"github.com/bird-app/bird/internal/desktop"
)

// Compact window: just the bird and its speech bubble, never full screen.
const (
	winW = 640
	winH = 300
)

var flightDurations = map[string]time.Duration{
	"slow": 2600 * time.Millisecond, "normal": 1600 * time.Millisecond, "fast": 900 * time.Millisecond,
}

const (
	leaveDuration = 900 * time.Millisecond
	revealTimeout = time.Second
	settleDelay   = 150 * time.Millisecond
)

func main() {
	demo := flag.Bool("demo", false, "show a sample reminder without the daemon")
	flag.Parse()
	if err := run(*demo); err != nil {
		fmt.Fprintln(os.Stderr, "bird-overlay:", err)
		os.Exit(1)
	}
}

func run(demo bool) error {
	var (
		in  io.Reader
		out io.Writer
		rem desktop.Reminder
	)
	if demo {
		rem = demoReminder()
		in, out = blockingReader{}, io.Discard
	} else {
		proto := os.NewFile(desktop.ProtocolFD, "bird-protocol")
		if proto == nil {
			return errors.New("must be started by bird (try `bird --preview <event-id>` or `bird-overlay --demo`)")
		}
		if _, err := proto.Stat(); err != nil {
			return errors.New("must be started by bird (try `bird --preview <event-id>` or `bird-overlay --demo`)")
		}
		out = proto
		in = os.Stdin
	}

	app := newApp(in, out, demo)
	if !demo {
		var err error
		if rem, err = app.readShow(); err != nil {
			return err
		}
	}
	app.rem = rem

	return wails.Run(&options.App{
		Title:            "Bird — " + rem.Title,
		Width:            winW,
		Height:           winH,
		DisableResize:    true,
		Frameless:        true,
		StartHidden:      true, // positioned off-screen before it is shown
		AlwaysOnTop:      rem.AlwaysOnTop,
		BackgroundColour: &options.RGBA{R: 0, G: 0, B: 0, A: 0},
		AssetServer:      &assetserver.Options{Assets: frontend.Assets},
		OnStartup:        app.startup,
		OnBeforeClose:    app.beforeClose,
		Bind:             []any{app},
		Logger:           logger.NewDefaultLogger(),
		LogLevel:         logger.ERROR,
		Linux: &linux.Options{
			Icon:                assets.PNG(128),
			WindowIsTranslucent: true,
			WebviewGpuPolicy:    linux.WebviewGpuPolicyOnDemand,
			ProgramName:         "bird",
		},
	})
}

// ReadyInfo tells the page how the arrival will be animated.
type ReadyInfo struct {
	// Mode is "window" (the window itself flies in; X11), "inline" (the bird
	// flies inside a centred window; Wayland) or "static" (reduced motion).
	Mode     string `json:"mode"`
	FlightMS int    `json:"flight_ms"`
}

// App is bound to the page as window.go.main.App.
type App struct {
	ctx  context.Context
	rem  desktop.Reminder
	demo bool

	in       *bufio.Scanner
	outMu    sync.Mutex
	out      io.Writer
	replies  chan string // "keep" / "close" answers to Choose and Cancel
	waiting  atomic.Bool // a Choose/Cancel is waiting for its answer
	leaving  atomic.Bool
	pending  atomic.Bool // close requested by the window manager
	revealed atomic.Bool

	flight       time.Duration
	windowFlight bool // the window itself flies in (see Ready)

	posMu      sync.Mutex
	x, y       int // where the window would be if it could leave the screen
	screenW    int
	lastDX     int // bird offset inside the window, last sent to the page
	windowMode bool
}

func newApp(in io.Reader, out io.Writer, demo bool) *App {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return &App{in: sc, out: out, demo: demo, replies: make(chan string, 1)}
}

// readShow reads the first protocol message, which must be "show".
func (a *App) readShow() (desktop.Reminder, error) {
	type result struct {
		r   desktop.Reminder
		err error
	}
	ch := make(chan result, 1)
	go func() {
		if !a.in.Scan() {
			ch <- result{err: errors.New("no reminder received on stdin")}
			return
		}
		var m desktop.DaemonMsg
		if err := json.Unmarshal(a.in.Bytes(), &m); err != nil || m.Type != desktop.MsgShow || m.Reminder == nil {
			ch <- result{err: errors.New("first message must be a show message")}
			return
		}
		ch <- result{r: *m.Reminder}
	}()
	select {
	case res := <-ch:
		return res.r, res.err
	case <-time.After(10 * time.Second):
		return desktop.Reminder{}, errors.New("timed out waiting for the reminder")
	}
}

func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	go a.readDaemon()
}

// readDaemon routes daemon messages. EOF means the daemon is gone: exit.
func (a *App) readDaemon() {
	for a.in.Scan() {
		var m desktop.DaemonMsg
		if json.Unmarshal(a.in.Bytes(), &m) != nil {
			continue
		}
		switch m.Type {
		case desktop.MsgKeep, desktop.MsgClose:
			if a.waiting.Load() {
				a.replies <- m.Type
			} else if m.Type == desktop.MsgClose {
				runtime.EventsEmit(a.ctx, "leave") // timeout or daemon shutdown
			}
		}
	}
	if !a.demo {
		a.quit()
	}
}

func (a *App) send(m desktop.OverlayMsg) {
	b, _ := json.Marshal(m)
	a.outMu.Lock()
	defer a.outMu.Unlock()
	_, _ = a.out.Write(append(b, '\n'))
}

// ask sends a choice to the daemon and waits for keep/close.
func (a *App) ask(m desktop.OverlayMsg) string {
	if a.demo {
		runtime.EventsEmit(a.ctx, "leave")
		return desktop.MsgClose
	}
	a.waiting.Store(true)
	defer a.waiting.Store(false)
	a.send(m)
	select {
	case r := <-a.replies:
		if r == desktop.MsgClose {
			runtime.EventsEmit(a.ctx, "leave")
		}
		return r
	case <-time.After(15 * time.Second):
		// The daemon is unresponsive; do not leave a stuck window.
		runtime.EventsEmit(a.ctx, "leave")
		return desktop.MsgClose
	}
}

// Reminder returns the reminder to render.
func (a *App) Reminder() desktop.Reminder { return a.rem }

// Choose reports a button press. Returns "keep" or "close".
func (a *App) Choose(actionID string) string {
	return a.ask(desktop.OverlayMsg{Type: desktop.MsgAction, Action: actionID})
}

// Cancel reports Escape. Returns "keep" or "close".
func (a *App) Cancel() string {
	return a.ask(desktop.OverlayMsg{Type: desktop.MsgCancel})
}

// Ready is called by the page once rendered: position, show and fly in.
func (a *App) Ready(reducedMotion bool) ReadyInfo {
	flight := flightDurations[a.rem.Speed]
	if flight == 0 {
		flight = flightDurations["normal"]
	}
	a.measureScreen()
	info := ReadyInfo{Mode: "inline", FlightMS: int(flight.Milliseconds())}
	switch {
	case reducedMotion:
		info.Mode = "static"
	case a.windowMode:
		info.Mode = "window"
	}

	// WebKitGTK shows an opaque white box until it has painted after the
	// window is mapped: map the window invisible and reveal it (and start
	// the flight) once the page reports a painted frame.
	setWindowOpacity(0)
	if info.Mode == "window" {
		a.setPos(-winW, a.y)
		a.flight = flight
	} else {
		runtime.WindowCenter(a.ctx)
	}
	a.windowFlight = info.Mode == "window"
	runtime.WindowShow(a.ctx)
	go func() {
		time.Sleep(revealTimeout) // in case the page never reports
		a.Reveal()
	}()
	if !a.demo {
		a.send(desktop.OverlayMsg{Type: desktop.MsgReady})
	}
	return info
}

// Reveal is called by the page once it has painted in the (still invisible)
// window: make the window visible and start a window flight.
func (a *App) Reveal() {
	if a.revealed.Swap(true) {
		return
	}
	setWindowOpacity(1)
	if a.windowFlight {
		go func() {
			a.animate(-winW, a.targetX(), a.flight, easeOutCubic)
			runtime.EventsEmit(a.ctx, "landed")
		}()
	}
}

// Leave is called by the page after its exit animation starts.
func (a *App) Leave() {
	if a.leaving.Swap(true) {
		return
	}
	if a.windowMode {
		a.posMu.Lock()
		from := a.x
		a.posMu.Unlock()
		a.animate(from, a.screenW+40, leaveDuration, easeInCubic)
		// The last steps of the flight are drawn by the page (see setPos);
		// give it time to paint them before the window disappears.
		time.Sleep(settleDelay)
	} else {
		time.Sleep(leaveDuration)
	}
	a.quit()
}

func (a *App) quit() {
	a.leaving.Store(true)
	runtime.Quit(a.ctx)
}

// beforeClose handles the window manager closing the window (Alt+F4):
// treated like Escape, so a persistent reminder is never silently lost.
func (a *App) beforeClose(ctx context.Context) (prevent bool) {
	if a.leaving.Load() {
		return false
	}
	if !a.pending.Swap(true) {
		go func() {
			defer a.pending.Store(false)
			if a.Cancel() == desktop.MsgKeep {
				return
			}
		}()
	}
	return true
}

// measureScreen picks the current (or primary) monitor and decides whether
// the window can be moved by the app at all.
func (a *App) measureScreen() {
	screens, err := runtime.ScreenGetAll(a.ctx)
	w, h := 1920, 1080
	if err == nil && len(screens) > 0 {
		pick := screens[0]
		for _, s := range screens {
			if s.IsCurrent {
				pick = s
				break
			}
			if s.IsPrimary {
				pick = s
			}
		}
		w, h = pick.Size.Width, pick.Size.Height
	}
	a.screenW = w
	a.y = max(0, (h-winH)/2-h/12) // a little above centre, where eyes rest
	a.windowMode = canPositionWindows()
}

func (a *App) targetX() int { return max(0, (a.screenW-winW)/2) }

// setPos moves the flight to x. Window managers (Mutter in particular) keep
// windows fully on screen, and positions are relative to the current
// monitor, so the window itself stays within this monitor; the part of the
// flight beyond its edges is drawn by shifting the bird inside the window.
func (a *App) setPos(x, y int) {
	a.posMu.Lock()
	a.x = x
	a.posMu.Unlock()
	wx := min(max(x, 0), max(0, a.screenW-winW))
	runtime.WindowSetPosition(a.ctx, wx, y)
	if dx := x - wx; dx != a.lastDX {
		a.lastDX = dx
		runtime.EventsEmit(a.ctx, "shift", dx)
	}
}

// animate moves the window horizontally at ~60 fps.
func (a *App) animate(from, to int, d time.Duration, ease func(float64) float64) {
	const frame = 16 * time.Millisecond
	start := time.Now()
	for {
		p := float64(time.Since(start)) / float64(d)
		if p >= 1 {
			a.setPos(to, a.y)
			return
		}
		a.setPos(from+int(math.Round(float64(to-from)*ease(p))), a.y)
		time.Sleep(frame)
	}
}

func easeOutCubic(p float64) float64 { return 1 - math.Pow(1-p, 3) }
func easeInCubic(p float64) float64  { return p * p * p }

// canPositionWindows reports whether GTK is talking to an X server. Native
// Wayland ignores client-requested window positions.
func canPositionWindows() bool {
	if b := os.Getenv("GDK_BACKEND"); b != "" {
		return strings.HasPrefix(b, "x11")
	}
	if os.Getenv("WAYLAND_DISPLAY") != "" {
		return false
	}
	return os.Getenv("DISPLAY") != ""
}

func demoReminder() desktop.Reminder {
	return desktop.Reminder{
		EventID: "demo", Title: "Stand-up meeting", Message: "Stop coding. Time to meet your team!",
		Persistent: true, Character: "bird", Sound: true, AlwaysOnTop: true, Speed: "normal",
		Animation: "fly-across", AppName: "Bird",
		Actions: []desktop.Button{
			{ID: "join", Label: "Join meeting", Kind: "open_url"},
			{ID: "snooze", Label: "Remind me in 5 minutes", Kind: "snooze"},
		},
	}
}

// blockingReader never returns: in demo mode there is no daemon.
type blockingReader struct{}

func (blockingReader) Read([]byte) (int, error) { select {} }
