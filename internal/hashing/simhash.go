package hashing

import (
	"strings"
	"unicode"
)

// SimHash tuning. Each of these is a claim about documents, not about hashing.
const (
	// SimHashBits is the width of a text fingerprint. It matches a perceptual
	// hash deliberately, so both go through Distance and both feed the same
	// grouping code.
	SimHashBits = 64

	// ShingleWords is how many consecutive words make one feature.
	//
	// Four is specific enough that unrelated documents essentially never share
	// a feature, while a single edited word destroys only the four shingles
	// that overlap it rather than the whole signature. Three picks up the
	// boilerplate every business letter contains ("please find attached the");
	// five is brittle enough that ordinary revision noise starts to matter.
	ShingleWords = 4

	// MinShingles is the fewest distinct features a document must produce
	// before a 64-bit sketch means anything. Below roughly two dozen, each bit
	// is decided by a handful of votes and the distance between two revisions
	// of one document starts swinging on noise instead of content.
	MinShingles = 24
)

// DefaultTextThreshold is the Hamming distance below which two SimHashes are
// treated as the same document.
//
// For SimHash the expected distance is 64·θ/π, where θ is the angle between the
// two feature sets. The tests in this package measure the bands on real prose:
//
//	same text reflowed, re-cased, re-punctuated    0
//	three words changed in four hundred            5
//	two letters from one boilerplate              16
//	unrelated documents of similar length         32
//
// Unrelated files are not the risk: they sit a full 32 bits away, and anything
// from 8 to 20 would separate them. The risk is documents built from a shared
// template, because a wrong delete there destroys a real document.
//
// Six rather than eight buys margin against the case the numbers above do not
// show. Distance depends on the *fraction* of the text that differs, so two
// short letters from a template land at 16, but a twenty-page contract where
// only the names and dates change differs in perhaps three percent of its words
// and lands near five — indistinguishable from a genuine revision, because
// structurally that is what it is. No threshold fixes that, which is why
// "similar" is a tier the user reviews rather than one photocull acts on, and
// why the comparison view shows what actually changed.
//
// Google's 3 works for near-duplicate web pages, which are ~99% identical. That
// regime does not describe draft_v1 against draft_v2.
const DefaultTextThreshold = 6

// FNV-1a 64-bit parameters.
//
// The choice of hash matters for one reason: hash/maphash is seeded randomly
// per process, so fingerprints would differ between runs and re-scanning a
// folder would produce different groups from the same files. FNV-1a is
// specified and stable across Go versions and machines, which is also what
// makes a golden-value test possible.
const (
	fnvOffset64 = 14695981039346656037
	fnvPrime64  = 1099511628211
)

// SimHash reduces a document's text to a 64-bit fingerprint of what it says.
//
// ok is false when the text was too thin to fingerprint honestly — a
// spreadsheet of bare numbers, a PDF that is really a scan with a caption. A
// meaningless fingerprint is worse than none: it would match other meaningless
// fingerprints and invite the user to delete unrelated files.
func SimHash(text string) (h uint64, ok bool) {
	words := words(text)
	if len(words) < ShingleWords {
		return 0, false
	}

	// One vote per *distinct* shingle — a set, not a bag.
	//
	// Charikar's original weights features by tf-idf. Without a corpus there is
	// no idf, and raw term frequency is actively harmful here: a page header
	// repeated two hundred times through a long PDF would dominate the
	// signature and make every document from that template collide. Set
	// weighting turns this into a smeared Jaccard sketch, which is the
	// semantics actually wanted.
	//
	// The set holds hashes rather than the shingle strings themselves. A large
	// document yields over a million shingles, and materialising each as a
	// string would cost tens of megabytes per file — multiplied by one worker
	// per CPU core. Two distinct shingles colliding on 64 bits is possible and
	// numerically irrelevant at these counts.
	seen := make(map[uint64]struct{}, len(words))
	for i := 0; i+ShingleWords <= len(words); i++ {
		seen[shingleHash(words[i:i+ShingleWords])] = struct{}{}
	}
	if len(seen) < MinShingles {
		return 0, false
	}

	var votes [SimHashBits]int
	for feature := range seen {
		for bit := range SimHashBits {
			if feature&(1<<uint(bit)) != 0 {
				votes[bit]++
			} else {
				votes[bit]--
			}
		}
	}

	// Ties resolve to zero, deterministically. Which way they fall does not
	// matter; that they always fall the same way does.
	for bit := range SimHashBits {
		if votes[bit] > 0 {
			h |= 1 << uint(bit)
		}
	}
	return h, true
}

// words lowercases the text and splits it on everything that is not a letter or
// a digit.
//
// This one step is what makes cross-format matching work. It erases the
// difference between a PDF's hard-wrapped lines, a DOCX's flowing paragraphs
// and a TXT file's CRLFs — which is exactly the case that matters, because the
// same document existing as both a .docx and its .pdf export is the most common
// document duplicate there is. unicode.IsLetter covers accented text without
// any extra work; accents are deliberately kept, since two documents that
// differ only in their accents are two different documents.
func words(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// shingleHash folds a run of words into one FNV-1a value without joining them
// into a string first.
func shingleHash(words []string) uint64 {
	h := uint64(fnvOffset64)
	for i, w := range words {
		if i > 0 {
			h ^= ' '
			h *= fnvPrime64
		}
		for j := range len(w) {
			h ^= uint64(w[j])
			h *= fnvPrime64
		}
	}
	return h
}
