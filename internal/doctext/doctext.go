// Package doctext pulls the plain text out of the document formats people
// actually accumulate duplicates of.
//
// The design decision worth explaining is what it does when it fails. Text
// extraction is best-effort by nature: a scanned PDF has no text layer, a
// spreadsheet may be nothing but numbers, a legacy .doc is a compound binary
// nobody should parse for this. Rather than guessing, doctext says so and says
// why, and photocull drops that file into the low-confidence tier where a human
// decides. Every failure mode here is a designed outcome, not an accident.
package doctext

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

// Limits. Each one is a ceiling on what a single worker may spend on one file,
// and there is one worker per CPU core, so the worst case is this times the
// core count.
const (
	// MaxBytes is the most a document may weigh before photocull stops trying
	// to read it. Both the ZIP and PDF containers need random access, so a
	// document is buffered whole rather than streamed.
	MaxBytes = 16 << 20

	// MaxText is the most text one document may yield. This is the zip-bomb
	// ceiling: a one-kilobyte .docx can legitimately declare gigabytes of
	// decompressed content.
	MaxText = 8 << 20

	// MinRunes is the least text worth fingerprinting. Below this there are not
	// enough words to form the shingles a SimHash needs, and a fingerprint of
	// almost nothing would match other fingerprints of almost nothing.
	MinRunes = 128
)

// DefaultExtensions lists what doctext knows how to read.
//
// Everything else is still scanned — it is just compared by content hash only.
// That is deliberate: a .zip or an .mp3 nobody can parse still has to be
// deduplicated, or the scan has blind spots exactly where the user assumed
// photocull was looking.
var DefaultExtensions = []string{".pdf", ".docx", ".xlsx", ".pptx", ".txt", ".md", ".csv"}

// Reasons a document yielded nothing usable. They are shown to the user, so
// they are phrased for one.
const (
	ReasonUnsupported = "photocull cannot read inside this kind of file"
	ReasonTooLarge    = "too large to read"
	ReasonThin        = "not enough text to compare"
	ReasonEmpty       = "no text found inside"
	ReasonScanned     = "this PDF looks like a scan, with little or no real text"
)

// errBomb is returned when a container claims to hold more than MaxText.
var errBomb = errors.New("doctext: archive declares more content than the limit allows")

// Extraction is what doctext made of one document.
type Extraction struct {
	// Text is the normalised plain text. It is empty unless Usable.
	Text string

	// Usable reports that enough text came out to be worth fingerprinting.
	Usable bool

	// Reason says why not, in words meant for the person deciding whether to
	// delete the file.
	Reason string
}

// Handles reports whether doctext knows how to read inside this path.
func Handles(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	for _, known := range DefaultExtensions {
		if ext == known {
			return true
		}
	}
	return false
}

// Extract reads a whole document from r and returns its text.
//
// r is the scanner's tee'd reader: forward-only, and shared with the content
// hasher. Whatever this function leaves unread is drained by the caller, so
// stopping early is safe.
//
// An error means the file claimed to be a format it is not — a .docx that is
// not a valid ZIP. A file that is simply not readable as text is not an error:
// it comes back Usable=false with a Reason, which is the normal path for every
// .zip and .mp3 on the disk.
func Extract(r io.Reader, path string) (Extraction, error) {
	ext := strings.ToLower(filepath.Ext(path))
	if !Handles(path) {
		// Do not even read: there is nothing this package could do with it, and
		// the caller still has to hash the bytes itself.
		return Extraction{Reason: ReasonUnsupported}, nil
	}

	data, err := readCapped(r)
	if errors.Is(err, errTooLarge) {
		return Extraction{Reason: ReasonTooLarge}, nil
	}
	if err != nil {
		return Extraction{}, err
	}

	var text string
	switch ext {
	case ".txt", ".md", ".csv":
		text, err = decodeText(data)
	case ".docx", ".xlsx", ".pptx":
		text, err = extractOOXML(data, ext)
	case ".pdf":
		text, err = extractPDF(data)
		// A PDF is the one format that is routinely valid and yet holds no text
		// whatsoever, so it gets its own verdict instead of the generic one.
		// Two shapes mean the same thing. Nothing came out at all: a photograph
		// of a page. Or so little came out relative to the file's size that what
		// did is a watermark rather than the document — see pdfTextIsThin, which
		// is deliberately not applied to the other formats.
		if err == nil && (strings.TrimSpace(text) == "" || pdfTextIsThin(text, len(data))) {
			return Extraction{Reason: ReasonScanned}, nil
		}
	default:
		return Extraction{Reason: ReasonUnsupported}, nil
	}
	if err != nil {
		return Extraction{}, err
	}

	return classify(text), nil
}

