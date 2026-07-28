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
