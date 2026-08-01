package doctext

import (
	"archive/zip"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/text/encoding/unicode"
	"golang.org/x/text/transform"
)

const testdataDir = "../../testdata"

// extractFile runs Extract over a path, failing the test on a hard error.
func extractFile(t *testing.T, path string) Extraction {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()

	got, err := Extract(f, path)
	if err != nil {
		t.Fatalf("Extract(%s): %v", filepath.Base(path), err)
	}
	return got
}

// writeOOXML builds a minimal Office file: a ZIP holding the one or two XML
// parts doctext actually reads.
//
// The repo commits its image fixtures because a JPEG cannot be written legibly
// in Go. An Office file can, so it is built here instead — the test then states
// exactly what it is testing, rather than pointing at a binary nobody can open
// in a diff.
func writeOOXML(t *testing.T, path string, parts map[string]string) {
	t.Helper()

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range parts {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// wordDoc wraps body text in the parts of a WordprocessingML document that
// matter here.
func wordDoc(body string) string {
	return `<?xml version="1.0" encoding="UTF-8"?>` +
		`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">` +
		`<w:body>` + body + `</w:body></w:document>`
}

func TestExtractPlainText(t *testing.T) {
	got := extractFile(t, filepath.Join(testdataDir, "docs", "text", "report.txt"))

	if !got.Usable {
		t.Fatalf("Usable = false (%s) for a page of ordinary prose", got.Reason)
	}
	if !strings.Contains(got.Text, "storage estate") {
		t.Error("the extracted text does not contain a phrase from the file")
	}
}

// TestExtractHandlesUTF16 is not a theoretical case: saving as "Unicode" in
// Windows Notepad produces UTF-16LE with a BOM. Read as UTF-8 those bytes come
// out interleaved with NULs, which normalises to garbage — so the file would
// silently never match its own copies.
func TestExtractHandlesUTF16(t *testing.T) {
	utf8Text := extractFile(t, filepath.Join(testdataDir, "docs", "text", "report.txt"))

	for _, tt := range []struct {
		name string
		enc  encodingCase
	}{
		{"UTF-16LE with BOM", encodingCase{unicode.LittleEndian}},
		{"UTF-16BE with BOM", encodingCase{unicode.BigEndian}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "report.txt")
			writeUTF16(t, path, utf8Text.Text, tt.enc.order)

			got := extractFile(t, path)
			if !got.Usable {
				t.Fatalf("Usable = false (%s)", got.Reason)
			}
			if strings.ContainsRune(got.Text, 0) {
				t.Fatal("the text still contains NUL bytes, so it was read as UTF-8 rather than decoded")
			}
			if got.Text != utf8Text.Text {
				t.Errorf("text differs from the UTF-8 original; the same document in two encodings would not match itself")
			}
		})
	}
}

type encodingCase struct{ order unicode.Endianness }

func writeUTF16(t *testing.T, path, text string, order unicode.Endianness) {
	t.Helper()
	enc := unicode.UTF16(order, unicode.UseBOM).NewEncoder()
	out, _, err := transform.Bytes(enc, []byte(text))
	if err != nil {
		t.Fatalf("encode UTF-16: %v", err)
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

// TestExtractRefusesUnknownFormatWithoutError checks the normal path for most
// of a disk. A .zip is not a failure — it is a file photocull will deduplicate
// by content hash alone, and saying so is not the same as erroring.
func TestExtractRefusesUnknownFormatWithoutError(t *testing.T) {
	path := filepath.Join(testdataDir, "docs", "binary", "archive.zip")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	got, err := Extract(f, path)
	if err != nil {
		t.Fatalf("Extract returned an error for an ordinary .zip: %v", err)
	}
	if got.Usable {
		t.Error("Usable = true for a .zip")
	}
	if got.Reason != ReasonUnsupported {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonUnsupported)
	}
}

func TestExtractDocx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "memo.docx")
	writeOOXML(t, path, map[string]string{
		"word/document.xml": wordDoc(
			`<w:p><w:r><w:t>` + strings.Repeat("The estate holds forty-one terabytes across six drives. ", 4) + `</w:t></w:r></w:p>` +
				`<w:p><w:r><w:t>Provable duplication is a smaller claim than similarity.</w:t></w:r></w:p>`),
		// Boilerplate that must NOT appear: it is repeated across every
		// document from one template.
		"word/header1.xml": wordDoc(`<w:p><w:r><w:t>CONFIDENTIAL DRAFT</w:t></w:r></w:p>`),
	})

	got := extractFile(t, path)
	if !got.Usable {
		t.Fatalf("Usable = false (%s)", got.Reason)
	}
	if !strings.Contains(got.Text, "Provable duplication") {
		t.Error("body text missing from the extraction")
	}
	if strings.Contains(got.Text, "CONFIDENTIAL") {
		t.Error("header text was extracted; template boilerplate makes unrelated documents look alike, which is why it is skipped")
	}
}

