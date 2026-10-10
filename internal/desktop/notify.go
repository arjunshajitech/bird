package desktop

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/godbus/dbus/v5"
)

const (
	notifyDest  = "org.freedesktop.Notifications"
	notifyPath  = dbus.ObjectPath("/org/freedesktop/Notifications")
	notifyIface = "org.freedesktop.Notifications"

	urgencyNormal   = byte(1)
	urgencyCritical = byte(2)

	closedExpired   = uint32(1)
	closedDismissed = uint32(2)
)

// Notification renders reminders as freedesktop desktop notifications with
// action buttons. It talks to the notification server over D-Bus directly so
// it can track the notification ID, react to clicks and close it on timeout
// or shutdown. Persistent reminders use critical urgency, which GNOME, KDE and
// most servers keep on screen until the user responds.
type Notification struct {
	IconPath string // optional absolute path to a PNG
	Log      *slog.Logger
}

func (n *Notification) Name() string { return "notification" }

func (n *Notification) Present(ctx context.Context, r Reminder, decide Decide) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return fmt.Errorf("%w: session bus: %v", ErrUnavailable, err)
	}
	defer conn.Close()

	if err := conn.AddMatchSignal(dbus.WithMatchObjectPath(notifyPath), dbus.WithMatchInterface(notifyIface)); err != nil {
		return fmt.Errorf("%w: subscribe: %v", ErrUnavailable, err)
	}
	signals := make(chan *dbus.Signal, 16)
	conn.Signal(signals)
	obj := conn.Object(notifyDest, notifyPath)

	id, err := n.notify(ctx, obj, r, 0)
	if err != nil {
		return fmt.Errorf("%w: notify: %v", ErrUnavailable, err)
	}
	closeIt := func() { _ = obj.Call(notifyIface+".CloseNotification", 0, id).Err }

	for {
		select {
		case <-ctx.Done():
			closeIt()
			return ctx.Err()

		case sig, ok := <-signals:
			if !ok {
				return fmt.Errorf("session bus closed")
			}
			if len(sig.Body) < 2 {
				continue
			}
			if sigID, _ := sig.Body[0].(uint32); sigID != id {
				continue // another application's notification
			}
			var choice Choice
			switch sig.Name {
			case notifyIface + ".ActionInvoked":
				action, _ := sig.Body[1].(string)
				if action == "default" { // body click: not a decision
					continue
				}
				choice = Choice{ActionID: action}
			case notifyIface + ".NotificationClosed":
				reason, _ := sig.Body[1].(uint32)
				if reason != closedDismissed && reason != closedExpired {
					continue // closed because an action was invoked, or by us
				}
				if reason == closedExpired && !r.Persistent {
					// Leave the decision to the caller's timeout.
					<-ctx.Done()
					return ctx.Err()
				}
				choice = Choice{Cancel: true}
			default:
				continue
			}
			if decide(choice) {
				closeIt()
				return nil
			}
			// Most servers remove a notification once an action is clicked;
			// show it again (replacing the old ID) so it stays visible.
			if id, err = n.notify(ctx, obj, r, id); err != nil {
				return err
			}
		}
	}
}

func (n *Notification) notify(ctx context.Context, obj dbus.BusObject, r Reminder, replaces uint32) (uint32, error) {
	actions := make([]string, 0, 2*len(r.Actions))
	for _, a := range r.Actions {
		actions = append(actions, a.ID, a.Label)
	}
	urgency, expire := urgencyNormal, int32(-1)
	if r.Persistent {
		urgency, expire = urgencyCritical, 0 // 0 = never expire
	}
	hints := map[string]dbus.Variant{
		"urgency":       dbus.MakeVariant(urgency),
		"desktop-entry": dbus.MakeVariant("bird"),
	}
	if r.Sound {
		hints["sound-name"] = dbus.MakeVariant("message-new-instant")
	}
	title := r.Title
	if r.Snoozed {
		title += " (snoozed)"
	}
	var id uint32
	err := obj.CallWithContext(ctx, notifyIface+".Notify", 0,
		r.AppName, replaces, n.IconPath, title, r.Message, actions, hints, expire,
	).Store(&id)
	return id, err
}
