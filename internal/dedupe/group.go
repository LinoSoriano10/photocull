// Package dedupe turns a flat list of scanned files into groups of duplicates,
// and suggests which file in each group is the one worth keeping.
//
// Nothing in here knows what a file is. Exact grouping compares content hashes,
// similarity grouping compares 64-bit fingerprints by Hamming distance, and
// neither cares whether those numbers came from pixels or from prose — which is
// why photographs and documents share this code rather than each having their
// own copy of it. The one pass that does look at a file is GroupRelated, and it
// looks only at the name and the size, because it exists precisely for the
// files nothing could read inside.
package dedupe

import (
	"runtime"
	"sort"
	"sync"

	"photocull/internal/hashing"
	"photocull/internal/scanner"
)

// DefaultThreshold is the photo threshold, kept here because that is where the
// --threshold flag reaches for it. The number and the reasoning behind it live
// beside the algorithm it belongs to.
//
// It is deliberately exposed as a flag: the right value depends on the library,
// and the safe move is to review the groups before deleting either way.
const DefaultThreshold = hashing.DefaultPhotoThreshold

// MatchType records why a group's files ended up together.
type MatchType string

const (
	// Exact means every file in the group is byte-for-byte identical.
	Exact MatchType = "exact"
	// Similar means the files only look alike: resized, re-compressed or
	// re-encoded versions of the same photo. These need a human eye.
	Similar MatchType = "similar"

	// Related means photocull could not read inside these files at all, and is
	// only pointing out that their names and sizes line up. It is a hint, not a
	// finding: nothing is pre-selected, clean will not touch them, and they do
	// not count towards the reclaimable total.
	Related MatchType = "related"
)

// Group is a set of files that photocull believes are the same photo.
type Group struct {
	ID    string             `json:"id"`
	Type  MatchType          `json:"type"`
	Files []scanner.FileMeta `json:"files"`

	// KeepIndex is the suggested file to keep. It is only a suggestion.
	KeepIndex int `json:"keepIndex"`
}

// Keep returns the file the group suggests keeping.
func (g Group) Keep() scanner.FileMeta {
	if g.KeepIndex < 0 || g.KeepIndex >= len(g.Files) {
		return scanner.FileMeta{}
	}
	return g.Files[g.KeepIndex]
}

// Duplicates returns every file in the group except the one to keep.
func (g Group) Duplicates() []scanner.FileMeta {
	dups := make([]scanner.FileMeta, 0, len(g.Files))
	for i, f := range g.Files {
		if i != g.KeepIndex {
			dups = append(dups, f)
		}
	}
	return dups
}

// ReclaimableBytes is the space freed by deleting every file but the keeper.
func (g Group) ReclaimableBytes() int64 {
	var total int64
	for _, f := range g.Duplicates() {
		total += f.Size
	}
	return total
}

// GroupExact finds files that are byte-for-byte identical.
//
// A matching SHA-256 is proof, not a guess, so these groups are safe to act on
// without looking at the pictures.
func GroupExact(files []scanner.FileMeta, keep KeepStrategy) []Group {
	uf := newUnionFind(len(files))
	unionByContentHash(files, uf)
	return buildGroups(files, uf, keep)
}

// GroupSimilar finds files with the same content even if the bytes differ.
//
// It unions on two signals at once: an identical content hash, and fingerprints
// within threshold of each other. Doing both in a single pass means a photo,
// its resized copy and a byte-identical backup of it all land in one group
// instead of being reported twice — and files whose contents could not be read
// still get deduplicated through their content hash.
//
// The fingerprints are compared as bare 64-bit words, which only works because
// the scanner guarantees every file in one run was fingerprinted by the same
// algorithm. See scanner.FileMeta.Fingerprint.
func GroupSimilar(files []scanner.FileMeta, threshold int, keep KeepStrategy) []Group {
	uf := newUnionFind(len(files))
	unionByContentHash(files, uf)
	unionBySimilarity(files, threshold, uf)
	return buildGroups(files, uf, keep)
}

