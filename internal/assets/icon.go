// Package assets renders Bird's icon for the tray and notifications.
//
// The icon is drawn procedurally (anti-aliased by supersampling) so the repo
// has no binary assets and any size can be produced. It is a simplified
// version of the bird in frontend/dist/index.html.
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

// grown returns the triangle scaled about its centroid, for outlines.
func (t triangle) grown(f float64) triangle {
	cx, cy := (t.ax+t.bx+t.cx)/3, (t.ay+t.by+t.cy)/3
	g := func(x, y float64) (float64, float64) { return cx + (x-cx)*f, cy + (y-cy)*f }
	ax, ay := g(t.ax, t.ay)
	bx, by := g(t.bx, t.by)
	px, py := g(t.cx, t.cy)
	return triangle{ax, ay, bx, by, px, py}
}

var (
	blue     = color.NRGBA{0x3B, 0x82, 0xE0, 0xFF}
	deepBlue = color.NRGBA{0x25, 0x61, 0xC4, 0xFF}
	light    = color.NRGBA{0x8C, 0xC4, 0xF7, 0xFF}
	line     = color.NRGBA{0x13, 0x29, 0x5C, 0xFF}
	orange   = color.NRGBA{0xFF, 0xA4, 0x1B, 0xFF}
	ink      = color.NRGBA{0x10, 0x17, 0x2A, 0xFF}
	white    = color.NRGBA{0xFF, 0xFF, 0xFF, 0xFF}
)

const outline = 0.018

// outlined draws e with a dark border.
func outlined(e ellipse, c color.NRGBA) []layer {
	return []layer{
		{ellipse{e.cx, e.cy, e.rx + outline, e.ry + outline}, line},
		{e, c},
	}
}

// outlinedTri draws t with a dark border.
func outlinedTri(t triangle, c color.NRGBA) []layer {
	return []layer{{t.grown(1.12), line}, {t, c}}
}

// Painter's order, in a unit square, bird facing right with wings spread.
var bird = concat(
	outlinedTri(triangle{0.50, 0.52, 0.03, 0.27, 0.32, 0.61}, deepBlue), // back wing
	outlinedTri(triangle{0.40, 0.67, 0.06, 0.74, 0.22, 0.84}, blue),     // tail
	outlinedTri(triangle{0.53, 0.25, 0.62, 0.04, 0.65, 0.22}, blue),     // crest
	outlinedTri(triangle{0.53, 0.25, 0.40, 0.13, 0.59, 0.20}, blue),     // crest
	outlined(ellipse{0.52, 0.66, 0.24, 0.20}, blue),                     // body
	[]layer{{ellipse{0.58, 0.71, 0.15, 0.12}, light}},                   // belly
	outlined(ellipse{0.61, 0.40, 0.21, 0.21}, blue),                     // head
	outlined(ellipse{0.56, 0.36, 0.07, 0.085}, white),                   // eyes
	outlined(ellipse{0.705, 0.36, 0.06, 0.075}, white),
	[]layer{
		{ellipse{0.58, 0.385, 0.035, 0.035}, ink}, // pupils
		{ellipse{0.725, 0.385, 0.03, 0.03}, ink},
	},
	outlinedTri(triangle{0.61, 0.46, 0.95, 0.49, 0.62, 0.54}, orange), // beak, from between the eyes
	outlinedTri(triangle{0.59, 0.68, 0.99, 0.50, 0.62, 0.77}, blue),   // front wing
)

func concat(groups ...[]layer) []layer {
	var all []layer
	for _, g := range groups {
		all = append(all, g...)
	}
	return all
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
