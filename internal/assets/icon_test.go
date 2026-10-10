package assets

import (
	"bytes"
	"image/png"
	"testing"
)

func TestPNGDecodes(t *testing.T) {
	img, err := png.Decode(bytes.NewReader(PNG(64)))
	if err != nil {
		t.Fatal(err)
	}
	if b := img.Bounds(); b.Dx() != 64 || b.Dy() != 64 {
		t.Errorf("size = %v", b)
	}
	// Corners transparent, centre opaque.
	if _, _, _, a := img.At(0, 0).RGBA(); a != 0 {
		t.Error("corner should be transparent")
	}
	if _, _, _, a := img.At(32, 36).RGBA(); a == 0 {
		t.Error("centre should be opaque")
	}
}
