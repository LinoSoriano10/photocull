package fingerprint

import (
	"io"

	"photocull/internal/doctext"
	"photocull/internal/hashing"
)

// docExtractor fingerprints a document by what it says rather than by which
// bytes it is made of.
//
// This matters more for documents than it ever did for photographs. Saving a
// .docx twice with identical content never produces identical bytes — the ZIP
// carries timestamps and revision identifiers — so a content hash alone misses
// most real Office duplicates. The text is the only stable thing about them.
type docExtractor struct{}

func (docExtractor) Kind() Kind { return Docs }

// Extensions returns nil: documents mode looks at every file.
//
// The formats doctext can read inside are a strict subset of the files worth
// deduplicating. A .zip, an .mp3 or a legacy .doc still has to be compared by
// content hash, and filtering them out would leave blind spots exactly where
// the user assumed photocull was looking. Narrowing the scan is what --ext is
// for, and that is the user's decision, not this package's.
func (docExtractor) Extensions() []string { return nil }

// Fingerprint extracts a document's text and reduces it to a SimHash.
//
// A file that yields no usable text is not an error and not a failure: it is
// most of a disk. It comes back Understood=false with no error at all, keeps
// its content hash, and is deduplicated on that alone.
func (docExtractor) Fingerprint(r io.Reader, in Input) Result {
	extracted, err := doctext.Extract(r, in.Path)
	if err != nil {
		// The file claimed a format it is not — a .docx that is not a ZIP.
		// Worth telling the user about; never worth stopping the scan for.
		return Result{Err: err}
	}
	if !extracted.Usable {
		return Result{}
	}

	// A shallow scan wants no fingerprint, but the text was already read on the
	// way past the hasher, so there was nothing to save by checking earlier.
	if !in.Deep {
		return Result{Understood: true}
	}

	sim, ok := hashing.SimHash(extracted.Text)
	if !ok {
		// Text came out, but too little of it to sketch honestly. Reporting a
		// meaningless fingerprint would be worse than reporting none: it would
		// match other meaningless fingerprints.
		return Result{Understood: true}
	}
	return Result{Understood: true, Fingerprint: sim, HasFingerprint: true}
}
