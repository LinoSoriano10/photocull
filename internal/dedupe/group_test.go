package dedupe

import (
	"testing"
	"time"

	"photocull/internal/scanner"
)

// The grouping logic is pure: it only reads FileMeta. Building those by hand
// keeps these tests fast, deterministic and independent of any image on disk.
func file(path, sha string) scanner.FileMeta {
	return scanner.FileMeta{
		Path:    path,
		SHA256:  sha,
		Size:    1000,
		Width:   100,
		Height:  100,
		Decoded: true,
		ModTime: time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC),
	}
}

func withFingerprint(f scanner.FileMeta, fingerprint uint64) scanner.FileMeta {
	f.Fingerprint = fingerprint
	f.HasFingerprint = true
	return f
}

func withSize(f scanner.FileMeta, size int64) scanner.FileMeta {
	f.Size = size
	return f
}

// paths lists a group's files, for readable failure messages.
func paths(g Group) []string {
	out := make([]string, 0, len(g.Files))
	for _, f := range g.Files {
		out = append(out, f.Path)
	}
	return out
}

func TestGroupExact(t *testing.T) {
	tests := []struct {
		name       string
		files      []scanner.FileMeta
		wantGroups int
		wantSizes  []int
	}{
		{
			name:       "no files",
			files:      nil,
			wantGroups: 0,
		},
		{
			name:       "all unique",
			files:      []scanner.FileMeta{file("a.jpg", "h1"), file("b.jpg", "h2")},
			wantGroups: 0,
		},
		{
			name:       "one pair",
			files:      []scanner.FileMeta{file("a.jpg", "h1"), file("b.jpg", "h1")},
			wantGroups: 1,
			wantSizes:  []int{2},
		},
		{
			name: "a pair and a triple",
			files: []scanner.FileMeta{
				file("a.jpg", "h1"), file("b.jpg", "h1"),
				file("c.jpg", "h2"), file("d.jpg", "h2"), file("e.jpg", "h2"),
				file("f.jpg", "h3"),
			},
			wantGroups: 2,
			wantSizes:  []int{3, 2},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			groups := GroupExact(tt.files, DefaultKeepStrategy)

			if len(groups) != tt.wantGroups {
				t.Fatalf("got %d groups, want %d", len(groups), tt.wantGroups)
			}
			for i, want := range tt.wantSizes {
				if len(groups[i].Files) != want {
					t.Errorf("group %d has %d files, want %d: %v", i, len(groups[i].Files), want, paths(groups[i]))
				}
			}
			for _, g := range groups {
				if g.Type != Exact {
					t.Errorf("group %s typed %q, want %q", g.ID, g.Type, Exact)
				}
			}
		})
	}
}

func TestGroupSimilarClustersNearbyHashes(t *testing.T) {
	files := []scanner.FileMeta{
		withFingerprint(file("photo.jpg", "h1"), 0b0000),
		withFingerprint(file("photo_resized.jpg", "h2"), 0b0011), // 2 bits away
		withFingerprint(file("unrelated.jpg", "h3"), ^uint64(0)), // 64 bits away
	}

	groups := GroupSimilar(files, 8, DefaultKeepStrategy)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %v", len(groups), groups)
	}
	if len(groups[0].Files) != 2 {
		t.Errorf("group holds %v, want the photo and its resized copy", paths(groups[0]))
	}
	if groups[0].Type != Similar {
		t.Errorf("type = %q, want %q: the files are not byte-identical", groups[0].Type, Similar)
	}
}

// TestGroupSimilarFollowsChains is why grouping uses union-find rather than
// bucketing. Similarity is not transitive: A can be close to B and B to C
// while A and C are far apart. They are still the same photo, and splitting
// them would hand the user the same decision twice.
func TestGroupSimilarFollowsChains(t *testing.T) {
	files := []scanner.FileMeta{
		withFingerprint(file("a.jpg", "h1"), 0x00), // 4 bits from b
		withFingerprint(file("b.jpg", "h2"), 0x0F), // 4 bits from c
		withFingerprint(file("c.jpg", "h3"), 0xFF), // 8 bits from a: beyond the threshold
	}

	groups := GroupSimilar(files, 4, DefaultKeepStrategy)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1 chained group", len(groups))
	}
	if len(groups[0].Files) != 3 {
		t.Errorf("group holds %v, want all three linked files", paths(groups[0]))
	}
}

func TestGroupSimilarRespectsThreshold(t *testing.T) {
	files := []scanner.FileMeta{
		withFingerprint(file("a.jpg", "h1"), 0b0000),
		withFingerprint(file("b.jpg", "h2"), 0b0111), // exactly 3 bits away
	}

	if groups := GroupSimilar(files, 3, DefaultKeepStrategy); len(groups) != 1 {
		t.Errorf("threshold 3 gave %d groups, want 1: the distance is exactly 3", len(groups))
	}
	if groups := GroupSimilar(files, 2, DefaultKeepStrategy); len(groups) != 0 {
		t.Errorf("threshold 2 gave %d groups, want 0", len(groups))
	}
}

