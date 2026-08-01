package dedupe

import (
	"fmt"
	"math/rand"
	"testing"

	"photocull/internal/scanner"
)

// benchFiles builds a population that looks like a real scan rather than a
// convenient one.
//
// The shape matters more than the size. A tenth of the files are near-duplicates
// of an earlier file — a few bits apart, which is what a resized photo or a
// re-saved document looks like — and the rest are unrelated. A population of
// entirely random fingerprints would make any candidate filter look perfect,
// because nothing would ever collide; a population of near-identical ones would
// make it look useless. Real drives are mostly the first with pockets of the
// second.
func benchFiles(n int) []scanner.FileMeta {
	rng := rand.New(rand.NewSource(1))
	files := make([]scanner.FileMeta, n)

	for i := range files {
		var fp uint64
		if i > 0 && i%10 == 0 {
			// A near-duplicate of an earlier file: flip three bits.
			fp = files[rng.Intn(i)].Fingerprint
			for range 3 {
				fp ^= 1 << rng.Intn(64)
			}
		} else {
			fp = rng.Uint64()
		}

		files[i] = scanner.FileMeta{
			Path:           fmt.Sprintf("/photos/%06d.jpg", i),
			Size:           int64(100_000 + rng.Intn(4_000_000)),
			SHA256:         fmt.Sprintf("%064x", rng.Uint64()),
			Fingerprint:    fp,
			HasFingerprint: true,
			Width:          4032,
			Height:         3024,
			Decoded:        true,
		}
	}
	return files
}

// BenchmarkGroupSimilar is the number the optimisation has to beat.
//
// The sizes are chosen from what the two modes actually meet. A photo library
// is a few thousand files, which is where this tool started and where the
// original pairwise loop was the right call. A documents scan walks every
// extension on purpose, so pointing it at a whole drive puts the candidate
// count two orders of magnitude higher — and n(n-1)/2 does not survive that.
func BenchmarkGroupSimilar(b *testing.B) {
	for _, n := range []int{1_000, 10_000, 100_000} {
		files := benchFiles(n)
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				GroupSimilar(files, DefaultThreshold, DefaultKeepStrategy)
			}
		})
	}
}

// BenchmarkGroupExact is the control. It has always been a single pass over a
// map, so it should stay flat while the similarity numbers move — and if it
// moves too, the change touched something it should not have.
func BenchmarkGroupExact(b *testing.B) {
	for _, n := range []int{10_000, 100_000} {
		files := benchFiles(n)
		b.Run(fmt.Sprintf("files=%d", n), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				GroupExact(files, DefaultKeepStrategy)
			}
		})
	}
}
