# Bird

A small Linux desktop reminder. When something is due, a little bird flies
in from the left side of the screen and holds up your reminder until you
answer it.

![Bird flying in with a stand-up meeting, a lunch break and a water break reminder](bird-docs/demo.gif)

You can use it for anything that happens at a fixed time: a meeting, lunch,
a water break, medication, a stretch, a weekly report, the end of your work
day. Each reminder can have up to four buttons, for example *Join meeting*
(opens a link), *Remind me in 10 minutes* (snooze), or *Done* (dismiss).

---

## Quick start

```bash
# Build dependencies (Debian / Ubuntu / Zorin / Mint)
sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

make                          # builds ./build/bird and ./build/bird-overlay
./build/bird-overlay --demo   # see the bird once, no setup needed

make install-config           # copies examples/workday.yaml to ~/.config/bird/config.yaml
./build/bird --check-config   # checks the file and lists upcoming reminders
./build/bird                  # run in the foreground (Ctrl+C to stop)
```

To start it automatically when you log in:

```bash
make install install-service
```

Other distros:
`sudo dnf install gcc pkgconf gtk3-devel webkit2gtk4.1-devel` (Fedora),
`sudo pacman -S base-devel gtk3 webkit2gtk-4.1` (Arch).

---

## A small example

`~/.config/bird/config.yaml`:

```yaml
events:
  - id: standup
    title: "Stand-up meeting"
    message: "Time to meet your team."
    schedule: { type: weekly, time: "11:00", days: [mon, tue, wed, thu, fri] }
    actions:
      - { id: join,   label: "Join meeting",  action: open_url, url: "https://meet.example.com/abc" }
      - { id: snooze, label: "In 5 minutes",  action: snooze,   minutes: 5 }

  - id: lunch
    title: "Lunch break"
    schedule: { type: daily, time: "13:00" }

  - id: meds
    title: "Evening medication"
    schedule: { type: daily, time: "20:00" }
    actions:
      - { id: taken, label: "Taken",          action: dismiss }
      - { id: later, label: "In 10 minutes",  action: snooze, minutes: 10 }
```

Save the file and Bird picks up the change by itself. If you make a mistake,
it keeps the previous version running and tells you what's wrong.

Everything you can put in the file is described in
**[bird-docs/CONFIGURATION.md](bird-docs/CONFIGURATION.md)**. A longer example is in
[`examples/workday.yaml`](examples/workday.yaml).

---

## Using a reminder

- Click a button, or press its number (`1`–`4`), or `Tab` + `Enter`.
- `Esc` or closing the window snoozes (if the reminder has a snooze button),
  so a stray key doesn't lose it.
- Keys are ignored for a moment after the bird lands and while you're typing
  fast, so text meant for another window doesn't press a button.
- If your desktop shows tray icons, the tray menu shows the next reminder and
  lets you preview, reload or quit.

---

## A few things worth knowing

- **Restarts are safe.** Bird remembers what it has already shown, so
  restarting it (or your computer) won't show the same reminder twice.
- **Missed reminders.** If the computer was asleep or off at reminder time,
  Bird shows it when you come back, but only if it's at most 15 minutes late
  (`catch_up_window`). Older ones are skipped instead of piling up.
- **Snoozing doesn't move the schedule.** Tomorrow's reminder is still at the
  usual time.
- **Links only.** Buttons can open `http`/`https` links, snooze or dismiss.
  Bird never runs commands from the config file, and it never writes links
  to its logs.
- **Fallback.** If the animated window can't be shown, you get a normal
  desktop notification with the same buttons.

How all of this works inside (design, libraries, the reminder state machine,
memory and CPU use) is explained in
**[bird-docs/ARCHITECTURE.md](bird-docs/ARCHITECTURE.md)**.

---

## Running in the background

`make install-service` installs a `systemd --user` unit that starts with your
desktop session. Nothing needs root.

| | |
|---|---|
| Status | `systemctl --user status bird` |
| Logs | `journalctl --user -u bird -f` |
| Reload config | `systemctl --user reload bird` (or just save the file) |
| Restart | `systemctl --user restart bird` |
| Stop autostart | `systemctl --user disable --now bird` |
| Uninstall | `make uninstall` (keeps your config) |

## Command line

```
bird                      run the reminder daemon
bird --check-config       validate the config and list upcoming reminders
bird --preview ID         show one reminder now (changes nothing, opens nothing)
bird --config PATH        use another config file
bird --log-level debug    more detailed logs
bird --no-tray            no tray icon
bird --version

bird-overlay --demo       show the bird with sample content
```

---

## Files

| What | Where |
|---|---|
| Your config | `~/.config/bird/config.yaml` |
| What Bird has already shown, pending snoozes | `~/.local/state/bird/state.json` |
| Logs | the journal (`journalctl --user -u bird`) |

---

## Troubleshooting

| Problem | Try |
|---|---|
| Plain notifications instead of the bird | Check the logs. Usually `bird-overlay` isn't installed next to `bird`. Try `bird-overlay --demo`. |
| Grey box around the bird | Your desktop has no compositor. Everything else still works. |
| Blank or crashing window (often NVIDIA) | Add `Environment=WEBKIT_DISABLE_DMABUF_RENDERER=1` with `systemctl --user edit bird`. |
| No tray icon on GNOME | Install the "AppIndicator and KStatusNotifierItem Support" extension, or ignore it; Bird works without it. |
| "bird is already running" | The service is running; stop it before starting `./build/bird` by hand. |
| A reminder didn't show after sleep | It was more than `catch_up_window` late. You can raise that value. |
| A link didn't open | `xdg-open https://example.com` has to work in your session. |

---

## Development

```bash
make test                       # unit tests (go test -race ./...)
./build/bird --log-level debug  # verbose run
./build/bird --preview lunch    # show one reminder now
```

The UI is plain HTML/CSS/JS in `frontend/dist`. You can open
`index.html` in a browser to work on it; it uses a mock backend there.

Requirements: Go 1.25+. The daemon is pure Go; the overlay needs GTK 3 and
WebKitGTK 4.1 (`make overlay WEBKIT_TAG=` for 4.0).

---

## License

[MIT](LICENSE). The bundled Google Sans font is under the SIL Open Font
License (`frontend/dist/fonts/OFL.txt`).
