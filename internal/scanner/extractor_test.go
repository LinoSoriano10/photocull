package scanner

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"testing"

	"photocull/internal/fingerprint"
	"photocull/internal/hashing"
)

// stubExtractor records what it was asked about and reads only a prefix of each
// file, which is what makes it useful: it stands in for a document extractor
// that stops once it has enough text.
type stubExtractor struct {
	prefixBytes int
	exts        []string

	mu   sync.Mutex
	seen map[string]int64 // path -> size reported to the extractor
}

func newStub(prefixBytes int, exts []string) *stubExtractor {
	return &stubExtractor{prefixBytes: prefixBytes, exts: exts, seen: map[string]int64{}}
}

func (s *stubExtractor) Kind() fingerprint.Kind { return fingerprint.Docs }
func (s *stubExtractor) Extensions() []string   { return s.exts }

func (s *stubExtractor) Fingerprint(r io.Reader, in fingerprint.Input) fingerprint.Result {
	buf := make([]byte, s.prefixBytes)
	n, _ := io.ReadFull(r, buf)

	s.mu.Lock()
	s.seen[in.Path] = in.Size
	s.mu.Unlock()

	if !in.Deep {
		return fingerprint.Result{Understood: true}
	}
	// Any deterministic function of the prefix will do; the point is only that
	// it reaches FileMeta unchanged.
	var h uint64
	for _, b := range buf[:n] {
		h = h*31 + uint64(b)
	}
	return fingerprint.Result{Understood: true, Fingerprint: h, HasFingerprint: true}
}

func (s *stubExtractor) paths() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.seen))
	for p := range s.seen {
		out = append(out, filepath.Base(p))
	}
	sort.Strings(out)
	return out
}

// writeFile drops a file with known contents into dir.
func writeFile(t *testing.T, dir, name, body string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
	return path
}

// TestScanUsesTheExtractorItWasGiven is what the Extractor interface bought:
// the scanner can be driven over files that are not photographs at all, with no
// image decoder anywhere in the picture.
func TestScanUsesTheExtractorItWasGiven(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "notes.txt", "the quick brown fox jumps over the lazy dog")
	writeFile(t, dir, "archive.zip", "PK\x03\x04 not really a zip, just bytes")
	writeFile(t, dir, "song.mp3", "ID3 also not really an mp3")

	stub := newStub(8, nil) // nil extensions: every file
	res, err := Scan(t.Context(), Options{Root: dir, Extractor: stub, DeepScan: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}

	want := []string{"archive.zip", "notes.txt", "song.mp3"}
	if got := basenames(res); !equalStrings(got, want) {
		t.Errorf("scanned %v, want %v: a nil extension set must mean every file, or a documents scan has blind spots", got, want)
	}
	if got := stub.paths(); !equalStrings(got, want) {
		t.Errorf("the extractor was shown %v, want %v", got, want)
	}
	for _, f := range res.Files {
		if !f.HasFingerprint {
			t.Errorf("%s came back without a fingerprint, so the extractor's answer never reached FileMeta", filepath.Base(f.Path))
		}
	}
}

// TestScanHashesTheWholeFileWhateverTheExtractorReads is the invariant that
// makes a partial-reading extractor safe.
//
// An extractor is allowed to stop early — a document one will, once it has
// enough text. If the remainder were not drained through the hasher afterwards,
// SHA-256 would cover only the prefix, and two files that agree for their first
// few bytes and differ completely after that would be reported as byte-for-byte
// identical. That is the worst failure this tool can have: it deletes data.
func TestScanHashesTheWholeFileWhateverTheExtractorReads(t *testing.T) {
	dir := t.TempDir()
	// Same first 8 bytes, different after. The stub only ever reads 8.
	writeFile(t, dir, "a.bin", "PREFIX__aaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	writeFile(t, dir, "b.bin", "PREFIX__bbbbbbbbbbbbbbbbbbbbbbbbbbbb")

	res, err := Scan(t.Context(), Options{Root: dir, Extractor: newStub(8, nil), DeepScan: true})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if len(res.Files) != 2 {
		t.Fatalf("scanned %d files, want 2", len(res.Files))
	}

	for _, f := range res.Files {
		want, err := hashing.SumFile(f.Path)
		if err != nil {
			t.Fatalf("SumFile(%s): %v", f.Path, err)
		}
		if f.SHA256 != want {
			t.Errorf("%s hashed to %s, want %s: the file was hashed only as far as the extractor read, so two files differing past that point would be reported as identical", filepath.Base(f.Path), f.SHA256, want)
		}
	}
	if res.Files[0].SHA256 == res.Files[1].SHA256 {
		t.Error("two files sharing only their first 8 bytes got the same content hash")
	}
}

// TestScanExtensionsOverrideTheExtractor checks that an explicit --ext still
// wins, so a documents scan can be narrowed to just the formats worth parsing.
func TestScanExtensionsOverrideTheExtractor(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, dir, "keep.txt", "text")
	writeFile(t, dir, "skip.bin", "binary")

	res, err := Scan(t.Context(), Options{
		Root:       dir,
		Extractor:  newStub(8, nil), // the extractor would take everything
		Extensions: []string{"txt"}, // but the caller asked for one format
	})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := basenames(res); !equalStrings(got, []string{"keep.txt"}) {
		t.Errorf("scanned %v, want [keep.txt]: an explicit --ext must override the extractor's default set", got)
	}
}

// TestScanWithoutAnExtractorStillMeansPhotos guards every caller written before
// the seam existed.
func TestScanWithoutAnExtractorStillMeansPhotos(t *testing.T) {
	dir := t.TempDir()
	copyFixture(t, filepath.Join(testdataDir, "exact", "original.jpg"), filepath.Join(dir, "photo.jpg"))
	writeFile(t, dir, "notes.txt", "not a photo")

	res, err := Scan(t.Context(), Options{Root: dir})
	if err != nil {
		t.Fatalf("Scan: %v", err)
	}
	if got := basenames(res); !equalStrings(got, []string{"photo.jpg"}) {
		t.Errorf("scanned %v, want [photo.jpg]: a nil Extractor must still mean photos", got)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
