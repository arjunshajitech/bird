# Bird

**A Linux desktop reminder that flies in when it's time.**

Bird runs quietly in the background, reads your events from a YAML file, and
when one is due a small bird flies in from the left edge of the screen, lands
in the middle and holds up your reminder. It stays there until you act on it:
join the meeting, snooze it, or dismiss it.

![Bird flying in with a stand-up meeting, a lunch break and a water break reminder](docs/demo.gif)

---

## Contents

1. [Quick start](#quick-start)
2. [How it works](#how-it-works)
3. [Configuration](#configuration)
4. [Using reminders](#using-reminders)
5. [Running as a service](#running-as-a-service-systemd---user)
6. [Command line](#command-line)
7. [Building from source](#building-from-source)
8. [Files and paths](#files-and-paths)
9. [Troubleshooting](#troubleshooting)
10. [Linux limitations (X11 / Wayland)](#linux-limitations-x11--wayland)
11. [Development and tests](#development-and-tests)
12. [Extending Bird](#extending-bird)
13. [Status: implemented, tested, known limitations](#status)

---

## Quick start

```bash
# 1. Build dependencies (Debian/Ubuntu/Zorin; other distros: see below)
sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev

# 2. Build both binaries into ./build
make

# 3. See the bird right away (no config needed)
./build/bird-overlay --demo

# 4. Create your config and check it
make install-config                  # copies config.example.yaml to ~/.config/bird/config.yaml
./build/bird --check-config

# 5. Run in the foreground (Ctrl+C to stop)
./build/bird

# 6. Optional: start automatically at login
make install install-service
```

---

## How it works

### The big picture

Bird is **two programs** that talk over a pipe:

```
                         ┌──────────────────────────────── bird (daemon, always running, ~6 MB, no cgo) ─┐
  ~/.config/bird/        │                                                                              │
  config.yaml ──watch──▶ │  config ─▶ events.Engine ◀── one goroutine owns all scheduling state        │
                         │  (validate)  │  ▲                                                             │
                         │              │  │ Resolve(action)                                             │
  ~/.local/state/bird/   │       Tick   ▼  │                                                             │
  state.json ◀─persist── │           dispatcher (queue, one reminder at a time)                        │
                         │              │                                                              │
                         │      Presenter chain:  overlay ──fails?──▶ desktop notification            │
                         └──────────────┼───────────────────────────────────────────────────────────────┘
                                        │ spawn per reminder; JSON lines over stdin / fd 3
                         ┌──────────────▼─────────── bird-overlay (Wails + WebKitGTK, only while visible) ┐
                         │  transparent, frameless window → bird flies in → speech bubble → buttons      │
                         └──────────────────────────────────────────────────────────────────────────────┘
```

**Why two processes?** A WebKit window costs about 100 MB of RAM and pulls in
GTK. Keeping that out of the always-on daemon means:

- the daemon stays tiny and has no native dependencies (`CGO_ENABLED=0`);
- a crash in the UI can't take the scheduler down. The daemon notices and
  falls back to a desktop notification;
- the daemon can start before the desktop is fully ready and still never
  lose a reminder.

### Life of a reminder

```
   scheduled ──(time comes)──▶ ACTIVE ──dismiss / open link / timeout──▶ done
                                 │  ▲
                        snooze   │  │  snooze time comes
                     (or UI failed, retry in 1 min)
                                 ▼  │
                               SNOOZED
```

1. **Scheduling.** Each event's schedule (`daily` or `weekly` at `HH:MM`) is
   compiled into a `Schedule` with `Next(t)` and `Prev(t)`, computed in your time zone.
2. **The loop.** The daemon sleeps until the next due time, but **never longer
   than 30 seconds**. Go timers use a monotonic clock that stops while a laptop
   is suspended, so one long timer would fire hours late after sleep. Checking
   the real wall clock every 30 s fixes that. It also handles NTP and manual
   clock changes, and costs almost nothing (no busy-looping).
3. **Deciding what's due (`Engine.Tick`).** For each event Bird asks: *what is
   the latest scheduled time at or before now?* If that time is newer than the
   event's saved **watermark**, it's a new occurrence. The watermark moves
   forward and the reminder is shown, but only if it's within the catch-up
   window (below).
4. **Write before showing.** The new watermark and the "active" marker are
   saved to `state.json` *before* the bird appears. Saving uses a temp file,
   `fsync` and an atomic rename. If Bird crashes at any point, the occurrence
   can't be shown twice.
5. **Showing.** The dispatcher shows one reminder at a time (stacked birds in
   the middle of the screen would be unreadable). It starts `bird-overlay` and
   sends it the reminder. The overlay never sees URLs; it only reports which
   button was pressed.
6. **Acting (`Engine.Resolve`).** The daemon applies the choice. `dismiss`
   clears it. `snooze` sets a separate snooze timer. `open_url` opens the link
   with your default browser. The overlay is then told `close` (fly away) or
   `keep` (stay open, e.g. after a link with `acknowledge: false`).

### Rules that keep reminders reliable

| Situation | What Bird does |
|---|---|
| Bird restarts after you already acted | Nothing. The watermark says this occurrence is done. |
| Bird crashes **while** a reminder is on screen | On restart it shows that same reminder again, once, if it is recent (within `catch_up_window`). It was never answered, so this is not a duplicate. |
| The computer was off/asleep at reminder time | Shown on wake/start if it is at most `catch_up_window` late (default 15 min). Older ones are skipped and logged, so you never get a flood of stale reminders after a weekend. |
| You snooze | A separate timer. Your regular schedule doesn't move: tomorrow is still 11:00. |
| A new occurrence arrives while an old snooze is pending | The newer reminder wins; the stale snooze is dropped. |
| You edit `config.yaml` | State is keyed by event `id`, so unchanged events keep their history. Removed events are cleaned up. Disabled events lose pending snoozes. |
| Invalid `config.yaml` | Rejected with clear errors. The last good config stays active and you get a notification. At startup Bird waits for a valid file instead of crash-looping. |
| The overlay can't start (no display, missing binary, crash) | Falls back to a desktop notification with the same buttons. If nothing can be shown, it retries every minute (up to an hour). |
| Two copies of Bird started | The second exits (`flock` on `$XDG_RUNTIME_DIR/bird/bird.lock`). Two daemons would mean duplicate reminders. |
| Daylight-saving change | Times are calendar-based. A time skipped by spring-forward fires right after the jump (02:30 → 03:30). A time that happens twice in fall-back fires once. |

### Daemon ⇄ overlay protocol

Newline-delimited JSON. Messages from the overlay use a **dedicated file
descriptor (fd 3)** rather than stdout, so stray GTK/WebKit output can't
corrupt the protocol.

```
daemon  → overlay (stdin):  {"type":"show","reminder":{...}}   {"type":"keep"}   {"type":"close"}
overlay → daemon  (fd 3):   {"type":"ready"}   {"type":"action","action":"join"}   {"type":"cancel"}
```

If the daemon goes away, the overlay sees EOF on stdin and exits. If the
overlay hasn't reported `ready` within 20 s, it is killed and the notification
fallback is used.

### The overlay animation

- On **X11** the *window itself* flies from the left edge to the centre
  (~60 fps, ease-out). Window managers keep windows on screen, so for the part
  of the path beyond the screen edge the window waits at the edge and the bird
  moves inside it. The window stays compact (640×300), so it never covers the
  whole screen or blocks clicks elsewhere. It is kept invisible until WebKit
  has painted, so no blank box flashes up first.
- On **Wayland**, apps can't position their own windows. Bird runs the overlay
  through XWayland (`GDK_BACKEND=x11`) when available. Otherwise the window is
  centred and the bird flies in *inside* it.
- With **reduced motion** (`gtk-enable-animations=false` / `prefers-reduced-motion`)
  or `animation.type: none`, the reminder just appears.

Speed (`slow`/`normal`/`fast`) maps to 2.6 s / 1.6 s / 0.9 s of flight. The
bird, its flapping, blinking and breathing are inline SVG and CSS. The chirp is
synthesised with WebAudio. The only bundled file is the Google Sans font
(`frontend/dist/fonts/`, SIL Open Font License), and nothing is loaded from the
network.

---

## Configuration

Bird reads `~/.config/bird/config.yaml` (override with `--config`). The fully
commented reference is [`config.example.yaml`](config.example.yaml).

```yaml
app:
  timezone: Local          # or an IANA name: Europe/Berlin, Asia/Kolkata, ...

appearance:
  animation_speed: normal

events:
  - id: daily-standup
    title: "Stand-up meeting"
    message: "Stop coding. Time to meet your team!"
    schedule:
      type: weekly
      time: "11:00"
      days: [mon, tue, wed, thu, fri]
    actions:
      - id: join
        label: "Join meeting"
        action: open_url
        url: "https://your-meeting-url"
      - id: snooze
        label: "Remind me in 5 minutes"
        action: snooze
        minutes: 5

  - id: lunch
    title: "Lunch break"
    message: "Take a break and get some food!"
    schedule: { type: daily, time: "13:00" }
```

### Reference

**`app`**

| Key | Default | Meaning |
|---|---|---|
| `name` | `Bird` | Shown in notifications. |
| `timezone` | `Local` | `Local` or an IANA zone name. All event times use it. |
| `start_minimized` | `true` | `false` shows a "Bird is running" notification at startup. |
| `catch_up_window` | `15m` | How late a missed reminder may still be shown. `0s` = never catch up. Max `24h`. |
| `tray` | `true` | Tray icon (needs a StatusNotifier/AppIndicator host). |

**`appearance`**

| Key | Default | Meaning |
|---|---|---|
| `character` | `bird` | The character. Only `bird` ships today. |
| `always_on_top` | `true` | Keep the overlay above other windows. |
| `animation_speed` | `normal` | `slow`, `normal` or `fast`. |
| `sound_enabled` | `true` | Short chirp on arrival (notification sound in fallback mode). |
| `renderer` | `auto` | `auto` (overlay, falling back to a notification), `overlay`, or `notification`. |

**`events[]`**

| Key | Required | Default | Meaning |
|---|---|---|---|
| `id` | ✔ | | Unique. Lowercase letters, digits, `-`, `_`. **Keep it stable**: it identifies the event's history. |
| `title` | ✔ | | Up to 120 chars. |
| `message` | | empty | Up to 500 chars. |
| `enabled` | | `true` | `false` keeps the event but never shows it. |
| `schedule.type` | ✔ | | `daily` or `weekly`. |
| `schedule.time` | ✔ | | 24-hour `"HH:MM"`. **Quote it** (YAML may otherwise read `11:00` oddly). |
| `schedule.days` | weekly | | `mon`…`sun` (or `monday`…`sunday`). Not allowed for `daily`. |
| `animation.type` | | `fly-across` | `fly-across` or `none`. |
| `animation.stop_at` | | `center` | Only `center` today. |
| `persistent` | | `true` | `true`: stays until you act. `false`: disappears after `timeout` (counts as dismissed). |
| `timeout` | | `60s` | Only for non-persistent events. `5s`–`24h`. |
| `sound` | | (global) | Per-event override of `appearance.sound_enabled`. |
| `actions` | | one "Got it" dismiss | Up to 4 buttons. The first is the highlighted primary button. |

**`actions[]`**

| Key | For | Meaning |
|---|---|---|
| `id`, `label` | all | Unique id within the event; button text (max 40 chars). |
| `action` | all | `dismiss`, `snooze` or `open_url`. Nothing else: Bird **never runs commands** from YAML. |
| `minutes` | `snooze` | 1–1440, default 5. |
| `url` | `open_url` | Must be an absolute `http://` or `https://` URL. **Never written to logs.** |
| `acknowledge` | `open_url` | Default `true` (close after opening). `false` keeps the reminder up. |

Unknown keys are errors (a typo like `titel:` is caught). Every problem is
reported in one go, with its location:

```
$ bird --check-config
~/.config/bird/config.yaml: invalid configuration:
  - events[0] (Bad).id: must be lowercase letters, digits, '-' or '_' (max 64 chars)
  - events[0] (Bad).schedule: time "9:00" must use 24-hour HH:MM format
  - events[0] (Bad).actions[0] (x).url: must use http or https
```

### Reloading

Save the file and Bird reloads it within a second. Bird watches the
*directory*, so editors that save via rename work too. You can also reload
from the tray menu or with `systemctl --user reload bird` (SIGHUP). If the new
file is invalid, the previous configuration keeps running and the error goes
to the log and a notification. Changing `renderer` takes effect for the next
reminder.

---

## Using reminders

| Input | Effect |
|---|---|
| Click a button | Performs that action. |
| `Tab` / `Shift+Tab` | Move between buttons. The first button is focused when the bird lands. |
| `Enter` / `Space` | Activate the focused button. |
| `1` … `4` | Activate that button (numbers are shown on the buttons). |
| `Esc`, or closing the window (Alt+F4) | **Snooze** if the event has a snooze button, otherwise dismiss. A stray key never silently loses a reminder. |

**Typing protection.** A reminder may get keyboard focus while you are typing
in another app. So keys only count as a choice once the reminder has been
visible for 1.2 s, and never in the middle of a typing burst (a key within
0.7 s of the previous one). Mouse clicks work straight away. Bird only sees
keys sent to its own window; it never grabs the keyboard and never blocks the
rest of your desktop.

**Tray menu** (if your desktop shows AppIndicators): next reminder,
*Preview next reminder*, *Open configuration*, *Reload configuration*,
*Quit Bird*.

---

## Running as a service (`systemd --user`)

Nothing is installed or enabled unless you run these commands.

```bash
make install            # binaries → ~/.local/bin
make install-config     # only if you don't have ~/.config/bird/config.yaml yet
make install-service    # unit → ~/.config/systemd/user/bird.service, enable + start
```

The unit ([`packaging/systemd/bird.service`](packaging/systemd/bird.service))
is tied to `graphical-session.target`. It starts only after your desktop
session is ready and stops when you log out. It needs no root.

| Task | Command |
|---|---|
| Status | `systemctl --user status bird` |
| Start / stop | `systemctl --user start bird` / `systemctl --user stop bird` |
| Restart | `systemctl --user restart bird` |
| Reload config | `systemctl --user reload bird` |
| Logs (follow) | `journalctl --user -u bird -f` |
| Disable autostart | `systemctl --user disable --now bird` |
| Uninstall | `make uninstall` (keeps your config and state) |

Manual install without make:

```bash
install -Dm755 build/bird build/bird-overlay -t ~/.local/bin/
install -Dm644 packaging/systemd/bird.service ~/.config/systemd/user/bird.service
systemctl --user daemon-reload
systemctl --user enable --now bird
```

Links opened from Bird run in their own transient systemd scope
(`systemd-run --user --scope`), so restarting Bird never kills the browser it
opened.

---

## Command line

```
bird                          run the daemon in the foreground (development mode)
bird --config PATH            use another config file
bird --check-config           validate, print each event's next time, exit 0/1
bird --preview EVENT_ID       show one event's reminder now (no state changes, no actions run)
bird --version                print the version
bird --log-level debug        debug|info|warn|error (default info)
bird --no-tray                don't show the tray icon

bird-overlay --demo           show the overlay with sample content, no daemon needed
```

```
$ bird --check-config
Configuration OK: /home/me/.config/bird/config.yaml
Time zone: Local (now Fri 2026-10-09 22:32 IST)
Catch-up window: 15m0s

EVENT          SCHEDULE                                NEXT                  ACTIONS
daily-standup  weekly at 11:00 on mon,tue,wed,thu,fri  Mon 2026-10-12 11:00  join:open_url, snooze:snooze
lunch          daily at 13:00                          Sat 2026-10-10 13:00  dismiss:dismiss
```

---

## Building from source

**Requirements**

| | Daemon (`bird`) | Overlay (`bird-overlay`) |
|---|---|---|
| Go | 1.25+ (developed on 1.26) | same |
| C toolchain | not needed (`CGO_ENABLED=0`) | `gcc`, `pkg-config` |
| Libraries | none | GTK 3, WebKitGTK **4.1** (dev packages to build; runtime libs to run) |
| Runtime tools | `xdg-open` (xdg-utils), a D-Bus session | a display (X11 or XWayland) |

```bash
# Debian / Ubuntu / Zorin / Mint
sudo apt install build-essential pkg-config libgtk-3-dev libwebkit2gtk-4.1-dev
# Fedora
sudo dnf install gcc pkgconf gtk3-devel webkit2gtk4.1-devel
# Arch
sudo pacman -S base-devel gtk3 webkit2gtk-4.1
```

```bash
make            # both binaries → ./build
make bird       # daemon only (no GUI deps needed)
make overlay    # overlay only
make test       # unit tests with the race detector
make vet fmt
```

Older distros with only WebKitGTK 4.0: `make overlay WEBKIT_TAG=`.

No Node/npm step: the UI is plain HTML/CSS/JS in `frontend/dist`, embedded
into the binary. To tweak the design, open `frontend/dist/index.html` in a
browser; it runs with a mock backend. Try `?snoozed&speed=slow`.

The daemon works without the overlay. If `bird-overlay` isn't found (next to
`bird`, on `$PATH`, or via `$BIRD_OVERLAY`), reminders appear as desktop
notifications with action buttons.

---

## Files and paths

| What | Where |
|---|---|
| Configuration | `$XDG_CONFIG_HOME/bird/config.yaml` (`~/.config/bird/config.yaml`) |
| Runtime state (watermarks, snoozes) | `$XDG_STATE_HOME/bird/state.json` (`~/.local/state/bird/`) |
| Single-instance lock | `$XDG_RUNTIME_DIR/bird/bird.lock` |
| Icon cache (for notifications) | `$XDG_CACHE_HOME/bird/bird.png` |
| Logs | stderr → the journal when run as a service |

State is kept apart from your config, so Bird never rewrites the file you
edit. If `state.json` is ever unreadable, it is moved to `state.json.corrupt`
(not deleted) and Bird starts fresh.

---

## Troubleshooting

| Symptom | Fix |
|---|---|
| `make overlay` says GTK/WebKit is missing | Install the dev packages listed above. |
| Reminders show as plain notifications, not the bird | Check `journalctl --user -u bird`. Usually `bird-overlay not found` (install it next to `bird`) or a display problem. Try `bird-overlay --demo`. |
| The overlay is a grey/white rectangle instead of transparent | Your session has no compositor (some X11 window managers). Enable one (e.g. `picom`) or live with the box. Everything else works. |
| Overlay is blank or crashes (often NVIDIA) | Run with `WEBKIT_DISABLE_DMABUF_RENDERER=1` (add `Environment=WEBKIT_DISABLE_DMABUF_RENDERER=1` to the unit via `systemctl --user edit bird`). |
| No tray icon on GNOME | Stock GNOME hides AppIndicators. Install the "AppIndicator and KStatusNotifierItem Support" extension, or set `tray: false`. Bird works the same without it. |
| "bird is already running" | `systemctl --user status bird`. Stop the service before running `./build/bird` manually. |
| A reminder didn't show after a long sleep | Working as designed: it was more than `catch_up_window` late. The log says `missed occurrence skipped`. Increase the window if you prefer. |
| The service doesn't start at login | `systemctl --user is-enabled bird` and `systemctl --user status graphical-session.target`. Some minimal window managers never start that target; add `systemctl --user start bird` to your session autostart instead. |
| Link didn't open | `xdg-open https://example.com` must work in your session. Bird logs the failure (never the URL). |
| Check what Bird thinks is next | `bird --check-config`, or the first line of the tray menu. |

---

## Linux limitations (X11 / Wayland)

Tested on **Zorin OS 18 (GNOME/mutter, X11)**. Behaviour depends on the
display server and compositor:

| Feature | X11 + compositor (GNOME, KDE, Xfce+compositing) | X11, no compositor | Wayland (GNOME/KDE) with XWayland | Wayland, no XWayland |
|---|---|---|---|---|
| Transparent, borderless overlay | ✅ tested | ⚠️ opaque panel | ✅ expected | ✅ expected |
| Window flies in from the screen's left edge | ✅ tested | ✅ | ⚠️ expected via XWayland, **untested** | ❌ centred window, bird flies inside it |
| Always on top | ✅ tested (WM may still lower it in fullscreen apps) | ✅ | ⚠️ compositor-dependent, **untested** | ❌ not available to apps |
| Notification fallback | ✅ tested | ✅ | ✅ | ✅ |

Other notes:

- **Multi-monitor:** the bird lands on the monitor the window opens on
  (usually the primary/active one). The window never leaves that monitor, so
  it can't jump to a neighbouring one mid-flight.
- **Focus:** the WM decides whether a new window takes keyboard focus. Bird's
  typing protection (above) makes accidental key presses harmless either way.
- **Fullscreen apps / games / screen sharing:** the overlay is a normal
  always-on-top window. It can't appear over exclusive-fullscreen apps, and it
  shows up in screen shares like any window.
- **Sound:** the chirp needs WebAudio in WebKitGTK (GStreamer). If that's
  unavailable the reminder is just silent.
- **Startup latency:** WebKit takes about 1–1.5 s to start, so the bird
  appears about a second after the scheduled time.

---

## Development and tests

```bash
make test                          # go test -race ./...
./build/bird --log-level debug     # verbose foreground run
./build/bird --preview lunch       # show one reminder right now
./build/bird-overlay --demo        # UI only
```

Scheduling code never reads the real clock: every engine method takes `now`.
Tests are deterministic and cover:

- **config:** valid/invalid YAML, unknown keys, missing fields, duplicate IDs,
  bad times/weekdays/time zones, every action kind, URL scheme rules,
  option-injection attempts, that URLs never appear in errors, and that
  `config.example.yaml` stays valid;
- **schedule:** daily/weekday maths, time-zone calendars, DST spring-forward
  gaps, DST fall-back (fires once);
- **events engine:** firing on time, no duplicates (same run, after restart,
  after reload, stale UI responses), crash recovery, missed-event policy
  (including `0s`), snooze timing/persistence/superseding, `open_url`
  acknowledge, cancel→snooze, timeouts, display-failure retries (bounded), disabling and removing events;
- **desktop:** the overlay process protocol against a fake overlay (choose,
  keep→cancel, crash, timeout/close, no display, stdout noise), and fallback chain behaviour;
- **daemon:** end-to-end with a fake presenter (fire → snooze → persisted;
  open link → restart → no duplicate; second instance rejected; broken config
  → hot-fixed → reminder fires);
- **platform:** XDG paths, session detection, the exclusive lock.

### Project layout

```
cmd/bird/              daemon + CLI (pure Go)
cmd/bird-overlay/      Wails overlay process (build tags: desktop,production,webkit2_41)
frontend/dist/         overlay UI: index.html, style.css, app.js (inline SVG bird), fonts/
internal/config/       YAML schema, defaults, validation
internal/schedule/     Schedule interface; daily/weekly; DST-safe maths
internal/events/       Engine (state machine), persisted State
internal/desktop/      Presenter interface, overlay process protocol, D-Bus notifications
internal/daemon/       main loop, dispatcher queue, config watcher, tray
internal/platform/     XDG paths, session detection, lock, xdg-open
internal/assets/       procedurally drawn icon (tray + notifications)
packaging/systemd/     bird.service (user unit)
config.example.yaml    documented example configuration
```

---

## Extending Bird

The design keeps each moving part behind a small interface, so new features
stay local:

- **New schedule type** (e.g. `interval`, `once`, cron): implement
  `schedule.Schedule` (`Next`, `Prev`, `String`) and add a case to
  `schedule.New`. The engine, dedup and catch-up logic work unchanged.
- **New action type:** add it to the allowlist in `config` (validation), then
  handle it in `events.Engine.Resolve`. Actions are performed by the daemon,
  never by the UI.
- **New renderer** (e.g. a native Wayland layer-shell overlay, or other OSes):
  implement `desktop.Presenter` and add it to the chain in
  `daemon.selectPresenter`.
- **New character:** add the SVG and CSS in `frontend/dist` keyed by
  `reminder.character`, and allow the name in `config`.
- **Scaling to many events:** each tick is O(events) with tiny constants, and
  state is one small JSON file. Hundreds of events are fine. If you ever need
  thousands, swap the per-tick scan for a min-heap of next-fire times inside
  `Engine`; its public API wouldn't change.

---

## Status

**Implemented**

- Go daemon with YAML config, strict validation and documented defaults
- `daily` and `weekly` schedules in `Local` or any IANA time zone, DST-safe
- Deduplication watermark, write-before-show persistence, crash recovery,
  catch-up window for missed events, independent snoozes
- Actions: `dismiss`, `snooze`, `open_url` (http/https only, URLs never logged)
- Animated Wails/WebKitGTK overlay: transparent, borderless, always-on-top,
  window flight on X11, inline flight fallback, reduced-motion support,
  keyboard access with typing protection, light/dark themes, synthesised chirp
- D-Bus notification fallback with action buttons; one-minute retries
- Hot reload (file watcher, tray, SIGHUP), safe rejection of bad edits
- Tray icon/menu (StatusNotifierItem over D-Bus; no GTK in the daemon)
- Single-instance lock, clean shutdown on SIGINT/SIGTERM/tray quit
- CLI: `--config`, `--check-config`, `--preview`, `--version`, `--log-level`, `--no-tray`
- `systemd --user` unit plus opt-in install/uninstall via `make`

**Tested**

- All unit/integration tests pass with `go test -race ./...`
- On Zorin OS 18 (GNOME, X11): a real scheduled event fired 22 ms after its
  due time, the overlay flew in, and clicking a button was handled and
  persisted. Transparency, window flight, light and dark themes, and
  daemon-initiated close (SIGINT/SIGTERM) were all checked with screenshots.
  `systemd-analyze --user verify` passes on the unit.

**Known limitations / not verified**

- Wayland was not tested (see the table above).
- Keyboard handling (digits, Enter, Esc, typing-burst protection) is reviewed
  in code but was **not verified with automated key injection**. Please try
  it once yourself.
- The tray menu was built against the StatusNotifierItem API but not
  exercised by clicking through it in testing.
- The systemd unit was verified but not installed or enabled during
  development.
- Only one character (the bird) and one landing spot (`center`) exist.
- Several reminders due at the same moment are shown one after another, not
  together.
