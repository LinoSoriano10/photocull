// Package fingerprint isolates the one question that differs between the kinds
// of file photocull deduplicates: given this file's bytes, what 64-bit number
// describes what is *in* it?
//
// Everything downstream of that question turned out not to care about the
// answer. The scanner reads bytes and hashes them, dedupe compares 64-bit words
// by Hamming distance, report counts groups, the trash moves paths and the web
// UI shows what it is given — none of that is about photographs. This package
// is the seam that proved it, and it is what lets one binary deduplicate a
// photo library and a documents folder without a second copy of any of that.
package fingerprint

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"photocull/internal/hashing"
)

// Kind is a family of files photocull knows how to look inside.
type Kind string

const (
	Photos Kind = "photos"
	Docs   Kind = "docs"
)

// Input is what an Extractor is told about a file besides its bytes.
type Input struct {
	Path string
	Size int64

	// Deep asks for a similarity fingerprint rather than only what comes free
	// alongside the content hash. It is off for an exact-duplicates scan, where
	// SHA-256 decides everything and the extra work would buy nothing.
	Deep bool
}

// Result is what an Extractor learned from one file.
type Result struct {
	// Understood reports that the contents were interpreted: an image decoded,
	// text extracted. A file that was read perfectly well but holds nothing
	// this extractor can read is not an error — it simply has no fingerprint,
	// and is still deduplicated by its content hash.
	Understood bool

	Fingerprint    uint64
	HasFingerprint bool

	// Width and Height are meaningful for images and zero for everything else.
	Width, Height int

	// Err marks contents that were genuinely malformed, as opposed to merely
	// unreadable by this extractor. It is reported to the user and never aborts
	// the scan: on a drive holding years of backups some damaged files are
	// expected, and the other few thousand still matter.
	Err error
}

// Extractor turns one file's bytes into a Result.
//
// The contract on r is what keeps the scanner to a single pass over the disk:
// r is a tee that is simultaneously feeding the SHA-256 hasher, so an
// implementation may read as much or as little of it as it likes, and must
// never close it, seek it, or open the file itself. Whatever it leaves unread
// is drained through the hasher afterwards. Reading every file twice would
// double the I/O, and on the external drive where a photo library actually
// lives the disk is the bottleneck, not the CPU.
type Extractor interface {
	Kind() Kind

	// Extensions is the set of files this kind looks at. A nil slice means
	// every file, which is what documents will need: a .zip or a legacy .doc
	// still has to be compared by content hash, or the scan has blind spots
	// exactly where the user assumed it was looking.
	Extensions() []string

	Fingerprint(r io.Reader, in Input) Result
}

// Defaults are the settings that suit a kind but are policy rather than
// mechanism: how close is close enough, which copy to suggest keeping, and
// whether the low-confidence name-and-size pass applies at all.
//
// They live beside the extractors for two reasons. Adding a kind then forces
// someone to decide them rather than inherit whatever photographs happened to
// need. And the CLI and the web UI both read the answer from here, so the two
// cannot drift into disagreeing about what --threshold defaults to.
type Defaults struct {
	// Threshold is the Hamming distance at which two fingerprints of this kind
	// are treated as the same thing.
	Threshold int

	// Strategy names the dedupe keep strategy to suggest.
	Strategy string

	// Related enables the low-confidence pass that matches files by name and
	// size when their contents could not be read at all. It is off for photos,
	// where a file that will not decode is rare and usually just broken, and on
	// for documents, where unreadable files are ordinary — a scanned PDF, a
	// legacy .doc, an .mp3 — and their names are the only remaining signal.
	Related bool
}

// DefaultsFor returns the policy for a kind. An unknown kind gets the photo
// settings, which are the conservative choice: no speculative matching.
func DefaultsFor(k Kind) Defaults {
	if d, ok := defaults[k]; ok {
		return d
	}
	return defaults[Photos]
}

var defaults = map[Kind]Defaults{
	Photos: {
		Threshold: hashing.DefaultPhotoThreshold,
		Strategy:  "default",
		Related:   false,
	},
	Docs: {
		Threshold: hashing.DefaultTextThreshold,
		Strategy:  "document",
		Related:   true,
	},
}

// extractors is the registry. It is the one place a new kind is added.
var extractors = map[Kind]Extractor{
	Photos: imageExtractor{},
	Docs:   docExtractor{},
}

// Default is the extractor for callers that do not choose one. Photos came
// first, and every caller that predates this package means photos.
func Default() Extractor { return imageExtractor{} }

// Lookup resolves a --kind value.
func Lookup(name string) (Extractor, error) {
	if ex, ok := extractors[Kind(strings.ToLower(strings.TrimSpace(name)))]; ok {
		return ex, nil
	}
	return nil, fmt.Errorf("unknown kind %q (available: %s)", name, strings.Join(Kinds(), ", "))
}

// Kinds lists the accepted --kind values, sorted for help text.
func Kinds() []string {
	names := make([]string, 0, len(extractors))
	for k := range extractors {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return names
}