// parallelFrom is the population size above which the similarity pass is worth
// splitting across cores. Below it the goroutines cost more than the work they
// save, and a photo library of a few hundred files finishes in under a
// millisecond either way.
const parallelFrom = 4000

// unionBySimilarity joins every pair of files whose fingerprints are within
// threshold of each other.
//
// This used to be a plain double loop, and the comment here used to argue —
// correctly, at the time — that optimising n(n-1)/2 XORs would be solving a
// problem photocull did not have. Documents mode changed the input: it walks
// every extension on purpose, so a whole-drive scan is a hundred thousand
// candidates rather than a few thousand, and the measured cost went from a
// millisecond to nearly ten seconds.
//
// Two changes, in the order that matters. First the index does the same search
// with far fewer comparisons, which is the real win. Only then is it worth
// spreading over cores, because parallelising a quadratic loop just buys a
// constant factor on the wrong algorithm.
func unionBySimilarity(files []scanner.FileMeta, threshold int, uf *unionFind) {
	// Only files that actually produced a fingerprint can be compared.
	candidates := make([]int, 0, len(files))
	for i, f := range files {
		if f.HasFingerprint {
			candidates = append(candidates, i)
		}
	}
	if len(candidates) < 2 {
		return
	}

	// Indexed by position in candidates, not by position in files: the index
	// treats every id it holds as a real fingerprint, and seeding it with zeros
	// for the files that have none would make them all neighbours of each other.
	index := hashing.NewIndex(threshold, len(candidates))
	for a, i := range candidates {
		index.Add(a, files[i].Fingerprint)
	}

	workers := runtime.NumCPU()
	if len(candidates) < parallelFrom || workers < 2 {
		joinRange(files, candidates, index.Search(), uf, 0, len(candidates))
		return
	}

	// Each worker gets its own union-find over the same ids and the caller
	// merges them, rather than every worker locking the shared one.
	//
	// The obvious alternative — collect the pairs and union them afterwards —
	// is unbounded: a directory that really is all copies of one file yields
	// n(n-1)/2 pairs, and a list of them is far larger than the files. A local
	// union-find holds the same information in two ints per file however many
	// pairs produced it, and merging is exact because the union of the workers'
	// partitions is the transitive closure of everything they found.
	locals := make([]*unionFind, workers)
	var wg sync.WaitGroup
	span := (len(candidates) + workers - 1) / workers
	for w := range workers {
		lo := w * span
		if lo >= len(candidates) {
			break
		}
		hi := min(lo+span, len(candidates))

		local := newUnionFind(len(files))
		locals[w] = local

		wg.Add(1)
		go func() {
			defer wg.Done()
			// One Searcher per goroutine: it carries the scratch space that
			// keeps a file reachable through several bands from being offered
			// twice, and sharing it would silently drop matches.
			joinRange(files, candidates, index.Search(), local, lo, hi)
		}()
	}
	wg.Wait()

	for _, local := range locals {
		if local == nil {
			continue
		}
		for _, i := range candidates {
			if root := local.find(i); root != i {
				uf.union(i, root)
			}
		}
	}
}

// joinRange searches the index for every candidate in [lo, hi) and unions what
// it finds into uf.
func joinRange(files []scanner.FileMeta, candidates []int, search *hashing.Searcher, uf *unionFind, lo, hi int) {
	for a := lo; a < hi; a++ {
		i := candidates[a]
		search.Near(files[i].Fingerprint, func(b int) {
			// Every pair comes back from both ends, and a file is its own
			// nearest neighbour; taking only the forward half of each pair
			// halves the unions without changing the partition.
			if b > a {
				uf.union(i, candidates[b])
			}
		})
	}
}

// unionByContentHash joins every file that shares a SHA-256.
func unionByContentHash(files []scanner.FileMeta, uf *unionFind) {
	firstWithHash := make(map[string]int, len(files))
	for i, f := range files {
		if f.SHA256 == "" {
			continue
		}
		if first, seen := firstWithHash[f.SHA256]; seen {
			uf.union(first, i)
		} else {
			firstWithHash[f.SHA256] = i
		}
	}
}

