package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"photocull/internal/hashing"
)

const testdataDir = "../../testdata"

func scanFixture(t *testing.T, dir string, opts Options) *Result {
	t.Helper()

	opts.Root = filepath.Join(testdataDir, dir)
	res, err := Scan(context.Background(), opts)
	if err != nil {
		t.Fatalf("Scan(%s): %v", dir, err)
	}
	return res
}

func basenames(res *Result) []string {
	names := make([]string, 0, len(res.Files))
	for _, f := range res.Files {
		names = append(names, filepath.Base(f.Path))
	}
	sort.Strings(names)
	return names
}

func TestScanWalksNestedDirectories(t *testing.T) {
	res := scanFixture(t, "exact", Options{})

	// original.jpg, its copy under backup/, and one unrelated photo.
	if len(res.Files) != 3 {
		t.Fatalf("scanned %d files, want 3: %v", len(res.Files), basenames(res))
	}
	if len(res.Errors) != 0 {
		t.Errorf("unexpected errors: %+v", res.Errors)
	}
}

func TestScanResultsAreSortedByPath(t *testing.T) {
	res := scanFixture(t, "similar", Options{})

	if !sort.SliceIsSorted(res.Files, func(i, j int) bool {
		return res.Files[i].Path < res.Files[j].Path
	}) {
		t.Error("results are not sorted by path; output would vary between runs")
	}
}

func TestScanHonoursExtensionFilter(t *testing.T) {
	res := scanFixture(t, "exact", Options{Extensions: []string{".png"}})

	if len(res.Files) != 0 {
		t.Errorf("scanning for .png found %d files: %v", len(res.Files), basenames(res))
	}
}

func TestScanExtensionFilterIsForgiving(t *testing.T) {
	// "JPG" without a dot and in the wrong case has to mean the same thing as
	// ".jpg", because that is what people type.
	res := scanFixture(t, "exact", Options{Extensions: []string{"JPG"}})

	if len(res.Files) != 3 {
		t.Errorf("scanning for \"JPG\" found %d files, want 3", len(res.Files))
	}
}

// TestScanHashCoversWholeFile guards the subtlest part of the scanner: the
// image decoder and the hasher share one pass over the file through an
// io.TeeReader, and the decoder usually stops before the end. If the remainder
// were not drained through the hasher, every digest would silently be the hash
// of a prefix — and duplicate detection would quietly break.
func TestScanHashCoversWholeFile(t *testing.T) {
	for _, computePHash := range []bool{false, true} {
		name := "header only"
		if computePHash {
			name = "full decode"
		}

		t.Run(name, func(t *testing.T) {
			res := scanFixture(t, "similar", Options{ComputePHash: computePHash})

			for _, f := range res.Files {
				want, err := hashing.SumFile(f.Path)
				if err != nil {
					t.Fatalf("SumFile(%s): %v", f.Path, err)
				}
				if f.SHA256 != want {
					t.Errorf("%s: scanner hashed %s, direct hash is %s",
						filepath.Base(f.Path), f.SHA256, want)
				}
			}
		})
	}
}

func TestScanReadsDimensionsWithoutFullDecode(t *testing.T) {
	res := scanFixture(t, "similar", Options{})

	for _, f := range res.Files {
		if !f.Decoded {
			t.Errorf("%s: not decoded", filepath.Base(f.Path))
			continue
		}
		if f.Width == 0 || f.Height == 0 {
			t.Errorf("%s: dimensions %dx%d", filepath.Base(f.Path), f.Width, f.Height)
		}
		if f.HasPHash {
			t.Errorf("%s: computed a perceptual hash that was never requested", filepath.Base(f.Path))
		}
	}
}

