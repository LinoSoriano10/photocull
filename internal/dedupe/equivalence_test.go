package dedupe

import (
	"fmt"
	"sort"
	"strings"
	"testing"

	"photocull/internal/hashing"
	"photocull/internal/scanner"
)

// groupSimilarBruteForce is the implementation GroupSimilar had before the
// banded index, kept as the reference the fast one has to match.
//
// It is not dead code and it is not nostalgia. The optimisation's whole claim
// is that it changes the cost and nothing else, and the only way to state that
// as a test rather than as a comment is to keep the definition around and
// compare against it.
func groupSimilarBruteForce(files []scanner.FileMeta, threshold int, keep KeepStrategy) []Group {
	uf := newUnionFind(len(files))
	unionByContentHash(files, uf)

	candidates := make([]int, 0, len(files))
	for i, f := range files {
		if f.HasFingerprint {
			candidates = append(candidates, i)
		}
	}
	for a := range candidates {
		for b := a + 1; b < len(candidates); b++ {
			i, j := candidates[a], candidates[b]
			if hashing.Distance(files[i].Fingerprint, files[j].Fingerprint) <= threshold {
				uf.union(i, j)
			}
		}
	}
	return buildGroups(files, uf, keep)
}

// describe renders a set of groups as comparable text. Groups are sorted by
// their contents rather than compared in order, because the order they come
// back in is the sort's business and only the partition is being tested here.
func describe(groups []Group) string {
	lines := make([]string, 0, len(groups))
	for _, g := range groups {
		files := paths(g)
		sort.Strings(files)
		lines = append(lines, fmt.Sprintf("%s keep=%s [%s]",
			g.Type, g.Keep().Path, strings.Join(files, " ")))
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// TestGroupSimilarUnchangedByIndexing is the test that lets the index ship.
//
// hashing's own equivalence test proves the index returns the same pairs; this
// one proves the pairs still become the same groups, which is what a user sees.
// The distinction matters because similarity is not transitive: a single pair
// that goes missing does not remove one group, it splits a cluster in two, and
// a cluster that used to be one decision becomes two half-answers.
func TestGroupSimilarUnchangedByIndexing(t *testing.T) {
	// Both shipping thresholds, and sizes either side of parallelFrom so the
	// serial path and the worker path are both compared.
	for _, threshold := range []int{hashing.DefaultTextThreshold, DefaultThreshold} {
		for _, n := range []int{2, 60, 500, parallelFrom + 500} {
			for seed := int64(1); seed <= 2; seed++ {
				name := fmt.Sprintf("threshold=%d/n=%d/seed=%d", threshold, n, seed)
				t.Run(name, func(t *testing.T) {
					files := syntheticFiles(seed, n)

					want := describe(groupSimilarBruteForce(files, threshold, DefaultKeepStrategy))
					got := describe(GroupSimilar(files, threshold, DefaultKeepStrategy))

					if got != want {
						t.Errorf("indexed grouping differs from brute force\n--- brute force ---\n%s\n--- indexed ---\n%s", want, got)
					}
				})
			}
		}
	}
}

// TestGroupSimilarIsDeterministic runs the parallel path repeatedly. Workers
// find their pairs in whatever order they finish, so if the merge were
// order-sensitive this is where it would show — as a flake in production and
// nowhere else.
func TestGroupSimilarIsDeterministic(t *testing.T) {
	files := syntheticFiles(3, parallelFrom+1000)

	first := describe(GroupSimilar(files, DefaultThreshold, DefaultKeepStrategy))
	for range 5 {
		if got := describe(GroupSimilar(files, DefaultThreshold, DefaultKeepStrategy)); got != first {
			t.Fatal("two runs over the same files produced different groups")
		}
	}
}

// syntheticFiles builds a population with the mix a real scan has: mostly
// unrelated files, a tenth of them near-duplicates of an earlier one, some with
// no fingerprint at all, and a few byte-identical pairs so the content-hash
// pass has something to do too.
func syntheticFiles(seed int64, n int) []scanner.FileMeta {
	rng := newDeterministicRand(seed)
	files := make([]scanner.FileMeta, n)

	for i := range files {
		f := scanner.FileMeta{
			Path:   fmt.Sprintf("/library/%06d.jpg", i),
			Size:   int64(1000 + rng.intn(9000)),
			SHA256: fmt.Sprintf("%064x", rng.uint64()),
		}

		switch {
		case i > 0 && i%37 == 0:
			// A file nothing could be read out of: no fingerprint, so it can
			// only ever be grouped by its content hash.
		case i > 0 && i%23 == 0:
			// A byte-identical copy of an earlier file.
			src := files[rng.intn(i)]
			f.SHA256 = src.SHA256
			f.Size = src.Size
			f.Fingerprint = src.Fingerprint
			f.HasFingerprint = src.HasFingerprint
		case i > 0 && i%10 == 0:
			// A near-duplicate: a handful of bits away.
			fp := files[rng.intn(i)].Fingerprint
			for range rng.intn(10) {
				fp ^= 1 << rng.intn(64)
			}
			f.Fingerprint = fp
			f.HasFingerprint = true
		default:
			f.Fingerprint = rng.uint64()
			f.HasFingerprint = true
		}

		files[i] = f
	}
	return files
}

// newDeterministicRand is a tiny xorshift generator, so these fixtures stay
// identical whatever the standard library's generator does between versions.
// A test that compares two implementations is worthless if the input drifts.
type deterministicRand struct{ state uint64 }

func newDeterministicRand(seed int64) *deterministicRand {
	return &deterministicRand{state: uint64(seed)*2862933555777941757 + 3037000493}
}

func (r *deterministicRand) uint64() uint64 {
	r.state ^= r.state << 13
	r.state ^= r.state >> 7
	r.state ^= r.state << 17
	return r.state
}

func (r *deterministicRand) intn(n int) int {
	if n <= 0 {
		return 0
	}
	return int(r.uint64() % uint64(n))
}
