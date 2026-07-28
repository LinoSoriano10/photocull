package hashing

import (
	"image"
	"os"
	"path/filepath"
	"testing"

	_ "image/jpeg"
)

// decodeFixture loads one of the committed test images.
func decodeFixture(t *testing.T, parts ...string) image.Image {
	t.Helper()

	path := filepath.Join(append([]string{testdataDir}, parts...)...)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	return img
}

func perceptualOf(t *testing.T, parts ...string) uint64 {
	t.Helper()

	h, err := Perceptual(decodeFixture(t, parts...))
	if err != nil {
		t.Fatalf("Perceptual(%v): %v", parts, err)
	}
	return h
}

// TestPerceptualSurvivesResizeAndRecompression is the whole reason perceptual
// hashing is in this tool: the same photo, resized or re-compressed, has to
// still read as the same photo.
func TestPerceptualSurvivesResizeAndRecompression(t *testing.T) {
	// 8 of 64 bits is the threshold photocull ships with; these fixtures must
	// stay comfortably inside it.
	const threshold = 8

	source := perceptualOf(t, "similar", "source.jpg")

	tests := []struct {
		name  string
		parts []string
		alike bool
	}{
		{"resized to half", []string{"similar", "source_resized.jpg"}, true},
		{"re-compressed at low quality", []string{"similar", "source_recompressed.jpg"}, true},
		{"a genuinely different photo", []string{"similar", "different.jpg"}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			distance := Distance(source, perceptualOf(t, tt.parts...))

			if tt.alike && distance > threshold {
				t.Errorf("distance = %d, want <= %d: a copy of the same photo was not recognised", distance, threshold)
			}
			if !tt.alike && distance <= threshold {
				t.Errorf("distance = %d, want > %d: two different photos were treated as duplicates", distance, threshold)
			}
		})
	}
}

func TestPerceptualIsDeterministic(t *testing.T) {
	first := perceptualOf(t, "similar", "source.jpg")
	second := perceptualOf(t, "similar", "source.jpg")

	if first != second {
		t.Errorf("hashing the same image twice gave %#x then %#x", first, second)
	}
	if d := Distance(first, second); d != 0 {
		t.Errorf("Distance of a hash with itself = %d, want 0", d)
	}
}

func TestDistance(t *testing.T) {
	tests := []struct {
		name string
		a, b uint64
		want int
	}{
		{"identical", 0xDEADBEEF, 0xDEADBEEF, 0},
		{"one bit apart", 0b0000, 0b0001, 1},
		{"three bits apart", 0b0000, 0b1011, 3},
		{"every bit apart", 0, ^uint64(0), PerceptualBits},
		{"symmetric", 0xFF00FF00FF00FF00, 0x00FF00FF00FF00FF, PerceptualBits},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Distance(tt.a, tt.b); got != tt.want {
				t.Errorf("Distance(%#x, %#x) = %d, want %d", tt.a, tt.b, got, tt.want)
			}
			// Hamming distance must not care about argument order.
			if got := Distance(tt.b, tt.a); got != tt.want {
				t.Errorf("Distance is not symmetric: got %d, want %d", got, tt.want)
			}
		})
	}
}
