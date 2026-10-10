# FlyBy — Linux Desktop Reminder Daemon

## Instructions for the coding agent

Build **FlyBy**, a lightweight Linux desktop reminder application. It runs in the background, reads events from a YAML configuration file, and displays a playful animated character when an event is due. The character flies from the left edge of the screen toward the center, stops, and remains visible until the user chooses an action.

Implement a working application, not just a design or a plan. First inspect the target repository and existing environment, then implement the project in small, testable steps. Do not overwrite unrelated files. Document any platform limitations clearly.

## Product goals

- Remind me about scheduled events that I tend to forget while focused on work.
- Make reminders noticeable and fun without relying only on standard desktop notifications.
- Keep an important reminder on screen until I explicitly act on it.
- Let me configure events without changing code.
- Keep the scheduler lightweight and reliable in the background.

## Preferred technology

- **Language:** Go.
- **Configuration:** YAML.
- **Desktop UI:** Prefer Wails with HTML/CSS/JavaScript for the animated UI, if compatible with the target Linux environment.
- **Background startup:** `systemd --user` service.
- **Persistence:** Start with YAML configuration and runtime state stored separately. Use SQLite only if it becomes necessary.
- **Tests:** Go unit tests for configuration, scheduling, and event state transitions.

Before committing to a UI framework, inspect the machine's available display session and dependencies. Support X11 where practical and identify Wayland limitations honestly. Do not claim reliable arbitrary window positioning or always-on-top behavior on Wayland unless it has been tested with the chosen toolkit/compositor. Provide a useful fallback if the overlay cannot be positioned as requested.

## Core user experience

1. FlyBy starts in the background when I log in.
2. It reads and validates `config.yaml`.
3. It schedules configured events in the selected time zone.
4. When an event becomes due, it opens a transparent, borderless desktop window if the platform supports it.
5. A cute bird (the default character) flies from the left side of the screen toward the center.
6. The bird stops at the center and displays the event title, message, and action buttons.
7. The reminder stays visible until the user selects an action. Do not auto-dismiss a persistent reminder.
8. Selecting an action triggers the configured behavior, then the character flies away and the window closes or returns to its idle state.
9. Snoozing schedules the same event again after the configured delay.
10. Restarting FlyBy must not accidentally trigger duplicate reminders for the same event occurrence.

### Example: daily stand-up

At 11:00 AM on weekdays, show the bird with:

- Title: `Stand-up meeting`
- Message: `Stop coding. Time to meet your team!`
- Button: `Join meeting` — open the configured meeting URL.
- Button: `Remind me in 5 minutes` — dismiss temporarily and show the event again after five minutes.

The reminder should use the machine's local time by default.

## YAML configuration

Create a documented `config.yaml` with a schema similar to this:

```yaml
app:
  name: FlyBy
  timezone: Local
  start_minimized: true

appearance:
  character: bird
  always_on_top: true
  animation_speed: normal
  sound_enabled: true

events:
  - id: daily-standup
    enabled: true
    title: "Stand-up meeting"
    message: "Stop coding. Time to meet your team!"
    schedule:
      type: weekly
      time: "11:00"
      days: [mon, tue, wed, thu, fri]
    animation:
      type: fly-across
      stop_at: center
    persistent: true
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
    enabled: true
    title: "Lunch break"
    message: "Take a break and get some food!"
    schedule:
      type: daily
      time: "13:00"
    animation:
      type: fly-across
      stop_at: center
    persistent: true
    actions:
      - id: dismiss
        label: "Got it"
        action: dismiss
```

This is an example contract, not a reason to hard-code these two events. Implement the schema so users can add, edit, disable, and remove events by editing YAML.

### Configuration requirements

- Validate required fields and report helpful errors with the field/event ID.
- Support `timezone: Local` and valid IANA time-zone names.
- Support daily and selected-weekday schedules.
- Use 24-hour `HH:MM` time values.
- Support optional event enable/disable, sound, animation, persistence, URL, and snooze settings.
- Define sensible defaults and document them.
- Reload configuration safely when the file changes, if practical. If hot reload is not implemented, clearly document that restart/reload is required.
- Never execute arbitrary shell commands from YAML.
- Validate URLs and only allow `http` and `https` for `open_url` actions.
- Keep secrets and private meeting URLs out of logs.

## Action types

Implement a small allowlist of actions:

- `dismiss`: acknowledge the event and close the reminder.
- `snooze`: schedule the event again after `minutes`.
- `open_url`: open a validated HTTP(S) URL using the system's default browser, then acknowledge the reminder if configured to do so.

Validate action definitions at startup. Invalid actions should produce clear errors and must not crash the daemon.

## Animation and interface