// TestGroupSimilarStillDeduplicatesUndecodableFiles covers corrupt photos:
// they never get a perceptual hash, but two identical copies of the same
// broken file are still duplicates and must not be dropped from the report.
func TestGroupSimilarStillDeduplicatesUndecodableFiles(t *testing.T) {
	files := []scanner.FileMeta{
		file("broken.jpg", "same"),        // no perceptual hash
		file("backup/broken.jpg", "same"), // no perceptual hash
		withFingerprint(file("fine.jpg", "other"), 0x1234),
	}

	groups := GroupSimilar(files, 8, DefaultKeepStrategy)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].Type != Exact {
		t.Errorf("type = %q, want %q: the two files are byte-identical", groups[0].Type, Exact)
	}
}

// TestGroupSimilarMergesExactAndSimilar checks that a photo, a byte-identical
// backup of it and a resized version all land in one group instead of being
// reported as two overlapping ones.
func TestGroupSimilarMergesExactAndSimilar(t *testing.T) {
	files := []scanner.FileMeta{
		withFingerprint(file("photo.jpg", "same"), 0b0000),
		withFingerprint(file("backup/photo.jpg", "same"), 0b0000),
		withFingerprint(file("photo_small.jpg", "other"), 0b0001),
	}

	groups := GroupSimilar(files, 8, DefaultKeepStrategy)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1: %v", len(groups), groups)
	}
	if len(groups[0].Files) != 3 {
		t.Errorf("group holds %v, want all three", paths(groups[0]))
	}
	if groups[0].Type != Similar {
		t.Errorf("type = %q, want %q: not every file is byte-identical", groups[0].Type, Similar)
	}
}

func TestGroupsAreSortedByReclaimableSpace(t *testing.T) {
	files := []scanner.FileMeta{
		withSize(file("small_a.jpg", "h1"), 100), withSize(file("small_b.jpg", "h1"), 100),
		withSize(file("big_a.jpg", "h2"), 9000), withSize(file("big_b.jpg", "h2"), 9000),
	}

	groups := GroupExact(files, DefaultKeepStrategy)

	if len(groups) != 2 {
		t.Fatalf("got %d groups, want 2", len(groups))
	}
	if groups[0].ReclaimableBytes() < groups[1].ReclaimableBytes() {
		t.Errorf("groups are not ordered by reclaimable space: %d then %d",
			groups[0].ReclaimableBytes(), groups[1].ReclaimableBytes())
	}
}

func TestGroupIDIsStableAcrossOrderings(t *testing.T) {
	forwards := []scanner.FileMeta{file("a.jpg", "h1"), file("b.jpg", "h1")}
	backwards := []scanner.FileMeta{file("b.jpg", "h1"), file("a.jpg", "h1")}

	first := GroupExact(forwards, DefaultKeepStrategy)
	second := GroupExact(backwards, DefaultKeepStrategy)

	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("expected one group each, got %d and %d", len(first), len(second))
	}
	if first[0].ID != second[0].ID {
		t.Errorf("group ID changed with input order: %q vs %q", first[0].ID, second[0].ID)
	}
	if first[0].Files[0].Path != second[0].Files[0].Path {
		t.Error("file order inside a group depends on input order")
	}
}

func TestGroupAccessors(t *testing.T) {
	files := []scanner.FileMeta{
		withSize(file("keep.jpg", "h1"), 500),
		withSize(file("nested/dup_one.jpg", "h1"), 500),
		withSize(file("nested/dup_two.jpg", "h1"), 500),
	}

	groups := GroupExact(files, DefaultKeepStrategy)
	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	g := groups[0]

	if g.Keep().Path != "keep.jpg" {
		t.Errorf("Keep() = %q, want the file at the shallowest path", g.Keep().Path)
	}
	if len(g.Duplicates()) != 2 {
		t.Errorf("Duplicates() returned %d files, want 2", len(g.Duplicates()))
	}
	if got := g.ReclaimableBytes(); got != 1000 {
		t.Errorf("ReclaimableBytes = %d, want 1000", got)
	}
	for _, d := range g.Duplicates() {
		if d.Path == g.Keep().Path {
			t.Error("Duplicates() included the file that should be kept")
		}
	}
}

func TestGroupKeepHandlesInvalidIndex(t *testing.T) {
	g := Group{Files: []scanner.FileMeta{file("a.jpg", "h1")}, KeepIndex: 99}

	if g.Keep().Path != "" {
		t.Error("Keep() with an out-of-range index should return a zero value, not panic or guess")
	}
}

func TestGroupExactFallsBackToDefaultStrategy(t *testing.T) {
	files := []scanner.FileMeta{file("a.jpg", "h1"), file("b.jpg", "h1")}

	groups := GroupExact(files, nil)

	if len(groups) != 1 {
		t.Fatalf("got %d groups, want 1", len(groups))
	}
	if groups[0].KeepIndex < 0 || groups[0].KeepIndex >= len(groups[0].Files) {
		t.Errorf("KeepIndex = %d, out of range", groups[0].KeepIndex)
	}
}
