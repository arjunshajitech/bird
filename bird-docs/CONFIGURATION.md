# Configuring Bird

This is the complete reference for Bird's YAML file: every key, its type,
default, allowed values and limits, plus the command-line flags, environment
variables and build variables that change Bird's behaviour.

Everything here is taken from the validation code in
[`internal/config/config.go`](../internal/config/config.go) and
[`internal/schedule/schedule.go`](../internal/schedule/schedule.go). If this
document and the code ever disagree, the code wins, and
`bird --check-config` will tell you exactly what it thinks.

---

## Contents

1. [Where the file lives](#where-the-file-lives)
2. [The shape of the file](#the-shape-of-the-file)
3. [`app`](#app)
4. [`appearance`](#appearance)
5. [`events[]`](#events)
6. [`events[].schedule`](#eventsschedule)
7. [`events[].actions[]`](#eventsactions)
8. [Value formats (durations, times, ids, URLs)](#value-formats)
9. [Defaults Bird fills in for you](#defaults-bird-fills-in-for-you)
10. [Validation and error messages](#validation-and-error-messages)
11. [Reloading and when changes take effect](#reloading-and-when-changes-take-effect)
12. [Recipes for different kinds of events](#recipes-for-different-kinds-of-events)
13. [YAML tips (quoting, anchors, comments)](#yaml-tips)
14. [Command-line flags](#command-line-flags)
15. [Environment variables](#environment-variables)
16. [Build and install variables (`make`)](#build-and-install-variables-make)
17. [Cheat sheet](#cheat-sheet)

---

## Where the file lives

| | Path |
|---|---|
| Default | `$XDG_CONFIG_HOME/bird/config.yaml`, which is normally `~/.config/bird/config.yaml` |
| Override | `bird --config /some/other/file.yaml` |
| Starter file | [`examples/workday.yaml`](../examples/workday.yaml) (`make install-config` copies it into place, and never overwrites an existing file) |

Bird **never writes** to this file. Its own runtime data (what has already
fired, pending snoozes) goes to `~/.local/state/bird/state.json`, so your
comments and formatting are never touched.

The file is read with a hard 1 MiB limit. That is far more than any real
configuration needs (hundreds of events fit in a few dozen KB).

---

## The shape of the file

There are exactly three top-level keys. All are optional; an empty
`events` list is valid (Bird just has nothing to do).

```yaml
app:          # how the daemon behaves
  ...
appearance:   # how reminders look and sound
  ...
events:       # the list of things to remind you about
  - id: ...
    ...
```

**Unknown keys are errors.** A typo such as `titel:` or `schedul:` is
rejected instead of being silently ignored, so a misspelled key never turns
into a reminder that quietly never fires.

---

## `app`

Daemon-wide behaviour.

| Key | Type | Default | Allowed values | What it does |
|---|---|---|---|---|
| `name` | string | `Bird` | any text | Application name shown in desktop notifications (fallback mode) and in the "is running" notification. |
| `timezone` | string | `Local` | `Local` (any case), or an IANA zone name such as `Europe/Berlin`, `Asia/Kolkata`, `America/New_York`, `UTC` | The time zone every `schedule.time` is interpreted in. `Local` follows the machine's zone. |
| `start_minimized` | bool | `true` | `true` / `false` | `false` shows a one-off "Bird is running – N reminder(s) scheduled" notification when the daemon starts. `true` starts silently. |
| `catch_up_window` | duration | `15m` | `0s` to `24h` | How late a reminder may still be shown if the computer was off, asleep or Bird wasn't running at the exact time. Anything later is skipped and logged. `0s` means "never show late reminders". Also applies to snoozes that came due while asleep and to reminders that were on screen when Bird crashed. |
| `tray` | bool | `true` | `true` / `false` | Show the tray icon and menu. Needs a StatusNotifierItem/AppIndicator host (KDE, Xfce, Cinnamon, GNOME with the AppIndicator extension). Without a host it is simply invisible, so leaving it on costs nothing. `bird --no-tray` overrides it. |

Example:

```yaml
app:
  name: Bird
  timezone: Asia/Kolkata
  start_minimized: true
  catch_up_window: 30m
  tray: true
```

### Choosing `catch_up_window`

| Value | Effect |
|---|---|
| `0s` | Strict. A reminder shows only if Bird is awake at its time (within the ≤30 s check interval). Nothing is shown late. |
| `15m` (default) | Closing the lid for a few minutes doesn't cost you a reminder; a whole weekend away doesn't produce a pile of stale ones. |
| `2h`–`24h` | Good for things that still matter later the same day (medication, "submit timesheet"). Morning reminders will show when you open the laptop at noon. |

Only the **latest** missed occurrence of each event can be shown. If your
laptop sleeps through three daily 09:00 reminders, you will see at most
one, and only if it is within the window.

---

## `appearance`

How reminders are rendered. These apply to every event unless an event
overrides them (only `sound` can be overridden per event).

| Key | Type | Default | Allowed values | What it does |
|---|---|---|---|---|
| `character` | string | `bird` | `bird` | The animated character. Only `bird` ships today; the key exists so others can be added later. |
| `always_on_top` | bool | `true` | `true` / `false` | Ask the window manager to keep the overlay above other windows. On native Wayland without XWayland, apps can't request this. |
| `animation_speed` | string | `normal` | `slow`, `normal`, `fast` | Flight duration: `slow` 2.6 s, `normal` 1.6 s, `fast` 0.9 s. |
| `sound_enabled` | bool | `true` | `true` / `false` | A short synthesised two-note chirp when the bird lands. In notification mode, asks the notification server to play `message-new-instant`. |
| `renderer` | string | `auto` | `auto`, `overlay`, `notification` | See below. |

### `renderer`

| Value | Behaviour |
|---|---|
| `auto` | Try the animated `bird-overlay` window. If it can't start (binary missing, no display, crash, not ready within 20 s), fall back to a desktop notification with the same buttons. **Recommended.** |
| `overlay` | Only the animated overlay. If it fails, there is no notification fallback; Bird retries every minute (up to 60 times). |
| `notification` | Never start the overlay; always use a standard desktop notification with action buttons. Useful on Wayland without XWayland, on very low-RAM machines, or if you just prefer notifications. |

Persistent reminders in notification mode are sent with *critical* urgency
and no expiry, which GNOME, KDE and most notification servers keep on screen
until you answer.

---

## `events[]`

Each entry is one recurring reminder. Bird is not limited to meetings:
an event is any recurring moment you want to be interrupted for: a
stand-up, lunch, a water break, medication, a stretch, a weekly report,
bedtime, a focus block ending, taking the bins out.

| Key | Type | Required | Default | Allowed values / limits | What it does |
|---|---|---|---|---|---|
| `id` | string | ✔ | | 1–64 chars, regex `^[a-z0-9][a-z0-9_-]{0,63}$` (lowercase letters, digits, `-`, `_`; must start with a letter or digit). Unique across all events. | The event's identity. Bird's history (what already fired, pending snooze) is stored under this id. **Keep it stable**: renaming an id makes Bird treat it as a brand-new event (its old history is deleted). |
| `title` | string | ✔ | | Non-blank, at most 120 bytes | Big text in the speech bubble / notification title. |
| `message` | string | | empty | At most 500 bytes | Smaller text under the title. |
| `enabled` | bool | | `true` | `true` / `false` | `false` keeps the event in the file (still validated) but it never fires and any pending snooze is cancelled. Handy for vacations. |
| `schedule` | map | ✔ | | see [`events[].schedule`](#eventsschedule) | When the event fires. |
| `persistent` | bool | | `true` | `true` / `false` | `true`: stays on screen until you choose something. `false`: goes away by itself after `timeout`, which counts as **dismissed** (not snoozed). |
| `timeout` | duration | | `60s` | `5s` to `24h` | How long a non-persistent reminder stays up. Validated on every event but **only used when `persistent: false`**. Note: `0s` is treated as "not set" and becomes `60s`. |
| `sound` | bool | | value of `appearance.sound_enabled` | `true` / `false` | Per-event sound override (e.g. silence the half-hourly water reminder but keep the meeting chirp). |
| `animation.type` | string | | `fly-across` | `fly-across`, `none` | `none` makes the reminder simply appear (no flight). The system "reduce motion" setting does the same for all events. |
| `animation.stop_at` | string | | `center` | `center` | Where the bird lands. Only `center` exists today. |
| `actions` | list | | one button: `{id: dismiss, label: "Got it", action: dismiss}` | 0–4 entries; see [`events[].actions[]`](#eventsactions) | The buttons. The **first** one is the highlighted primary button and gets keyboard focus. |

Lengths are counted in **bytes** of UTF-8, so a title with emoji or
non-Latin script hits the 120 limit with fewer visible characters.

---

## `events[].schedule`

| Key | Type | Required | Allowed values | Notes |
|---|---|---|---|---|
| `type` | string | ✔ | `daily`, `weekly` | Case-sensitive (lowercase). |
| `time` | string | ✔ | `"HH:MM"`, 24-hour, `00:00`–`23:59` | Exactly five characters: two-digit hour, colon, two-digit minute. `9:00`, `09:00:00`, `9am` are all rejected. Quote it. |
| `days` | list of strings | for `weekly` only | `mon` `tue` `wed` `thu` `fri` `sat` `sun`, or the full names `monday` … `sunday`, in any case | At least one day; no duplicates. **Not allowed** with `daily` (an error, not ignored). |

```yaml
schedule: { type: daily, time: "13:00" }

schedule:
  type: weekly
  time: "11:00"
  days: [mon, tue, wed, thu, fri]

schedule: { type: weekly, time: "17:30", days: [Friday] }
```

### How times behave

- **Time zone.** Times are wall-clock times in `app.timezone`.
- **Daylight saving, spring forward.** A time that doesn't exist that day
  (e.g. `02:30` when clocks jump 02:00→03:00) fires right after the jump,
  at `03:30`. It is never skipped and never fires early.
- **Daylight saving, fall back.** A time that happens twice (e.g. `01:30`
  when 02:00 goes back to 01:00) fires **once**.
- **Precision.** Bird fires within milliseconds of the time while awake.
  After suspend it notices within 30 seconds of waking.

### What isn't supported (yet)

There is no `interval`, `once`/date, cron, "last Friday of the month", or
"every 2 weeks" type. Workarounds:

- **Every N minutes/hours:** one event per slot. YAML anchors keep it short
  (see [YAML tips](#yaml-tips) and `examples/workday.yaml`).
- **Several times a day:** one event per time.
- **One-off reminder:** a `weekly` event on that weekday, then set
  `enabled: false` or delete it afterwards.

---

## `events[].actions[]`

Up to **4** buttons per event. Every action is one of three kinds. Bird
deliberately has **no "run a command" action**: nothing in the YAML can make
Bird execute a program other than opening a link in your browser.

| Key | Type | Applies to | Required | Default | Allowed values / limits |
|---|---|---|---|---|---|
| `id` | string | all | ✔ | | Same rules as event ids (`^[a-z0-9][a-z0-9_-]{0,63}$`); unique **within this event** (different events may reuse `snooze`, `done`, …). |
| `label` | string | all | ✔ | | Non-blank, at most 40 bytes. The button text. |
| `action` | string | all | ✔ | | `dismiss`, `snooze`, `open_url` |
| `minutes` | int | `snooze` only | | `5` | `1`–`1440` (24 h). |
| `url` | string | `open_url` only | ✔ | | Absolute `http://` or `https://` URL with a host. No spaces/tabs/newlines, must not start with `-`. |
| `acknowledge` | bool | `open_url` only | | `true` | `true`: opening the link also closes the reminder. `false`: the link opens and the reminder **stays** on screen (useful when you want to open notes first and still have a "Done" button). |

Keys that don't belong to an action kind are **errors**, not ignored:
`dismiss` takes no `url`/`minutes`/`acknowledge`, `snooze` takes no
`url`/`acknowledge`, and `open_url` takes no `minutes`.

### What each kind does

| Kind | Effect |
|---|---|
| `dismiss` | Done with this occurrence. The bird flies away. The next occurrence comes at the next scheduled time. |
| `snooze` | The bird flies away and comes back after `minutes`, with a "Snoozed reminder" badge. The regular schedule is **not** shifted: a 11:00 daily event snoozed to 11:05 is still due at 11:00 tomorrow. If the next regular occurrence comes before the snooze is due, the snooze is dropped (you never get yesterday's reminder on top of today's). |
| `open_url` | The **daemon** (not the UI) opens the URL with `xdg-open`, in your default browser or app. With `acknowledge: true` (default) the reminder closes; with `false` it stays. URLs are never written to logs or error messages, so links with embedded passwords (Zoom `?pwd=`, etc.) stay private. |

### Escape, Alt+F4 and dismissing a notification

Closing a reminder without pressing a button is not "lost". Bird maps it to:

1. the event's **first `snooze` action**, if it has one; otherwise
2. its **first `dismiss` action**; otherwise
3. a plain close (counts as done).

So if you want "Esc means remind me later", include a snooze button.

### Buttons and the keyboard

Buttons are numbered `1`–`4` in the order you list them. Pressing the
number activates that button, `Enter`/`Space` activate the focused one,
`Tab` moves between them. Keys are ignored for the first 1.2 s after the bird
lands and during fast typing bursts, so text you were typing in another app
can't accidentally pick an action.

### Common action sets

```yaml
# Meeting
actions:
  - { id: join,   label: "Join meeting",  action: open_url, url: "https://meet.example.com/abc" }
  - { id: snooze, label: "In 5 minutes",  action: snooze,   minutes: 5 }

# Habit / break
actions:
  - { id: done,   label: "Done",          action: dismiss }
  - { id: later,  label: "In 10 minutes", action: snooze,   minutes: 10 }

# Open a document but keep the reminder until you confirm
actions:
  - { id: open,   label: "Open report",   action: open_url, url: "https://docs.example.com/weekly", acknowledge: false }
  - { id: done,   label: "Submitted",     action: dismiss }
  - { id: later,  label: "In 1 hour",     action: snooze,   minutes: 60 }

# No actions key at all → a single "Got it" button
```

---

## Value formats

| Kind | Format | Examples | Rejected |
|---|---|---|---|
| **duration** (`catch_up_window`, `timeout`) | Go duration **string**: a number followed by `ms`, `s`, `m`, `h`; units can be combined | `"90s"`, `15m`, `1h30m`, `0s` | `90` (bare number), `"15 min"`, `1d` (no day unit; use `24h`) |
| **time** (`schedule.time`) | `"HH:MM"` 24-hour | `"07:05"`, `"13:00"`, `"23:59"` | `"7:05"`, `"24:00"`, `"13:00:00"`, `"1pm"` |
| **weekday** (`schedule.days[]`) | short or long English name, any case | `mon`, `Monday`, `FRI` | `mo`, `weekday`, `1` |
| **id** (event and action) | `^[a-z0-9][a-z0-9_-]{0,63}$` | `daily-standup`, `water_1030`, `x` | `Standup` (uppercase), `-x` (leading dash), `stand up` (space), names longer than 64 |
| **bool** | YAML boolean | `true`, `false` (also accepted: `yes`/`no`, `on`/`off`, `y`/`n`) | `"true"` in quotes, `1`/`0` |
| **URL** (`open_url.url`) | absolute `http`/`https` with host | `https://zoom.us/j/123?pwd=abc` | `zoom.us/j/123` (no scheme), `file:///etc/passwd`, `javascript:…`, `mailto:…`, anything with whitespace or starting with `-` |
| **timezone** | `Local` or IANA name | `Local`, `UTC`, `Europe/London` | `IST`, `GMT+5:30`, `PST` (abbreviations are ambiguous) |

---

## Defaults Bird fills in for you

Before validation, missing values are filled in. A minimal valid file is
just:

```yaml
events:
  - id: lunch
    title: "Lunch break"
    schedule: { type: daily, time: "13:00" }
```

which Bird expands to:

```yaml
app:
  name: Bird
  timezone: Local
  start_minimized: true
  catch_up_window: 15m
  tray: true
appearance:
  character: bird
  always_on_top: true
  animation_speed: normal
  sound_enabled: true
  renderer: auto
events:
  - id: lunch
    title: "Lunch break"
    message: ""
    enabled: true
    schedule: { type: daily, time: "13:00" }
    animation: { type: fly-across, stop_at: center }
    persistent: true
    timeout: 60s
    # sound: inherits appearance.sound_enabled
    actions:
      - { id: dismiss, label: "Got it", action: dismiss }
```

Booleans are stored as "unset / true / false", so writing `false` explicitly
is always respected; only an absent key gets the default.

---

## Validation and error messages

Run `bird --check-config` (add `--config PATH` for another file) after every
edit. On success it prints the time zone, catch-up window and each event's
next firing time:

```
$ bird --check-config
Configuration OK: /home/me/.config/bird/config.yaml
Time zone: Local (now Sat 2026-10-10 10:08 IST)
Catch-up window: 10m0s

EVENT          SCHEDULE                                NEXT                  ACTIONS
daily-standup  weekly at 11:00 on mon,tue,wed,thu,fri  Mon 2026-10-12 11:00  join:open_url, snooze:snooze
lunch          daily at 13:00                          Sat 2026-10-10 13:00  dismiss:dismiss, snooze:snooze
```

Exit code is `0` if valid, `1` if not, which makes it usable in scripts or a
git pre-commit hook.

Checking happens in two stages:

1. **YAML decoding.** Syntax errors, unknown keys, wrong types (a list where
   a map is expected, a bare number for a duration). These stop at the first
   problem and show a line number:
   ```
   invalid YAML: yaml: unmarshal errors:
     line 4: field titel not found in type config.Event
   invalid YAML: line 5: invalid duration "90" (examples: 90s, 15m, 1h)
   ```
2. **Validation.** Once the file decodes, *every* remaining problem is
   listed at once, each with its location (`events[index] (id).field`):
   ```
   invalid configuration:
     - app.timezone: unknown time zone "Mars/Base" (use Local or an IANA name like Europe/Berlin)
     - events[0] (Bad).id: must be lowercase letters, digits, '-' or '_' (max 64 chars)
     - events[0] (Bad).schedule: time "9:00" must use 24-hour HH:MM format
     - events[0] (Bad).actions[0] (x).url: must use http or https
   ```

URLs are **never** echoed in errors, even invalid ones.

### Full list of validation rules

| Where | Rule |
|---|---|
| `app.timezone` | Must be `Local` or a known IANA zone. |
| `app.catch_up_window` | `0s` ≤ value ≤ `24h`. |
| `appearance.character` | `bird`. |
| `appearance.animation_speed` | `slow`, `normal`, `fast`. |
| `appearance.renderer` | `auto`, `overlay`, `notification`. |
| `events[].id` | Required, id pattern, unique. |
| `events[].title` | Required (non-blank), ≤ 120 bytes. |
| `events[].message` | ≤ 500 bytes. |
| `events[].schedule.type` | Required, `daily` or `weekly`. |
| `events[].schedule.time` | Required, `HH:MM`, 00:00–23:59. |
| `events[].schedule.days` | Required and non-empty for `weekly`; forbidden for `daily`; known names; no duplicates. |
| `events[].animation.type` | `fly-across` or `none`. |
| `events[].animation.stop_at` | `center`. |
| `events[].timeout` | `5s` ≤ value ≤ `24h`. |
| `events[].actions` | At most 4. |
| `actions[].id` | Required, id pattern, unique within the event. |
| `actions[].label` | Required (non-blank), ≤ 40 bytes. |
| `actions[].action` | Required: `dismiss`, `snooze`, `open_url`. |
| `snooze.minutes` | 1–1440. |
| `open_url.url` | Required, http/https, has host, no whitespace, no leading `-`. |
| Cross-kind keys | `dismiss` has no `url`/`minutes`/`acknowledge`; `snooze` has no `url`/`acknowledge`; `open_url` has no `minutes`. |
| Anywhere | No unknown keys. File ≤ 1 MiB. File not empty. |

---

## Reloading and when changes take effect

Save the file and the running daemon reloads it automatically about 0.3 s
later (it watches the directory, so editors that save via rename work too).
Other ways: tray → *Reload configuration*, or
`systemctl --user reload bird` (SIGHUP).

| If the new file is… | Bird… |
|---|---|
| valid | swaps it in. Each event's history follows its `id`. |
| invalid | **keeps running the previous configuration**, logs the errors and shows a "Bird configuration problem" notification. |
| missing at startup | starts with no events and waits for a valid file (it does not crash-loop under systemd). |

What a change does to existing state:

| Change | Effect |
|---|---|
| Edit title, message, actions, sound, timeout | Used from the next time the reminder is shown. |
| Change `schedule.time` / `days` | Takes effect immediately. If the new time has *just* passed (within `catch_up_window`) and that slot was never handled, it fires right away; otherwise the event simply waits for its next time. It never fires twice for a slot it already showed. |
| Rename an `id` | Treated as delete + add: old history dropped, new event starts fresh. |
| Remove an event | Its history (and any pending snooze) is deleted. |
| `enabled: false` | Stops firing; a pending snooze is cancelled. |
| Change `renderer` | Applies from the next reminder; the one on screen keeps its current renderer. |
| Change `tray` | Needs a restart (`systemctl --user restart bird`). |
| Change `start_minimized` | Only matters at startup. |

---

## Recipes for different kinds of events

Bird reminds you of **anything that happens at a fixed time on a fixed set of
days**. Some ideas:

### Meeting with a link

```yaml
- id: daily-standup
  title: "Stand-up meeting"
  message: "Stop coding. Time to meet your team!"
  schedule: { type: weekly, time: "11:00", days: [mon, tue, wed, thu, fri] }
  actions:
    - { id: join,   label: "Join meeting",           action: open_url, url: "https://meet.example.com/abc-defg-hij" }
    - { id: snooze, label: "Remind me in 5 minutes", action: snooze,   minutes: 5 }
```

### Meal or break

```yaml
- id: lunch
  title: "Lunch break"
  message: "Step away from the screen and eat something."
  schedule: { type: daily, time: "13:00" }
  actions:
    - { id: done,  label: "Going now",     action: dismiss }
    - { id: later, label: "In 15 minutes", action: snooze, minutes: 15 }
```

### Medication (must not be missed)

```yaml
- id: meds-evening
  title: "Evening medication"
  message: "Take your tablets with water."
  schedule: { type: daily, time: "20:00" }
  persistent: true            # stays until you answer
  sound: true
  actions:
    - { id: taken, label: "Taken",        action: dismiss }
    - { id: later, label: "In 10 minutes", action: snooze, minutes: 10 }
```

Pair it with a larger `app.catch_up_window` (e.g. `3h`) so a reminder that
fell during sleep/suspend is still shown when you come back.

### Quick nudge that disappears on its own

```yaml
- id: posture
  title: "Posture check"
  message: "Shoulders down, back straight."
  schedule: { type: daily, time: "15:00" }
  persistent: false
  timeout: 20s
  sound: false
  animation: { type: none }
```

### Weekly task with a document

```yaml
- id: weekly-report
  title: "Weekly report"
  message: "Fill in this week's status before you log off."
  schedule: { type: weekly, time: "16:30", days: [fri] }
  actions:
    - { id: open,  label: "Open report", action: open_url, url: "https://docs.example.com/status", acknowledge: false }
    - { id: done,  label: "Submitted",   action: dismiss }
    - { id: later, label: "In 1 hour",   action: snooze, minutes: 60 }
```

### End of work day / bedtime

```yaml
- id: wrap-up
  title: "Wrap up"
  message: "Commit, push, and write tomorrow's first task down."
  schedule: { type: weekly, time: "18:30", days: [mon, tue, wed, thu, fri] }

- id: bedtime
  title: "Screens off"
  schedule: { type: daily, time: "23:00" }
  actions:
    - { id: ok,   label: "Good night",     action: dismiss }
    - { id: more, label: "20 more minutes", action: snooze, minutes: 20 }
```

### Weekend-only or specific days

```yaml
- id: plants
  title: "Water the plants"
  schedule: { type: weekly, time: "10:00", days: [wed, sun] }
```

### An event in another time zone

`app.timezone` is global, so pick the zone the majority of your events are
in. A one-off event in another zone has to be converted to that zone by
hand (and rechecked around DST changes).

### Temporarily turn something off

```yaml
- id: daily-standup
  enabled: false     # on vacation
  ...
```

---

## YAML tips

**Quote times.** `time: "09:30"` always works. Unquoted `09:30` happens to
work too, but `9:30` is rejected and quoting avoids any surprises.

**Booleans:** prefer `true` and `false`. For boolean keys Bird also accepts
`yes`/`no` and `on`/`off`, but a quoted `"true"` or a number is an error.

**Flow style** (`{ ... }`, `[ ... ]`) and **block style** can be mixed freely:

```yaml
schedule: { type: weekly, time: "11:00", days: [mon, wed, fri] }
```

**Anchors and merge keys** (`&name`, `*name`, `<<:`) are supported and are
the cleanest way to write "every 30 minutes":

```yaml
events:
  - &water                     # define once
    id: water-0930
    schedule: { type: daily, time: "09:30" }
    title: "Drink some water"
    persistent: false
    timeout: 60s
    actions:
      - { id: done,  label: "Done",          action: dismiss }
      - { id: later, label: "In 10 minutes", action: snooze, minutes: 10 }
  - <<: *water                 # copy it, override id and time
    id: water-1000
    schedule: { type: daily, time: "10:00" }
  - <<: *water
    id: water-1030
    schedule: { type: daily, time: "10:30" }
```

Anchors also work for lists: `days: &workdays [mon, tue, wed, thu, fri]`
then `days: *workdays` elsewhere.

**Avoid collisions.** Reminders are shown one at a time. If two events are
due at the same minute the second waits until you've answered the first.
`examples/workday.yaml` deliberately skips the water reminder at 11:00 on
weekdays (stand-up) and 13:00 (lunch).

**Comments** (`# ...`) are fine anywhere; Bird never rewrites the file.

---

## Command-line flags

### `bird` (the daemon)

| Flag | Default | What it does |
|---|---|---|
| *(none)* | | Run the daemon in the foreground. This is what the systemd unit runs. |
| `--config PATH` | `~/.config/bird/config.yaml` | Use a different configuration file (works with every mode below). |
| `--check-config` | | Validate the file, print the next time of every event, exit `0`/`1`. Does not need a display and does not touch state. |
| `--preview ID` | | Show event `ID`'s reminder right now, wait for you to choose, print `chosen action: <id>` and exit. Never changes state and **never performs the action** (no link is opened). Works while the daemon is running. |
| `--log-level LEVEL` | `info` | `debug`, `info`, `warn`, `error`. `debug` also shows GTK/WebKit output from the overlay (first 50 lines per reminder). |
| `--no-tray` | off | Don't create the tray icon, regardless of `app.tray`. |
| `--version` | | Print the version (set from `git describe` at build time). |
| `-h`, `--help` | | Usage. |

Exit codes: `0` success, `1` error (invalid config, already running,
fatal), `2` bad command-line usage.

Only one daemon can run per user. A second `bird` exits with
`bird is already running`. `--check-config` and `--preview` don't take the
lock, so they are always safe to run.

### `bird-overlay` (the UI)

| Flag | What it does |
|---|---|
| `--demo` | Show a sample stand-up reminder without the daemon. Buttons just make the bird fly away. Good for testing your display setup. |

Without `--demo` it must be started by `bird` (it expects the protocol pipe
on file descriptor 3) and exits with an explanatory message otherwise.

### Systemd unit

`packaging/systemd/bird.service` runs plain `bird`. To add flags or
environment variables without editing the installed file:

```bash
systemctl --user edit bird
```
```ini
[Service]
ExecStart=
ExecStart=%h/.local/bin/bird --log-level debug
Environment=WEBKIT_DISABLE_DMABUF_RENDERER=1
```

---

## Environment variables

| Variable | Read by | Effect |
|---|---|---|
| `XDG_CONFIG_HOME` | bird | Base for the default config path (`$XDG_CONFIG_HOME/bird/config.yaml`). Must be absolute. |
| `XDG_STATE_HOME` | bird | Base for `state.json` (default `~/.local/state`). |
| `XDG_CACHE_HOME` | bird | Base for the cached notification icon (default `~/.cache`). |
| `XDG_RUNTIME_DIR` | bird | Where the single-instance lock lives (`$XDG_RUNTIME_DIR/bird/bird.lock`); falls back to the state dir. |
| `BIRD_OVERLAY` | bird | Absolute path to the `bird-overlay` binary. Otherwise Bird looks next to its own executable, then on `$PATH`. |
| `DISPLAY`, `WAYLAND_DISPLAY`, `XDG_SESSION_TYPE`, `XDG_CURRENT_DESKTOP` | bird, bird-overlay | Session detection. With neither `DISPLAY` nor `WAYLAND_DISPLAY`, the overlay isn't attempted. |
| `GDK_BACKEND` | bird-overlay | If unset on a Wayland session with XWayland, Bird sets `x11` so the window can fly and stay on top. Set it yourself (e.g. `wayland`) to override. |
| `WEBKIT_DISABLE_DMABUF_RENDERER=1` | bird-overlay (WebKitGTK) | Workaround for a blank or crashing overlay, common with NVIDIA drivers. |
| `INVOCATION_ID` | bird | Set by systemd. When present, links are opened in their own `systemd-run --user --scope` so restarting Bird doesn't kill the browser. |
| `JOURNAL_STREAM` | bird | Set by systemd. Bird then omits its own timestamps (the journal adds them). |

---

## Build and install variables (`make`)

| Variable | Default | Effect |
|---|---|---|
| `VERSION` | `git describe --tags --always --dirty` | Embedded in `bird --version`. |
| `PREFIX` | `~/.local` | Install prefix. |
| `BINDIR` | `$(PREFIX)/bin` | Where `make install` puts the two binaries (the unit is rewritten to point here). |
| `CONFIGDIR` | `$XDG_CONFIG_HOME/bird` or `~/.config/bird` | Where `make install-config` creates `config.yaml`. |
| `UNITDIR` | `~/.config/systemd/user` | Where the user unit is installed. |
| `WEBKIT_TAG` | `webkit2_41` | Build the overlay against WebKitGTK 4.1. Set it empty (`make overlay WEBKIT_TAG=`) for WebKitGTK 4.0 on older distros. |

---

## Cheat sheet

```yaml
app:
  name: Bird                 # string
  timezone: Local            # Local | IANA name
  start_minimized: true      # bool
  catch_up_window: 15m       # duration 0s..24h
  tray: true                 # bool

appearance:
  character: bird            # bird
  always_on_top: true        # bool
  animation_speed: normal    # slow | normal | fast
  sound_enabled: true        # bool
  renderer: auto             # auto | overlay | notification

events:
  - id: my-event             # required, ^[a-z0-9][a-z0-9_-]{0,63}$, unique
    title: "Title"           # required, ≤120 bytes
    message: "Details"       # ≤500 bytes
    enabled: true            # bool
    schedule:                # required
      type: weekly           # daily | weekly
      time: "09:00"          # "HH:MM" 24h
      days: [mon, fri]       # weekly only: mon..sun / monday..sunday
    animation:
      type: fly-across       # fly-across | none
      stop_at: center        # center
    persistent: true         # bool; false = auto-close after timeout (= dismiss)
    timeout: 60s             # duration 5s..24h (used only if persistent: false)
    sound: true              # bool, overrides appearance.sound_enabled
    actions:                 # 0..4; default: one "Got it" dismiss
      - id: open             # required, unique in event
        label: "Open"        # required, ≤40 bytes
        action: open_url     # dismiss | snooze | open_url
        url: "https://..."   # open_url only, required, http(s)
        acknowledge: true    # open_url only, bool
      - id: later
        label: "Later"
        action: snooze
        minutes: 5           # snooze only, 1..1440
```
