// Package frontend embeds the overlay UI (plain HTML/CSS/JS, no build step).
// Wails injects its runtime (/wails/runtime.js, /wails/ipc.js) into index.html.
package frontend

import "embed"

//go:embed all:dist
var Assets embed.FS
