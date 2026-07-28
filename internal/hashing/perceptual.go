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

// DefaultPhotoThreshold is the Hamming distance below which two perceptual
// hashes are treated as the same photo.
//
// Out of 64 bits, 8 is the value that in practice catches resized and
// re-compressed copies while leaving genuinely different photos apart. It sits
// here beside DefaultTextThreshold so the two can be read against each other:
// each number belongs to the algorithm whose distance distribution justifies
// it, and photographs and text are distributed very differently.
const DefaultPhotoThreshold = 8

// Distance is deliberately shared by both fingerprint kinds. It is safe only
// because a single scan never mixes them — see scanner.FileMeta.Fingerprint.

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
