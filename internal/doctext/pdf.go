package doctext

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/ledongthuc/pdf"
)

// MaxPDFPages caps how many pages one document contributes.
//
// A fingerprint is a summary, and the first fifty pages of a report already
// describe it well enough to tell it from a different report. The cap exists so
// that one thousand-page PDF cannot hold a worker — and there is one worker per
// core — for as long as it takes to parse the whole thing.
const MaxPDFPages = 50

// extractPDF pulls the text layer out of a PDF.
//
// The deferred recover is not defensive habit, it is required. PDF is a format
// that arrives malformed as a matter of routine — truncated downloads, partial
// syncs, generators that emit slightly invalid xref tables — and this parser
// responds to some of them by panicking rather than returning an error. A panic
// in a scan worker takes the whole process down with it, losing every result
// gathered so far, which is precisely the failure TestScanSurvivesCorruptFile
// exists to prevent. One bad file must cost one file.
func extractPDF(data []byte) (text string, err error) {
	defer func() {
		if r := recover(); r != nil {
			text = ""
			err = fmt.Errorf("doctext: the PDF parser panicked on this file: %v", r)
		}
	}()

	rd, err := pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", fmt.Errorf("doctext: not a readable PDF: %w", err)
	}

	pages := rd.NumPage()
	if pages > MaxPDFPages {
		pages = MaxPDFPages
	}

	var b strings.Builder
	remaining := MaxText

	for i := 1; i <= pages && remaining > 0; i++ {
		p := rd.Page(i)
		if p.V.IsNull() {
			continue
		}

		// Rows, not the library's plain-text helper.
		//
		// A PDF has no line breaks: it has glyphs at coordinates. The plain-text
		// helper concatenates them in the order the content stream draws them
		// and adds nothing between, so the last word of every line fuses to the
		// first word of the next — "the storage estateThe estate now holds".
		// Measured on a page of ordinary prose, that alone moved the SimHash 13
		// bits, twice the threshold: a document and its own PDF export would
		// never have been offered as duplicates. Grouping by row restores the
		// line breaks from the glyph coordinates, and sorts into reading order
		// on the way.
		//
		// Known limit: the parser tracks position on the Tm operator and
		// ignores Td, so a generator that lays out lines with relative offsets
		// collapses back into one fused row. Word processors emit Tm, which is
		// the case that matters here, and there is no way to tell the two apart
		// after the fact — a document affected by this simply fails to match its
		// twin, which is the safe direction to fail in.
		rows, perr := p.GetTextByRow()
		if perr != nil {
			// One unreadable page must not cost the other forty-nine: a
			// fingerprint built from most of a document still matches its copy.
			continue
		}

		for _, row := range rows {
			// Within one row the pieces join with nothing between them, because
			// a generator splits a single word across pieces to kern it. Real
			// gaps between words carry their own space characters.
			for _, piece := range row.Content {
				s := piece.S
				if len(s) > remaining {
					s = s[:remaining]
				}
				b.WriteString(s)
				remaining -= len(s)
				if remaining <= 0 {
					return b.String(), nil
				}
			}
			b.WriteByte('\n')
			remaining--
		}
	}
	return b.String(), nil
}

// pdfTextIsThin reports that a PDF yielded far too little text for its size.
//
// This is what a scan looks like: pages of images with a watermark, a stamped
// cover sheet, or a signature block sitting on top. Some text does come out, so
// the empty check never fires — but it describes the watermark rather than the
// document, and every scan sharing that watermark would fingerprint alike and be
// offered up as duplicates of each other.
//
// The rule is one character per kilobyte, which real prose clears by a wide
// margin. It is applied to PDFs only: a .docx holding one embedded photograph
// and two honest pages of text would fail the same test unfairly, because its
// bytes are mostly image while its text is entirely real.
func pdfTextIsThin(text string, size int) bool {
	return int64(utf8.RuneCountInString(text))*1024 < int64(size)
}
