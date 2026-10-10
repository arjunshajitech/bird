# How Bird works

This document explains Bird from the inside: how the code is arranged, which
libraries it uses and why, what happens step by step when a reminder fires,
how the pieces talk to each other, which design decisions were made (and
what they cost), and how much RAM, CPU and disk it uses.

It is written for someone who wants to change Bird, debug it, or just
understand it. For the YAML reference see [CONFIGURATION.md](CONFIGURATION.md).

---

## Contents

1. [The one-paragraph version](#the-one-paragraph-version)
2. [Code layout](#code-layout)
3. [Libraries and what each one is for](#libraries-and-what-each-one-is-for)
4. [Two processes: the daemon and the overlay](#two-processes-the-daemon-and-the-overlay)
5. [Startup, step by step](#startup-step-by-step)
6. [Concurrency: goroutines and channels](#concurrency-goroutines-and-channels)
7. [Configuration pipeline](#configuration-pipeline)
8. [Schedules and time maths](#schedules-and-time-maths)
9. [The event engine (state machine)](#the-event-engine-state-machine)
10. [Persistence: `state.json`](#persistence-statejson)
11. [The main loop and why it wakes every 30 seconds](#the-main-loop-and-why-it-wakes-every-30-seconds)
12. [The dispatcher: one reminder at a time](#the-dispatcher-one-reminder-at-a-time)
13. [Presenters: overlay, notification, chain](#presenters-overlay-notification-chain)
14. [The daemon ⇄ overlay protocol](#the-daemon--overlay-protocol)
15. [Inside `bird-overlay`](#inside-bird-overlay)
16. [The web UI (`frontend/dist`)](#the-web-ui-frontenddist)
17. [Desktop notifications over D-Bus](#desktop-notifications-over-d-bus)
18. [Tray icon](#tray-icon)
19. [Config hot reload](#config-hot-reload)
20. [Platform glue: XDG paths, lock, opening links](#platform-glue-xdg-paths-lock-opening-links)
21. [The icon](#the-icon)
22. [Security model](#security-model)
23. [systemd integration](#systemd-integration)
24. [Build system](#build-system)
25. [Cost: RAM, CPU, disk, battery](#cost-ram-cpu-disk-battery)
26. [Testing strategy](#testing-strategy)
27. [Design questions and answers](#design-questions-and-answers)
28. [Known limitations](#known-limitations)
29. [Where to change things](#where-to-change-things)

---

## The one-paragraph version

`bird` is a small pure-Go daemon. It reads `config.yaml`, turns each event's
schedule into a function that can answer "when was the last occurrence?" and
"when is the next one?", and runs a loop that wakes at least every 30 seconds.
Each time, an **engine** compares the latest scheduled time of each event with
a saved **watermark** to decide whether something new is due. If so, it
**writes that decision to disk first**, then hands the reminder to a
**dispatcher**, which starts a separate short-lived program, `bird-overlay`
(a transparent Wails/WebKitGTK window), and sends it the reminder over a pipe
as JSON. The overlay animates the bird, shows the buttons, and reports back
*which button* was pressed, never *what to do*. The daemon decides: dismiss,
snooze (a separate timer), or open a URL with `xdg-open`. If the overlay can't
run, the same reminder is shown as a desktop notification over D-Bus.

---

## Code layout

```
bird/
├── cmd/
│   ├── bird/main.go               CLI + daemon entry point (flags, logger, check-config, preview)
│   └── bird-overlay/
│       ├── main.go                the overlay program (Wails app, window flight, protocol client)
│       ├── opacity_linux.go       tiny cgo helper: set GTK window opacity
│       └── stub.go                built when the "desktop" tag is missing: prints how to build
├── frontend/
│   ├── embed.go                   //go:embed all:dist → frontend.Assets
│   └── dist/                      index.html, style.css, app.js, fonts/ (no build step, no npm)
├── internal/
│   ├── config/                    YAML schema, defaults, validation
│   ├── schedule/                  Schedule interface; daily/weekly; DST-safe maths
│   ├── events/                    Engine (state machine) + State (persisted JSON)
│   ├── daemon/                    main loop, dispatcher queue, config watcher, tray
│   ├── desktop/                   Presenter interface, overlay process client, D-Bus notifications, wire protocol
│   ├── platform/                  XDG paths, session detection, flock, xdg-open
│   └── assets/                    procedurally drawn PNG icon
├── examples/workday.yaml          starter configuration (also checked by a unit test)
├── packaging/systemd/bird.service user unit
├── docs/                          the website (GitHub Pages)
├── bird-docs/                     these documents, demo GIF/MP4, screenshot
├── Makefile                       build/test/install targets
└── SPEC.md                        the original product brief
```

### Package dependencies

Arrows mean "imports". Nothing points back up, so there are no cycles, and
the core logic (`config`, `schedule`, `events`) imports nothing that touches
the desktop.

```
cmd/bird ──────▶ daemon ──┬──▶ events ──▶ config ──▶ schedule
                          ├──▶ desktop ──▶ platform
                          ├──▶ platform
                          └──▶ assets

cmd/bird-overlay ──▶ desktop (only the protocol types), assets, frontend, wails
```

- `schedule`: pure maths. No I/O, no clock.
- `config`: reads bytes, produces a validated `*Config` with compiled schedules.
- `events`: pure state machine. Every method takes `now`; it never sleeps, reads the
  clock, or touches the UI. It also owns the JSON state file format.
- `desktop`: everything about getting pixels on screen. Knows nothing about
  schedules.
- `daemon`: the glue. Owns goroutines, channels, timers, signals, the tray.
- `platform`: the Linux-specific bits that don't belong anywhere else.

This split is why most of Bird can be tested with plain `go test` and a fake
clock, with no display.

---

## Libraries and what each one is for

### Direct dependencies (`go.mod`)

| Library | Used in | Purpose | Why this one |
|---|---|---|---|
| `go.yaml.in/yaml/v3` | `config` | Parse `config.yaml` | The maintained fork of `gopkg.in/yaml.v3`. Supports `KnownFields(true)` (reject unknown keys), YAML anchors/merge keys, and custom `UnmarshalYAML` on `yaml.Node` (used for the `Duration` type, with line numbers in errors). |
| `github.com/fsnotify/fsnotify` | `daemon/watch.go` | Get told when `config.yaml` changes | Thin wrapper over Linux `inotify`. Event-driven, zero polling. |
| `github.com/godbus/dbus/v5` | `desktop/notify.go` (and indirectly the tray) | Talk to the desktop's notification server | Pure Go D-Bus client. Lets the daemon send notifications **with action buttons** and listen for clicks, without linking any C library. |
| `fyne.io/systray` | `daemon/tray.go` | Tray icon and menu | On Linux it implements the StatusNotifierItem / DBusMenu protocols over D-Bus in pure Go (no GTK), so the daemon stays `CGO_ENABLED=0`. `RunWithExternalLoop` lets it run beside Bird's own loop instead of taking over `main`. |
| `github.com/wailsapp/wails/v2` | `cmd/bird-overlay` only | The overlay window | Turns a Go program + HTML/CSS/JS into a native window using the system WebKitGTK. Gives frameless, transparent, always-on-top windows, Go↔JS method binding, and events. Much smaller than shipping Chromium (Electron). |

### Indirect dependencies

Almost all of these come with Wails. Many are only compiled on Windows/macOS
or in Wails' development mode, so they don't end up in the Linux binaries:

| Library | Brought by | Role |
|---|---|---|
| `wailsapp/go-webview2`, `jchv/go-winloader`, `go-ole/go-ole`, `go-toast` | Wails | Windows backend. Not compiled on Linux. |
| `labstack/echo`, `gorilla/websocket`, `labstack/gommon`, `valyala/fasttemplate`, `valyala/bytebufferpool` | Wails | Development server / live reload. Not used in the `production` build. |
| `wailsapp/mimetype` | Wails | Detects content types of the embedded assets it serves to WebKit. |
| `leaanthony/*`, `samber/lo`, `tkrajina/go-reflector`, `bep/debounce`, `rivo/uniseg`, `mattn/go-*` | Wails | Internal helpers: slices, reflection for bindings, terminal output. |
| `pkg/browser`, `google/uuid`, `pkg/errors` | Wails | Runtime helpers. |
| `golang.org/x/sys`, `x/net`, `x/text`, `x/crypto` | various | Standard extended libraries. |

### Standard library pieces that do real work

| Package | Where | Purpose |
|---|---|---|
| `log/slog` | everywhere | Structured logging (`key=value`). Lets Bird log event ids and actions while never logging URLs. |
| `embed` | `frontend` | Bakes the HTML/CSS/JS/fonts into the overlay binary. Nothing is loaded from disk or network at runtime. |
| `os/exec`, `os.Pipe` | `desktop/overlay.go`, `platform` | Start `bird-overlay` with an extra pipe as fd 3; run `xdg-open` without a shell. |
| `syscall.Flock` | `platform` | Single-instance lock. |
| `encoding/json` | `events/state.go`, `desktop/protocol.go` | State file and the daemon⇄overlay protocol. |
| `time` (with IANA tzdata from the OS) | `schedule` | Time zones and DST. |
| `image`, `image/png` | `assets` | Draw the icon. |
| `text/tabwriter` | `cmd/bird` | The `--check-config` table. |
| `os/signal` | `cmd/bird`, `daemon` | SIGINT/SIGTERM (shutdown), SIGHUP (reload). |

### Native / system pieces

| Component | Needed by | Purpose |
|---|---|---|
| GTK 3 | overlay | Window, X11/Wayland, opacity. |
| WebKitGTK 4.1 (or 4.0) | overlay | Renders the HTML/CSS/SVG UI. Spawns its own `WebKitWebProcess` and `WebKitNetworkProcess`. |
| GStreamer | overlay (optional) | Backs WebAudio for the chirp. Missing = silent, nothing breaks. |
| D-Bus session bus | daemon | Notifications and tray. |
| `xdg-open` (xdg-utils) | daemon | Opens links in the default browser. |
| `systemd-run` | daemon (when run as a service) | Puts the browser in its own scope. |
| tzdata (`/usr/share/zoneinfo`) | daemon | IANA time zones. |

---

## Two processes: the daemon and the overlay

```
                         ┌───────────────────────────── bird (daemon, always running, pure Go) ─┐
  ~/.config/bird/        │                                                                     │
  config.yaml ──inotify─▶│  config ─▶ events.Engine ◀── one goroutine owns all scheduling state│
                         │              │  ▲                                                    │
  ~/.local/state/bird/   │       Tick   ▼  │ Resolve(choice)                                    │
  state.json ◀─persist── │           dispatcher (queue, one reminder at a time)               │
                         │              │                                                     │
                         │   Presenter chain:  overlay ──fails?──▶ D-Bus notification         │
                         └──────────────┼──────────────────────────────────────────────────────┘
                                        │ spawn per reminder; JSON lines over stdin / fd 3
                         ┌──────────────▼───────── bird-overlay (Wails + WebKitGTK, only while visible) ┐
                         │  transparent frameless window → bird flies in → speech bubble → buttons      │
                         └──────────────────────────────────────────────────────────────────────────────┘
```

| | `bird` | `bird-overlay` |
|---|---|---|
| Lifetime | Whole login session | Only while a reminder is on screen |
| cgo | No (`CGO_ENABLED=0`) | Yes (GTK, WebKitGTK) |
| Binary size | ~5.8 MB | ~9.1 MB (+ system WebKit) |
| RAM | ~10 MB | ~200 MB (proportional, including WebKit helpers) |
| Knows about | config, schedules, state, URLs | one reminder: title, message, button labels |

Splitting them means:

- the thing that runs all day is tiny and has no native dependencies;
- a WebKit or GPU driver crash kills only the overlay, and the daemon falls
  back to a notification;
- the daemon can start before the desktop is fully up and still never
  lose a reminder (it just retries);
- the UI can't do anything dangerous even if its JavaScript were somehow
  compromised: it never sees URLs and can only send back an action id.

---

## Startup, step by step

`cmd/bird/main.go` → `run()`:

1. Parse flags (`flag.NewFlagSet`, `ContinueOnError`; exit code 2 for bad usage).
2. `--version` → print and exit. `--check-config` → `config.Load`, print the
   table, exit 0/1 (no lock, no state, no display needed).
3. Build an `slog` text logger at `--log-level`. If `JOURNAL_STREAM` is set
   (running under systemd), drop the timestamp attribute because journald adds
   its own.
4. `signal.NotifyContext(SIGINT, SIGTERM)` → a context that is cancelled on
   Ctrl+C or `systemctl stop`.
5. `--preview ID` → `daemon.Preview`: load config, pick a presenter, call
   `Present` once with a `decide` that records the choice and returns
   `close=true`. No lock, no engine, no state, no action performed.
6. Otherwise `daemon.Run(ctx, opts)`:

`internal/daemon/daemon.go` → `Run()`:

1. **Lock.** `platform.Lock($XDG_RUNTIME_DIR/bird/bird.lock)`, a non-blocking
   exclusive `flock`. Already held → `ErrAlreadyRunning` → exit 1.
2. **State.** `events.LoadState(~/.local/state/bird/state.json)`. Missing →
   empty. Corrupt or wrong version → renamed to `state.json.corrupt`, start
   empty, log a warning.
3. **Config.** `config.Load`. If it fails, **don't exit**: remember the error,
   use an empty config and wait for a fix. (Exiting would make systemd
   restart it every 5 s forever.)
4. **Engine.** `events.NewEngine(cfg, state, log)`.
5. **Presenter.** `selectPresenter(cfg)` builds `overlay`, `notification`, or
   `Chain{overlay, notification}` depending on `appearance.renderer`.
   `FindOverlay()` looks at `$BIRD_OVERLAY`, then next to the running
   executable, then `$PATH`.
6. **Dispatcher.** `newDispatcher(presenter, d.requestResolve, log)`.
7. **Watcher.** `watchConfig` starts an inotify watch on the config
   *directory*.
8. **SIGHUP** goroutine → pushes onto the `reloads` channel.
9. **Tray** (if enabled, not `--no-tray`, and a display exists).
10. If the config failed, show a "configuration problem" notification. If
    `start_minimized: false`, show "Bird is running".
11. Start the dispatcher goroutine, then run `loop()` on the current goroutine.
12. On exit: cancel the context, **wait for the dispatcher** (so any overlay
    on screen is told to close), save state, release the lock.

---

## Concurrency: goroutines and channels

The key rule: **one goroutine (the loop) owns the Engine and the State.**
No mutex protects scheduling logic; nobody else touches it. Everyone else
sends messages.

| Goroutine | Lives | Does |
|---|---|---|
| main loop (`Daemon.loop`) | whole run | Tick, save, enqueue, reload, preview, resolve responses |
| dispatcher (`dispatcher.run`) | whole run | Pops jobs, calls `Presenter.Present` (blocks while shown) |
| config watcher | whole run | inotify → debounce → `reloads` |
| SIGHUP forwarder | whole run | signal → `reloads` |
| tray (systray internals + click handler) | whole run, if enabled | Clicks → `previews` / `reloads` / `quit` |
| per reminder: `cmd.Wait()` | while overlay runs | Reports overlay exit |
| per reminder: `readMessages` | while overlay runs | Reads fd 3 lines → channel |
| per link: `OpenURL` | a few ms–30 s | Runs `xdg-open` so the loop never blocks on it |

Channels:

| Channel | Buffer | From → to | Notes |
|---|---|---|---|
| `responses` | 0 | dispatcher → loop | Each carries its own `reply chan bool` (buffer 1). |
| `reloads` | 1 | watcher, SIGHUP, tray → loop | Sends use `select { case ch <- x: default: }`, so many triggers coalesce into one reload. |
| `previews` | 1 | tray → loop | Same coalescing. |
| `quit` | 0, closed | tray → loop | Closing it broadcasts. |
| `dispatcher.wake` | 1 | loop → dispatcher | "queue changed" doorbell. |

### Why it can't deadlock

The tricky cycle is: the loop gives a job to the dispatcher, and the
dispatcher (while a reminder is up) asks the loop to resolve a click.

- `dispatcher.enqueue` **never blocks**: it appends under a short mutex and
  rings a buffered doorbell with a non-blocking send.
- So the loop never waits for the dispatcher, and the dispatcher can always
  get through to the loop's `select`.
- `requestResolve` also selects on `ctx.Done()`, so shutdown can't hang on
  it either.

---

## Configuration pipeline

`config.Load(path)` → `Parse(reader)`:

1. `io.LimitReader(r, 1 MiB)`: a bound on what a file can make Bird allocate.
2. `yaml.NewDecoder(...).KnownFields(true).Decode(&c)`: unknown keys and
   type mismatches fail here with a line number. An empty document is
   reported as "configuration is empty".
3. `applyDefaults()` fills in names, zone, catch-up window, character, speed,
   renderer, animation, timeout, a default "Got it" action, and snooze
   minutes.
4. `validate()` collects **every** problem into a `problems` slice, each
   prefixed with a path like `events[2] (lunch).actions[0] (join).url`, and
   returns them together as a `*ValidationError`.

Design details worth knowing:

- **Tri-state booleans.** Optional booleans are `*bool`: `nil` means "not
  written" and gets the default; `false` is respected. Accessors like
  `IsEnabled()` / `IsPersistent()` / `SoundOn()` hide the pointer.
- **`Duration` type.** A `time.Duration` with `UnmarshalYAML` that accepts only
  strings (`"15m"`), so a bare `90` is an error instead of 90 nanoseconds.
- **Schedules are compiled during validation.** `schedule.New(...)` is called
  and the result is stored in `Event.Compiled`. If the time zone itself was
  invalid, schedule *shape* is still checked against UTC so you see all
  errors at once, but nothing is compiled.
- **URL rules.** `ValidateURL` requires `http`/`https`, a host, no
  whitespace, and no leading `-` (so it can't be mistaken for a command-line
  option by `xdg-open`). Its errors never include the URL.
- **Closed action list.** `dismiss`, `snooze`, `open_url`. There is no
  `run`/`exec` action, by design.
- **`CancelAction()`** decides what Escape/close means: first snooze, else
  first dismiss, else plain close.

---

## Schedules and time maths

`internal/schedule/schedule.go`.

```go
type Schedule interface {
    Next(t time.Time) time.Time   // first occurrence strictly after t
    Prev(t time.Time) time.Time   // latest occurrence at or before t
    String() string
}
```

Both `daily` and `weekly` are the same type, `dayTime{hour, minute,
days [7]bool, loc}`. Daily is just "all seven days on".

### `Next` and `Prev`

Take the calendar date of `t` in the schedule's zone, then look at that day
and the following (or preceding) 8 days. For each day, `at()` builds the
candidate instant and checks whether that weekday is enabled. Return the
first candidate after `t` (for `Next`) or at/before `t` (for `Prev`). Eight
days is enough: a weekly schedule always has an occurrence within 7 days, and
the extra day covers DST moving things across midnight. Cost is at most 9
`time.Date` calls, i.e. nanoseconds.

### DST handling in `at()`

- **Weekday from noon.** The weekday is computed from 12:00 on that date,
  which is never inside a DST transition.
- **Spring-forward gap.** For a non-existent time like 02:30, Go's
  `time.Date` normalises it using the *new* offset, which lands *before* the
  gap (01:30). Bird detects that the hour/minute didn't round-trip, measures
  the gap (offset 3 h later minus offset now), and shifts forward by it, so
  the reminder fires at 03:30, right after the jump, never early.
- **Fall-back overlap.** `time.Date` picks one of the two instants
  deterministically, so `Prev`/`Next` produce a single occurrence. Combined
  with the watermark (below), it fires once.

The package never reads the clock; tests feed it fixed instants in various
zones (including DST edges) and compare results.

---

## The event engine (state machine)

`internal/events/engine.go` and `state.go`. This is the heart of Bird's
reliability.

### Vocabulary

| Term | Meaning |
|---|---|
| **Occurrence** | `{EventID, Scheduled, Snoozed}`: one specific slot, e.g. "daily-standup at 2026-10-12 11:00". A snoozed reminder keeps its original `Scheduled` and sets `Snoozed: true`. |
| **Watermark** | Per event: the latest regular slot that Bird has *handled* (shown, or deliberately skipped). A slot fires only if it is **newer** than the watermark. |
| **Active** | Per event: the occurrence currently on screen (or queued), plus `Since` (when it was activated) and `Retries`. Written to disk **before** it is shown. |
| **Snooze** | Per event: an occurrence plus `Until`. Independent of the regular schedule. |
| **inFlight** | In-memory set of event ids this process is presenting right now. Not persisted. |
| **dirty** | Set when state changes; the loop saves only when dirty. |

### Lifecycle of one occurrence

```
   scheduled ──Tick──▶ ACTIVE ──Resolve(dismiss | open_url+ack | timeout)──▶ done
                         │  ▲
     Resolve(snooze)     │  │ Tick (snooze due)
     Resolve(failed)     ▼  │
                       SNOOZED
```

### `Tick(now)`: what is due?

For each enabled event that is not already in flight, `due(ev, now)` decides,
in this priority order:

1. **Orphaned Active.** If the state file has an `Active` entry but this
   process isn't showing it, a previous run died while it was on screen
   (crash, logout, kill). It was never answered, so showing it again is *not*
   a duplicate. If `now - Since ≤ catch_up_window` it is picked, otherwise
   dropped. Either way, the old `Active` is cleared.
2. **New regular occurrence.** `prev := Compiled.Prev(now)`. If `prev` is
   after the watermark:
   - the watermark moves to `prev` (always, so it's never reconsidered);
   - if step 1 already picked something, this slot is consumed ("superseded")
     rather than queued behind it;
   - else if `now - prev ≤ catch_up_window`, it is picked;
   - else it is skipped and logged as "missed occurrence skipped" (only if
     there *was* a watermark, so a brand-new event doesn't log about the past).
   - If a regular occurrence is picked while an older snooze is pending, the
     snooze is dropped: the newer reminder wins.
3. **Due snooze.** If nothing was picked and `now ≥ Snooze.Until`: within the
   catch-up window it is picked (with `Snoozed: true`, carrying its retry
   count), otherwise dropped and logged.

Whatever is picked becomes `Active{Occurrence, Since: now}`, the id goes into
`inFlight`, `dirty` is set, and a `Trigger` is returned.

The first time Bird ever sees an event, its watermark is zero, so the latest
past slot is considered "new". If that slot is within the catch-up window it
shows (you added "11:00" at 11:05 → you get it); otherwise the watermark just
moves silently.

### `Resolve(occ, response, now)`: what the user did

First a staleness check: if the event has no `Active`, or the `Active` is a
different occurrence (`Same()` compares id, `Scheduled` with `time.Equal`, and
`Snoozed`), it returns `ErrStale` with `Close: true`. This protects against
late clicks from an old overlay, or a response that arrives after a reload.

| Response | Result |
|---|---|
| `TimedOut` (non-persistent expired) | finish → done |
| `Failed` (UI couldn't be shown) | finish, then `Snooze{Until: now+1m, Retries+1}`. After 60 retries (~1 h) it is dropped. |
| `Cancelled` (Esc / close / notification dismissed) | rewritten to the event's `CancelAction()` (first snooze, else first dismiss); if none, finish → done |
| `Chose dismiss` | finish → done |
| `Chose snooze` | finish, `Snooze{Until: now + minutes}` |
| `Chose open_url` | returns `OpenURL` for the daemon to open. With `acknowledge` (default) also finish + `Close: true`; otherwise the reminder stays (`Close: false`, still Active). |
| unknown action id | error, `Close: false` (reminder stays) |
| event removed from config meanwhile | finish, delete its state, close |

`finish()` clears `Active`, removes the id from `inFlight`, marks dirty.

### `SetConfig(cfg)`: reloads

Events are looked up by id. For each id in the state:
- not in the new config and not on screen → its state is deleted;
- present but `enabled: false` with a pending snooze → snooze cleared.

Everything else (watermarks, snoozes) carries over, so editing a title or
time never re-fires a slot that was already handled.

### `NextWake` and `Upcoming`

`Upcoming(now)` lists, for each enabled, not-in-flight event,
`min(Compiled.Next(now), Snooze.Until)`, sorted. The tray's "Next: …" line
uses it. `NextWake` is just the earliest of those, which tells the loop
how long it may sleep.

### A worked timeline

Event `standup`, weekly Mon–Fri 11:00, catch-up 15 m, snooze 5 m.

| Wall time | What happens | State after |
|---|---|---|
| Mon 10:59:31 | Loop wakes (30 s cap), `Prev` = last Fri 11:00 = watermark → nothing | wm = Fri 11:00 |
| Mon 11:00:00 | Loop woke exactly at `NextWake`. `Prev` = Mon 11:00 > wm → pick | wm = Mon 11:00, Active{Mon 11:00, since 11:00} → **saved**, then shown |
| 11:00:40 | You press *Remind me in 5 minutes* | Active cleared, Snooze{Mon 11:00, until 11:05:40} |
| 11:02 | Bird crashes, systemd restarts it at 11:02:05 | Loaded from disk: same snooze, wm Mon 11:00. Nothing due. |
| 11:05:40 | Snooze due | Active{Mon 11:00, snoozed} → saved, shown with "Snoozed" badge |
| 11:06 | Laptop lid closed with the bird still on screen; reopened 11:12 | On resume the overlay is still there and still waiting; the click is resolved normally. |
| 11:12:03 | You press *Join meeting* | `xdg-open` the URL; Active cleared. Done. |
| Tue 11:00 | New slot | fires normally |

If instead the machine had been **off** from 10:50 to 11:20, the first tick
at 11:20 sees `Prev` = Mon 11:00, 20 min late > 15 m → watermark moves,
"missed occurrence skipped" is logged, nothing is shown.

---

## Persistence: `state.json`

`~/.local/state/bird/state.json` (directory mode `0700`):

```json
{
  "version": 1,
  "events": {
    "daily-standup": {
      "watermark": "2026-10-09T11:00:00+05:30"
    },
    "lunch": {
      "watermark": "2026-10-10T13:00:00+05:30",
      "snooze": {
        "occurrence": { "event_id": "lunch", "scheduled": "2026-10-10T13:00:00+05:30" },
        "until": "2026-10-10T13:15:12+05:30"
      }
    }
  }
}
```

- **Separate from the config.** Bird never rewrites your YAML.
- **Atomic write.** `SaveState` writes to a temp file in the same directory,
  `fsync`s it, closes it, then `rename`s it over `state.json`. A crash or
  power cut leaves either the old file or the new one, never half a file.
- **Write before show.** In the loop, `Tick` → `save()` → only then
  `enqueue`. If Bird dies between showing and your answer, the `Active` entry
  on disk makes the next run re-show it (once, if recent); if it dies before
  showing, same. There is no window where an occurrence is shown but not
  recorded, so it can't be shown twice.
- **Only when dirty.** `TakeDirty()` gates every save, so a quiet loop
  iteration writes nothing.
- **Corruption.** Unparseable JSON or an unknown `version` → file is moved to
  `state.json.corrupt` (kept for inspection) and Bird starts with empty
  state. Worst case: one reminder within the catch-up window may show again.

---

## The main loop and why it wakes every 30 seconds

```go
for {
    now := Now()
    triggers := engine.Tick(now)
    save()                         // persist before presenting
    for each trigger: dispatcher.enqueue(...)
    update tray status if changed
    sleep := min(30s, engine.NextWake(now) - now)
    timer.Reset(sleep)
    select { ctx done | quit | timer | reload | preview | response }
}
```

**Why not one long timer until 11:00?** Go timers run on the *monotonic*
clock, which doesn't advance while the machine is suspended. A timer set at
09:00 for "2 hours" on a laptop that sleeps from 10:00 to 12:00 would fire at
13:00 wall-clock time. By sleeping at most 30 s and re-reading the
**wall clock** each time, Bird:

- notices a missed slot within 30 s of resume;
- follows NTP corrections and manual clock/time-zone changes;
- still fires on time when awake, because it sleeps exactly until
  `NextWake` when that's sooner than 30 s.

The cost is one wake-up every 30 s doing a few microseconds of arithmetic
(2,880 tiny wake-ups a day). There is no busy loop.

Go ≥ 1.23 timer semantics mean `Reset` on an unfired or fired timer discards
any stale value, so no manual channel draining is needed.

Responses from the dispatcher are handled in the same `select`, so a click
is applied by the same goroutine that ticks: no races between "the user
snoozed" and "the next tick".

---

## The dispatcher: one reminder at a time

`internal/daemon/dispatcher.go`.

- A FIFO `queue []job` plus a `queued` set of event ids, under a mutex.
- `run()` pops one job, calls `show()` (which blocks for as long as the
  reminder is visible), marks it done, repeats; it waits on the doorbell
  when empty.
- **Why serial?** The bird lands in the centre of the screen. Two at once
  would sit on top of each other, unreadable. Events due at the same minute
  are shown back-to-back.
- **Previews** (tray / `--preview`) are skipped if that event is already
  queued or showing, and their `decide` returns `close` without telling the
  engine, so they never change state or perform actions.
- **Non-persistent events** get `context.WithTimeout(ctx, timeout)`. When it
  expires, the presenter closes the UI and returns `DeadlineExceeded`, which
  the dispatcher reports as `TimedOut`.
- **Error classification** after `Present` returns:
  - `nil` → the user's choice was already resolved through `decide`;
  - parent context cancelled (shutdown) → do nothing; the occurrence stays
    Active on disk and is re-shown next start if recent;
  - `DeadlineExceeded` → `TimedOut`;
  - anything else → `Failed` (→ retry in 1 min).
- `setPresenter` swaps the renderer on reload (under its own mutex); a
  reminder already on screen keeps the presenter it started with.
- `reminderFor` builds the `desktop.Reminder` view: title, message, button
  ids/labels/kinds, flags. **It copies no URL.**

---

## Presenters: overlay, notification, chain

`internal/desktop/presenter.go`:

```go
type Presenter interface {
    Name() string
    Present(ctx context.Context, r Reminder, decide Decide) error
}
type Decide func(Choice) (close bool)
```

The contract: `Present` blocks until the reminder is gone. For every user
choice it calls `decide`; if that returns `false` the reminder stays (e.g.
`open_url` with `acknowledge: false`) and the presenter keeps waiting. It
returns `nil` (closed by a decision), `ctx.Err()` (timeout/shutdown, UI
removed), or another error (couldn't show / renderer died). `decide` is
where the dispatcher calls back into the loop.

`Chain` tries presenters in order and moves to the next only if one returns
a non-nil error **and** the context is still alive. With `renderer: auto`
that is `overlay → notification`.

`ErrUnavailable` marks "this presenter can't work in this session at all"
(no display, binary missing, no session bus).

---

## The daemon ⇄ overlay protocol

`internal/desktop/protocol.go` and `overlay.go`.

Newline-delimited JSON, one object per line:

```
daemon  ──stdin──▶ overlay:  {"type":"show","reminder":{...}}   first line, exactly once
                             {"type":"keep"}                   your choice was handled, stay
                             {"type":"close"}                  fly away and exit
overlay ──fd 3───▶ daemon:   {"type":"ready"}                  window is up
                             {"type":"action","action":"join"}
                             {"type":"cancel"}                 Esc / Alt+F4
```

How the daemon side (`Overlay.Present`) runs one reminder:

1. Refuse early if there's no `DISPLAY`/`WAYLAND_DISPLAY` or no binary path
   (`ErrUnavailable`).
2. `os.Pipe()` → `(fromOverlay, protoW)`. `cmd.ExtraFiles = [protoW]` makes
   it **fd 3** in the child. The parent closes its copy of `protoW` right
   after `Start`, so when the child exits the parent reads EOF.
3. The child's stdout and stderr go to a `lineLogger` that forwards up to 50
   lines per reminder to the **debug** log (GTK/WebKit are chatty), and drops
   absurdly long lines.
4. Environment: on a Wayland session with XWayland (`DISPLAY` set) and no
   user-set `GDK_BACKEND`, add `GDK_BACKEND=x11`, so the window can be
   positioned and kept on top.
5. Send `show`. A `readMessages` goroutine turns fd 3 lines into a channel.
6. Wait in a `select`:
   - `ready` → note it (cancels the 20 s ready timeout);
   - `action` / `cancel` → `decide(choice)` → if close: `finish()`, else send `keep`;
   - fd 3 closed (overlay exited without a decision) → error → fallback;
   - no `ready` within **20 s** → kill → error → fallback;
   - context done (timeout or shutdown) → `finish()`, return `ctx.Err()`.
7. `finish()` sends `close`, waits up to **5 s** for the fly-away and exit,
   then kills it if needed.

**Why fd 3 instead of stdout?** GTK, WebKit and Wails print warnings to
stdout/stderr whenever they like. Mixing those with protocol messages would
corrupt them. A dedicated descriptor keeps the protocol clean, and the noise
just goes to the debug log.

**Why stdin EOF matters:** if the daemon dies, the overlay's stdin closes,
`readDaemon` loop ends, and the overlay quits. No orphaned birds.

---

## Inside `bird-overlay`

`cmd/bird-overlay/main.go` (built only with `-tags desktop`; without it,
`stub.go` prints build instructions).

### Startup

1. Check fd 3 exists (`os.NewFile(3).Stat()`). If not, you ran it by hand:
   exit with a hint (`--demo` or `bird --preview`).
2. Read the first stdin line within 10 s; it must be `show`.
3. `wails.Run(options.App{...})` with:
   - 640×300, `Frameless`, `DisableResize`, `AlwaysOnTop` from the reminder;
   - `StartHidden: true` (positioned before it's ever visible);
   - fully transparent `BackgroundColour` and `Linux.WindowIsTranslucent`;
   - `AssetServer` serving the embedded `frontend/dist` (no HTTP server, no
     network; Wails serves it through WebKit's custom scheme handler);
   - `Bind: []any{app}`: the `App` struct's exported methods become
     JavaScript functions at `window.go.main.App.*`;
   - `WebviewGpuPolicyOnDemand`, error-level logging only.

### Go methods the page calls

| JS call | Go method | Does |
|---|---|---|
| `Reminder()` | `App.Reminder` | Returns the reminder to render. |
| `Ready(reducedMotion)` | `App.Ready` | Measures the screen, picks a mode, sets window opacity to 0, places the window (off the left edge, or centred), shows it, sends `ready` to the daemon, and returns `{mode, flight_ms}`. A 1 s fallback timer will call `Reveal` if the page never does. |
| `Reveal()` | `App.Reveal` | Opacity → 1. In window mode, starts the window flight; emits `landed` at the end. Runs once (`atomic.Bool.Swap`). |
| `Choose(id)` | `App.Choose` | Sends `action` on fd 3 and waits for `keep`/`close` (15 s max; if the daemon doesn't answer, leaves anyway so no window gets stuck). |
| `Cancel()` | `App.Cancel` | Same with `cancel`. |
| `Leave()` | `App.Leave` | Flies the window off to the right (window mode) or waits for the CSS fly-out, then quits. |

Go → JS events: `landed`, `leave`, `shift` (see below).

### The anti-flash trick

WebKitGTK paints an opaque white rectangle in a newly mapped window until
the first frame is rendered. To hide that: `Ready` sets the GTK window
opacity to 0 *before* showing it, the page waits two `requestAnimationFrame`s
(one painted frame), then calls `Reveal` which sets opacity to 1.
`opacity_linux.go` does that with a few lines of cgo:
`gtk_widget_set_opacity` on every toplevel, queued with `g_idle_add` so it
runs on the GTK main thread *in order* with Wails' own calls.

### Three animation modes

| Mode | When | How the bird arrives |
|---|---|---|
| `window` | X11, or XWayland (`canPositionWindows()`: `GDK_BACKEND` starts with `x11`, or `DISPLAY` without `WAYLAND_DISPLAY`) | The **window itself** moves from x = −640 to the centre with an ease-out cubic, every 16 ms (~60 fps) via `WindowSetPosition`. |
| `inline` | Native Wayland (apps can't position windows) | The window is centred; the **bird** flies in inside it with a CSS animation. |
| `static` | `prefers-reduced-motion` / GTK animations off, or `animation.type: none` | Just appears. |

**Off-screen part of the flight.** Window managers (Mutter especially) won't
let a window sit partly off-screen; they clamp it to the monitor. So
`setPos(x)` clamps the window to `[0, screenW−640]` and sends the leftover
offset to the page as a `shift` event; the page translates the SVG bird by
that many pixels. The bird appears to come from beyond the screen edge while
the window never leaves the monitor (and never jumps to another monitor).

**Vertical position:** a little above centre (`(h−300)/2 − h/12`), where
eyes naturally rest.

**Screen choice:** `ScreenGetAll`, preferring the "current" monitor, then
primary, else 1920×1080 as a fallback.

### Closing from the window manager

Alt+F4 / the WM's close → `OnBeforeClose` → `beforeClose` **prevents** the
close and treats it as `Cancel()` (snooze if the event has one). Only when
the app is already leaving does it allow the close. So a persistent reminder
can't be lost by accident.

### Demo mode

`--demo` uses a built-in stand-up reminder, a stdin that blocks forever, and
discards output. Buttons immediately emit `leave`.

---

## The web UI (`frontend/dist`)

Plain HTML/CSS/JS, no framework, no bundler, no npm. About 560 lines in total.

- **`index.html`**: one `<main class="stage" data-state="arriving">`, an
  inline SVG bird (body, belly, head, eyes with lids, beak, tuft, two wings
  on pivot groups, tail, feet), and a speech-bubble `<section
  role="alertdialog">` with title, message, "Snoozed reminder" badge and the
  buttons container.
- **`style.css`**: the state machine is the `data-state` attribute:
  - `arriving`: wings flap (140 ms alternate), body bobs (420 ms); in
    `inline` mode the bird slides in over `--flight` ms.
  - `perched`: feet appear, the body "breathes" (2.4 s), eyes blink (4.2 s),
    wings ruffle (5 s); the bubble fades/scales in and becomes clickable.
  - `leaving`: bubble fades out (160 ms), bird flies off (900 ms).
  - Light and dark themes via `prefers-color-scheme`.
  - `prefers-reduced-motion` and the `.static` class disable all animation.
  - The Google Sans font is bundled (`fonts/`, SIL OFL) via `@font-face`.
- **`app.js`**:
  - Waits up to 3 s for the Wails bridge (`window.go.main.App`). If it's not
    there (opened in a normal browser), it installs a **mock backend**, so you
    can design the UI by opening `index.html` directly
    (`?snoozed&speed=slow&title=...`).
  - `render()` builds buttons with `textContent` (never `innerHTML`, so
    config text can't inject markup). The first button gets `primary`; each
    shows its number key.
  - `land()`: state → `perched`, chirp, then after 400 ms enable the
    buttons and focus the first.
  - **Typing protection.** Keys are swallowed unless the reminder has been
    armed for ≥ 1.2 s, the key isn't a repeat, and it isn't within 0.7 s of
    the previous key (a typing burst). `Tab` is always allowed. If a Space or
    Enter keydown was swallowed, its keyup is blocked too (buttons activate
    on keyup). Mouse clicks work as soon as the buttons are enabled.
  - `choose(id)` / `cancel()` disable the buttons, await the Go answer,
    re-enable on `keep`; on `close` the Go side emits `leave`.
  - **Chirp.** Two short sine sweeps (2.6→3.4 kHz, 2.9→3.9 kHz) with quick
    gain envelopes via WebAudio. No audio files. Fails silently.

---

## Desktop notifications over D-Bus

`internal/desktop/notify.go`. Used as the fallback, or always with
`renderer: notification`, and for the "configuration problem" / "is running"
messages.

1. Connect to the session bus; subscribe to signals from
   `/org/freedesktop/Notifications`.
2. Call `org.freedesktop.Notifications.Notify(app_name, replaces_id, icon,
   summary, body, actions, hints, expire_timeout)`:
   - `actions` = `[id1, label1, id2, label2, …]`, the same ids as the
     overlay buttons;
   - persistent → `urgency=critical` and `expire_timeout=0` (most servers then
     keep it until you answer); otherwise normal urgency and server default;
   - `sound-name=message-new-instant` if sound is on;
   - title gets " (snoozed)" for snoozed reminders.
3. Loop over signals, ignoring other apps' notification ids:
   - `ActionInvoked(id, action)` → `Choice{ActionID}` (the "default" body
     click is ignored; it's not a decision);
   - `NotificationClosed(id, reason)`: reason 2 (dismissed by user) →
     `Cancel`; reason 1 (expired) on a non-persistent reminder → wait for the
     dispatcher's timeout; other reasons (closed by us / by invoking an
     action) are ignored.
4. If `decide` says keep, re-send the notification with `replaces_id` (many
   servers remove a notification once an action is clicked).
5. On timeout/shutdown, `CloseNotification(id)`.

The icon is a PNG written once to `~/.cache/bird/bird.png`.

---

## Tray icon

`internal/daemon/tray.go`. `fyne.io/systray` speaks StatusNotifierItem +
DBusMenu over D-Bus, so there's no GTK in the daemon. `RunWithExternalLoop`
returns start/end functions instead of owning `main`.

Menu: a disabled status line ("Next: Lunch break at 13:00", "Configuration
error — see logs"), *Preview next reminder*, *Open configuration* (via
`xdg-open`), *Reload configuration*, *Quit Bird*. Clicks are forwarded to the
loop through the same coalescing channels as everything else. The status
line is only updated when its text changes. All tray methods are safe on a
`nil` tray, so the loop doesn't need `if tray != nil` everywhere.

Without a tray host (stock GNOME) the icon simply doesn't appear; nothing
fails.

---

## Config hot reload

`internal/daemon/watch.go`.

- Watches the config's **directory** with inotify, not the file. Many
  editors (vim, VS Code, gedit) save by writing a temp file and renaming it
  over the original; a watch on the old file would silently stop working.
- Filters to events whose basename is `config.yaml` and op is
  write/create/rename/remove.
- **Debounces 300 ms**: an editor save produces several events; they become
  one reload.
- Sends on `reloads` non-blockingly; if a reload is already pending, nothing
  more is queued.

`Daemon.reload` parses the new file. Invalid → keep the old config, log,
notify, show the error in the tray. Valid → swap the presenter if `renderer`
changed, `engine.SetConfig`, save. The next loop iteration ticks with the
new config immediately.

---

## Platform glue: XDG paths, lock, opening links

`internal/platform/platform.go`.

- **XDG paths.** Honour `XDG_CONFIG_HOME`, `XDG_STATE_HOME`,
  `XDG_CACHE_HOME`, `XDG_RUNTIME_DIR` if they are absolute; otherwise the
  standard `~/.config`, `~/.local/state`, `~/.cache`. Runtime falls back to
  the state dir.
- **Session detection** from `XDG_SESSION_TYPE`, `DISPLAY`,
  `WAYLAND_DISPLAY`, `XDG_CURRENT_DESKTOP`. `HasDisplay()` decides whether
  any UI is attempted.
- **Single-instance lock.** `flock(LOCK_EX|LOCK_NB)` on
  `$XDG_RUNTIME_DIR/bird/bird.lock`; the PID is written into it for humans.
  The kernel drops the lock when the process dies, so a crash never leaves a
  stale lock (unlike PID files).
- **Opening links.** `exec` of `xdg-open <url>` directly (no shell, so no
  quoting or injection issues), refusing anything starting with `-`. Under
  systemd (`INVOCATION_ID` set), it runs `systemd-run --user --scope --quiet
  --collect -- xdg-open <url>`, so the browser lives in its own cgroup and
  isn't killed when `bird.service` restarts. 30 s timeout. Errors are logged
  without the URL.

---

## The icon

`internal/assets/icon.go` draws the bird procedurally: a list of layers
(ellipses and triangles, with slightly grown dark copies as outlines), each
tested per pixel with a point-in-shape function, using **4×4 supersampling**
(16 samples per pixel) for anti-aliasing. `PNG(size)` encodes it;
`IconFile(dir)` writes it once to the cache for the notification server. The
tray uses 64 px, the overlay window 128 px. No image files in the repo for
the icon, and any size can be produced.

---

## Security model

Bird reads a file you write and acts on your desktop, so the rules are simple:

| Rule | How |
|---|---|
| YAML can't run programs | Only `dismiss`, `snooze`, `open_url`. No exec action exists. |
| Links can't become options or local files | `http`/`https` only, host required, no whitespace, no leading `-`; `xdg-open` gets it as a single argv element, never through a shell. |
| Secrets in URLs stay private | URLs are never logged, never in validation errors, never sent to the overlay. |
| The UI has no authority | The overlay only receives labels and ids and only sends back ids. The daemon looks up the action itself. |
| No network | The UI is embedded; nothing is fetched. Bird doesn't open sockets (D-Bus is a local Unix socket). |
| Config text can't inject HTML | The page uses `textContent`. |
| Bounded inputs | 1 MiB config limit; 64 KiB protocol lines; 1 MiB first `show` message; capped overlay log lines. |
| No privileges | Runs as your user; the unit sets `NoNewPrivileges=yes`; files are `0600`/`0700`. |

---

## systemd integration

`packaging/systemd/bird.service` is a **user** unit:

- `PartOf`/`After`/`Requisite=graphical-session.target`: starts only inside a
  graphical session, after it's ready; stops at logout.
- `Type=simple`, `ExecStart=%h/.local/bin/bird` (rewritten to `$BINDIR` by
  `make install-service`).
- `ExecReload=/bin/kill -HUP $MAINPID` → reload config.
- `Restart=on-failure`, `RestartSec=5`. Because a bad config doesn't exit,
  this only triggers on real crashes.
- `NoNewPrivileges=yes`.
- Logs go to the journal (stderr); Bird drops its own timestamps there.

---

## Build system

| Target | Command | Notes |
|---|---|---|
| `make bird` | `CGO_ENABLED=0 go build -trimpath -ldflags '-s -w -X main.version=…'` | Static-ish pure Go binary; `-s -w` strips symbols. |
| `make overlay` | `go build -tags desktop,production,webkit2_41 …` | Checks `pkg-config` for GTK/WebKit first. `desktop` selects the real `main.go` (vs `stub.go`); `production` makes Wails use the embedded assets and skip dev tooling; `webkit2_41` links WebKitGTK 4.1 (`WEBKIT_TAG=` for 4.0). |
| `make test` | `go test -race ./...` | Overlay isn't built by tests (no `desktop` tag), so tests need no GUI libs. |
| `make install` / `install-config` / `install-service` / `uninstall` | | Opt-in; never overwrites your config; uninstall keeps config and state. |

There's no Wails CLI step and no JavaScript build: Wails is used as a plain Go
library.

---

## Cost: RAM, CPU, disk, battery

Measured on the development machine (Zorin OS 18, GNOME on X11, 16 cores,
14 GB RAM, Go 1.26) with the 19-event `examples/workday.yaml`.

### Daemon (`bird`), always running

| Metric | Measured |
|---|---|
| Resident memory (RSS) | **~9.7 MB** (≈4 MB heap/anon + ≈5.5 MB mapped binary); peak 10.9 MB |
| CPU | **0.35 s total over 74 minutes** (including startup), i.e. well under 0.01 % of one core |
| Threads | 13 (Go runtime + tray/D-Bus); all idle |
| Open file descriptors | 8 (lock, inotify, D-Bus, std streams) |
| Wake-ups | One every ≤ 30 s, plus exact wake-ups at due times |
| Binary on disk | 5.8 MB |
| Virtual memory (VSZ) | ~1.2 GB: Go's reserved address space, **not** real memory. Ignore it. |

Per loop iteration the work is: for each event, one `Prev` (≤ 9
`time.Date` calls), a few comparisons, and for `NextWake` one `Next` per
event plus a sort. With 19 events that's microseconds. It scales linearly;
hundreds of events are still negligible. For thousands, the per-tick scan
could become a min-heap of next times without changing the engine's API.

### Overlay (`bird-overlay`), only while a reminder is visible

| Metric | Measured |
|---|---|
| Memory, proportional (PSS) | **~200 MB total**: `bird-overlay` ~63 MB, `WebKitWebProcess` ~121 MB, `WebKitNetworkProcess` ~15 MB |
| Memory, RSS sum | ~420 MB, but much of that is shared libraries (GTK, WebKit, Mesa) also used by other apps; PSS is the fairer number |
| Startup CPU | ~2 CPU-seconds across the three processes (WebKit cold start) |
| Time to appear | ~1–1.5 s after the due time (WebKit start-up) |
| While perched | Small continuous cost from the idle CSS animations (breathing, blinking); not separately measured |
| Binary on disk | 9.1 MB (plus system GTK/WebKitGTK) |
| After closing | 0: all three processes exit |

If 200 MB for a few seconds is too much on a small machine, use
`renderer: notification`: the daemon then never starts WebKit at all.

### Disk

| What | Size / frequency |
|---|---|
| `state.json` | ~1.4 KB for 19 events (~75 bytes per event). Written (with `fsync`) only when something changes: a reminder fires, you answer, or the config reloads. A handful of writes per reminder, none while idle. |
| Icon cache | One small PNG, written once. |
| Logs | A few lines per reminder at `info` level, in the journal. |

### Network and battery

No network use at all. Battery impact is the 30 s wake-up (negligible) plus
the overlay while it is on screen.

---

## Testing strategy

`make test` runs `go test -race ./...`. Because the engine and schedules take
`now` as a parameter, tests are deterministic and fast; nothing sleeps for
real minutes.

| Package | What's covered |
|---|---|
| `config` (9 tests) | Valid/invalid YAML, unknown keys, missing fields, duplicate ids, bad times/days/zones, every action kind, URL scheme rules, option-injection attempts, URLs never in errors, `examples/workday.yaml` stays valid. |
| `schedule` (10) | Daily/weekly maths, zones, DST spring-forward gap, fall-back fires once. |
| `events` (24) | Firing on time; no duplicates (same run, after restart, after reload, stale responses); crash recovery; catch-up incl. `0s`; snooze timing/persistence/superseding; `open_url` acknowledge; cancel→snooze; timeouts; bounded display retries; disabling/removing events. |
| `desktop` (8) | The overlay protocol against a fake overlay process (choose, keep→cancel, crash, timeout/close, no display, stdout noise) and chain fallback. |
| `daemon` (4) | End-to-end with a fake presenter: fire → snooze → persisted; open link → restart → no duplicate; second instance rejected; broken config → fixed → reminder fires. |
| `platform` (3), `assets` (1) | XDG paths, session detection, lock; icon renders. |

Not automated: the real WebKit window, key injection, the tray against a
real host, Wayland.

---

## Design questions and answers

**Why Go?** One static-ish binary for the daemon, good concurrency
primitives for a loop + UI process + watchers, a solid `time` package with
IANA zones, and Wails for the UI in the same language.

**Why not cron or systemd timers?** They can run a command at a time, but
they don't know whether you answered, can't snooze an occurrence without
changing the schedule, don't deduplicate across restarts with a catch-up
policy, and would need a separate UI anyway. Bird's value is that state
machine.

**Why not keep the WebKit window open all the time (hidden)?** It would cost
~200 MB all day for something visible a few minutes a day, and a WebKit crash
would take the scheduler with it.

**Why not Electron?** It ships its own Chromium (100+ MB on disk, more RAM).
Wails uses the system WebKitGTK that is usually already installed.

**Why a watermark instead of "last fired at"?** The watermark is the
*scheduled* slot, not the wall-clock moment. Comparing slots makes "is this
new?" exact and independent of when Bird happened to wake up, how long the
reminder stayed up, or snoozes.

**Why write state before showing?** Showing is a side effect you can't take
back. Recording first means the worst a crash can do is re-show a reminder
you never answered, never show one you already did.

**Why can a snooze be superseded?** If you snooze yesterday's 17:00 reminder
and the machine sleeps until today's 17:00, you want today's reminder, not
both.

**Why the catch-up window?** Without one, opening a laptop after a long
weekend would play back every missed water reminder. With one, recent
reminders survive short sleeps.

**Why only one reminder on screen?** They all land in the same spot. Queuing
is predictable and readable.

**Why does Esc snooze?** A key pressed by accident should never make you miss
a meeting.

**Why does the UI not get the URL?** Least privilege. The renderer is the
most complex, most exposed part (a web engine); keeping it powerless means a
bug there can't open arbitrary links.

**Why fd 3?** So third-party log noise on stdout can't break the protocol.

**Why move the window instead of making a full-screen transparent window?**
A full-screen transparent window would block clicks on everything beneath
it (or need input-shape tricks that differ per compositor). A small moving
window doesn't get in your way.

**Why D-Bus directly for notifications instead of `notify-send`?** To get
action buttons and to know which one was clicked, which `notify-send` can't
report reliably, and to close/replace notifications by id.

**What happens if the clock jumps?** Forward: slots in between are treated
like missed ones (shown if within the catch-up window). Backward: `Prev` is
older than the watermark, so nothing re-fires.

**What happens on logout?** systemd stops the service (SIGTERM). Any visible
reminder is told to close; its `Active` record stays on disk and is
re-shown at next login if still within the catch-up window.

---

## Known limitations

- Wayland is untested; native Wayland without XWayland can't position
  windows or force always-on-top, so the bird flies inside a centred window.
- No compositor (some bare X11 WMs) → the overlay is an opaque rectangle.
- Overlay can't appear over exclusive-fullscreen apps and shows up in screen
  shares like any window.
- Only `daily`/`weekly` schedules, one time each; one global time zone.
- Only one character and one landing spot.
- Keyboard handling and the tray were reviewed but not tested with
  automation.

---

## Where to change things

| You want to… | Change |
|---|---|
| Add a schedule type (interval, once, cron) | Implement `schedule.Schedule` and add a case in `schedule.New`; add any new YAML fields to `config.Schedule`. The engine's dedup and catch-up logic work unchanged. |
| Add an action kind | Allow it in `config.validateAction`, handle it in `events.Engine.Resolve`. The UI only needs a label. |
| Add a renderer (layer-shell Wayland overlay, another OS) | Implement `desktop.Presenter`, wire it into `daemon.selectPresenter`. |
| Add a character | SVG + CSS in `frontend/dist`, keyed by `reminder.character`; allow the name in `config.validate`. |
| Change animation timings | `flightDurations` in `cmd/bird-overlay/main.go` **and** `SPEED_MS` in `app.js` (keep them equal). |
| Change the wake interval | `maxSleep` in `internal/daemon/daemon.go`. |
| Change retry policy | `RetryDelay` / `MaxRetries` in `internal/events/engine.go`. |
