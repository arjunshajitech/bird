// Package assets renders Bird's icon for the tray and notifications.
//
// The icon is drawn procedurally (anti-aliased by supersampling) so the repo
// has no binary assets and any size can be produced. It matches the bird in
// frontend/dist/bird.svg.
package assets

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"sync"
)

type shape interface{ contains(x, y float64) bool }

type ellipse struct{ cx, cy, rx, ry float64 }

func (e ellipse) contains(x, y float64) bool {
	dx, dy := (x-e.cx)/e.rx, (y-e.cy)/e.ry
	return dx*dx+dy*dy <= 1
}

type triangle struct{ ax, ay, bx, by, cx, cy float64 }

func (t triangle) contains(x, y float64) bool {
	s := func(px, py, qx, qy float64) float64 { return (x-qx)*(py-qy) - (px-qx)*(y-qy) }
	d1, d2, d3 := s(t.ax, t.ay, t.bx, t.by), s(t.bx, t.by, t.cx, t.cy), s(t.cx, t.cy, t.ax, t.ay)
	neg := d1 < 0 || d2 < 0 || d3 < 0
	pos := d1 > 0 || d2 > 0 || d3 > 0
	return !(neg && pos)
}

type layer struct {
	s shape
	c color.NRGBA
}

var (
	blue     = color.NRGBA{0x3B, 0xA3, 0xE8, 0xFF}
	deepBlue = color.NRGBA{0x25, 0x78, 0xC0, 0xFF}
	cream    = color.NRGBA{0xFF, 0xF3, 0xD6, 0xFF}
	orange   = color.NRGBA{0xFF, 0x9F, 0x1C, 0xFF}
	ink      = color.NRGBA{0x1E, 0x2A, 0x38, 0xFF}
	white    = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
	blush    = color.NRGBA{0xFF, 0x8F, 0xA3, 0xC0}
)

// Painter's order, in a unit square, bird facing right.
var bird = []layer{
	{triangle{0.06, 0.50, 0.30, 0.56, 0.10, 0.74}, deepBlue}, // tail
	{ellipse{0.53, 0.22, 0.05, 0.08}, blue},                  // head tuft
	{ellipse{0.50, 0.57, 0.33, 0.31}, blue},                  // body
	{ellipse{0.57, 0.68, 0.21, 0.16}, cream},                 // belly
	{ellipse{0.38, 0.59, 0.16, 0.10}, deepBlue},              // wing
	{ellipse{0.71, 0.59, 0.04, 0.03}, blush},                 // cheek
	{ellipse{0.63, 0.46, 0.075, 0.075}, white},               // eye
	{ellipse{0.65, 0.465, 0.045, 0.045}, ink},                // pupil
	{ellipse{0.665, 0.445, 0.016, 0.016}, white},             // sparkle
	{triangle{0.80, 0.49, 0.95, 0.545, 0.80, 0.60}, orange},  // beak
}

const supersample = 4

// Render draws the bird into a size×size transparent image.
func Render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	n := float64(supersample * supersample)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < supersample; sy++ {
				for sx := 0; sx < supersample; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/supersample) / float64(size)
					y := (float64(py) + (float64(sy)+0.5)/supersample) / float64(size)
					// Composite layers back to front ("over"), premultiplied.
					var cr, cg, cb, ca float64
					for _, l := range bird {
						if !l.s.contains(x, y) {
							continue
						}
						la := float64(l.c.A) / 255
						cr = float64(l.c.R)/255*la + cr*(1-la)
						cg = float64(l.c.G)/255*la + cg*(1-la)
						cb = float64(l.c.B)/255*la + cb*(1-la)
						ca = la + ca*(1-la)
					}
					r, g, b, a = r+cr, g+cg, b+cb, a+ca
				}
			}
			if a == 0 {
				continue
			}
			img.SetNRGBA(px, py, color.NRGBA{
				R: uint8(r / a * 255), G: uint8(g / a * 255), B: uint8(b / a * 255), A: uint8(a / n * 255),
			})
		}
	}
	return img
}

// PNG returns the icon encoded as PNG.
func PNG(size int) []byte {
	var buf bytes.Buffer
	_ = png.Encode(&buf, Render(size))
	return buf.Bytes()
}

var writeOnce sync.Once

// IconFile writes a 128px icon to dir (once per process) and returns its path,
// for consumers that need a file (notification servers).
func IconFile(dir string) string {
	path := filepath.Join(dir, "bird.png")
	writeOnce.Do(func() {
		if err := os.MkdirAll(dir, 0o700); err == nil {
			_ = os.WriteFile(path, PNG(128), 0o600)
		}
	})
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return path
}
