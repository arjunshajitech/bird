// Bird overlay UI.
//
// The Go side (cmd/bird-overlay) owns the window and all decisions; this page
// only renders the reminder and reports which button was chosen. It never sees
// URLs: actions are opened by the daemon.
//
// Opened directly in a browser (no Wails runtime), it runs a mock backend so
// the design can be iterated on without building the overlay.
"use strict";

const SPEED_MS = { slow: 2600, normal: 1600, fast: 900 };
const ARM_DELAY_MS = 400;   // buttons become clickable shortly after landing
// The overlay may receive keyboard focus while the user is typing elsewhere.
// Keys only count as a decision once the reminder has been visible for a
// moment, and never as part of a typing burst, so a stray Enter or digit
// meant for an editor cannot pick an action.
const KEY_GRACE_MS = 1200;
const TYPING_BURST_MS = 700;

const $ = (id) => document.getElementById(id);
const stage = $("stage");
let reminder = null;
let armed = false;
let armedAt = Infinity;
let lastKeyAt = -Infinity;
let swallowed = false;
let busy = false;

function backend() {
  return window.go && window.go.main && window.go.main.App;
}

function waitForBackend(timeoutMs) {
  return new Promise((resolve) => {
    const start = Date.now();
    (function poll() {
      if (backend()) return resolve(backend());
      if (Date.now() - start > timeoutMs) return resolve(null);
      setTimeout(poll, 20);
    })();
  });
}

function on(event, fn) {
  if (window.runtime && window.runtime.EventsOn) window.runtime.EventsOn(event, fn);
}

function render(r) {
  document.title = r.title;
  $("title").textContent = r.title;
  $("message").textContent = r.message || "";
  $("badge").hidden = !r.snoozed;
  const box = $("actions");
  box.replaceChildren();
  r.actions.forEach((a, i) => {
    const b = document.createElement("button");
    b.type = "button";
    b.dataset.id = a.id;
    b.disabled = true;
    // The first action is the primary one (e.g. "Join meeting").
    if (i === 0) b.classList.add("primary");
    const label = document.createElement("span");
    label.textContent = a.label;
    const key = document.createElement("kbd");
    key.textContent = String(i + 1);
    key.setAttribute("aria-hidden", "true");
    b.append(label, key);
    b.setAttribute("aria-keyshortcuts", String(i + 1));
    b.addEventListener("click", () => choose(a.id));
    box.append(b);
  });
}

function buttons() {
  return Array.from(document.querySelectorAll(".actions button"));
}

function setEnabled(enabled) {
  buttons().forEach((b) => { b.disabled = !enabled; });
}

function land() {
  if (stage.dataset.state !== "arriving") return;
  stage.dataset.state = "perched";
  chirp();
  setTimeout(() => {
    armed = true;
    armedAt = performance.now();
    setEnabled(true);
    const first = buttons()[0];
    if (first) first.focus({ preventScroll: true });
  }, ARM_DELAY_MS);
}

async function choose(id) {
  if (!armed || busy) return;
  busy = true;
  setEnabled(false);
  const result = await backend().Choose(id);
  busy = false;
  if (result === "keep") setEnabled(true);
  // "close": the backend emits "leave".
}

async function cancel() {
  if (!armed || busy) return;
  busy = true;
  setEnabled(false);
  const result = await backend().Cancel();
  busy = false;
  if (result === "keep") setEnabled(true);
}

function leave() {
  if (stage.dataset.state === "leaving") return;
  armed = false;
  armedAt = Infinity;
  setEnabled(false);
  stage.dataset.state = "leaving";
  // Let the bubble fade and the bird take off, then the backend closes.
  setTimeout(() => backend().Leave(), stage.classList.contains("static") ? 0 : 250);
}

