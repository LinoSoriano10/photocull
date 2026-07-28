package imageutil

import (
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const testdataDir = "../../testdata"

func TestDecodeJPEG(t *testing.T) {
	f, err := os.Open(filepath.Join(testdataDir, "similar", "source.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := Decode(f)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if img.Bounds().Empty() {
		t.Error("decoded image has empty bounds")
	}
}

func TestDecodeConfigReadsDimensions(t *testing.T) {
	f, err := os.Open(filepath.Join(testdataDir, "similar", "source.jpg"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	cfg, err := DecodeConfig(f)
	if err != nil {
		t.Fatalf("DecodeConfig: %v", err)
	}
	if cfg.Width != 640 || cfg.Height != 480 {
		t.Errorf("dimensions %dx%d, want 640x480", cfg.Width, cfg.Height)
	}
}

// TestDefaultExtensionsIncludeHEIC guards the iPhone case: the whole reason the
// HEIC decoder is pulled in is that a photo library scan must not silently skip
// every .heic file.
//
// It checks the slice directly rather than through fingerprint's extension
// helpers. Those moved out of this package, and reaching for them here would
// import a package that imports this one — a cycle the compiler only reports
// when the tests are built.
func TestDefaultExtensionsIncludeHEIC(t *testing.T) {
	if !slices.Contains(DefaultExtensions, ".heic") {
		t.Error("DefaultExtensions must include .heic or iPhone photos are ignored")
	}
}

// TestDecodeHEIC exercises the real HEIC decoder when a sample is available.
// No HEIC is committed to the repo (to avoid redistributing third-party image
// data of unclear provenance), so drop any .heic file into testdata/heic/ to
// turn this into a live check; otherwise it skips.
func TestDecodeHEIC(t *testing.T) {
	matches, _ := filepath.Glob(filepath.Join(testdataDir, "heic", "*.heic"))
	if len(matches) == 0 {
		t.Skip("no HEIC sample in testdata/heic/ (decoder verified separately)")
	}

	f, err := os.Open(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := Decode(f)
	if err != nil {
		t.Fatalf("Decode HEIC %s: %v", matches[0], err)
	}
	if img.Bounds().Empty() {
		t.Error("decoded HEIC has empty bounds")
	}
}
