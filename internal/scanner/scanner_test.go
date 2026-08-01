package scanner

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
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
	for _, deepScan := range []bool{false, true} {
		name := "header only"
		if deepScan {
			name = "full decode"
		}

		t.Run(name, func(t *testing.T) {
			res := scanFixture(t, "similar", Options{DeepScan: deepScan})

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
		if f.HasFingerprint {
			t.Errorf("%s: computed a perceptual hash that was never requested", filepath.Base(f.Path))
		}
	}
}

func TestScanComputesPerceptualHashOnRequest(t *testing.T) {
	res := scanFixture(t, "similar", Options{DeepScan: true})

	for _, f := range res.Files {
		if !f.HasFingerprint {
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
	res := scanFixture(t, "corrupt", Options{DeepScan: true})

	if len(res.Files) != 1 {
		t.Fatalf("scanned %d files, want 1", len(res.Files))
	}

	f := res.Files[0]
	if f.SHA256 == "" {
		t.Error("a corrupt file was left without a content hash, so it cannot be deduplicated at all")
	}
	if f.HasFingerprint {
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

// TestBadRootErrorIsReadable pins the wording, because these two errors are not
// log lines: the launcher prints them straight under the folder box, and a
// mistyped path is the commonest thing to get wrong in this application.
//
// The specific trap is Windows. %q escapes every backslash, so a path the user
// typed as C:\Photos came back as "C:\\Photos" and they were left comparing it
// against what they wrote, wondering whether the doubling was the problem.
func TestBadRootErrorIsReadable(t *testing.T) {
	missing := filepath.Join(testdataDir, "no-such-directory")

	_, err := Scan(context.Background(), Options{Root: missing})
	if err == nil {
		t.Fatal("scanning a missing directory returned no error")
	}
	msg := err.Error()

	if !strings.Contains(msg, missing) {
		t.Errorf("error does not name the path the user gave:\n  %s", msg)
	}
	if strings.Contains(msg, `\\`) {
		t.Errorf("error double-escapes the path, which is what %%q does to a Windows path:\n  %s", msg)
	}
	// The wrapped OS error names a Win32 entry point, which means nothing to
	// somebody who mistyped a folder.
	if strings.Contains(msg, "GetFileAttributesEx") {
		t.Errorf("error leaks the OS call rather than saying the folder is not there:\n  %s", msg)
	}

	// A file where a folder was expected is a different mistake and gets a
	// different sentence, otherwise "there is no folder at ..." would be a lie
	// about a path that plainly exists.
	file := filepath.Join(testdataDir, "exact", "original.jpg")
	_, err = Scan(context.Background(), Options{Root: file})
	if err == nil {
		t.Fatal("scanning a file returned no error")
	}
	if !strings.Contains(err.Error(), "not a folder") {
		t.Errorf("pointing at a file should say so, got:\n  %s", err)
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