// Keyboard: Tab / Shift+Tab move between buttons (native), Enter / Space
// activate (native), 1..n pick an action, Escape closes for now.
// Only events delivered to this window are seen; nothing is grabbed globally.
document.addEventListener("keydown", (e) => {
  const now = performance.now();
  const burst = now - lastKeyAt < TYPING_BURST_MS;
  lastKeyAt = now;
  swallowed = false;
  if (e.key === "Tab") return; // moving focus is always fine
  if (!armed || burst || e.repeat || now - armedAt < KEY_GRACE_MS) {
    e.preventDefault(); // swallow: likely typing meant for another window
    swallowed = true;
    return;
  }
  if (e.key === "Escape") { e.preventDefault(); cancel(); return; }
  const n = Number.parseInt(e.key, 10);
  const list = buttons();
  if (n >= 1 && n <= list.length && !e.ctrlKey && !e.altKey && !e.metaKey) {
    e.preventDefault();
    choose(list[n - 1].dataset.id);
  }
});

// Space activates buttons on keyup; block it if its keydown was swallowed.
document.addEventListener("keyup", (e) => {
  if (swallowed && (e.key === " " || e.key === "Enter")) e.preventDefault();
});

// A short, synthesised two-note chirp (no audio files).
function chirp() {
  if (!reminder || !reminder.sound) return;
  try {
    const Ctx = window.AudioContext || window.webkitAudioContext;
    if (!Ctx) return;
    const ctx = new Ctx();
    const now = ctx.currentTime;
    [[0, 2600, 3400], [0.13, 2900, 3900]].forEach(([at, f0, f1]) => {
      const osc = ctx.createOscillator();
      const gain = ctx.createGain();
      osc.type = "sine";
      osc.frequency.setValueAtTime(f0, now + at);
      osc.frequency.exponentialRampToValueAtTime(f1, now + at + 0.08);
      gain.gain.setValueAtTime(0.0001, now + at);
      gain.gain.exponentialRampToValueAtTime(0.18, now + at + 0.015);
      gain.gain.exponentialRampToValueAtTime(0.0001, now + at + 0.1);
      osc.connect(gain).connect(ctx.destination);
      osc.start(now + at);
      osc.stop(now + at + 0.12);
    });
    setTimeout(() => ctx.close(), 600);
  } catch (_) {
    // Sound is best-effort.
  }
}

// Mock backend for designing in a plain browser.
function installMock() {
  const params = new URLSearchParams(location.search);
  window.go = { main: { App: {
    Reminder: async () => ({
      event_id: "demo", title: params.get("title") || "Stand-up meeting",
      message: params.get("message") || "Stop coding. Time to meet your team!",
      snoozed: params.has("snoozed"), persistent: true, sound: false,
      speed: params.get("speed") || "normal", animation: "fly-across",
      actions: [
        { id: "join", label: "Join meeting", kind: "open_url" },
        { id: "snooze", label: "Remind me in 5 minutes", kind: "snooze" },
      ],
    }),
    Reveal: async () => {},
    Ready: async (reduced) => ({ mode: reduced ? "static" : "inline", flight_ms: SPEED_MS[params.get("speed") || "normal"] }),
    Choose: async (id) => { console.log("chose", id); setTimeout(leave, 0); return "close"; },
    Cancel: async () => { console.log("cancel"); setTimeout(leave, 0); return "close"; },
    Leave: async () => console.log("left"),
  } } };
}

async function main() {
  let app = await waitForBackend(3000);
  if (!app) { installMock(); app = backend(); }

  reminder = await app.Reminder();
  render(reminder);
  const flight = SPEED_MS[reminder.speed] || SPEED_MS.normal;
  document.documentElement.style.setProperty("--flight", flight + "ms");

  const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches ||
    reminder.animation === "none";
  on("landed", land);
  on("leave", leave);
  // Window flights: the part of the path beyond the screen edge, which the
  // window itself cannot travel.
  on("shift", (dx) => { $("bird").style.transform = dx ? `translateX(${dx}px)` : ""; });

  const info = await app.Ready(reduced);
  // The window is mapped but invisible; reveal it once a frame is painted.
  await new Promise((r) => requestAnimationFrame(() => requestAnimationFrame(r)));
  stage.classList.add(info.mode === "window" ? "window-flight" : info.mode);
  await app.Reveal();
  if (info.mode !== "window") {
    // The backend emits "landed" for window flights; otherwise time it here.
    setTimeout(land, info.mode === "static" ? 0 : info.flight_ms);
  }
}

main();
