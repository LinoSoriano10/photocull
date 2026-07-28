package hashing

import (
	"fmt"
	"image"
	"math/bits"

	"github.com/corona10/goimagehash"
)

// PerceptualBits is the width of a perceptual hash, and therefore the largest
// distance two hashes can be apart.
const PerceptualBits = 64

// Perceptual reduces an image to a 64-bit fingerprint of what it looks like.
//
// Unlike a content hash, this survives resizing, re-compression and format
// conversion: the same photo exported from a phone as HEIC and from a backup
// as JPEG lands on the same, or a very nearby, hash.
func Perceptual(img image.Image) (uint64, error) {
	h, err := goimagehash.PerceptionHash(img)
	if err != nil {
		return 0, fmt.Errorf("hashing: perceptual hash failed: %w", err)
	}
	return h.GetHash(), nil
}

// Distance counts the bits that differ between two perceptual hashes.
//
// Zero means the hashes are identical; the larger the number, the less the two
// images have in common. Anything up to roughly 10 is worth a human look.
func Distance(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}
