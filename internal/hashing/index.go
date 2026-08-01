package hashing

// Finding which fingerprints are close to which is the one part of photocull
// that does not scale with the drive. Comparing every pair costs n(n-1)/2, and
// each comparison is only an XOR and a popcount — which is why it was the right
// call while the input was a few thousand photos. Documents mode walks every
// extension on purpose, so pointing it at a whole drive moves the candidate
// count two orders of magnitude and the quadratic term stops being free:
// measured here, 1,000 files took a millisecond, 10,000 took 87, and 100,000
// took nearly ten seconds.
//
// Index cuts that down with the pigeonhole principle. Split a 64-bit
// fingerprint into B bands. Two fingerprints that differ in at most T bits have
// those bits spread across at most T bands, so if B is greater than T then at
// least one band must be *identical*. Bucket every fingerprint by each of its
// bands, and a search only has to look at the buckets it shares a band with.
//
// It is a filter, not a replacement: it produces a superset of the true
// neighbours and the real Hamming distance still decides. That is what makes
// the optimisation safe to make, and what the equivalence test pins down —
// swapping brute force for this must not change a single group.
//
// Honest about what it buys: this improves the constant, not the asymptotic
// class. With B = T+1 bands each band is 64/(T+1) bits wide, so a bucket holds
// roughly n / 2^(64/(T+1)) entries and the work drops by that factor. It is
// still quadratic in the end. Getting below that needs a different data
// structure and a different set of trade-offs, and the measured win here is
// large enough that it would be solving a problem photocull does not have.
//
// Measured by BenchmarkGroupSimilar on this machine, threshold 8:
//
//	files    every pair    indexed    and parallel
//	  1,000      1.1 ms     0.9 ms          0.9 ms
//	 10,000       87 ms      20 ms          8.0 ms
//	100,000      9.8 s      2.0 s           0.50 s
//
// A thousand files barely move, because building the index costs about what the
// comparisons it saves did. That is the honest shape of this optimisation: it
// buys nothing at the size photocull started at and everything at the size
// documents mode reaches.

// maxIndexThreshold is the largest distance worth indexing.
//
// Bands are 64/(T+1) bits wide, so a big threshold makes them too narrow to
// separate anything: at T=15 a band is 4 bits, which sorts everything into
// sixteen buckets and filters nothing. Past this the honest answer is to
// compare every pair, which is what an unindexed search does.
const maxIndexThreshold = 15

// Index finds the fingerprints within a Hamming distance of a query.
//
// Build it once with Add, then search it. It is read-only once built, and
// concurrent searches are safe as long as each goroutine has its own Searcher.
type Index struct {
	threshold int
	fps       []uint64

	// bands[i] maps the value of band i to the ids holding it. Nil when the
	// threshold is too large for banding to help, in which case every search
	// walks the whole population.
	bands  []map[uint64][]int32
	shifts []uint
	masks  []uint64
}

// NewIndex returns an empty index for the given distance. hint is the expected
// number of entries and only sizes the allocations.
func NewIndex(threshold, hint int) *Index {
	ix := &Index{threshold: threshold, fps: make([]uint64, 0, hint)}
	if threshold > maxIndexThreshold {
		return ix
	}

	// B = T+1 is the smallest number of bands that guarantees a match, and the
	// smallest is what we want: every extra band makes each one narrower, which
	// means bigger buckets and a weaker filter.
	b := threshold + 1
	ix.bands = make([]map[uint64][]int32, b)
	ix.shifts = make([]uint, b)
	ix.masks = make([]uint64, b)

	// Spread 64 bits over B bands as evenly as they will go, giving the
	// remainder to the first few rather than leaving one band tiny.
	width, extra := 64/b, 64%b
	shift := uint(0)
	for i := range b {
		w := width
		if i < extra {
			w++
		}
		ix.bands[i] = make(map[uint64][]int32, hint/4+1)
		ix.shifts[i] = shift
		ix.masks[i] = (uint64(1) << w) - 1
		shift += uint(w)
	}
	return ix
}

// Add records one fingerprint under the caller's own id.
func (ix *Index) Add(id int, fingerprint uint64) {
	for len(ix.fps) <= id {
		ix.fps = append(ix.fps, 0)
	}
	ix.fps[id] = fingerprint

	for i, buckets := range ix.bands {
		key := (fingerprint >> ix.shifts[i]) & ix.masks[i]
		buckets[key] = append(buckets[key], int32(id))
	}
}

// Len is how many fingerprints the index holds.
func (ix *Index) Len() int { return len(ix.fps) }

// Searcher is a cursor over an Index. One goroutine, one Searcher: it carries
// scratch space so that a fingerprint reachable through several bands is only
// offered to the caller once, and sharing that scratch across goroutines would
// silently drop results.
//
// A Searcher may be kept across calls to Add, which is what the merge path
// does: it asks whether each incoming file is already known, then adds the ones
// that were not. Only Add and Search from the same goroutine in that case.
type Searcher struct {
	ix *Index

	// seen[id] holds the generation at which id was last offered. A counter
	// beats clearing a bitmap between searches, which would make every search
	// cost O(n) whatever the filter saved.
	seen []int32
	gen  int32
}

// Search returns a cursor. Each goroutine needs its own.
func (ix *Index) Search() *Searcher {
	return &Searcher{ix: ix, seen: make([]int32, len(ix.fps))}
}

// Near calls visit with the id of every fingerprint within the index's
// threshold of the query, each at most once, in no particular order.
//
// The distance is checked here rather than left to the caller: the bands only
// narrow the field, and a caller who forgot to verify would silently widen what
// counts as a duplicate.
func (s *Searcher) Near(fingerprint uint64, visit func(id int)) {
	ix := s.ix
	s.gen++

	// The index may have grown since this Searcher was made. Extending is
	// cheap and the new entries start at generation zero, which is never a
	// live generation because gen was incremented above.
	if len(s.seen) < len(ix.fps) {
		s.seen = append(s.seen, make([]int32, len(ix.fps)-len(s.seen))...)
	}

	if ix.bands == nil {
		// No useful banding at this threshold: compare against everything.
		for id, fp := range ix.fps {
			if Distance(fingerprint, fp) <= ix.threshold {
				visit(id)
			}
		}
		return
	}

	for i, buckets := range ix.bands {
		key := (fingerprint >> ix.shifts[i]) & ix.masks[i]
		for _, id := range buckets[key] {
			if s.seen[id] == s.gen {
				continue
			}
			s.seen[id] = s.gen
			if Distance(fingerprint, ix.fps[id]) <= ix.threshold {
				visit(int(id))
			}
		}
	}
}
