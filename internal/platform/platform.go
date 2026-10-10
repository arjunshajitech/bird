// Package platform holds Linux/desktop-session specifics: XDG paths, session
// detection, the single-instance lock and launching the default browser.
package platform

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const appDir = "bird"

func xdgDir(env, fallback string) string {
	if d := os.Getenv(env); d != "" && filepath.IsAbs(d) {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = os.TempDir()
	}
	return filepath.Join(home, fallback)
}

// ConfigPath is $XDG_CONFIG_HOME/bird/config.yaml.
func ConfigPath() string {
	return filepath.Join(xdgDir("XDG_CONFIG_HOME", ".config"), appDir, "config.yaml")
}

// StateDir is $XDG_STATE_HOME/bird. Runtime state lives here, never next to
// the user's config and never in a system-wide path.
func StateDir() string { return filepath.Join(xdgDir("XDG_STATE_HOME", ".local/state"), appDir) }

// CacheDir is $XDG_CACHE_HOME/bird.
func CacheDir() string { return filepath.Join(xdgDir("XDG_CACHE_HOME", ".cache"), appDir) }

// RuntimeDir is $XDG_RUNTIME_DIR/bird, falling back to the state dir.
func RuntimeDir() string {
	if d := os.Getenv("XDG_RUNTIME_DIR"); d != "" && filepath.IsAbs(d) {
		return filepath.Join(d, appDir)
	}
	return StateDir()
}

// Session describes the graphical session Bird runs in.
type Session struct {
	Type    string // x11, wayland, tty, or unknown
	Desktop string // XDG_CURRENT_DESKTOP
	Display string // DISPLAY (X11 / XWayland)
	Wayland string // WAYLAND_DISPLAY
}

// DetectSession reads the session from the environment.
func DetectSession() Session {
	s := Session{
		Type:    strings.ToLower(os.Getenv("XDG_SESSION_TYPE")),
		Desktop: os.Getenv("XDG_CURRENT_DESKTOP"),
		Display: os.Getenv("DISPLAY"),
		Wayland: os.Getenv("WAYLAND_DISPLAY"),
	}
	if s.Type == "" {
		switch {
		case s.Wayland != "":
			s.Type = "wayland"
		case s.Display != "":
			s.Type = "x11"
		default:
			s.Type = "unknown"
		}
	}
	return s
}

// HasDisplay reports whether a GUI can be shown at all.
func (s Session) HasDisplay() bool { return s.Display != "" || s.Wayland != "" }

// IsWayland reports a native Wayland session.
func (s Session) IsWayland() bool { return s.Type == "wayland" || (s.Wayland != "" && s.Type != "x11") }

func (s Session) String() string {
	desk := s.Desktop
	if desk == "" {
		desk = "unknown desktop"
	}
	return fmt.Sprintf("%s session on %s (DISPLAY=%q, WAYLAND_DISPLAY=%q)", s.Type, desk, s.Display, s.Wayland)
}

// ErrAlreadyRunning means another Bird daemon holds the lock.
var ErrAlreadyRunning = errors.New("another bird instance is already running")

// Lock takes an exclusive, non-blocking flock on path. Two daemons would
// otherwise both fire every reminder. The kernel releases the lock if the
// process dies, so a crash never leaves a stale lock behind.
func Lock(path string) (release func(), err error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			return nil, ErrAlreadyRunning
		}
		return nil, err
	}
	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
	return func() { f.Close() }, nil
}

// Open hands a validated URL or a local path to the desktop's default handler
// via xdg-open. It never goes through a shell. When Bird runs as a systemd
// user service, the handler is started in its own transient scope so that a
// browser launched by Bird does not die when the Bird service is restarted.
//
// The target is never logged; meeting URLs often carry credentials.
func Open(ctx context.Context, target string) error {
	if strings.HasPrefix(target, "-") {
		return errors.New("refusing to open a target that looks like an option")
	}
	xdgOpen, err := exec.LookPath("xdg-open")
	if err != nil {
		return errors.New("xdg-open not found; install xdg-utils")
	}
	argv := []string{xdgOpen, target}
	if os.Getenv("INVOCATION_ID") != "" { // set by systemd for services
		if run, err := exec.LookPath("systemd-run"); err == nil {
			argv = append([]string{run, "--user", "--scope", "--quiet", "--collect", "--"}, argv...)
		}
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("default handler failed: %w", err)
	}
	return nil
}
