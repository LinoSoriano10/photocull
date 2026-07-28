package imageutil

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	"os"
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

// ThumbCache builds JPEG previews on demand and remembers them, so scrolling
// back through the review UI does not re-decode the same photo twice.
//
// The web UI is the only caller, and it may serve many images at once, so the
// cache is safe for concurrent use. Entries are keyed by both path and size,
// because the grid and the close-up view ask for different sizes of the same
// photo.
type ThumbCache struct {
	mu    sync.RWMutex
	cache map[string][]byte
}

// NewThumbCache returns an empty cache.
func NewThumbCache() *ThumbCache {
	return &ThumbCache{cache: make(map[string][]byte)}
}

// Get returns a JPEG preview of the image at path whose longest edge is at most
// size pixels, generating and caching it on first request.
func (c *ThumbCache) Get(path string, size int) ([]byte, error) {
	key := fmt.Sprintf("%d|%s", size, path)

	c.mu.RLock()
	cached, ok := c.cache[key]
	c.mu.RUnlock()
	if ok {
		return cached, nil
	}

	data, err := Thumbnail(path, size)
	if err != nil {
		return nil, err
	}

	c.mu.Lock()
	c.cache[key] = data
	c.mu.Unlock()
	return data, nil
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
