package pipeline

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeImage copies one of the committed fixtures to dst, creating parents.
func writeImage(t *testing.T, srcFixture, dst string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../testdata", srcFixture))
	if err != nil {
		t.Fatalf("read fixture %s: %v", srcFixture, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}

func TestMergeFindsOnlyNewPhotos(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()

	// Library already has "original".
	writeImage(t, "exact/original.jpg", filepath.Join(base, "in_library.jpg"))

	// Source has a byte-identical copy of that (a duplicate) and a genuinely
	// different photo (new).
	writeImage(t, "exact/original.jpg", filepath.Join(source, "copy_of_library.jpg"))
	writeImage(t, "exact/unrelated.jpg", filepath.Join(source, "brand_new.jpg"))

	res, err := Merge(context.Background(), MergeOptions{Base: base, Source: source})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if len(res.New) != 1 {
		t.Fatalf("New = %d files, want 1: %+v", len(res.New), res.New)
	}
	if filepath.Base(res.New[0].Path) != "brand_new.jpg" {
		t.Errorf("new file = %q, want brand_new.jpg", res.New[0].Path)
	}
	if res.Duplicates != 1 {
		t.Errorf("Duplicates = %d, want 1", res.Duplicates)
	}
}

func TestMergeSkipsSimilarWhenAsked(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()

	writeImage(t, "similar/source.jpg", filepath.Join(base, "have.jpg"))
	// A resized version of a photo already in the library: new by content,
	// but the same photo — so with similarity on it should be skipped.
	writeImage(t, "similar/source_resized.jpg", filepath.Join(source, "resized.jpg"))
	writeImage(t, "similar/different.jpg", filepath.Join(source, "different.jpg"))

	withSimilar, err := Merge(context.Background(), MergeOptions{Base: base, Source: source, Similar: true, Threshold: 8})
	if err != nil {
		t.Fatal(err)
	}
	if len(withSimilar.New) != 1 || filepath.Base(withSimilar.New[0].Path) != "different.jpg" {
		t.Errorf("with similarity, New = %+v, want only different.jpg", withSimilar.New)
	}

	// Without similarity, the resized copy is byte-different, so it counts as new.
	exactOnly, err := Merge(context.Background(), MergeOptions{Base: base, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(exactOnly.New) != 2 {
		t.Errorf("without similarity, New = %d, want 2 (resized counts as new)", len(exactOnly.New))
	}
}

func TestMergeDeduplicatesWithinSource(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()

	writeImage(t, "exact/original.jpg", filepath.Join(base, "have.jpg"))
	// Two identical new photos in the source: only one should be offered.
	writeImage(t, "exact/unrelated.jpg", filepath.Join(source, "new_a.jpg"))
	writeImage(t, "exact/unrelated.jpg", filepath.Join(source, "sub", "new_b.jpg"))

	res, err := Merge(context.Background(), MergeOptions{Base: base, Source: source})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.New) != 1 {
		t.Errorf("New = %d, want 1 (source-internal duplicate collapsed)", len(res.New))
	}
}

// TestMergeCollapsesSimilarWithinSource covers the half of the similarity
// check that grows as the import runs.
//
// The library's fingerprints are all known before the loop starts; the ones
// already accepted from the source are not, so that index is added to while it
// is being searched. It is the only place in photocull where those two happen
// together, and getting it wrong imports the same photo twice — which is
// exactly what the user asked the tool to prevent.
func TestMergeCollapsesSimilarWithinSource(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()

	writeImage(t, "similar/different.jpg", filepath.Join(base, "have.jpg"))
	// The same photo twice in the source, one of them resized. Different bytes,
	// so the content hash cannot collapse them and only the fingerprint can.
	writeImage(t, "similar/source.jpg", filepath.Join(source, "a.jpg"))
	writeImage(t, "similar/source_resized.jpg", filepath.Join(source, "sub", "b.jpg"))

	res, err := Merge(context.Background(), MergeOptions{
		Base: base, Source: source, Similar: true, Threshold: 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.New) != 1 {
		t.Errorf("New = %d, want 1: the resized copy should not be imported too", len(res.New))
	}
	if res.Duplicates != 1 {
		t.Errorf("Duplicates = %d, want 1", res.Duplicates)
	}
}

// TestMergeInDocumentsModeReadsEveryFile is the merge half of the kind seam.
// Before it, Merge called scanner.Scan with no extractor at all, so "add to
// library" silently stayed on photographs however the request was phrased: a
// folder of documents compared against a library came back with nothing in it
// and no explanation.
func TestMergeInDocumentsModeReadsEveryFile(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()
	docs := filepath.Join("..", "..", "testdata", "docs", "text")

	copyInto(t, filepath.Join(docs, "report.txt"), filepath.Join(base, "report.txt"))
	// The same prose with a few words changed: different bytes, same meaning,
	// so only a text fingerprint can tell it is already there.
	copyInto(t, filepath.Join(docs, "report_edited.txt"), filepath.Join(source, "report_v2.txt"))
	copyInto(t, filepath.Join(docs, "unrelated.txt"), filepath.Join(source, "planting.txt"))

	res, err := Merge(context.Background(), MergeOptions{
		Base: base, Source: source, Kind: "docs", Similar: true,
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}

	if res.Kind != "docs" {
		t.Errorf("Kind = %q, want docs", res.Kind)
	}
	if res.BaseFiles != 1 || res.SourceFiles != 2 {
		t.Errorf("counted %d base and %d source files, want 1 and 2", res.BaseFiles, res.SourceFiles)
	}
	if len(res.New) != 1 || filepath.Base(res.New[0].Path) != "planting.txt" {
		t.Fatalf("New = %+v, want only planting.txt: the edited report is the "+
			"document the library already has", res.New)
	}
}

// TestMergeNeverGuessesFromNamesAlone protects the decision recorded on Merge
// itself. The related tier exists for exactly the pair below, and it must not
// reach here: a guess answering "you already have this" means a genuinely new
// document is never imported while the user is told the import was complete.
func TestMergeNeverGuessesFromNamesAlone(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()

	// Two files nothing can read inside, with the same stem and near-identical
	// sizes — the textbook related pair.
	opaque := strings.Repeat("\x00\x01\x02\x03", 8192)
	write(t, filepath.Join(base, "informe.bin"), opaque)
	write(t, filepath.Join(source, "informe (1).bin"), opaque+"tail")

	res, err := Merge(context.Background(), MergeOptions{
		Base: base, Source: source, Kind: "docs", Similar: true,
	})
	if err != nil {
		t.Fatalf("Merge: %v", err)
	}
	if len(res.New) != 1 {
		t.Errorf("New = %+v, want the source file: it is not byte-identical to "+
			"anything in the library, and a matching name is not evidence enough "+
			"to drop a file the user would never see again", res.New)
	}
}

func TestMergeRejectsSameFolder(t *testing.T) {
	dir := t.TempDir()
	if _, err := Merge(context.Background(), MergeOptions{Base: dir, Source: dir}); err == nil {
		t.Error("merging a folder with itself should error")
	}
}

func TestCopyIntoAddsToLibrary(t *testing.T) {
	base := t.TempDir()
	source := t.TempDir()

	one := filepath.Join(source, "a.jpg")
	two := filepath.Join(source, "b.jpg")
	writeImage(t, "exact/original.jpg", one)
	writeImage(t, "exact/unrelated.jpg", two)

	copied, failed, err := CopyInto(base, []string{one, two})
	if err != nil {
		t.Fatalf("CopyInto: %v", err)
	}
	if copied != 2 || len(failed) != 0 {
		t.Fatalf("copied %d, failed %v; want 2 and none", copied, failed)
	}

	dest := filepath.Join(base, ImportSubdir)
	entries, _ := os.ReadDir(dest)
	if len(entries) != 2 {
		t.Errorf("import folder has %d files, want 2", len(entries))
	}
	// Originals must be untouched.
	if _, err := os.Stat(one); err != nil {
		t.Errorf("source original went missing: %v", err)
	}
}

func TestCopyIntoAvoidsNameCollisions(t *testing.T) {
	base := t.TempDir()
	srcA := t.TempDir()
	srcB := t.TempDir()

	// Same file name from two different folders.
	a := filepath.Join(srcA, "IMG_1.jpg")
	b := filepath.Join(srcB, "IMG_1.jpg")
	writeImage(t, "exact/original.jpg", a)
	writeImage(t, "exact/unrelated.jpg", b)

	if _, _, err := CopyInto(base, []string{a, b}); err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(filepath.Join(base, ImportSubdir))
	if len(entries) != 2 {
		t.Errorf("expected 2 files after collision-safe copy, got %d", len(entries))
	}
}
