package fingerprint

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"photocull/internal/hashing"
)

// docFingerprint runs the document extractor over a path.
func docFingerprint(t *testing.T, path string, deep bool) Result {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	ex, err := Lookup("docs")
	if err != nil {
		t.Fatalf("Lookup(docs): %v", err)
	}
	return ex.Fingerprint(f, Input{Path: path, Deep: deep})
}

func docPath(parts ...string) string {
	return filepath.Join(append([]string{testdataDir, "docs"}, parts...)...)
}

// TestDocumentsLookAtEveryFile is the no-blind-spots promise, expressed where
// it is decided.
func TestDocumentsLookAtEveryFile(t *testing.T) {
	ex, err := Lookup("docs")
	if err != nil {
		t.Fatal(err)
	}
	if exts := ex.Extensions(); exts != nil {
		t.Errorf("Extensions() = %v, want nil: a .zip or a legacy .doc still has to be compared by content hash", exts)
	}
}

func TestDocumentFingerprintOfProse(t *testing.T) {
	got := docFingerprint(t, docPath("text", "report.txt"), true)

	if got.Err != nil {
		t.Fatalf("Err = %v, want none", got.Err)
	}
	if !got.Understood {
		t.Error("Understood = false for a page of ordinary prose")
	}
	if !got.HasFingerprint {
		t.Fatal("no fingerprint, so nothing could ever be matched as a revision of this document")
	}
}

// TestDocumentRevisionsLandWithinTheThreshold is the whole feature in one
// assertion: two versions of a document must be close, and a different document
// must be far.
func TestDocumentRevisionsLandWithinTheThreshold(t *testing.T) {
	original := docFingerprint(t, docPath("text", "report.txt"), true)
	edited := docFingerprint(t, docPath("text", "report_edited.txt"), true)
	other := docFingerprint(t, docPath("text", "unrelated.txt"), true)

	if d := hashing.Distance(original.Fingerprint, edited.Fingerprint); d > hashing.DefaultTextThreshold {
		t.Errorf("a revision measured %d away, past the %d threshold: two drafts of one document would not be offered as duplicates", d, hashing.DefaultTextThreshold)
	}
	if d := hashing.Distance(original.Fingerprint, other.Fingerprint); d <= hashing.DefaultTextThreshold {
		t.Errorf("an unrelated document measured %d away, inside the %d threshold: it would be offered for deletion", d, hashing.DefaultTextThreshold)
	}
}

// TestUnreadableFileIsNotAnError is the normal path for most of a disk. A .zip
// yields nothing, and saying so is not a failure — the file still gets a
// content hash and is still deduplicated on it.
func TestUnreadableFileIsNotAnError(t *testing.T) {
	got := docFingerprint(t, docPath("binary", "archive.zip"), true)

	if got.Err != nil {
		t.Errorf("Err = %v; a .zip photocull cannot read inside is ordinary, not broken", got.Err)
	}
	if got.Understood || got.HasFingerprint {
		t.Error("a .zip was reported as understood; nothing was read out of it")
	}
}

// TestMalformedDocumentIsReported is the other side: a file claiming a format
// it is not should be surfaced, because the user may want to know their .docx
// is damaged.
func TestMalformedDocumentIsReported(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.docx")
	if err := os.WriteFile(path, []byte(strings.Repeat("not a zip ", 100)), 0o644); err != nil {
		t.Fatal(err)
	}

	got := docFingerprint(t, path, true)
	if got.Err == nil {
		t.Error("Err = nil for a .docx that is not a ZIP; the user would never learn the file is damaged")
	}
	if got.HasFingerprint {
		t.Error("a malformed document produced a fingerprint")
	}
}

func TestShallowDocumentScanSkipsTheFingerprint(t *testing.T) {
	got := docFingerprint(t, docPath("text", "report.txt"), false)

	if !got.Understood {
		t.Error("Understood = false for readable prose")
	}
	if got.HasFingerprint {
		t.Error("a shallow scan produced a fingerprint; exact mode does not need one")
	}
}

func TestDefaultsDifferByKind(t *testing.T) {
	photos := DefaultsFor(Photos)
	docs := DefaultsFor(Docs)

	if photos.Threshold != hashing.DefaultPhotoThreshold {
		t.Errorf("photo threshold = %d, want %d", photos.Threshold, hashing.DefaultPhotoThreshold)
	}
	if docs.Threshold != hashing.DefaultTextThreshold {
		t.Errorf("document threshold = %d, want %d", docs.Threshold, hashing.DefaultTextThreshold)
	}
	if photos.Related {
		t.Error("the related pass is on for photos; a photo that will not decode is rare and usually just broken")
	}
	if !docs.Related {
		t.Error("the related pass is off for documents, where unreadable files are ordinary and their names are the only signal left")
	}
	if photos.Strategy == docs.Strategy {
		t.Errorf("both kinds use the %q keep strategy; the photo rule keeps the oldest, which for a document is the superseded draft", photos.Strategy)
	}
}

// TestDefaultsForUnknownKindIsConservative checks the fallback does not enable
// speculative matching for something nobody has thought about.
func TestDefaultsForUnknownKindIsConservative(t *testing.T) {
	if DefaultsFor(Kind("videos")).Related {
		t.Error("an unknown kind got the related pass enabled")
	}
}