// buildGroups turns the union-find partition into sorted, labelled groups,
// dropping every file that turned out to be unique.
func buildGroups(files []scanner.FileMeta, uf *unionFind, keep KeepStrategy) []Group {
	if keep == nil {
		keep = DefaultKeepStrategy
	}

	members := make(map[int][]scanner.FileMeta)
	for i, f := range files {
		root := uf.find(i)
		members[root] = append(members[root], f)
	}

	groups := make([]Group, 0, len(members))
	// The sort below used to call ReclaimableBytes inside the comparator, and
	// that method builds a fresh slice of copied FileMeta values on every call —
	// two allocations per comparison, n log n comparisons. The totals are
	// computed once here instead and carried alongside.
	reclaimable := make([]int64, 0, len(members))

	for _, groupFiles := range members {
		if len(groupFiles) < 2 {
			continue
		}

		sort.Slice(groupFiles, func(i, j int) bool {
			return groupFiles[i].Path < groupFiles[j].Path
		})

		g := Group{
			ID:        groupID(groupFiles),
			Type:      classify(groupFiles),
			Files:     groupFiles,
			KeepIndex: keep(groupFiles),
		}
		groups = append(groups, g)
		reclaimable = append(reclaimable, g.ReclaimableBytes())
	}

	// Biggest win first: the groups that free the most space are the ones
	// worth a human's attention.
	sort.Sort(byReclaimable{groups: groups, bytes: reclaimable})

	return groups
}

// byReclaimable orders groups by the space deleting their duplicates would
// free, carrying the precomputed totals so the comparator never recomputes one.
type byReclaimable struct {
	groups []Group
	bytes  []int64
}

func (b byReclaimable) Len() int { return len(b.groups) }

func (b byReclaimable) Less(i, j int) bool {
	if b.bytes[i] != b.bytes[j] {
		return b.bytes[i] > b.bytes[j]
	}
	return b.groups[i].ID < b.groups[j].ID
}

func (b byReclaimable) Swap(i, j int) {
	b.groups[i], b.groups[j] = b.groups[j], b.groups[i]
	b.bytes[i], b.bytes[j] = b.bytes[j], b.bytes[i]
}

// classify reports whether a group is provably identical or merely alike.
func classify(files []scanner.FileMeta) MatchType {
	for _, f := range files[1:] {
		if f.SHA256 != files[0].SHA256 {
			return Similar
		}
	}
	return Exact
}

// groupID derives a short, stable identifier from the group's contents, so
// that the same scan always produces the same IDs and the web UI can refer to
// a group across requests.
func groupID(files []scanner.FileMeta) string {
	lowest := files[0].SHA256
	for _, f := range files[1:] {
		if f.SHA256 < lowest {
			lowest = f.SHA256
		}
	}
	if len(lowest) > 12 {
		return lowest[:12]
	}
	return lowest
}

// unionFind is a disjoint-set structure with path compression and union by
// rank.
//
// Similarity is not transitive — A can be within the threshold of B, and B of
// C, without A and C being close — so a simple "bucket by hash" approach would
// split real clusters. Union-find follows the chain and puts the whole cluster
// in front of the user as one decision.
type unionFind struct {
	parent []int
	rank   []int
}

func newUnionFind(n int) *unionFind {
	uf := &unionFind{parent: make([]int, n), rank: make([]int, n)}
	for i := range uf.parent {
		uf.parent[i] = i
	}
	return uf
}

func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path compression
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b int) {
	rootA, rootB := u.find(a), u.find(b)
	if rootA == rootB {
		return
	}
	if u.rank[rootA] < u.rank[rootB] {
		rootA, rootB = rootB, rootA
	}
	u.parent[rootB] = rootA
	if u.rank[rootA] == u.rank[rootB] {
		u.rank[rootA]++
	}
}
