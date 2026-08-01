package dedupe

import (
	"testing"

	"photocull/internal/scanner"
)

// relatedFile builds an unreadable file of a given name and size — the only two
// things the related tier gets to look at.
func relatedFile(path string, size int64) scanner.FileMeta {
	return withSize(file(path, "sha-"+path), size)
}

func TestNormaliseStemStripsCopySuffixes(t *testing.T) {
	tests := []struct {
		name string
		path string
		want string
	}{
		{"windows explorer copy", "Informe (1).pdf", "informe"},
		{"repeated explorer copy", "Informe (12).pdf", "informe"},
		{"spanish copy suffix", "Informe - copia.pdf", "informe"},
		{"spanish copy prefix", "Copia de Informe.pdf", "informe"},
		{"english copy suffix", "Report - Copy.pdf", "report"},
		{"english copy prefix", "Copy of Report.pdf", "report"},
		{"stacked suffixes", "Informe - copia (2).pdf", "informe"},
		{"version suffix", "report_v2.docx", "report"},
		{"stage suffix", "report-final.docx", "report"},
		{"short trailing counter", "note 1.txt", "note"},
		{"case and punctuation folded", "My__Report!!.PDF", "my report"},

		// The important negative: four digits are a year or an invoice number,
		// and stripping them would collapse two genuinely different documents
		// onto one stem. This is the worst false positive this tier can make.
		// Words that merely end in a suffix word must survive: the separator is
		// what makes a suffix a suffix.
		{"rev inside a word", "report-rev3.docx", "report"},
		{"a word ending in borrador", "elaborador.docx", "elaborador"},
		{"a word ending in copia", "recopia.docx", "recopia"},
		{"a v-number inside a word", "revision7.docx", "revision7"},

		{"a year is not a counter", "budget-2024.xlsx", "budget 2024"},
		{"another year", "budget-2023.xlsx", "budget 2023"},
		{"an invoice number is not a counter", "invoice-40817.pdf", "invoice 40817"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := normaliseStem(tt.path); got != tt.want {
				t.Errorf("normaliseStem(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

func TestNormaliseStemKeepsYearsApart(t *testing.T) {
	if normaliseStem("budget-2023.xlsx") == normaliseStem("budget-2024.xlsx") {
		t.Error("two years collapsed onto one stem; the tier would group a document with last year's version of itself and offer one for deletion")
	}
}

// TestGroupRelatedIgnoresFilesThatHaveText is the constraint that keeps this
// tier from adding noise. Where the contents *were* read, the fingerprint is a
// strictly better signal, and a name match on top of it could only introduce
// false positives into a group that was already decided correctly.
func TestGroupRelatedIgnoresFilesThatHaveText(t *testing.T) {
	files := []scanner.FileMeta{
		withFingerprint(relatedFile("report.pdf", 100_000), 0x1234),
		withFingerprint(relatedFile("report (1).pdf", 100_000), 0x1234),
	}

	if groups := GroupRelated(files, nil, DefaultKeepStrategy); len(groups) != 0 {
		t.Errorf("got %d group(s), want 0: files whose contents were read must be left to the fingerprint tier", len(groups))
	}
}

func TestGroupRelatedSkipsFilesAlreadyGrouped(t *testing.T) {
	files := []scanner.FileMeta{
		relatedFile("report.pdf", 100_000),
		relatedFile("report (1).pdf", 100_000),
	}
	exclude := map[string]bool{"report.pdf": true}

	if groups := GroupRelated(files, exclude, DefaultKeepStrategy); len(groups) != 0 {
		t.Errorf("got %d group(s), want 0: a file already claimed by an exact group must not be reported twice", len(groups))
	}
}

// TestGroupRelatedDoesNotChainUnrelatedFiles is the anti-union-find test, and
// the reason this pass exists separately at all.
//
// Union-find is transitive: it follows a chain of pairwise links and hands the
// whole cluster over as one decision. That is right for a hash. For a filename
// it is not — a chain would link a 1 MB file to a 1.5 MB file through a 1.05 MB
// one, and present all three as copies of each other.
func TestGroupRelatedDoesNotChainUnrelatedFiles(t *testing.T) {
	files := []scanner.FileMeta{
		relatedFile("informe.pdf", 1_000_000),
		relatedFile("informe (1).pdf", 1_050_000), // within 10% of the 1.05 MB seed
		relatedFile("informe - copia.pdf", 1_500_000),
	}

	groups := GroupRelated(files, nil, DefaultKeepStrategy)
	if len(groups) != 1 {
		t.Fatalf("got %d group(s), want exactly 1", len(groups))
	}
	if n := len(groups[0].Files); n != 2 {
		t.Errorf("group holds %d files, want 2: the 1.5 MB file was chained in through the 1.05 MB one, so the group's two ends have nothing to do with each other", n)
	}
	for _, f := range groups[0].Files {
		if f.Size == 1_500_000 {
			t.Error("the 1.5 MB file ended up grouped with the 1.0 MB one")
		}
	}
}

func TestGroupRelatedRespectsSizeProximity(t *testing.T) {
	files := []scanner.FileMeta{
		relatedFile("informe.pdf", 1_000_000),
		relatedFile("informe (1).pdf", 4_000_000), // nothing like the same size
	}

	if groups := GroupRelated(files, nil, DefaultKeepStrategy); len(groups) != 0 {
		t.Errorf("got %d group(s), want 0: two files four times apart in size are not copies whatever they are called", len(groups))
	}
}

func TestGroupRelatedDropsGenericStems(t *testing.T) {
	files := []scanner.FileMeta{
		relatedFile("a.pdf", 100_000),
		relatedFile("a (1).pdf", 100_000),
	}

	if groups := GroupRelated(files, nil, DefaultKeepStrategy); len(groups) != 0 {
		t.Errorf("got %d group(s), want 0: a one-letter stem carries no evidence at all", len(groups))
	}
}

// TestGroupRelatedDropsOverlargeGroups checks the cap. A stem that attracts
// dozens of files is a common word, not a document name, and forty files is
// more than anyone will review.
func TestGroupRelatedDropsOverlargeGroups(t *testing.T) {
	var files []scanner.FileMeta
	for i := range MaxRelatedGroup + 5 {
		files = append(files, relatedFile(string(rune('a'+i))+"/scanned document.pdf", 100_000))
	}

	if groups := GroupRelated(files, nil, DefaultKeepStrategy); len(groups) != 0 {
		t.Errorf("got %d group(s), want 0: a stem shared by %d files is a word, not a finding", len(groups), len(files))
	}
}

// TestGroupRelatedAlwaysHasAUsableKeepIndex guards a trap rather than a
// preference. Group.Duplicates() returns *every* file when KeepIndex is out of
// range, so a sentinel like -1 would make clean collect the whole group.
func TestGroupRelatedAlwaysHasAUsableKeepIndex(t *testing.T) {
	files := []scanner.FileMeta{
		relatedFile("informe.pdf", 1_000_000),
		relatedFile("informe (1).pdf", 1_000_000),
		relatedFile("informe - copia.pdf", 1_000_000),
	}

	groups := GroupRelated(files, nil, DefaultKeepStrategy)
	if len(groups) != 1 {
		t.Fatalf("got %d group(s), want 1", len(groups))
	}
	g := groups[0]

	if g.KeepIndex < 0 || g.KeepIndex >= len(g.Files) {
		t.Fatalf("KeepIndex = %d for a group of %d: Duplicates() would return every file, and clean would collect the lot", g.KeepIndex, len(g.Files))
	}
	if n := len(g.Duplicates()); n != len(g.Files)-1 {
		t.Errorf("Duplicates() returned %d of %d files, want %d", n, len(g.Files), len(g.Files)-1)
	}
}

func TestGroupRelatedMarksItsGroupsRelated(t *testing.T) {
	files := []scanner.FileMeta{
		relatedFile("informe.pdf", 1_000_000),
		relatedFile("informe (1).pdf", 1_000_000),
	}

	groups := GroupRelated(files, nil, DefaultKeepStrategy)
	if len(groups) != 1 {
		t.Fatalf("got %d group(s), want 1", len(groups))
	}
	if groups[0].Type != Related {
		t.Errorf("Type = %q, want %q: anything else would let clean delete these", groups[0].Type, Related)
	}
}
