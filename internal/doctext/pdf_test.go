package doctext

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"photocull/internal/hashing"
)

// prose is long enough to clear MinRunes and to survive the thin-text ratio at
// the size these generated PDFs come out to.
const prose = "The north bed was replanted in April with hellebores and epimedium, " +
	"both of which prefer the dry shade under the beech. The soil there is thin " +
	"and full of roots, so the holes were dug wider than deep and backfilled with " +
	"leaf mould from the heap behind the shed. Watering was needed twice in the " +
	"first fortnight and not at all afterwards. By June the epimedium had spread " +
	"far enough to close the gaps between the hellebores, which is what the bed " +
	"had been wanting for several years."

// buildPDF assembles a genuinely valid PDF holding one page per entry in pages.
// An empty entry produces a page with graphics operators but no text ones, which
// is what a scanned page looks like to a parser.
//
// This follows the same reasoning as writeOOXML: a fixture a reviewer can read
// beats an opaque binary. The honest limit is that it exercises the parser's
// structural path — xref, catalogue, page tree, content streams — with one
// simple base font, and says nothing about the CID and embedded-subset font
// encodings where real-world extraction gets hard. A PDF produced by Word or
// LibreOffice would cover that, and would be worth adding as a committed
// fixture.
func buildPDF(t *testing.T, pages []string) []byte {
	t.Helper()

	// Objects 1, 2 and 3 are reserved for the catalogue, the page tree and the
	// font; each page then contributes a page object and a content stream.
	objs := []string{"", "", "<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"}

	var kids []string
	for _, text := range pages {
		pageNum, contentNum := len(objs)+1, len(objs)+2
		stream := pageStream(text)
		objs = append(objs,
			fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] "+
				"/Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>", contentNum),
			fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(stream), stream),
		)
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNum))
	}

	objs[0] = "<< /Type /Catalog /Pages 2 0 R >>"
	objs[1] = fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), len(pages))

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objs))
	for i, body := range objs {
		offsets[i] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	// Every xref entry is exactly twenty bytes. Readers seek by multiplying, so
	// a single byte out of place here breaks the whole file.
	start := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objs)+1)
	buf.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&buf, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objs)+1, start)

	return buf.Bytes()
}

// pageStream lays the text out one line at a time, the way a real generator
// does, rather than as one long string.
//
// That detail is the point rather than decoration: in a PDF the line breaks are
// baked into the layout, so the same prose exported from a word processor comes
// out hard-wrapped at a width the .docx never had. If the fingerprint were
// sensitive to that, a document and its own PDF export would never match, which
// is the single most common document duplicate there is.
//
// Each line gets an absolute text matrix (Tm) rather than a relative offset
// (Td), because that is what word processors emit and, more to the point, it is
// the only one this parser follows: its interpreter updates the current position
// on Tm and ignores it on Td. A PDF laid out with Td extracts as a single fused
// row — a real limitation, recorded in extractPDF, not something this fixture
// should paper over by pretending Td works.
func pageStream(text string) string {
	if text == "" {
		return "q 1 0 0 1 0 0 cm Q" // a graphics-only page: no text operators
	}

	var b strings.Builder
	b.WriteString("BT /F1 12 Tf")
	y := 720
	for _, line := range strings.Split(text, "\n") {
		fmt.Fprintf(&b, " 1 0 0 1 72 %d Tm (%s) Tj", y, escapePDFString(line))
		y -= 14
	}
	b.WriteString(" ET")
	return b.String()
}

func escapePDFString(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `(`, `\(`, `)`, `\)`)
	return r.Replace(s)
}

func extractBytes(t *testing.T, data []byte, name string) (Extraction, error) {
	t.Helper()
	return Extract(bytes.NewReader(data), name)
}

func TestExtractPDF(t *testing.T) {
	got, err := extractBytes(t, buildPDF(t, []string{prose}), "report.pdf")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !got.Usable {
		t.Fatalf("Usable = false (reason %q), want a PDF with a text layer to be usable", got.Reason)
	}
	for _, word := range []string{"hellebores", "epimedium", "leaf"} {
		if !strings.Contains(got.Text, word) {
			t.Errorf("extracted text is missing %q; got %q", word, got.Text)
		}
	}
}

