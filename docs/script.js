// Hero demo: the bird flies in with one reminder after another. Clicking a
// button (or waiting) sends it away and brings the next one.
"use strict";

const REMINDERS = [
  { time: "11:00 · Mon–Fri", title: "Stand-up meeting", message: "Stop coding. Time to meet your team!",
    actions: ["Join meeting", "In 5 minutes"] },
  { time: "13:00 · Daily", title: "Lunch break", message: "Step away from the screen and eat something.",
    actions: ["Going now", "In 15 minutes"] },
  { time: "15:30 · Daily", title: "Drink some water", message: "Stretch your legs and refill your bottle.",
    actions: ["Done", "In 10 minutes"] },
  { time: "20:00 · Daily", title: "Evening medication", message: "Take your tablets with water.",
    actions: ["Taken", "In 10 minutes"] },
  { time: "16:30 · Fridays", title: "Weekly report", message: "Fill in this week's status before you log off.",
    actions: ["Open report", "Submitted"] },
];

const FLY_IN_MS = 1600;
const FLY_OUT_MS = 900;
const STAY_MS = 4200;

const stage = document.getElementById("stage");
const reduced = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
let index = 0;
let timer = 0;
let hovering = false;

function render(r) {
  document.getElementById("demo-time").textContent = r.time;
  document.getElementById("demo-title").textContent = r.title;
  document.getElementById("demo-message").textContent = r.message;
  const box = document.getElementById("demo-actions");
  box.replaceChildren();
  r.actions.forEach((label, i) => {
    const b = document.createElement("button");
    b.type = "button";
    if (i === 0) b.className = "primary";
    const span = document.createElement("span");
    span.textContent = label;
    const key = document.createElement("kbd");
    key.textContent = String(i + 1);
    key.setAttribute("aria-hidden", "true");
    b.append(span, key);
    b.addEventListener("click", next);
    box.append(b);
  });
}

function arrive() {
  render(REMINDERS[index]);
  stage.dataset.state = "arriving";
  clearTimeout(timer);
  timer = setTimeout(() => {
    stage.dataset.state = "perched";
    schedule();
  }, reduced ? 0 : FLY_IN_MS);
}

function schedule() {
  clearTimeout(timer);
  timer = setTimeout(() => (hovering ? schedule() : next()), STAY_MS);
}

function next() {
  if (stage.dataset.state !== "perched") return;
  stage.dataset.state = "leaving";
  index = (index + 1) % REMINDERS.length;
  clearTimeout(timer);
  timer = setTimeout(arrive, reduced ? 150 : FLY_OUT_MS + 300);
}

stage.addEventListener("mouseenter", () => { hovering = true; });
stage.addEventListener("mouseleave", () => { hovering = false; });
stage.addEventListener("focusin", () => { hovering = true; });
stage.addEventListener("focusout", () => { hovering = false; });

// Pause the loop while the hero is off screen.
if ("IntersectionObserver" in window) {
  new IntersectionObserver(([entry]) => { hovering = !entry.isIntersecting; }).observe(stage);
}

arrive();

// Copy buttons.
document.querySelectorAll(".copy").forEach((btn) => {
  btn.addEventListener("click", async () => {
    const text = document.getElementById(btn.dataset.copy).innerText
      .split("\n").map((l) => l.replace(/\s+#.*$/, "")).join("\n");
    try {
      await navigator.clipboard.writeText(text);
      btn.textContent = "Copied";
    } catch (_) {
      btn.textContent = "Select and copy";
    }
    setTimeout(() => { btn.textContent = "Copy"; }, 1600);
  });
});
