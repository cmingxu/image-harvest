// Package phash computes 64-bit difference hashes (dHash) for perceptual
// image similarity. dHash is invariant to size, and near-invariant to
// compression and small color changes — enough to find visually similar
// images without any ML dependencies.
package phash

import (
	"image"
	"math/bits"

	"golang.org/x/image/draw"
)

// DHash returns the 64-bit dHash of m: the image is scaled to 9x8 grayscale
// and each pixel records whether it is lighter than its right neighbor.
func DHash(m image.Image) uint64 {
	dst := image.NewGray(image.Rect(0, 0, 9, 8))
	draw.ApproxBiLinear.Scale(dst, dst.Bounds(), m, m.Bounds(), draw.Src, nil)
	var h uint64
	bit := 0
	for i := range dst.Pix {
		if i%9 == 8 {
			continue // last column has no right neighbor
		}
		if dst.Pix[i] > dst.Pix[i+1] {
			h |= 1 << bit
		}
		bit++
	}
	return h
}

// Hamming returns the number of differing bits between two hashes; 0 means
// identical, and small values (<=10) usually mean visually similar images.
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}