// TestPDFMatchesTheSameProseAsPlainText is the reason PDF support was worth a
// third-party dependency at all. Parsing a PDF is only useful if the text that
// comes out lands on the same fingerprint as the same words in another format —
// otherwise a report and the PDF someone exported from it stay in separate
// groups and the duplicate is never found.
//
// It lives in this package rather than in fingerprint because what is being
// tested is the extracted text; SimHash is only the instrument that measures it.
func TestPDFMatchesTheSameProseAsPlainText(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(testdataDir, "docs", "text", "report.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	asText, err := extractBytes(t, source, "report.txt")
	if err != nil {
		t.Fatalf("extract .txt: %v", err)
	}
	asPDF, err := extractBytes(t, buildPDF(t, []string{string(source)}), "report.pdf")
	if err != nil {
		t.Fatalf("extract .pdf: %v", err)
	}
	if !asText.Usable || !asPDF.Usable {
		t.Fatalf("usable: txt=%v pdf=%v (pdf reason %q)", asText.Usable, asPDF.Usable, asPDF.Reason)
	}

	textHash, okText := hashing.SimHash(asText.Text)
	pdfHash, okPDF := hashing.SimHash(asPDF.Text)
	if !okText || !okPDF {
		t.Fatalf("no fingerprint: txt=%v pdf=%v", okText, okPDF)
	}

	if d := hashing.Distance(textHash, pdfHash); d > hashing.DefaultTextThreshold {
		t.Errorf("the same prose measured %d apart as .txt and as .pdf, past the %d "+
			"threshold: a document and its own PDF export would never be offered "+
			"as duplicates of each other", d, hashing.DefaultTextThreshold)
	}
}

func TestExtractPDFWithoutTextLayerReadsAsAScan(t *testing.T) {
	got, err := extractBytes(t, buildPDF(t, []string{""}), "scanned.pdf")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if got.Usable {
		t.Fatalf("Usable = true for a page with no text operators; a scan would be "+
			"fingerprinted from nothing and grouped with every other scan. Text: %q", got.Text)
	}
	if got.Reason != ReasonScanned {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonScanned)
	}
}

// TestExtractSurvivesMalformedPDF is the test the recover() in extractPDF exists
// for. A panic here does not fail one assertion, it takes down the whole test
// binary — exactly as it would take down a scan worker and lose every result
// gathered from the drive so far.
func TestExtractSurvivesMalformedPDF(t *testing.T) {
	valid := buildPDF(t, []string{prose})

	corrupt := map[string][]byte{
		"truncated to 200 bytes": valid[:200],
		"header only":            []byte("%PDF-1.4\n"),
		"xref offsets are lies":  bytes.Replace(valid, []byte("0000000009"), []byte("0000009999"), 1),
		"body replaced by zeros": append(append([]byte{}, valid[:64]...), make([]byte, 512)...),
		"empty":                  {},
	}

	for name, data := range corrupt {
		t.Run(name, func(t *testing.T) {
			got, err := Extract(bytes.NewReader(data), "broken.pdf")
			// Either outcome is acceptable — a hard error, or no usable text.
			// What is not acceptable is a panic, and reaching this line at all
			// is the assertion.
			if err == nil && got.Usable {
				t.Errorf("a corrupt PDF came back usable with text %q", got.Text)
			}
		})
	}
}

func TestExtractPDFStopsAtThePageCap(t *testing.T) {
	pages := make([]string, MaxPDFPages+5)
	for i := range pages {
		pages[i] = fmt.Sprintf("Page %d. %s", i+1, prose)
	}

	got, err := extractBytes(t, buildPDF(t, pages), "long.pdf")
	if err != nil {
		t.Fatalf("Extract: %v", err)
	}
	if !got.Usable {
		t.Fatalf("Usable = false (reason %q)", got.Reason)
	}
	if !strings.Contains(got.Text, fmt.Sprintf("Page %d.", MaxPDFPages)) {
		t.Errorf("page %d is missing; the cap should read up to it, not stop short", MaxPDFPages)
	}
	if beyond := fmt.Sprintf("Page %d.", MaxPDFPages+1); strings.Contains(got.Text, beyond) {
		t.Errorf("found %q: the page cap is not being applied, so one enormous "+
			"document can hold a worker for as long as it likes", beyond)
	}
}

func TestPDFTextIsThinScalesWithFileSize(t *testing.T) {
	tests := []struct {
		name  string
		runes int
		size  int
		want  bool
	}{
		{"a scan with a watermark", 300, 4 << 20, true},
		{"no text at all", 0, 4096, true},
		{"honest prose in a small file", 2000, 40 << 10, false},
		{"a short note in a tiny file", 200, 900, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pdfTextIsThin(strings.Repeat("a", tt.runes), tt.size); got != tt.want {
				t.Errorf("pdfTextIsThin(%d runes, %d bytes) = %v, want %v",
					tt.runes, tt.size, got, tt.want)
			}
		})
	}
}
