package pipeline

import (
	"context"
	"os"
	"path/filepath"
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