// classify decides whether what came out is worth fingerprinting.
func classify(text string) Extraction {
	text = strings.TrimSpace(text)
	switch {
	case text == "":
		return Extraction{Reason: ReasonEmpty}
	case utf8.RuneCountInString(text) < MinRunes:
		return Extraction{Reason: ReasonThin}
	default:
		return Extraction{Text: text, Usable: true}
	}
}

var errTooLarge = errors.New("doctext: document exceeds the size limit")

func readCapped(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("doctext: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, errTooLarge
	}
	return data, nil
}

// decodeText turns a plain-text file into UTF-8, honouring a byte-order mark.
//
// The UTF-16 case is not theoretical: saving as "Unicode" in Windows Notepad
// produces UTF-16LE with a BOM, and reading those bytes as UTF-8 yields text
// interleaved with NULs. That normalises to garbage and fingerprints to
// something meaningless, so the file would silently never match its own copies.
//
// BOMOverride handles all three cases in one pass: it consumes a UTF-8 BOM,
// switches to the right UTF-16 decoder when it sees one, and otherwise passes
// the bytes through untouched.
func decodeText(data []byte) (string, error) {
	out, _, err := transform.Bytes(unicode.BOMOverride(unicode.UTF8.NewDecoder()), data)
	if err != nil {
		return "", fmt.Errorf("doctext: decode text: %w", err)
	}
	return string(out), nil
}

// ooxmlPart names the inner file to read for a format, and which elements mark
// a break in the text.
type ooxmlPart struct {
	// breakOn are elements whose end emits a newline.
	breakOn map[string]bool
	// spaceOn are empty elements that stand for whitespace.
	spaceOn map[string]bool
}

// extractOOXML reads the text out of an Office file, which is a ZIP of XML.
//
// Only the body is read, and the omissions are deliberate:
//
// Headers, footers and footnotes are skipped. They are boilerplate repeated
// across every document from the same template, which is the feature class most
// likely to make unrelated documents look alike.
//
// For spreadsheets only xl/sharedStrings.xml is read, which holds every
// distinct string in the workbook; the numbers live in the worksheet parts. A
// spreadsheet of pure numbers therefore yields nothing and falls to the
// low-confidence tier — which is right, because two numerically different
// exports of one report share all their labels and would otherwise look like
// the same document.
func extractOOXML(data []byte, ext string) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("doctext: %s is not a valid Office container: %w", ext, err)
	}

	var (
		parts     []*zip.File
		shape     ooxmlPart
		remaining = MaxText
	)

	switch ext {
	case ".docx":
		// A word is split across runs whenever formatting or the spell checker
		// touches it, so runs must join with nothing between them while
		// paragraphs must break.
		shape = ooxmlPart{breakOn: map[string]bool{"p": true}, spaceOn: map[string]bool{"tab": true, "br": true}}
		parts = named(zr, "word/document.xml")
	case ".xlsx":
		shape = ooxmlPart{breakOn: map[string]bool{"si": true}}
		parts = named(zr, "xl/sharedStrings.xml")
	case ".pptx":
		shape = ooxmlPart{breakOn: map[string]bool{"p": true}, spaceOn: map[string]bool{"br": true}}
		parts = slides(zr)
	}

	var b strings.Builder
	for _, f := range parts {
		text, err := partText(f, shape, &remaining)
		if err != nil {
			return "", err
		}
		b.WriteString(text)
		b.WriteByte('\n')
		if remaining <= 0 {
			break
		}
	}
	return b.String(), nil
}

