package hashing

import (
	"strings"
	"testing"
)

// simhashOf fails the test rather than returning ok, so the assertions below
// stay about distances instead of about error handling.
func simhashOf(t *testing.T, text string) uint64 {
	t.Helper()
	h, ok := SimHash(text)
	if !ok {
		t.Fatalf("SimHash refused a %d-word document as too thin", len(words(text)))
	}
	return h
}

// TestSimHashSurvivesReflowAndRecasing is the property the whole documents mode
// rests on: the same words laid out differently must give the *same* number.
//
// This is not a nicety. The most common document duplicate anyone has is a
// .docx and the .pdf it was exported to, and those two never agree on line
// breaks, capitalisation of headings, or which quotation marks were used. If
// normalisation did not erase all of that, the pair would never match and the
// feature would miss the case it exists for.
func TestSimHashSurvivesReflowAndRecasing(t *testing.T) {
	original := simhashOf(t, proseOriginal)

	// Rewrapped onto one long line, shouted, and re-punctuated — a harsher
	// mangling than any real export would apply.
	reflowed := strings.ToUpper(strings.ReplaceAll(strings.ReplaceAll(proseOriginal, "\n", " "), ", ", " -- "))

	if d := Distance(original, simhashOf(t, reflowed)); d != 0 {
		t.Errorf("distance = %d, want 0: the same text reflowed and re-cased produced a different fingerprint, so a .docx would never match its own .pdf export", d)
	}
}

// TestSimHashDistanceBands turns the arithmetic behind DefaultTextThreshold
// into an executable claim. If these bands ever overlap, the threshold stops
// separating "a revision of this document" from "a different document".
func TestSimHashDistanceBands(t *testing.T) {
	original := simhashOf(t, proseOriginal)

	tests := []struct {
		name    string
		other   string
		wantMax int // 0 means "no upper bound"
		wantMin int // 0 means "no lower bound"
		why     string
	}{
		{
			name:    "a revision with three words changed",
			other:   proseEdited,
			wantMax: DefaultTextThreshold,
			why:     "two drafts of one document would not be offered as duplicates at all",
		},
		{
			name:    "an unrelated document of similar length",
			other:   proseUnrelated,
			wantMin: 20,
			why:     "two documents with nothing in common would be offered for deletion",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := Distance(original, simhashOf(t, tt.other))
			if tt.wantMax > 0 && d > tt.wantMax {
				t.Errorf("distance = %d, want at most %d: %s", d, tt.wantMax, tt.why)
			}
			if tt.wantMin > 0 && d < tt.wantMin {
				t.Errorf("distance = %d, want at least %d: %s", d, tt.wantMin, tt.why)
			}
		})
	}
}

// TestSimHashKeepsTemplatesApart is the test that justifies choosing 6 over the
// 8 used for photographs.
//
// Two letters generated from one boilerplate share most of their words and are
// the dominant false positive in any real documents folder — and they are also
// the case where a wrong delete destroys a document nobody has another copy of.
// They must land clearly outside the threshold, not just barely outside it.
func TestSimHashKeepsTemplatesApart(t *testing.T) {
	d := Distance(simhashOf(t, templateA), simhashOf(t, templateB))

	if d <= DefaultTextThreshold {
		t.Errorf("distance = %d, want well above %d: two letters from the same boilerplate would be reported as the same document, and the user would be invited to delete one of them", d, DefaultTextThreshold)
	}
	// A comfortable gap, not a coincidence. If this ever tightens, the
	// threshold needs revisiting rather than the test relaxing.
	if d < DefaultTextThreshold+4 {
		t.Errorf("distance = %d, only %d above the threshold: the margin against same-template documents has narrowed enough to be worth re-deriving", d, d-DefaultTextThreshold)
	}
}

// TestSimHashIsStable pins the algorithm with a golden value.
//
// FNV-1a is specified and seeded identically everywhere, so this constant holds
// across machines and Go versions. That is the point: if a fingerprint ever
// changed, a re-scan would silently produce different groups from the same
// folder, and nothing else in the test suite would notice.
func TestSimHashIsStable(t *testing.T) {
	const golden = 0x4a8c3d223fbfcc38

	if got := simhashOf(t, proseOriginal); got != golden {
		t.Errorf("SimHash = %#016x, want %#016x: the algorithm changed, so every previously recorded fingerprint is now meaningless", got, golden)
	}
}

func TestSimHashIsDeterministic(t *testing.T) {
	first := simhashOf(t, proseOriginal)
	for range 5 {
		if got := simhashOf(t, proseOriginal); got != first {
			t.Fatalf("SimHash returned %#016x then %#016x for identical input", first, got)
		}
	}
}

// TestSimHashRefusesThinText checks the honest refusal. A fingerprint derived
// from a handful of features is noise, and noise matches other noise — which
// would group a spreadsheet of bare numbers with an unrelated one and invite a
// deletion.
func TestSimHashRefusesThinText(t *testing.T) {
	tests := []struct {
		name string
		text string
	}{
		{"empty", ""},
		{"whitespace only", "   \n\t  \n "},
		{"punctuation only", "--- !!! ... ??? ;;;"},
		{"fewer words than one shingle", "only three words"},
		{"too few distinct shingles", strings.Repeat("alpha beta gamma delta ", 3)},
		{"one word repeated", strings.Repeat("same ", 500)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if h, ok := SimHash(tt.text); ok {
				t.Errorf("SimHash accepted text with nothing to fingerprint, returning %#016x", h)
			}
		})
	}
}

// TestSimHashIgnoresRepetition guards the set-weighting decision. A header
// repeated through a long export must not drown out the body, or every document
// from one template would collapse onto the same fingerprint.
func TestSimHashIgnoresRepetition(t *testing.T) {
	once := simhashOf(t, proseOriginal)
	withRepeatedHeader := simhashOf(t, strings.Repeat("Confidential draft not for circulation\n", 200)+proseOriginal)

	if d := Distance(once, withRepeatedHeader); d > DefaultTextThreshold {
		t.Errorf("distance = %d, want at most %d: a boilerplate header repeated 200 times shifted the fingerprint of the document it was stamped on", d, DefaultTextThreshold)
	}
}

// TestWordsKeepsAccents checks that normalisation folds case and punctuation but
// not accents: "ano" and "año" are different words, and in Spanish embarrassingly
// so.
func TestWordsKeepsAccents(t *testing.T) {
	got := words("El AÑO pasado, el niño leyó 42 páginas.")
	want := []string{"el", "año", "pasado", "el", "niño", "leyó", "42", "páginas"}

	if len(got) != len(want) {
		t.Fatalf("words() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("word %d = %q, want %q", i, got[i], want[i])
		}
	}
}
