package imageutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// copyFixture puts one of the committed JPEGs at dst.
func copyFixture(t *testing.T, name, dst string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func TestThumbCacheServesTheSameBytesTwice(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	copyFixture(t, "exact/original.jpg", path)

	c := NewThumbCache()
	first, err := c.Get(path, GridSize)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	second, err := c.Get(path, GridSize)
	if err != nil {
		t.Fatalf("Get again: %v", err)
	}
	if len(first) == 0 || string(first) != string(second) {
		t.Error("the cache returned different bytes for the same unchanged file")
	}
	if len(c.cache) != 1 {
		t.Errorf("cache holds %d entries after two reads of one photo, want 1", len(c.cache))
	}
}

// TestThumbCacheNoticesTheFileChanged is the correctness half of the cache, and
// the reason the key carries a modification time.
//
// A photo edited between two scans keeps its path. Keyed on the path alone, the
// review would go on showing the old picture — and being shown the wrong image
// is a bad failure in a tool whose job is choosing which file to destroy.
func TestThumbCacheNoticesTheFileChanged(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	copyFixture(t, "exact/original.jpg", path)

	c := NewThumbCache()
	before, err := c.Get(path, GridSize)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	// Replace the contents, and make sure the timestamp really moved: a fast
	// filesystem can give both writes the same modification time.
	copyFixture(t, "exact/unrelated.jpg", path)
	if err := os.Chtimes(path, time.Now(), time.Now().Add(2*time.Second)); err != nil {
		t.Fatalf("chtimes: %v", err)
	}

	after, err := c.Get(path, GridSize)
	if err != nil {
		t.Fatalf("Get after edit: %v", err)
	}
	if string(before) == string(after) {
		t.Error("the cache served the old thumbnail for a file that changed on disk")
	}
}

func TestThumbCacheKeepsSizesApart(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	copyFixture(t, "exact/original.jpg", path)

	c := NewThumbCache()
	small, _ := c.Get(path, GridSize)
	large, _ := c.Get(path, PreviewSize)
	if len(small) == 0 || len(large) == 0 {
		t.Fatal("one of the two sizes came back empty")
	}
	if string(small) == string(large) {
		t.Error("the grid and preview sizes shared a cache entry")
	}
}

// TestThumbCacheStaysInsideItsBudget stops a long review from turning into a
// memory leak. Without eviction the cache grew for as long as the app was open.
func TestThumbCacheStaysInsideItsBudget(t *testing.T) {
	c := NewThumbCache()

	// Fill it past the budget directly: generating enough real thumbnails to
	// pass 64 MB would make this test take minutes for no extra confidence.
	chunk := make([]byte, 1<<20)
	for i := range 100 {
		c.store(string(rune('a'+i%26))+"|"+strings.Repeat("x", i+1), chunk)
	}

	if c.bytes > CacheBudget {
		t.Errorf("cache holds %d bytes, over the %d budget", c.bytes, CacheBudget)
	}
	if len(c.cache) == 0 {
		t.Error("eviction emptied the cache entirely; it should keep the recent entries")
	}
}

// TestThumbCacheEvictsTheLeastRecentlyUsed checks it drops the right ones. An
// eviction policy that threw away what the user is currently looking at would
// be worse than no cache at all.
func TestThumbCacheEvictsTheLeastRecentlyUsed(t *testing.T) {
	c := NewThumbCache()
	// Two of these fit inside the budget and three do not, so storing the third
	// forces exactly one eviction — which is the choice being tested.
	chunk := make([]byte, CacheBudget*2/5)

	c.store("old", chunk)
	c.store("new", chunk)
	// Touch "old" so it is no longer the least recently used.
	if _, ok := c.lookup("old"); !ok {
		t.Fatal("the first entry was already gone")
	}
	c.store("newest", chunk)

	if _, ok := c.lookup("old"); !ok {
		t.Error("evicted the entry that had just been read")
	}
	if _, ok := c.lookup("new"); ok {
		t.Error("kept the least recently used entry instead of the freshest")
	}
}

func TestThumbCacheForgetsDeletedFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "photo.jpg")
	copyFixture(t, "exact/original.jpg", path)

	c := NewThumbCache()
	if _, err := c.Get(path, GridSize); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, err := c.Get(path, PreviewSize); err != nil {
		t.Fatalf("Get preview: %v", err)
	}
	if len(c.cache) != 2 {
		t.Fatalf("cache holds %d entries, want 2", len(c.cache))
	}

	c.Forget(path)
	if len(c.cache) != 0 {
		t.Errorf("Forget left %d entries; every size of a recycled file should go", len(c.cache))
	}
	if c.bytes != 0 {
		t.Errorf("byte count is %d after forgetting everything, want 0", c.bytes)
	}
}

func TestThumbCacheGetFailsOnAMissingFile(t *testing.T) {
	c := NewThumbCache()
	if _, err := c.Get(filepath.Join(t.TempDir(), "nope.jpg"), GridSize); err == nil {
		t.Error("Get on a missing file returned no error")
	}
}