func named(zr *zip.Reader, name string) []*zip.File {
	for _, f := range zr.File {
		if f.Name == name {
			return []*zip.File{f}
		}
	}
	return nil
}

// slides returns a presentation's slide parts in presentation order.
//
// The sort is on the numeric suffix rather than the name, because lexical order
// puts slide10 before slide2. Slide order barely moves a SimHash, but a
// fingerprint that depended on ZIP entry order would not be reproducible, and
// reproducibility is the whole basis for re-scanning a folder and getting the
// same groups.
func slides(zr *zip.Reader) []*zip.File {
	var found []*zip.File
	for _, f := range zr.File {
		if strings.HasPrefix(f.Name, "ppt/slides/slide") && strings.HasSuffix(f.Name, ".xml") {
			found = append(found, f)
		}
	}
	sort.Slice(found, func(i, j int) bool {
		ni, nj := slideNumber(found[i].Name), slideNumber(found[j].Name)
		if ni != nj {
			return ni < nj
		}
		return found[i].Name < found[j].Name
	})
	return found
}

func slideNumber(name string) int {
	base := strings.TrimSuffix(strings.TrimPrefix(name, "ppt/slides/slide"), ".xml")
	n, err := strconv.Atoi(base)
	if err != nil {
		return 1 << 30 // unparseable names sort last, deterministically
	}
	return n
}

// partText reads one XML part, refusing to be used as a decompression bomb.
func partText(f *zip.File, shape ooxmlPart, remaining *int) (string, error) {
	if *remaining <= 0 {
		return "", nil
	}
	// Check what the entry claims before opening it, then cap what it actually
	// delivers. A declared size is a lie a crafted archive can tell, so both
	// halves are needed.
	if f.UncompressedSize64 > uint64(MaxText) {
		return "", errBomb
	}

	rc, err := f.Open()
	if err != nil {
		return "", fmt.Errorf("doctext: open %s: %w", f.Name, err)
	}
	defer rc.Close()

	return xmlText(io.LimitReader(rc, int64(*remaining)+1), shape, remaining)
}

// xmlText walks an XML part and returns only its character data.
//
// Stripping tags with a regular expression is the obvious approach and it is
// wrong twice over: it mangles CDATA and entities, and it cannot tell a run
// boundary from a paragraph boundary. Word splits a single word across runs
// whenever formatting or spell-check touches it, so <w:t>Hel</w:t><w:t>lo</w:t>
// has to join into "Hello" with nothing between, while </w:p> has to become a
// break. Getting that backwards changes the token stream and therefore the
// fingerprint.
func xmlText(r io.Reader, shape ooxmlPart, remaining *int) (string, error) {
	dec := xml.NewDecoder(r)
	// A stray or unknown encoding declaration must not abort the read: the
	// parts are UTF-8 by specification and the declaration is not worth
	// trusting over that.
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }

	var b strings.Builder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			// Return what was read: a truncated document still fingerprints
			// better than no document, and the caller decides if it is enough.
			return b.String(), nil
		}

		switch t := tok.(type) {
		case xml.CharData:
			chunk := []byte(t)
			if len(chunk) > *remaining {
				chunk = chunk[:*remaining]
			}
			b.Write(chunk)
			*remaining -= len(chunk)
			if *remaining <= 0 {
				return b.String(), nil
			}
		case xml.StartElement:
			if shape.spaceOn[t.Name.Local] {
				b.WriteByte(' ')
			}
		case xml.EndElement:
			if shape.breakOn[t.Name.Local] {
				b.WriteByte('\n')
			}
		}
	}
	return b.String(), nil
}