func TestExtractXlsx(t *testing.T) {
	path := filepath.Join(t.TempDir(), "book.xlsx")
	writeOOXML(t, path, map[string]string{
		"xl/sharedStrings.xml": `<?xml version="1.0"?><sst xmlns="http://schemas.openxmlformats.org/spreadsheetml/2006/main">` +
			`<si><t>` + strings.Repeat("Regional turnover by quarter and product line. ", 4) + `</t></si>` +
			`<si><t>Total including adjustments and carried forward balances</t></si></sst>`,
	})

	got := extractFile(t, path)
	if !got.Usable {
		t.Fatalf("Usable = false (%s)", got.Reason)
	}
	if !strings.Contains(got.Text, "Regional turnover") || !strings.Contains(got.Text, "carried forward") {
		t.Errorf("shared strings missing from the extraction: %q", got.Text)
	}
}

// TestExtractPptxReadsSlidesInOrder guards reproducibility. Lexical order puts
// slide10 before slide2, and ZIP entry order is whatever the writer chose.
// Slide order barely moves a SimHash, but a fingerprint that varied between
// runs of the same file would make re-scanning a folder produce different
// groups, which is the one thing a fingerprint must never do.
func TestExtractPptxReadsSlidesInOrder(t *testing.T) {
	parts := map[string]string{}
	for i := 1; i <= 12; i++ {
		parts[fmt.Sprintf("ppt/slides/slide%d.xml", i)] =
			fmt.Sprintf(`<?xml version="1.0"?><p:sld xmlns:a="x" xmlns:p="y"><a:p><a:t>marker%02d and some words to pad the slide out</a:t></a:p></p:sld>`, i)
	}
	path := filepath.Join(t.TempDir(), "deck.pptx")
	writeOOXML(t, path, parts)

	got := extractFile(t, path)
	if !got.Usable {
		t.Fatalf("Usable = false (%s)", got.Reason)
	}

	var positions []int
	for i := 1; i <= 12; i++ {
		idx := strings.Index(got.Text, fmt.Sprintf("marker%02d", i))
		if idx < 0 {
			t.Fatalf("slide %d missing from the extraction", i)
		}
		positions = append(positions, idx)
	}
	for i := 1; i < len(positions); i++ {
		if positions[i] < positions[i-1] {
			t.Fatalf("slide %d appears before slide %d: slides are being read in lexical rather than numeric order", i+1, i)
		}
	}
}

func TestExtractRejectsBrokenOfficeFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "broken.docx")
	if err := os.WriteFile(path, []byte("this is not a zip archive at all"), 0o644); err != nil {
		t.Fatal(err)
	}

	f, _ := os.Open(path)
	defer f.Close()
	if _, err := Extract(f, path); err == nil {
		t.Error("Extract accepted a .docx that is not a ZIP; a file claiming a format it is not should be reported, not silently treated as empty")
	}
}

