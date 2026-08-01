package imageutil

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

// compareFixtures runs Compare over two paths under testdata.
func compareFixtures(t *testing.T, a, b string) *Diff {
	t.Helper()
	d, err := Compare(filepath.Join(testdataDir, a), filepath.Join(testdataDir, b))
	if err != nil {
		t.Fatalf("Compare(%s, %s): %v", a, b, err)
	}
	return d
}

// TestCompareSeparatesNearDuplicatesFromDifferentPhotos is the test the whole
// comparison view rests on. If these two bands ever overlap, the ratio shown to
// the user stops meaning anything and the heatmap becomes noise.
//
// The measured values leave a wide gap — near-duplicates land under 4% and
// unrelated photos above 95% — so the bounds below are deliberately loose. They
// are there to catch a change of kind, not to pin down a number.
func TestCompareSeparatesNearDuplicatesFromDifferentPhotos(t *testing.T) {
	tests := []struct {
		name     string
		a, b     string
		wantNear bool
	}{
		{"a byte-identical copy", "exact/original.jpg", "exact/backup/original.jpg", true},
		{"the same photo at half size", "similar/source.jpg", "similar/source_resized.jpg", true},
		{"the same photo re-compressed", "similar/source.jpg", "similar/source_recompressed.jpg", true},
		{"a genuinely different photo", "similar/source.jpg", "similar/different.jpg", false},
		{"an unrelated photo", "exact/original.jpg", "exact/unrelated.jpg", false},
	}

	const (
		nearMax = 0.10 // near-duplicates measure under 0.04
		farMin  = 0.50 // different photos measure over 0.95
	)

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := compareFixtures(t, tt.a, tt.b)
			if d.AspectMismatch {
				t.Fatal("these fixtures are all 4:3, so no aspect mismatch should be reported")
			}
			switch {
			case tt.wantNear && d.Ratio > nearMax:
				t.Errorf("ratio = %.4f, want below %.2f: two copies of one photo look different enough to be called separate images, so the view would tell the user to keep both", d.Ratio, nearMax)
			case !tt.wantNear && d.Ratio < farMin:
				t.Errorf("ratio = %.4f, want above %.2f: two unrelated photos look nearly identical, so the view would invite the user to delete one of them", d.Ratio, farMin)
			}
		})
	}
}

// TestCompareIgnoresRecompressionNoise pins the reason DiffTolerance is not
// zero. Re-saving a JPEG at a lower quality nudges almost every pixel, so a
// strict comparison reports the whole frame as changed and is useless for
// precisely the case the user needs help with.
func TestCompareIgnoresRecompressionNoise(t *testing.T) {
	d := compareFixtures(t, "similar/source.jpg", "similar/source_recompressed.jpg")
	if d.Ratio > 0.10 {
		t.Errorf("ratio = %.4f: JPEG re-compression is being reported as a real difference", d.Ratio)
	}
}

// TestCompareOnIdenticalFilesFindsNothing guards the obvious case, because a
// difference reported between two identical files would make every "exact"
// group look suspicious.
func TestCompareOnIdenticalFilesFindsNothing(t *testing.T) {
	d := compareFixtures(t, "exact/original.jpg", "exact/backup/original.jpg")
	if d.Ratio != 0 {
		t.Errorf("ratio = %v, want 0: two byte-identical files differ", d.Ratio)
	}
	if !d.Box.Empty() {
		t.Errorf("Box = %v, want empty: nothing changed, so nothing should be highlighted", d.Box)
	}
}

// TestCompareScalesToTheSmallerPhoto checks that a full-size original and its
// downscaled copy are compared on a common grid. Without this the two are not
// comparable pixel for pixel at all, and this pairing — original plus a resized
// copy — is the single most common thing the similar tier finds.
func TestCompareScalesToTheSmallerPhoto(t *testing.T) {
	d := compareFixtures(t, "similar/source.jpg", "similar/source_resized.jpg")
	if d.Width != 320 || d.Height != 240 {
		t.Errorf("grid = %dx%d, want 320x240: the comparison should drop to the smaller photo's size rather than invent pixels for the larger one", d.Width, d.Height)
	}
}

