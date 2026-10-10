package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

const stateVersion = 1

// Occurrence identifies one firing of an event: the scheduled recurrence slot
// it belongs to. A snoozed reminder keeps the slot of the original occurrence,
// which is what makes deduplication and stale-response detection work.
type Occurrence struct {
	EventID   string    `json:"event_id"`
	Scheduled time.Time `json:"scheduled"`
	Snoozed   bool      `json:"snoozed,omitempty"`
}

// Same reports whether two values describe the same occurrence. time.Time must
// be compared with Equal, never ==.
func (o Occurrence) Same(p Occurrence) bool {
	return o.EventID == p.EventID && o.Scheduled.Equal(p.Scheduled) && o.Snoozed == p.Snoozed
}

// State is the persisted runtime state, kept separate from the user's config.
type State struct {
	Version int                    `json:"version"`
	Events  map[string]*EventState `json:"events"`
}

// EventState tracks one event across restarts.
type EventState struct {
	// Watermark is the latest regular occurrence that was presented or
	// deliberately skipped. An occurrence fires only if it is newer, so a
	// restart or config reload can never fire the same occurrence twice.
	Watermark time.Time `json:"watermark,omitzero"`
	// Active is set while a reminder is on screen (or queued for it).
	// Written before the UI is shown, so a crash cannot cause a duplicate.
	Active *Active `json:"active,omitempty"`
	// Snooze is independent of the regular recurrence.
	Snooze *Snooze `json:"snooze,omitempty"`
}

type Active struct {
	Occurrence Occurrence `json:"occurrence"`
	Since      time.Time  `json:"since"`
	// Retries carries Snooze.Retries while a display retry is on screen.
	Retries int `json:"retries,omitempty"`
}

type Snooze struct {
	Occurrence Occurrence `json:"occurrence"`
	Until      time.Time  `json:"until"`
	// Retries counts automatic re-attempts after the UI failed to appear.
	Retries int `json:"retries,omitempty"`
}

// NewState returns an empty state.
func NewState() *State {
	return &State{Version: stateVersion, Events: map[string]*EventState{}}
}

func (s *State) get(id string) *EventState {
	st, ok := s.Events[id]
	if !ok {
		st = &EventState{}
		s.Events[id] = st
	}
	return st
}

// LoadState reads the state file. A missing file yields an empty state.
// A corrupt file is moved aside (never silently deleted) and an empty state is
// returned together with a non-nil error the caller should log.
func LoadState(path string) (*State, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return NewState(), nil
	}
	if err != nil {
		return nil, err
	}
	s := NewState()
	if err := json.Unmarshal(data, s); err != nil || s.Version != stateVersion {
		aside := path + ".corrupt"
		_ = os.Rename(path, aside)
		if err == nil {
			err = fmt.Errorf("unsupported state version %d", s.Version)
		}
		return NewState(), fmt.Errorf("state file unreadable (moved to %s): %w", aside, err)
	}
	if s.Events == nil {
		s.Events = map[string]*EventState{}
	}
	return s, nil
}

// SaveState writes the state atomically (temp file, fsync, rename), so a
// crash or power loss leaves either the old or the new state, never a torn one.
func SaveState(path string, s *State) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