func TestScanComputesPerceptualHashOnRequest(t *testing.T) {
	res := scanFixture(t, "similar", Options{ComputePHash: true})

	for _, f := range res.Files {
		if !f.HasPHash {
			t.Errorf("%s: no perceptual hash", filepath.Base(f.Path))
		}
		if f.Pixels() != int64(f.Width)*int64(f.Height) {
			t.Errorf("%s: Pixels() disagrees with %dx%d", filepath.Base(f.Path), f.Width, f.Height)
		}
	}
}

// TestScanSurvivesCorruptFile covers the case that actually happens on a drive
// full of old backups: a truncated photo must be reported, not fatal, and must
// still be hashed so it can take part in exact deduplication.
func TestScanSurvivesCorruptFile(t *testing.T) {
	res := scanFixture(t, "corrupt", Options{ComputePHash: true})

	if len(res.Files) != 1 {
		t.Fatalf("scanned %d files, want 1", len(res.Files))
	}

	f := res.Files[0]
	if f.SHA256 == "" {
		t.Error("a corrupt file was left without a content hash, so it cannot be deduplicated at all")
	}
	if f.HasPHash {
		t.Error("a truncated image somehow produced a perceptual hash")
	}
	if got := res.DecodeErrors(); got != 1 {
		t.Errorf("DecodeErrors = %d, want 1", got)
	}
	if got := res.ReadErrors(); got != 0 {
		t.Errorf("ReadErrors = %d, want 0: the file was readable, only its pixels were not", got)
	}
}

func TestScanSkipsHiddenDirectories(t *testing.T) {
	root := t.TempDir()

	visible := filepath.Join(root, "photo.jpg")
	copyFixture(t, filepath.Join(testdataDir, "exact", "original.jpg"), visible)

	hidden := filepath.Join(root, ".thumbnails")
	if err := os.MkdirAll(hidden, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	copyFixture(t, filepath.Join(testdataDir, "exact", "original.jpg"), filepath.Join(hidden, "cached.jpg"))

	res, err := Scan(context.Background(), Options{Root: root})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	if len(res.Files) != 1 {
		t.Fatalf("scanned %d files, want 1 (the cache directory should be skipped): %v",
			len(res.Files), basenames(res))
	}
	if res.Files[0].Path != visible {
		t.Errorf("scanned %s, want %s", res.Files[0].Path, visible)
	}
}

func TestScanTotalBytes(t *testing.T) {
	res := scanFixture(t, "exact", Options{})

	var want int64
	for _, f := range res.Files {
		info, err := os.Stat(f.Path)
		if err != nil {
			t.Fatalf("Stat: %v", err)
		}
		want += info.Size()
	}

	if got := res.TotalBytes(); got != want {
		t.Errorf("TotalBytes = %d, want %d", got, want)
	}
}

func TestScanRejectsBadRoots(t *testing.T) {
	tests := []struct {
		name string
		root string
	}{
		{"empty path", ""},
		{"missing directory", filepath.Join(testdataDir, "no-such-directory")},
		{"a file, not a directory", filepath.Join(testdataDir, "exact", "original.jpg")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Scan(context.Background(), Options{Root: tt.root}); err == nil {
				t.Errorf("Scan(%q) returned no error", tt.root)
			}
		})
	}
}

// TestScanRespectsCancellation matters because a scan of a real drive takes
// long enough that Ctrl+C is a normal way to end it.
func TestScanRespectsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := Scan(ctx, Options{Root: filepath.Join(testdataDir, "similar")}); err == nil {
		t.Error("Scan with a cancelled context returned no error")
	}
}

func TestScanSingleWorker(t *testing.T) {
	// Forcing one worker exercises the pipeline without concurrency, which is
	// the useful baseline when a concurrency bug is suspected.
	res := scanFixture(t, "exact", Options{Workers: 1})

	if len(res.Files) != 3 {
		t.Errorf("scanned %d files with one worker, want 3", len(res.Files))
	}
}

func copyFixture(t *testing.T, src, dst string) {
	t.Helper()

	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("WriteFile(%s): %v", dst, err)
	}
}