// TestExtractSurvivesZipBomb checks the ceiling. A one-kilobyte .docx can
// declare gigabytes of decompressed content, and a worker per CPU core all
// doing that at once would exhaust memory.
func TestExtractSurvivesZipBomb(t *testing.T) {
	// Highly compressible XML that expands well past MaxText.
	huge := wordDoc(`<w:p><w:r><w:t>` + strings.Repeat("A", MaxText+(1<<20)) + `</w:t></w:r></w:p>`)
	path := filepath.Join(t.TempDir(), "bomb.docx")
	writeOOXML(t, path, map[string]string{"word/document.xml": huge})

	f, _ := os.Open(path)
	defer f.Close()

	got, err := Extract(f, path)
	// Either outcome is acceptable — refused outright, or truncated at the
	// ceiling. What must not happen is unbounded growth.
	if err == nil && len(got.Text) > MaxText {
		t.Errorf("extracted %d bytes of text, above the %d ceiling", len(got.Text), MaxText)
	}
}

func TestExtractStopsAtOversizeDocuments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "huge.txt")
	if err := os.WriteFile(path, bytes.Repeat([]byte("word "), (MaxBytes/5)+1000), 0o644); err != nil {
		t.Fatal(err)
	}

	got := extractFile(t, path)
	if got.Usable {
		t.Error("Usable = true for a document past the size ceiling")
	}
	if got.Reason != ReasonTooLarge {
		t.Errorf("Reason = %q, want %q", got.Reason, ReasonTooLarge)
	}
}

// TestXMLTextJoinsRunsWithoutInventingSpaces is the pure-string heart of the
// OOXML reader. Word splits a word across runs whenever formatting or the spell
// checker touches it, so runs must join with nothing between them while
// paragraphs must break. Getting it backwards changes the token stream and
// therefore the fingerprint.
func TestXMLTextJoinsRunsWithoutInventingSpaces(t *testing.T) {
	shape := ooxmlPart{breakOn: map[string]bool{"p": true}, spaceOn: map[string]bool{"tab": true, "br": true}}

	tests := []struct {
		name string
		xml  string
		want string
	}{
		{
			name: "a word split across runs rejoins",
			xml:  `<doc><p><r><t>Hel</t></r><r><t>lo</t></r></p></doc>`,
			want: "Hello\n",
		},
		{
			name: "paragraphs break",
			xml:  `<doc><p><t>one</t></p><p><t>two</t></p></doc>`,
			want: "one\ntwo\n",
		},
		{
			name: "tabs and breaks become spaces",
			xml:  `<doc><p><t>a</t><tab/><t>b</t><br/><t>c</t></p></doc>`,
			want: "a b c\n",
		},
		{
			name: "entities are decoded, not left raw",
			xml:  `<doc><p><t>Bread &amp; butter</t></p></doc>`,
			want: "Bread & butter\n",
		},
		{
			name: "CDATA survives",
			xml:  `<doc><p><t><![CDATA[a < b & c]]></t></p></doc>`,
			want: "a < b & c\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			remaining := MaxText
			got, err := xmlText(strings.NewReader(tt.xml), shape, &remaining)
			if err != nil {
				t.Fatalf("xmlText: %v", err)
			}
			if got != tt.want {
				t.Errorf("xmlText = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestClassifyRejectsThinText(t *testing.T) {
	tests := []struct {
		name       string
		text       string
		wantReason string
	}{
		{"empty", "", ReasonEmpty},
		{"whitespace only", "   \n\t ", ReasonEmpty},
		{"a caption, not a document", "Figure 3: the north bed in April", ReasonThin},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classify(tt.text)
			if got.Usable {
				t.Errorf("Usable = true for %q", tt.text)
			}
			if got.Reason != tt.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tt.wantReason)
			}
		})
	}
}

func TestHandlesKnownExtensions(t *testing.T) {
	cases := map[string]bool{
		"notes.txt": true,
		"NOTES.TXT": true,
		// Dropping .pdf from DefaultExtensions is the whole rollback for PDF
		// support: every PDF then falls back to the content-hash-and-related
		// path it took before, with no other change anywhere.
		"report.pdf":  true,
		"memo.docx":   true,
		"book.xlsx":   true,
		"deck.pptx":   true,
		"readme.md":   true,
		"data.csv":    true,
		"archive.zip": false,
		"song.mp3":    false,
		"old.doc":     false, // legacy OLE2, deliberately out of scope
		"noext":       false,
	}
	for path, want := range cases {
		if got := Handles(path); got != want {
			t.Errorf("Handles(%q) = %v, want %v", path, got, want)
		}
	}
}
