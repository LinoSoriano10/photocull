// Package textdiff compares two documents a word at a time and reports only
// what changed.
//
// It exists because the obvious tool is the wrong one twice over. A line diff
// highlights a whole paragraph when a single adjective moved, which is exactly
// the answer nobody needs from a thirty-page report. A character diff is worse:
// it splits highlights mid-word, so "the estate" against "this estate" comes
// back as "th[e|is] estate" and the reader has to reassemble the words in their
// head. Words are the unit a person actually compares documents in.
//
// The second job is deciding what not to show. Two drafts of a long document
// are almost entirely the same document; rendering all of it and colouring 1%
// leaves the reader scrolling for the change. So the identical stretches are
// collapsed to a count, with a little context kept either side of every change.
package textdiff

import (
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sergi/go-diff/diffmatchpatch"
)

const (
	// Context is how many identical words are kept either side of a change.
	// Enough to recognise the sentence, few enough that the changes stay close
	// together on screen.
	Context = 12

	// MaxWords caps how much of one document is compared. The underlying
	// algorithm costs O(n*d) — cheap when two documents are alike, which is the
	// normal case here, and expensive when they are not. The cap bounds the
	// worst case, and Timeout catches whatever it does not.
	MaxWords = 20000

	// Timeout stops a pathological pair from holding a request open. The
	// algorithm returns a valid, merely suboptimal diff when it runs out of
	// time, so this degrades the answer rather than losing it.
	Timeout = 2 * time.Second
)

// Hunk is one change with the words around it.
//
// Before and After are identical text; Del is what the left document says there
// and Ins what the right one says. Either may be empty: pure insertions and
// pure deletions are ordinary.
type Hunk struct {
	Before string
	Del    string
	Ins    string
	After  string

	// SkippedWords counts the identical words collapsed between the previous
	// hunk and this one. Skipped holds them, so the page can expand the run
	// without asking the server again.
	SkippedWords int
	Skipped      string
}

// Diff is the whole comparison.
type Diff struct {
	Hunks []Hunk

	// SameWords and ChangedWords describe the documents, not the hunks: they
	// count every word compared, including the ones inside collapsed runs. They
	// are what a headline like "3 changes · 412 of 1,847 words differ" is built
	// from, and that sentence is often the entire answer — a reader who sees 2%
	// knows to skim and one who sees 60% knows these are different documents.
	SameWords    int
	ChangedWords int

	// TrailingWords counts the identical words after the last change, with
	// Trailing holding them. Reported separately for the same reason the
	// leading run is: "nothing changed in the last nine pages" is information.
	TrailingWords int
	Trailing      string

	// Truncated reports that one or both documents were longer than MaxWords
	// and only the beginning was compared.
	Truncated bool

	// TimedOut reports that the comparison ran out of time and what came back
	// is a valid diff rather than the best one. It is reported because the
	// difference is visible: a timed-out diff tends to show one long change
	// where a finished one would have found several small ones, and a reader
	// who is not told that will conclude the two documents are further apart
	// than they are — and delete the wrong copy on the strength of it.
	TimedOut bool
}

// Identical reports that the two documents say exactly the same words.
func (d Diff) Identical() bool { return d.ChangedWords == 0 }

// Words compares a and b a word at a time.
//
// Whitespace is not compared, and that omission is the single most important
// decision in this file. A PDF has no paragraphs, only glyphs at coordinates,
// so the same prose exported from a word processor comes back hard-wrapped at a
// width the original never had. Comparing whitespace would report every one of
// those line breaks as a change and bury the real edit in a wall of them —
// which is precisely the failure this view exists to prevent. Case and
// punctuation *are* compared: unlike a line break, a changed comma is a change
// somebody made on purpose.
func Words(a, b string) Diff {
	return words(a, b, Timeout)
}

// words is Words with the deadline as a parameter, so a test can prove the
// timeout is noticed without spending the real budget waiting for it.
func words(a, b string, timeout time.Duration) Diff {
	wa, ta := split(a)
	wb, tb := split(b)

	ea, eb, vocab, ok := encode(wa, wb)
	if !ok {
		// More distinct words than there are code points to name them with.
		// Unreachable below MaxWords, and a wrong answer rather than a slow one
		// if that ever changes, so it is refused instead.
		return Diff{Truncated: true}
	}

	dmp := diffmatchpatch.New()
	dmp.DiffTimeout = timeout

	// The library has no "did you finish?" flag, so the clock is the only way
	// to ask. It gives up *at* the deadline rather than before it, so anything
	// that took the full budget came back early rather than complete; the
	// margin keeps a merely slow machine from being reported as a timeout.
	started := time.Now()
	diffs := dmp.DiffMain(ea, eb, false)
	timedOut := time.Since(started) >= timeout-timeout/10

	out := build(decode(diffs, vocab))
	out.Truncated = ta || tb
	out.TimedOut = timedOut
	return out
}

// split breaks text into words, reporting whether it had to stop early.
func split(text string) (words []string, truncated bool) {
	words = strings.Fields(text)
	if len(words) > MaxWords {
		return words[:MaxWords], true
	}
	return words, false
}

const (
	equal = iota
	deleted
	inserted
)

// seg is a run of words that are all equal, all deleted or all inserted.
type seg struct {
	kind  int
	words []string
}

