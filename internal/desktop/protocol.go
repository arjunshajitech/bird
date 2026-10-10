// Package desktop shows reminders to the user.
//
// The daemon never links a GUI toolkit. Reminders are rendered by a separate
// short-lived process (bird-overlay, Wails/WebKitGTK) that is started only
// while a reminder is on screen, or by a desktop notification as a fallback.
// This keeps the always-running daemon small, and a UI crash cannot take the
// scheduler down with it.
//
// Wire protocol (newline-delimited JSON):
//
//	daemon  ──stdin──▶ overlay:  {"type":"show","reminder":{...}}
//	                             {"type":"keep"}   reminder stays (after an action)
//	                             {"type":"close"}  fly away and exit
//	overlay ──fd 3───▶ daemon:   {"type":"ready"}
//	                             {"type":"action","action":"<id>"}
//	                             {"type":"cancel"} Escape / window closed
//
// The overlay→daemon channel is a dedicated file descriptor rather than
// stdout so that stray output from GTK, WebKit or Wails can never corrupt the
// protocol. If the daemon goes away, the overlay sees EOF on stdin and exits.
package desktop

// Message types.
const (
	MsgShow   = "show"
	MsgKeep   = "keep"
	MsgClose  = "close"
	MsgReady  = "ready"
	MsgAction = "action"
	MsgCancel = "cancel"
)

// ProtocolFD is the overlay's file descriptor for messages to the daemon.
const ProtocolFD = 3

// Reminder is everything a renderer needs. It deliberately carries no URLs:
// the renderer only reports action IDs and the daemon performs the action.
type Reminder struct {
	EventID     string   `json:"event_id"`
	Title       string   `json:"title"`
	Message     string   `json:"message"`
	Actions     []Button `json:"actions"`
	Snoozed     bool     `json:"snoozed"`
	Persistent  bool     `json:"persistent"`
	Character   string   `json:"character"`
	Sound       bool     `json:"sound"`
	AlwaysOnTop bool     `json:"always_on_top"`
	Speed       string   `json:"speed"`     // slow | normal | fast
	Animation   string   `json:"animation"` // fly-across | none
	AppName     string   `json:"app_name"`
}

// Button is one action in the reminder.
type Button struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Kind  string `json:"kind"` // dismiss | snooze | open_url
}

// DaemonMsg is sent from the daemon to the overlay.
type DaemonMsg struct {
	Type     string    `json:"type"`
	Reminder *Reminder `json:"reminder,omitempty"`
}

// OverlayMsg is sent from the overlay to the daemon.
type OverlayMsg struct {
	Type   string `json:"type"`
	Action string `json:"action,omitempty"`
}
