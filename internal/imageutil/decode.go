// Package imageutil owns every image format photocull can read.
//
// Format support in Go works through blank imports that register a decoder
// with the standard image package. Keeping those imports in one place means
// adding a format later is a one-line change here rather than a hunt through
// the codebase.
package imageutil

import (
	"image"
	"io"
	"path/filepath"
	"strings"

	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"

	// HEIC/HEIF is what iPhones save by default, so a photo library without it
	// is missing half the collection. This decoder is CGo-free (it runs libheif
	// compiled to WebAssembly), which keeps photocull a single static binary.
	_ "github.com/gen2brain/heic"
)

// DefaultExtensions lists the file extensions photocull treats as photos.
var DefaultExtensions = []string{".jpg", ".jpeg", ".png", ".gif", ".heic", ".heif"}

// Decode reads a full image. Callers that only need the dimensions should use
// DecodeConfig, which stops after the header.
func Decode(r io.Reader) (image.Image, error) {
	img, _, err := image.Decode(r)
	return img, err
}

// DecodeConfig reads just enough of r to learn the image's dimensions.
func DecodeConfig(r io.Reader) (image.Config, error) {
	cfg, _, err := image.DecodeConfig(r)
	return cfg, err
}

// NormaliseExtensions lowercases extensions and makes sure each starts with a
// dot, so "--ext JPG" and "--ext .jpg" mean the same thing.
func NormaliseExtensions(exts []string) map[string]bool {
	set := make(map[string]bool, len(exts))
	for _, ext := range exts {
		ext = strings.ToLower(strings.TrimSpace(ext))
		if ext == "" {
			continue
		}
		if !strings.HasPrefix(ext, ".") {
			ext = "." + ext
		}
		set[ext] = true
	}
	return set
}

// HasExtension reports whether path carries one of the accepted extensions.
func HasExtension(path string, accepted map[string]bool) bool {
	return accepted[strings.ToLower(filepath.Ext(path))]
}
