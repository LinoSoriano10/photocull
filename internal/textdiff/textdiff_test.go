package textdiff

import (
	"strings"
	"testing"
	"time"
)

// filler is prose to pad two documents apart with, so the collapsing logic has
// something to collapse.
func filler(n int) string {
	words := make([]string, n)
	for i := range words {
		words[i] = "padding"
	}
	return strings.Join(words, " ")
}

// TestWordDiffHighlightsOnlyTheChangedWord is the requirement in one test.
//
// The whole feature exists because nobody will read thirty pages to find out
// that an adjective moved. A line diff would report the paragraph, a character
// diff would cut the highlight mid-word, and either way the reader is back to
// comparing by eye.
func TestWordDiffHighlightsOnlyTheChangedWord(t *testing.T) {
	a := "The north bed was replanted in April with hellebores and epimedium, both of which prefer the dry shade."
	b := "The north bed was replanted in April with hellebores and epimedium, both of which prefer the deep shade."

	d := Words(a, b)

	if len(d.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1: two sentences differing in one word are one change: %+v", len(d.Hunks), d.Hunks)
	}
	h := d.Hunks[0]
	if h.Del != "dry" || h.Ins != "deep" {
		t.Errorf("del/ins = %q/%q, want dry/deep — anything longer means the whole "+
			"phrase was highlighted and the reader has to find the change again", h.Del, h.Ins)
	}
	if d.ChangedWords != 2 {
		t.Errorf("changedWords = %d, want 2 (one removed, one added)", d.ChangedWords)
	}
	if !strings.HasSuffix(h.Before, "prefer the") {
		t.Errorf("before = %q, want it to end with the words leading into the change", h.Before)
	}
	if h.After != "shade." {
		t.Errorf("after = %q, want the rest of the sentence", h.After)
	}
}

// TestWordDiffIgnoresLineBreaks is the property that makes a .docx comparable
// with its own PDF export. A PDF has no paragraphs, only glyphs at coordinates,
// so its text comes back hard-wrapped at a width the original never had. If
// that counted as a change, every line break in the document would be reported
// and the real edit would be lost among them.
func TestWordDiffIgnoresLineBreaks(t *testing.T) {
	flowing := "The soil there is thin and full of roots, so the holes were dug wider than deep."
	wrapped := "The soil there is thin and\nfull of roots, so the holes\nwere dug wider than deep."

	d := Words(flowing, wrapped)
	if !d.Identical() {
		t.Errorf("the same prose re-wrapped reported %d changed words and %d hunks; "+
			"a document and its own PDF export would show as a wall of layout "+
			"differences: %+v", d.ChangedWords, len(d.Hunks), d.Hunks)
	}
}

func TestWordDiffOnIdenticalTextHasNoHunks(t *testing.T) {
	text := "Watering was needed twice in the first fortnight and not at all afterwards."
	d := Words(text, text)

	if len(d.Hunks) != 0 {
		t.Errorf("identical text produced %d hunks, want none", len(d.Hunks))
	}
	if !d.Identical() {
		t.Errorf("Identical() = false for identical text (changedWords = %d)", d.ChangedWords)
	}
	if d.SameWords != 13 {
		t.Errorf("sameWords = %d, want 13", d.SameWords)
	}
}

// TestWordDiffCollapsesLongIdenticalRuns is the other half of the requirement:
// showing the change is only useful if everything else gets out of the way.
func TestWordDiffCollapsesLongIdenticalRuns(t *testing.T) {
	pad := filler(400)
	a := pad + " the estate holds twelve acres " + pad
	b := pad + " the estate holds fifteen acres " + pad

	d := Words(a, b)

	if len(d.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(d.Hunks), d.Hunks)
	}
	h := d.Hunks[0]

	// 400 padding words + "the estate holds" = 403 identical words before the
	// change, of which Context are kept as Before and the rest collapsed.
	if want := 403 - Context; h.SkippedWords != want {
		t.Errorf("skippedWords = %d, want %d", h.SkippedWords, want)
	}
	if got := len(strings.Fields(h.Before)); got != Context {
		t.Errorf("before holds %d words, want %d", got, Context)
	}
	if got := len(strings.Fields(h.After)); got != Context {
		t.Errorf("after holds %d words, want %d", got, Context)
	}
	if got := len(strings.Fields(h.Skipped)); got != h.SkippedWords {
		t.Errorf("skipped text holds %d words but skippedWords says %d; the page "+
			"expands one and labels it with the other", got, h.SkippedWords)
	}

	// "acres" plus 400 padding words trail the change, less the After context.
	if want := 401 - Context; d.TrailingWords != want {
		t.Errorf("trailingWords = %d, want %d", d.TrailingWords, want)
	}
}

