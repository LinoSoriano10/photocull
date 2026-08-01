package fingerprint

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const testdataDir = "../../testdata"

// fingerprintFixture runs the photo extractor over a committed image.
func fingerprintFixture(t *testing.T, deep bool, parts ...string) Result {
	t.Helper()
	path := filepath.Join(append([]string{testdataDir}, parts...)...)
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	return Default().Fingerprint(f, Input{Path: path, Deep: deep})
}

func TestLookupResolvesKnownKinds(t *testing.T) {
	for _, name := range []string{"photos", "PHOTOS", "  photos  "} {
		ex, err := Lookup(name)
		if err != nil {
			t.Errorf("Lookup(%q): %v", name, err)
			continue
		}
		if ex.Kind() != Photos {
			t.Errorf("Lookup(%q) gave kind %q, want %q", name, ex.Kind(), Photos)
		}
	}
}

// TestLookupRejectsUnknownKindHelpfully checks the error names the alternatives.
// A bare "unknown kind" leaves the user guessing at a flag value.
func TestLookupRejectsUnknownKindHelpfully(t *testing.T) {
	_, err := Lookup("videos")
	if err == nil {
		t.Fatal("Lookup accepted a kind that does not exist")
	}
	for _, kind := range Kinds() {
		if !strings.Contains(err.Error(), kind) {
			t.Errorf("error %q does not mention the available kind %q", err, kind)
		}
	}
}

func TestKindsAreSorted(t *testing.T) {
	kinds := Kinds()
	if !slices.IsSorted(kinds) {
		t.Errorf("Kinds() = %v, want sorted so help text is stable", kinds)
	}
	if !slices.Contains(kinds, string(Photos)) {
		t.Errorf("Kinds() = %v, missing %q", kinds, Photos)
	}
}

// TestShallowScanReadsDimensionsButNoFingerprint pins the split that keeps an
// exact-duplicates scan fast: dimensions decide which copy to keep and come
// almost free from the header, while the perceptual hash needs the whole image
// and is only worth paying for when similarity matching is actually wanted.
func TestShallowScanReadsDimensionsButNoFingerprint(t *testing.T) {
	got := fingerprintFixture(t, false, "similar", "source.jpg")

	if got.Err != nil {
		t.Fatalf("Err = %v, want none", got.Err)
	}
	if !got.Understood {
		t.Error("Understood = false for a perfectly good JPEG")
	}
	if got.Width != 640 || got.Height != 480 {
		t.Errorf("dimensions %dx%d, want 640x480", got.Width, got.Height)
	}
	if got.HasFingerprint {
		t.Error("a shallow scan produced a fingerprint; it should not have decoded the whole image")
	}
}

func TestDeepScanProducesAFingerprint(t *testing.T) {
	got := fingerprintFixture(t, true, "similar", "source.jpg")

	if got.Err != nil {
		t.Fatalf("Err = %v, want none", got.Err)
	}
	if !got.HasFingerprint {
		t.Fatal("a deep scan produced no fingerprint, so nothing can be matched as similar")
	}
	if got.Width != 640 || got.Height != 480 {
		t.Errorf("dimensions %dx%d, want 640x480", got.Width, got.Height)
	}
}

// TestFingerprintIsStableAcrossReads guards the property dedupe depends on:
// the same bytes must always give the same number, or a re-scan would produce
// different groups from the same folder.
func TestFingerprintIsStableAcrossReads(t *testing.T) {
	first := fingerprintFixture(t, true, "similar", "source.jpg")
	second := fingerprintFixture(t, true, "similar", "source.jpg")

	if first.Fingerprint != second.Fingerprint {
		t.Errorf("fingerprints differ between reads: %#x then %#x", first.Fingerprint, second.Fingerprint)
	}
}

// TestUnreadableContentIsReportedNotFatal checks the contract that keeps one bad
// file from ending a scan of ten thousand.
func TestUnreadableContentIsReportedNotFatal(t *testing.T) {
	got := Default().Fingerprint(bytes.NewReader([]byte("this is not an image at all")), Input{Path: "junk.jpg", Deep: true})

	if got.Err == nil {
		t.Error("Err = nil for content that is not an image; the user would never learn the file was skipped")
	}
	if got.Understood {
		t.Error("Understood = true for content that could not be decoded")
	}
	if got.HasFingerprint {
		t.Error("HasFingerprint = true for content that could not be decoded")
	}
}

// TestPhotoExtensionsCoverTheFormatsWeDecode guards against a decoder being
// registered while the walker still skips the files that need it.
func TestPhotoExtensionsCoverTheFormatsWeDecode(t *testing.T) {
	set := NormaliseExtensions(Default().Extensions())
	for _, ext := range []string{".jpg", ".jpeg", ".png", ".gif", ".heic", ".heif"} {
		if !set[ext] {
			t.Errorf("photos do not accept %q, so those files are never even opened", ext)
		}
	}
}