- Create a friendly, original bird asset using SVG/CSS or another locally bundled asset. Avoid runtime dependencies on remote images.
- Animate movement from off-screen left to the horizontal center.
- Respect reduced-motion/accessibility preferences where feasible; provide a non-animated fallback.
- Use a transparent background and borderless window where supported.
- Keep buttons large enough to click and labels readable.
- Ensure the reminder is keyboard accessible: focus an appropriate button, support Tab navigation, and provide sensible keyboard actions.
- Do not intercept keyboard input outside the app.
- Do not disable normal desktop input or create a system-wide input block. The reminder is persistent visually, but the user must retain control of the desktop.
- Make the UI usable on different screen sizes and scaling settings.
- Avoid covering the entire screen unnecessarily. Use a compact center overlay with the character and event panel.
- Provide a tray icon/menu if feasible: Show status, open configuration, reload configuration, and quit.
- If true transparency, topmost behavior, or screen positioning is unsupported in the current session, use a fallback and document it.

## Architecture

Keep scheduling logic separate from UI rendering. A suggested structure:

```text
flyby/
├── cmd/
│   └── flyby/
│       └── main.go
├── internal/
│   ├── config/       # YAML parsing, validation, defaults
│   ├── scheduler/    # Due-event calculation and schedule loop
│   ├── events/       # Event state, deduplication, snooze
│   ├── desktop/      # UI integration / desktop behavior
│   └── platform/     # OS/session-specific integration if needed
├── frontend/         # Wails UI, CSS animations, local assets
├── assets/           # Locally bundled icons/sounds if needed
├── config.example.yaml
├── go.mod
├── README.md
└── ...
```

Adapt the structure to the selected framework where appropriate; do not create empty abstraction layers solely to match this tree.

### Reliability requirements

- Scheduler logic must be testable without launching the UI.
- Do not busy-loop. Use timers or a sensible scheduler strategy.
- Calculate the next occurrence in the configured time zone and handle daylight-saving changes correctly.
- Handle missed events on startup using a documented policy. Default to not showing every old missed event after a long shutdown.
- Prevent duplicate triggers for one event occurrence, including after config reloads and UI reconnects.
- Ensure snooze state is independent from the regular recurrence schedule.
- Keep runtime state under an appropriate user config/state directory, not in a system-wide root-only path.
- Handle malformed YAML and unavailable UI/display sessions gracefully.
- Log actionable errors without flooding logs.
- Shut down cleanly when the user quits.
- Do not require root privileges for normal operation.

## Linux startup and packaging

- Provide a sample `systemd --user` unit and installation/uninstallation instructions.
- Do not install or enable the service automatically without user consent.
- Explain how to start, stop, restart, and view logs using `systemctl --user`.
- Include a manual run mode for development.
- Document runtime/build dependencies and how to build from source.
- Do not assume one desktop environment or display server. Detect/report relevant session information when useful.
- Do not run the UI before the user's graphical session is ready.

## Suggested command-line interface

Implement at least:

```text
flyby
flyby --config /path/to/config.yaml
flyby --check-config
flyby --version
```

`--check-config` should validate the configuration and exit with a useful success/error status without opening the UI.

## Testing requirements

Add tests for:

- Valid and invalid YAML configurations.
- Missing fields, duplicate event IDs, invalid times, weekdays, time zones, and actions.
- Daily and weekday-only schedule calculations.
- Snooze timing and acknowledgement.
- Duplicate prevention for an event occurrence.
- Startup/missed-event policy.
- Daylight-saving/time-zone edge cases where practical.

Tests must not depend on the actual wall clock; inject a clock or isolate time calculations so they can be tested deterministically.

## Deliverables

1. Working Go application and desktop UI.
2. Example YAML configuration and clear schema documentation.
3. Unit tests for configuration and scheduling.
4. `README.md` with build/run instructions, feature overview, configuration examples, troubleshooting, and Linux limitations.
5. Sample `systemd --user` service plus opt-in install/uninstall instructions.
6. A concise list of what is implemented, what was tested, and any known limitations.

## Implementation workflow

1. Inspect the repository, operating system, desktop session (X11/Wayland), and installed Go/toolchain versions.
2. State a brief implementation plan based on the actual environment.
3. Implement a minimal vertical slice first: load YAML, trigger a test event, animate the bird, and acknowledge/snooze it.
4. Add recurrence, deduplication, configuration validation, CLI flags, tests, and startup documentation.
5. Run formatting, tests, and available build checks. Fix failures before reporting completion.
6. Summarize the exact commands to build and run the app, and be explicit about any untested desktop behavior.

Prefer simple, maintainable code over premature abstraction. Do not stop after producing a plan; proceed with implementation unless blocked by a concrete missing dependency or a decision that requires user input.
