package hashing

import (
	"fmt"
	"math/rand"
	"testing"
)

// population builds fingerprints shaped like a real scan rather than a
// convenient one.
//
// The mix is the point. Entirely random 64-bit values are almost never within 8
// bits of each other, so an index over them would return nothing and still pass
// a careless test. Every tenth entry is therefore a few bits away from an
// earlier one — what a resized photo or a re-saved document looks like — so the
// comparison against brute force has real neighbours to disagree about.
func population(seed int64, n int) []uint64 {
	rng := rand.New(rand.NewSource(seed))
	fps := make([]uint64, n)
	for i := range fps {
		if i > 0 && i%10 == 0 {
			fp := fps[rng.Intn(i)]
			for range rng.Intn(12) {
				fp ^= 1 << rng.Intn(64)
			}
			fps[i] = fp
			continue
		}
		fps[i] = rng.Uint64()
	}
	return fps
}

// bruteForce is the definition the index has to live up to: every pair, checked
// directly. Slow on purpose — it is the answer, not the implementation.
func bruteForce(fps []uint64, threshold int) map[[2]int]bool {
	pairs := make(map[[2]int]bool)
	for i := range fps {
		for j := i + 1; j < len(fps); j++ {
			if Distance(fps[i], fps[j]) <= threshold {
				pairs[[2]int{i, j}] = true
			}
		}
	}
	return pairs
}

// viaIndex asks the index the same question.
func viaIndex(fps []uint64, threshold int) map[[2]int]bool {
	ix := NewIndex(threshold, len(fps))
	for i, fp := range fps {
		ix.Add(i, fp)
	}

	pairs := make(map[[2]int]bool)
	search := ix.Search()
	for i, fp := range fps {
		search.Near(fp, func(j int) {
			if i == j {
				return
			}
			lo, hi := i, j
			if lo > hi {
				lo, hi = hi, lo
			}
			pairs[[2]int{lo, hi}] = true
		})
	}
	return pairs
}

// TestIndexFindsExactlyWhatBruteForceFinds is the test that makes the whole
// optimisation safe to make.
//
// The index is a filter: it narrows the field with the pigeonhole principle and
// then checks the real distance. So the only thing that can go wrong is that it
// narrows too far and drops a pair that is genuinely within the threshold — a
// duplicate silently stops being reported, which is the worst failure this tool
// has, because nobody notices a group that never appeared. Set equality against
// the double loop is the strongest statement available, and it is cheap here.
func TestIndexFindsExactlyWhatBruteForceFinds(t *testing.T) {
	// Both shipping thresholds, plus the edges: 0 is exact-match, and 15 is the
	// last value that still bands at all.
	for _, threshold := range []int{0, 1, DefaultTextThreshold, DefaultPhotoThreshold, 12, maxIndexThreshold} {
		for _, n := range []int{1, 2, 50, 400} {
			for seed := int64(1); seed <= 3; seed++ {
				name := fmt.Sprintf("threshold=%d/n=%d/seed=%d", threshold, n, seed)
				t.Run(name, func(t *testing.T) {
					fps := population(seed, n)
					want := bruteForce(fps, threshold)
					got := viaIndex(fps, threshold)

					for pair := range want {
						if !got[pair] {
							t.Errorf("index missed %v: distance %d <= %d",
								pair, Distance(fps[pair[0]], fps[pair[1]]), threshold)
						}
					}
					for pair := range got {
						if !want[pair] {
							t.Errorf("index invented %v: distance %d > %d",
								pair, Distance(fps[pair[0]], fps[pair[1]]), threshold)
						}
					}
				})
			}
		}
	}
}

// TestIndexAboveMaxThresholdStillAnswersCorrectly covers the fallback. Past
// maxIndexThreshold the bands are too narrow to filter anything, so the index
// stops banding — but it must keep giving the same answers, just slowly.
func TestIndexAboveMaxThresholdStillAnswersCorrectly(t *testing.T) {
	const threshold = maxIndexThreshold + 1

	fps := population(7, 200)
	ix := NewIndex(threshold, len(fps))
	if ix.bands != nil {
		t.Fatalf("threshold %d should not band", threshold)
	}
	for i, fp := range fps {
		ix.Add(i, fp)
	}

	want := bruteForce(fps, threshold)
	got := viaIndex(fps, threshold)
	if len(want) != len(got) {
		t.Fatalf("unbanded index found %d pairs, brute force found %d", len(got), len(want))
	}
	for pair := range want {
		if !got[pair] {
			t.Errorf("unbanded index missed %v", pair)
		}
	}
}