// TestCompareLocatesTheChange checks the bounding box, which is what lets the UI
// say *where* a photo changed instead of only how much.
func TestCompareLocatesTheChange(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(testdataDir, "similar", "source.jpg")
	patched := filepath.Join(dir, "patched.jpg")
	// A solid block in one corner, well away from the edges of the frame.
	writePatched(t, base, patched, image.Rect(40, 40, 140, 140))

	d, err := Compare(base, patched)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if d.Box.Empty() {
		t.Fatal("Box is empty although a block of the photo was painted over")
	}
	// JPEG spreads a hard edge over neighbouring blocks, so allow slack rather
	// than asserting the exact rectangle.
	if d.Box.Min.X > 40 || d.Box.Min.Y > 40 || d.Box.Max.X < 140 || d.Box.Max.Y < 140 {
		t.Errorf("Box = %v, want it to contain (40,40)-(140,140): the highlighted region misses part of what changed", d.Box)
	}
	if d.Box.Min.X < 8 && d.Box.Min.Y < 8 && d.Box.Max.X > 632 && d.Box.Max.Y > 472 {
		t.Errorf("Box = %v covers essentially the whole frame, so it tells the user nothing about where to look", d.Box)
	}
}

// TestCompareRefusesMismatchedAspectRatio guards the honest answer. Two photos
// framed differently do not correspond pixel for pixel, so a difference map
// would be noise presented as a finding.
func TestCompareRefusesMismatchedAspectRatio(t *testing.T) {
	dir := t.TempDir()
	base := filepath.Join(testdataDir, "similar", "source.jpg")
	cropped := filepath.Join(dir, "cropped.jpg")
	writeCrop(t, base, cropped, image.Rect(0, 0, 640, 300)) // 64:30, not 4:3

	d, err := Compare(base, cropped)
	if err != nil {
		t.Fatalf("Compare: %v", err)
	}
	if !d.AspectMismatch {
		t.Fatal("a 4:3 photo compared against a 64:30 crop was not reported as differently framed")
	}
	if _, err := d.HeatmapPNG(); err == nil {
		t.Error("HeatmapPNG returned a map for two differently framed photos; it would show a shifted ghost of one image over the other and read as a huge false difference")
	}
}

// TestHeatmapRendersAtTheComparisonSize checks the map is a real PNG matching
// the grid, since the page sizes its overlay against the preview.
func TestHeatmapRendersAtTheComparisonSize(t *testing.T) {
	d := compareFixtures(t, "similar/source.jpg", "similar/source_resized.jpg")

	data, err := d.HeatmapPNG()
	if err != nil {
		t.Fatalf("HeatmapPNG: %v", err)
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("the heatmap is not a decodable image: %v", err)
	}
	if format != "png" {
		t.Errorf("format = %q, want png: the handler declares image/png", format)
	}
	if cfg.Width != d.Width || cfg.Height != d.Height {
		t.Errorf("heatmap is %dx%d, want %dx%d: it would not line up with the photo it is overlaid on", cfg.Width, cfg.Height, d.Width, d.Height)
	}
}

// TestCompareReportsMissingFile checks a bad path fails rather than producing an
// empty comparison that would read as "these two photos are identical".
func TestCompareReportsMissingFile(t *testing.T) {
	if _, err := Compare(filepath.Join(testdataDir, "similar", "source.jpg"), filepath.Join(t.TempDir(), "nope.jpg")); err == nil {
		t.Error("Compare succeeded on a file that does not exist")
	}
}

// writePatched copies a fixture with a solid black rectangle painted over it.
// Building the altered photo here rather than committing one keeps the test
// self-describing: the rectangle the assertions look for is written three lines
// above them.
func writePatched(t *testing.T, src, dst string, block image.Rectangle) {
	t.Helper()
	rgba := loadRGBA(t, src)
	for y := block.Min.Y; y < block.Max.Y; y++ {
		for x := block.Min.X; x < block.Max.X; x++ {
			i := rgba.PixOffset(x, y)
			rgba.Pix[i], rgba.Pix[i+1], rgba.Pix[i+2], rgba.Pix[i+3] = 0, 0, 0, 255
		}
	}
	encodeJPEG(t, dst, rgba)
}

// writeCrop saves a rectangular crop of a fixture, to get a genuinely different
// aspect ratio out of a real photograph.
func writeCrop(t *testing.T, src, dst string, r image.Rectangle) {
	t.Helper()
	full := loadRGBA(t, src)
	out := image.NewRGBA(image.Rect(0, 0, r.Dx(), r.Dy()))
	for y := range r.Dy() {
		for x := range r.Dx() {
			out.Set(x, y, full.At(r.Min.X+x, r.Min.Y+y))
		}
	}
	encodeJPEG(t, dst, out)
}

func loadRGBA(t *testing.T, path string) *image.RGBA {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return toRGBA(img)
}

func encodeJPEG(t *testing.T, path string, img image.Image) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("create %s: %v", path, err)
	}
	defer f.Close()

	// Near-lossless, so the assertions measure the change the test made rather
	// than the encoder's own noise.
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 98}); err != nil {
		t.Fatalf("encode %s: %v", path, err)
	}
}
