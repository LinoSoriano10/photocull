package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
	"strings"
	"sync"

	"github.com/nfnt/resize"
)

// Thumbnail sizes, as the longest edge in pixels. GridSize is small enough to
// send a whole group to the browser quickly; PreviewSize is the larger image
// shown when the user clicks a photo to compare it closely — and, importantly,
// it re-encodes formats a browser cannot show natively (HEIC) as JPEG.
const (
	GridSize    = 320
	PreviewSize = 1400
)

// CacheBudget is how much encoded JPEG the cache will hold before it starts
// evicting.
//
// 64 MB is roughly two thousand grid thumbnails, or a couple of hundred of the
// large previews — far more than anyone scrolls past in one sitting, and small
// enough that a review of a twenty-thousand-photo drive does not quietly turn
// into a memory leak. It is a budget rather than an entry count because the two
// sizes differ by an order of magnitude in bytes, and it is bytes that run out.
const CacheBudget = 64 << 20

// ThumbCache builds JPEG previews on demand and remembers them, so scrolling
// back through the review UI does not re-decode the same photo twice.
//
// The web UI is the only caller, and it may serve many images at once, so the
// cache is safe for concurrent use.
//
// An entry is keyed by path, size *and* the file's modification time. The
// modification time is what makes the cache correct rather than merely fast: a
// photo edited between two scans keeps its path, so without it the review would
// go on showing the old picture — and showing the wrong image is a bad failure
// in a tool whose whole job is deciding which file to destroy.
type ThumbCache struct {
	mu    sync.Mutex
	cache map[string]*cacheEntry
	bytes int64

	// clock counts accesses. Least-recently-used eviction needs an ordering,
	// and a counter is one without the bookkeeping of a linked list — a scan
	// for the oldest entry costs a pass over the map, which happens only when
	// the budget is already exceeded.
	clock int64
}

type cacheEntry struct {
	data []byte
	used int64
}

// NewThumbCache returns an empty cache.
func NewThumbCache() *ThumbCache {
	return &ThumbCache{cache: make(map[string]*cacheEntry)}
}

// Get returns a JPEG preview of the image at path whose longest edge is at most
// size pixels, generating and caching it on first request.
func (c *ThumbCache) Get(path string, size int) ([]byte, error) {
	// Stat before the lookup. It costs a syscall, but it is the only way to
	// notice that the file on disk is no longer the one that was cached, and a
	// stat is several orders of magnitude cheaper than the decode-and-resize it
	// is deciding whether to skip.
	var stamp int64
	if info, err := os.Stat(path); err == nil {
		stamp = info.ModTime().UnixNano()
	}
	key := fmt.Sprintf("%d|%d|%s", size, stamp, path)

	if data, ok := c.lookup(key); ok {
		return data, nil
	}

	data, err := Thumbnail(path, size)
	if err != nil {
		return nil, err
	}

	c.store(key, data)
	return data, nil
}

func (c *ThumbCache) lookup(key string) ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	e, ok := c.cache[key]
	if !ok {
		return nil, false
	}
	c.clock++
	e.used = c.clock
	return e.data, true
}

func (c *ThumbCache) store(key string, data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Two requests for the same photo can race to generate it; whichever
	// arrives second finds the entry already there and must not double-count
	// its bytes.
	if _, exists := c.cache[key]; exists {
		return
	}

	c.clock++
	c.cache[key] = &cacheEntry{data: data, used: c.clock}
	c.bytes += int64(len(data))
	c.evict()
}

// evict drops least-recently-used entries until the cache is inside its budget.
// Callers must hold c.mu.
func (c *ThumbCache) evict() {
	for c.bytes > CacheBudget && len(c.cache) > 1 {
		var oldestKey string
		var oldestUse int64 = -1
		for k, e := range c.cache {
			if oldestUse < 0 || e.used < oldestUse {
				oldestKey, oldestUse = k, e.used
			}
		}
		c.bytes -= int64(len(c.cache[oldestKey].data))
		delete(c.cache, oldestKey)
	}
}

// Forget drops every cached size of the given paths.
//
// The review server calls this when files go to the recycle bin. Their previews
// are no longer reachable through any endpoint, so keeping the bytes is pure
// waste — and if the user restores one and rescans, a stale entry is exactly
// what should not be waiting for them.
func (c *ThumbCache) Forget(paths ...string) {
	if len(paths) == 0 {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Keys carry the size and timestamp as prefixes, so a path is matched by
	// suffix. Cheap enough: this runs once per delete, not once per request.
	for key, e := range c.cache {
		for _, p := range paths {
			if strings.HasSuffix(key, "|"+p) {
				c.bytes -= int64(len(e.data))
				delete(c.cache, key)
				break
			}
		}
	}
}

// Thumbnail decodes the image at path and returns a JPEG preview of it whose
// longest edge is at most size pixels.
func Thumbnail(path string, size int) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("thumbnail: %w", err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("thumbnail: decode %s: %w", path, err)
	}

	small := resize.Thumbnail(uint(size), uint(size), img, resize.Bilinear)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, small, &jpeg.Options{Quality: 82}); err != nil {
		return nil, fmt.Errorf("thumbnail: encode: %w", err)
	}
	return buf.Bytes(), nil
}