// TestIndexOffersEachNeighbourOnce guards the deduplication inside Near. A pair
// within the threshold usually shares several bands, and a caller that unions
// or counts would see the same neighbour repeatedly without it.
func TestIndexOffersEachNeighbourOnce(t *testing.T) {
	// Identical fingerprints share every band, which is the worst case.
	const threshold = 8
	ix := NewIndex(threshold, 3)
	for i := range 3 {
		ix.Add(i, 0xABCDEF0123456789)
	}

	counts := make(map[int]int)
	ix.Search().Near(0xABCDEF0123456789, func(id int) { counts[id]++ })

	if len(counts) != 3 {
		t.Fatalf("visited %d ids, want 3", len(counts))
	}
	for id, n := range counts {
		if n != 1 {
			t.Errorf("id %d visited %d times, want 1", id, n)
		}
	}
}

// TestSearcherIsReusable checks that the generation counter really does reset
// the scratch space between searches. If it did not, the second search would
// skip everything the first one saw.
func TestSearcherIsReusable(t *testing.T) {
	fps := population(11, 100)
	ix := NewIndex(DefaultPhotoThreshold, len(fps))
	for i, fp := range fps {
		ix.Add(i, fp)
	}

	search := ix.Search()
	first := 0
	search.Near(fps[0], func(int) { first++ })

	second := 0
	search.Near(fps[0], func(int) { second++ })

	if first == 0 {
		t.Fatal("the first search found nothing, so this proves nothing")
	}
	if first != second {
		t.Errorf("same query twice gave %d then %d neighbours", first, second)
	}
}

// TestSearcherSeesEntriesAddedAfterIt covers the merge pattern: ask whether a
// fingerprint is already known, add it if it was not, repeat. The Searcher
// outlives the Adds there, and its scratch space has to grow with the index or
// the search walks off the end of it.
func TestSearcherSeesEntriesAddedAfterIt(t *testing.T) {
	ix := NewIndex(DefaultPhotoThreshold, 0)
	search := ix.Search()

	const fp = uint64(0x0F0F0F0F0F0F0F0F)
	search.Near(fp, func(id int) { t.Errorf("empty index offered id %d", id) })

	ix.Add(0, fp)

	found := 0
	search.Near(fp^1, func(int) { found++ })
	if found != 1 {
		t.Errorf("found %d neighbours after adding one, want 1", found)
	}
}

// TestIndexBandsCoverEveryBit is the arithmetic the pigeonhole argument rests
// on. If the bands left a gap, two fingerprints could differ only in the
// uncovered bits and share every band, and the guarantee would be false.
func TestIndexBandsCoverEveryBit(t *testing.T) {
	for threshold := range maxIndexThreshold + 1 {
		ix := NewIndex(threshold, 0)
		var covered uint64
		for i := range ix.bands {
			band := ix.masks[i] << ix.shifts[i]
			if covered&band != 0 {
				t.Errorf("threshold %d: band %d overlaps an earlier one", threshold, i)
			}
			covered |= band
		}
		if covered != ^uint64(0) {
			t.Errorf("threshold %d: bands cover %#016x, want every bit", threshold, covered)
		}
		if len(ix.bands) != threshold+1 {
			t.Errorf("threshold %d: %d bands, want %d", threshold, len(ix.bands), threshold+1)
		}
	}
}

// TestIndexSearchOnAnEmptyIndexIsHarmless: Merge builds an index over the
// library, and an empty library is an ordinary first run.
func TestIndexSearchOnAnEmptyIndexIsHarmless(t *testing.T) {
	ix := NewIndex(DefaultPhotoThreshold, 0)
	if ix.Len() != 0 {
		t.Fatalf("Len() = %d on a fresh index", ix.Len())
	}
	ix.Search().Near(1234, func(id int) {
		t.Errorf("empty index offered id %d", id)
	})
}
