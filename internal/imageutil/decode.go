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
	// is missing half the collection. This decoder is CGo-free — it embeds a
	// HEVC decoder compiled to WebAssembly and runs it in-process — which is
	// what keeps photocull a single static binary with no system libraries.
	//
	// It is also the most consequential dependency in the tree, and not for a
	// technical reason. The embedded WebAssembly is Imazen's Rust decoder,
	// which is AGPL-3.0-only, so it sets the license for the whole binary.
	// THIRD-PARTY-NOTICES.md explains it; removing this one import would take
	// wazero and purego with it and free photocull to be licensed as it liked.
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