// TestWordDiffMergesNearbyChanges keeps the view readable. Two edits a few
// words apart are one edit to the person reading; splitting them would print
// the same sentence twice with overlapping context.
func TestWordDiffMergesNearbyChanges(t *testing.T) {
	a := "the quick brown fox jumps over the lazy dog"
	b := "the slow brown wolf jumps over the lazy dog"

	d := Words(a, b)
	if len(d.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1: two changes three words apart belong "+
			"together: %+v", len(d.Hunks), d.Hunks)
	}
	if !strings.Contains(d.Hunks[0].Del, "quick") || !strings.Contains(d.Hunks[0].Del, "fox") {
		t.Errorf("del = %q, want both changed words", d.Hunks[0].Del)
	}
	if !strings.Contains(d.Hunks[0].Ins, "slow") || !strings.Contains(d.Hunks[0].Ins, "wolf") {
		t.Errorf("ins = %q, want both replacements", d.Hunks[0].Ins)
	}
}

// TestWordDiffSeparatesDistantChanges is the mirror image: edits at opposite
// ends of a document must not be glued into one hunk, or the collapsed run
// between them is never reported.
func TestWordDiffSeparatesDistantChanges(t *testing.T) {
	pad := filler(200)
	a := "alpha " + pad + " omega"
	b := "ALPHA " + pad + " OMEGA"

	d := Words(a, b)
	if len(d.Hunks) != 2 {
		t.Fatalf("got %d hunks, want 2: %+v", len(d.Hunks), d.Hunks)
	}
	if d.Hunks[1].SkippedWords == 0 {
		t.Error("the second hunk reports nothing skipped, so 200 identical words " +
			"between the two changes vanished without being accounted for")
	}
}

// TestWordDiffHandlesPureInsertion covers the shape where one side is empty: a
// paragraph added to a later draft, which is the commonest document edit there
// is.
func TestWordDiffHandlesPureInsertion(t *testing.T) {
	a := "The heap behind the shed is full."
	b := "The heap behind the shed is full. It was turned in March."

	d := Words(a, b)
	if len(d.Hunks) != 1 {
		t.Fatalf("got %d hunks, want 1: %+v", len(d.Hunks), d.Hunks)
	}
	if d.Hunks[0].Del != "" {
		t.Errorf("del = %q, want empty: nothing was removed", d.Hunks[0].Del)
	}
	if d.Hunks[0].Ins != "It was turned in March." {
		t.Errorf("ins = %q, want the added sentence", d.Hunks[0].Ins)
	}
}

func TestWordDiffHandlesEmptyDocuments(t *testing.T) {
	if d := Words("", ""); !d.Identical() || len(d.Hunks) != 0 {
		t.Errorf("two empty documents produced %+v", d)
	}
	d := Words("", "one two three")
	if len(d.Hunks) != 1 || d.Hunks[0].Ins != "one two three" {
		t.Errorf("empty against text produced %+v", d)
	}
}

// TestWordDiffTruncatesEnormousDocuments bounds the cost. The algorithm is
// cheap when two documents are alike and expensive when they are not, and
// "these two files are completely different" is a case a duplicate finder meets
// constantly.
func TestWordDiffTruncatesEnormousDocuments(t *testing.T) {
	huge := filler(MaxWords + 500)
	d := Words(huge, huge)

	if !d.Truncated {
		t.Error("a document past MaxWords was not marked truncated, so the page " +
			"would claim to have compared all of it")
	}
	if d.SameWords > MaxWords {
		t.Errorf("compared %d words, want at most %d", d.SameWords, MaxWords)
	}
}

// TestWordDiffCountsEveryWordItCompared checks the numbers behind the headline.
// "412 of 1,847 words differ" is often the whole answer, so the two figures
// have to add up to the document rather than to the visible hunks.
func TestWordDiffCountsEveryWordItCompared(t *testing.T) {
	pad := filler(50)
	a := pad + " one two three " + pad
	b := pad + " one four three " + pad

	d := Words(a, b)
	total := d.SameWords + d.ChangedWords
	// 100 padding + "one three" counted once each side of the change, plus the
	// changed pair. The point is only that nothing is lost or double-counted.
	if want := 100 + 2 + 2; total != want {
		t.Errorf("sameWords+changedWords = %d, want %d", total, want)
	}
	if d.ChangedWords != 2 {
		t.Errorf("changedWords = %d, want 2", d.ChangedWords)
	}
}

// TestWordDiffReportsWhenItRanOutOfTime.
//
// A timed-out comparison comes back valid but coarse: it tends to report one
// long change where a finished one would have found several small ones. On
// screen that is indistinguishable from a real answer, so a reader would
// conclude the two documents are further apart than they are — and might delete
// the wrong copy on the strength of it. The flag is what lets the page say so.
func TestWordDiffReportsWhenItRanOutOfTime(t *testing.T) {
	// Two documents with nothing in common are the expensive case: the
	// algorithm is cheap when texts are alike and costly when they are not.
	a := filler(4000)
	b := strings.Repeat("completely different words here ", 1000)

	if d := words(a, b, time.Nanosecond); !d.TimedOut {
		t.Error("a comparison given a nanosecond did not report timing out")
	}

	// And the ordinary case must not cry wolf: a flag that fires on healthy
	// input would train the user to ignore it.
	if d := words("one two three", "one two four", Timeout); d.TimedOut {
		t.Error("a trivial comparison was reported as having timed out")
	}
}
