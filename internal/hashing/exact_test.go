package hashing

import (
	"path/filepath"
	"strings"
	"testing"
)

// testdataDir is the shared fixture directory, relative to this package.
const testdataDir = "../../testdata"

func TestSumReaderMatchesKnownDigest(t *testing.T) {
	// The SHA-256 of "hello world", so the test pins the algorithm rather than
	// just checking our own output against itself.
	const want = "b94d27b9934d3e08a52e52d7da7dabfac484efe37a5380ee9088f7ace2efcde9"

	got, err := SumReader(strings.NewReader("hello world"))
	if err != nil {
		t.Fatalf("SumReader: %v", err)
	}
	if got != want {
		t.Errorf("SumReader = %q, want %q", got, want)
	}
}

func TestSumFileIdenticalFilesShareDigest(t *testing.T) {
	original := filepath.Join(testdataDir, "exact", "original.jpg")
	copyOf := filepath.Join(testdataDir, "exact", "backup", "original.jpg")
	other := filepath.Join(testdataDir, "exact", "unrelated.jpg")

	originalSum, err := SumFile(original)
	if err != nil {
		t.Fatalf("SumFile(%s): %v", original, err)
	}
	copySum, err := SumFile(copyOf)
	if err != nil {
		t.Fatalf("SumFile(%s): %v", copyOf, err)
	}
	otherSum, err := SumFile(other)
	if err != nil {
		t.Fatalf("SumFile(%s): %v", other, err)
	}

	if originalSum != copySum {
		t.Errorf("byte-identical files hashed differently:\n  %s\n  %s", originalSum, copySum)
	}
	if originalSum == otherSum {
		t.Errorf("different files shared a hash: %s", originalSum)
	}
}

func TestSumFileMissingFile(t *testing.T) {
	if _, err := SumFile(filepath.Join(testdataDir, "does-not-exist.jpg")); err == nil {
		t.Error("SumFile on a missing file returned no error")
	}
}

func TestHexSumIsLowercaseHex(t *testing.T) {
	h := NewSHA256()
	sum := HexSum(h)

	if len(sum) != 64 {
		t.Errorf("digest length = %d, want 64", len(sum))
	}
	if sum != strings.ToLower(sum) {
		t.Errorf("digest %q is not lowercase", sum)
	}
}
