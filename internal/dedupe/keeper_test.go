package dedupe

import (
	"testing"
	"time"

	"photocull/internal/scanner"
)

func meta(path string, pixels int64, size int64, mod time.Time) scanner.FileMeta {
	// Encode a pixel count as a square-ish resolution; the exact dimensions do
	// not matter to the strategies, only Pixels().
	return scanner.FileMeta{
		Path:    path,
		Width:   int(pixels),
		Height:  1,
		Size:    size,
		ModTime: mod,
		Decoded: true,
	}
}

func TestKeepHighestResolution(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	files := []scanner.FileMeta{
		meta("small.jpg", 100, 1000, old),
		meta("large.jpg", 900, 500, old),
		meta("medium.jpg", 400, 800, old),
	}

	if got := KeepHighestResolution(files); files[got].Path != "large.jpg" {
		t.Errorf("kept %q, want large.jpg", files[got].Path)
	}
}

func TestKeepOldest(t *testing.T) {
	files := []scanner.FileMeta{
		meta("newer.jpg", 100, 1000, time.Date(2023, 6, 1, 0, 0, 0, 0, time.UTC)),
		meta("oldest.jpg", 100, 1000, time.Date(2019, 1, 1, 0, 0, 0, 0, time.UTC)),
		meta("newest.jpg", 100, 1000, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)),
	}

	if got := KeepOldest(files); files[got].Path != "oldest.jpg" {
		t.Errorf("kept %q, want oldest.jpg", files[got].Path)
	}
}

func TestKeepLargest(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	files := []scanner.FileMeta{
		meta("a.jpg", 100, 500, old),
		meta("b.jpg", 100, 9000, old),
		meta("c.jpg", 100, 3000, old),
	}

	if got := KeepLargest(files); files[got].Path != "b.jpg" {
		t.Errorf("kept %q, want b.jpg", files[got].Path)
	}
}

// TestDefaultKeepStrategyPrefersResolution checks the primary rule.
func TestDefaultKeepStrategyPrefersResolution(t *testing.T) {
	old := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	files := []scanner.FileMeta{
		// Older and smaller file, but far higher resolution: resolution wins.
		meta("hi_res.jpg", 4000, 500, newer),
		meta("lo_res.jpg", 100, 9000, old),
	}

	if got := DefaultKeepStrategy(files); files[got].Path != "hi_res.jpg" {
		t.Errorf("kept %q, want hi_res.jpg (resolution should dominate)", files[got].Path)
	}
}

// TestDefaultKeepStrategyBreaksTiesByPathDepth is the case that matters most
// for byte-identical duplicates: every metric ties, so the file at the
// shallowest path wins. The original tends to live in the folder you actually
// organised, copies pile up under nested backup directories.
func TestDefaultKeepStrategyBreaksTiesByPathDepth(t *testing.T) {
	same := time.Date(2021, 5, 5, 0, 0, 0, 0, time.UTC)
	files := []scanner.FileMeta{
		meta("backups/phone/old/photo.jpg", 500, 2000, same),
		meta("photo.jpg", 500, 2000, same),
		meta("backups/photo.jpg", 500, 2000, same),
	}

	if got := DefaultKeepStrategy(files); files[got].Path != "photo.jpg" {
		t.Errorf("kept %q, want photo.jpg (shallowest path)", files[got].Path)
	}
}

// TestDefaultKeepStrategyIsDeterministic makes sure the suggestion never
// depends on the order files arrive in — the same group must always suggest
// keeping the same file.
func TestDefaultKeepStrategyIsDeterministic(t *testing.T) {
	same := time.Date(2021, 5, 5, 0, 0, 0, 0, time.UTC)
	forwards := []scanner.FileMeta{
		meta("a/photo.jpg", 500, 2000, same),
		meta("b/photo.jpg", 500, 2000, same),
	}
	backwards := []scanner.FileMeta{
		meta("b/photo.jpg", 500, 2000, same),
		meta("a/photo.jpg", 500, 2000, same),
	}

	if forwards[DefaultKeepStrategy(forwards)].Path != backwards[DefaultKeepStrategy(backwards)].Path {
		t.Error("keep suggestion depends on input order")
	}
}

func TestLookupStrategy(t *testing.T) {
	for _, name := range StrategyNames() {
		if _, err := LookupStrategy(name); err != nil {
			t.Errorf("LookupStrategy(%q) failed: %v", name, err)
		}
	}
	// Case and surrounding space should not matter.
	if _, err := LookupStrategy("  Default "); err != nil {
		t.Errorf("LookupStrategy should be forgiving of case and spaces: %v", err)
	}
	if _, err := LookupStrategy("nonsense"); err == nil {
		t.Error("LookupStrategy accepted an unknown strategy")
	}
}

func TestKeepStrategyHandlesEmptyInput(t *testing.T) {
	// A strategy must not panic on an empty slice; grouping never calls it that
	// way, but a public function should not crash on a degenerate input.
	if got := DefaultKeepStrategy(nil); got != 0 {
		t.Errorf("DefaultKeepStrategy(nil) = %d, want 0", got)
	}
}

// TestDocumentKeepStrategyPrefersNewest pins the semantic inversion, which is
// the whole reason documents need their own strategy.
//
// A photograph's oldest copy is the original and every later one has lost
// pixels. A document's newest copy is the revision the person actually worked
// on. Applying the photo default to documents would confidently suggest keeping
// the draft and recycling the final version.
func TestDocumentKeepStrategyPrefersNewest(t *testing.T) {
	old := file("draft.docx", "h1")
	old.ModTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	old.Size = 9000 // and bigger, so size cannot be what decides it

	recent := file("final.docx", "h2")
	recent.ModTime = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	recent.Size = 4000

	files := []scanner.FileMeta{old, recent}

	if got := DocumentKeepStrategy(files); got != 1 {
		t.Errorf("DocumentKeepStrategy kept %q; it should keep the newest revision, not the superseded draft", files[got].Path)
	}
	// And the contrast that makes the point: the photo default does the opposite.
	if got := DefaultKeepStrategy(files); got != 0 {
		t.Errorf("DefaultKeepStrategy kept %q; the photo rule is supposed to prefer the oldest, which is what makes it wrong for documents", files[got].Path)
	}
}

func TestKeepNewestIsRegistered(t *testing.T) {
	for _, name := range []string{"newest", "document"} {
		if _, err := LookupStrategy(name); err != nil {
			t.Errorf("LookupStrategy(%q): %v", name, err)
		}
	}
}

// TestDocumentKeepStrategyIgnoresMtimeForIdenticalCopies is the counterpart to
// the test above, and the case that actually bites on a real disk.
//
// Two byte-identical files are not two revisions; one is a copy of the other,
// and the copy is usually made later. Preferring the newest there would keep
// the backup and recycle the original, which is precisely backwards.
func TestDocumentKeepStrategyIgnoresMtimeForIdenticalCopies(t *testing.T) {
	original := file("notes.txt", "same-hash")
	original.ModTime = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	backup := file("backup/deep/notes.txt", "same-hash")
	backup.ModTime = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC) // copied later

	files := []scanner.FileMeta{original, backup}

	if got := DocumentKeepStrategy(files); got != 0 {
		t.Errorf("kept %q; for byte-identical files the later timestamp just means it was copied later, so the shallower path is the one to keep", files[got].Path)
	}
}
