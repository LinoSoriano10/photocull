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
	"sort"

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

	// Only files that actually produced a fingerprint can be compared.
	candidates := make([]int, 0, len(files))
	for i, f := range files {
		if f.HasFingerprint {
			candidates = append(candidates, i)
		}
	}

	// Pairwise comparison is O(n^2), but each comparison is a XOR and a
	// popcount on a 64-bit word. For the thousands of photos this tool is
	// aimed at that is a few million register operations — fast enough that
	// optimising it (a BK-tree, or bucketing by hash prefix) would be solving
	// a problem we do not have yet.
	for a := 0; a < len(candidates); a++ {
		for b := a + 1; b < len(candidates); b++ {
			i, j := candidates[a], candidates[b]
			if hashing.Distance(files[i].Fingerprint, files[j].Fingerprint) <= threshold {
				uf.union(i, j)
			}
		}
	}

	return buildGroups(files, uf, keep)
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
	for _, groupFiles := range members {
		if len(groupFiles) < 2 {
			continue
		}

		sort.Slice(groupFiles, func(i, j int) bool {
			return groupFiles[i].Path < groupFiles[j].Path
		})

		groups = append(groups, Group{
			ID:        groupID(groupFiles),
			Type:      classify(groupFiles),
			Files:     groupFiles,
			KeepIndex: keep(groupFiles),
		})
	}

	// Biggest win first: the groups that free the most space are the ones
	// worth a human's attention.
	sort.Slice(groups, func(i, j int) bool {
		if groups[i].ReclaimableBytes() != groups[j].ReclaimableBytes() {
			return groups[i].ReclaimableBytes() > groups[j].ReclaimableBytes()
		}
		return groups[i].ID < groups[j].ID
	})

	return groups
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