// encode gives every distinct word a code point and rewrites both documents as
// strings of them.
//
// This is what makes a character-diff library produce a word diff: the library
// compares runes, so if one rune stands for one whole word it can no longer cut
// a highlight in half. The alternative — diffing the text and then trying to
// snap the results outwards to word boundaries — has to guess, and guesses
// wrong on exactly the runs of short words where it matters.
func encode(a, b []string) (ea, eb string, vocab map[rune]string, ok bool) {
	index := make(map[string]rune, len(a)+len(b))
	vocab = make(map[rune]string, len(a)+len(b))
	next := rune(1) // 0 would be a NUL in the middle of a Go string

	take := func(w string) (rune, bool) {
		if r, seen := index[w]; seen {
			return r, true
		}
		// The surrogate range is not a legal rune in a Go string; writing one
		// would silently become U+FFFD and fuse two different words into one.
		// Unreachable while MaxWords is below 55296, and a landmine the moment
		// it is not — which is also why the reverse mapping is a map rather
		// than an index into a slice, since this skip breaks that arithmetic.
		if next >= 0xD800 && next <= 0xDFFF {
			next = 0xE000
		}
		if next > utf8.MaxRune {
			return 0, false
		}
		r := next
		next++
		index[w] = r
		vocab[r] = w
		return r, true
	}

	encodeOne := func(words []string) (string, bool) {
		var sb strings.Builder
		for _, w := range words {
			r, fine := take(w)
			if !fine {
				return "", false
			}
			sb.WriteRune(r)
		}
		return sb.String(), true
	}

	ea, ok = encodeOne(a)
	if !ok {
		return "", "", nil, false
	}
	eb, ok = encodeOne(b)
	if !ok {
		return "", "", nil, false
	}
	return ea, eb, vocab, true
}

// decode turns the library's rune diffs back into runs of real words.
func decode(diffs []diffmatchpatch.Diff, vocab map[rune]string) []seg {
	segs := make([]seg, 0, len(diffs))
	for _, d := range diffs {
		words := make([]string, 0, utf8.RuneCountInString(d.Text))
		for _, r := range d.Text {
			if w, known := vocab[r]; known {
				words = append(words, w)
			}
		}
		if len(words) == 0 {
			continue
		}
		var kind int
		switch d.Type {
		case diffmatchpatch.DiffDelete:
			kind = deleted
		case diffmatchpatch.DiffInsert:
			kind = inserted
		default:
			kind = equal
		}
		segs = append(segs, seg{kind: kind, words: words})
	}
	return segs
}

// build turns the runs into hunks, collapsing the long identical stretches.
func build(segs []seg) Diff {
	var out Diff
	for _, s := range segs {
		switch s.kind {
		case equal:
			out.SameWords += len(s.words)
		default:
			out.ChangedWords += len(s.words)
		}
	}

	var (
		carry   []string // context available to the next hunk's Before
		skipped []string // identical words collapsed before that context
	)

	for i := 0; i < len(segs); {
		if segs[i].kind == equal {
			carry, skipped = absorb(carry, skipped, segs[i].words)
			i++
			continue
		}

		var del, ins []string
	collect:
		for i < len(segs) {
			switch {
			case segs[i].kind == deleted:
				del = append(del, segs[i].words...)
			case segs[i].kind == inserted:
				ins = append(ins, segs[i].words...)
			// A short identical run between two changes is kept inside the hunk
			// rather than closing it. Two edits a few words apart are one edit
			// to the person reading, and splitting them into two hunks with
			// overlapping context shows the same sentence twice.
			case len(segs[i].words) <= Context && i+1 < len(segs):
				del = append(del, segs[i].words...)
				ins = append(ins, segs[i].words...)
			default:
				break collect
			}
			i++
		}

		var after []string
		if i < len(segs) && segs[i].kind == equal {
			w := segs[i].words
			after = w[:min(Context, len(w))]
			// The rest of this run belongs to the *next* hunk's context, so it
			// is left in place to be absorbed on the next turn of the loop.
			segs[i].words = w[len(after):]
			if len(segs[i].words) == 0 {
				i++
			}
		}

		out.Hunks = append(out.Hunks, Hunk{
			Before:       join(carry),
			Del:          join(del),
			Ins:          join(ins),
			After:        join(after),
			SkippedWords: len(skipped),
			Skipped:      join(skipped),
		})
		carry, skipped = nil, nil
	}

	out.TrailingWords = len(skipped)
	out.Trailing = join(skipped)
	// Whatever is left in carry sits between the last hunk's After and the end;
	// it is context nobody asked for, so it joins the trailing count.
	if len(carry) > 0 {
		out.TrailingWords += len(carry)
		out.Trailing = strings.TrimSpace(out.Trailing + " " + join(carry))
	}
	return out
}

// absorb adds an identical run to the pending context, pushing anything beyond
// Context words into the collapsed pile.
func absorb(carry, skipped, words []string) ([]string, []string) {
	carry = append(carry, words...)
	if len(carry) > Context {
		cut := len(carry) - Context
		skipped = append(skipped, carry[:cut]...)
		carry = carry[cut:]
	}
	return carry, skipped
}

func join(words []string) string { return strings.Join(words, " ") }
