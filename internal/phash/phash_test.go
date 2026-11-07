package phash

import (
	"image"
	"image/color"
	"testing"
)

func solid(c color.Color) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = 255
	}
	return img
}

func TestDHashSizeInvariant(t *testing.T) {
	// same content (solid color), different sizes -> same hash
	small := solid(color.RGBA{R: 255, G: 0, B: 0, A: 255})
	big := image.NewRGBA(image.Rect(0, 0, 300, 100))
	for i := range big.Pix {
		big.Pix[i] = 200
	}
	if DHash(small) != DHash(big) {
		t.Error("solid-color images of different sizes should hash identically")
	}
}

func TestDHashDistinguishesContent(t *testing.T) {
	// left half red / right half blue vs all red -> different structure
	mixed := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{R: 255, A: 255}
			if x >= 32 {
				c = color.RGBA{B: 255, A: 255}
			}
			mixed.Set(x, y, c)
		}
	}
	allRed := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 0; y < 64; y++ {
		for x := 0; x < 64; x++ {
			allRed.Set(x, y, color.RGBA{R: 255, A: 255})
		}
	}
	if d := Hamming(DHash(mixed), DHash(allRed)); d == 0 {
		t.Error("different layouts must not hash identically")
	}
}

func TestHamming(t *testing.T) {
	if Hamming(0, 0) != 0 {
		t.Error("identical hashes must have distance 0")
	}
	if Hamming(0x00, 0xFF) != 8 {
		t.Error("expected 8 differing bits")
	}
}
