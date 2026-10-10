package platform

import (
	"errors"
	"path/filepath"
	"testing"
)

func TestXDGPaths(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/cfg")
	t.Setenv("XDG_STATE_HOME", "/st")
	t.Setenv("XDG_RUNTIME_DIR", "/run/user/1")
	if got := ConfigPath(); got != "/cfg/bird/config.yaml" {
		t.Errorf("ConfigPath = %s", got)
	}
	if got := StateDir(); got != "/st/bird" {
		t.Errorf("StateDir = %s", got)
	}
	if got := RuntimeDir(); got != "/run/user/1/bird" {
		t.Errorf("RuntimeDir = %s", got)
	}
	// Relative XDG values are invalid per spec and must be ignored.
	t.Setenv("XDG_STATE_HOME", "relative")
	t.Setenv("HOME", "/home/u")
	if got := StateDir(); got != "/home/u/.local/state/bird" {
		t.Errorf("StateDir with relative XDG = %s", got)
	}
}

func TestDetectSession(t *testing.T) {
	t.Setenv("XDG_SESSION_TYPE", "")
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	s := DetectSession()
	if s.Type != "wayland" || !s.IsWayland() || !s.HasDisplay() {
		t.Errorf("session = %+v", s)
	}
	t.Setenv("WAYLAND_DISPLAY", "")
	if s := DetectSession(); s.HasDisplay() || s.Type != "unknown" {
		t.Errorf("headless session = %+v", s)
	}
}

func TestLockIsExclusive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bird.lock")
	release, err := Lock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Lock(path); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second lock: err = %v, want ErrAlreadyRunning", err)
	}
	release()
	release2, err := Lock(path)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	release2()
}
