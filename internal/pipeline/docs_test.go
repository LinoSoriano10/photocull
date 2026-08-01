package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"photocull/internal/dedupe"
)

// TestRunDocsProducesAllThreeTiers is the end-to-end check that documents mode
// actually works, and that its three tiers stay distinct.
//
// It is deliberately one scan rather than three, because the property that
// matters most is the one only a whole run can show: no file may appear in two
// groups. A file counted twice would be offered for deletion twice, on two
// different sets of evidence, and the weaker one could win.
func TestRunDocsProducesAllThreeTiers(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join("..", "..", "testdata", "docs", "text")

	// exact: a document and a byte-identical copy, with nothing else resembling
	// it. It has to stand alone, because a similarity scan deliberately merges
	// an identical pair into the same group as anything similar to it — see
	// TestRunDocsMergesIdenticalAndSimilarIntoOneGroup below.
	copyInto(t, filepath.Join(docs, "unrelated.txt"), filepath.Join(dir, "planting.txt"))
	copyInto(t, filepath.Join(docs, "unrelated.txt"), filepath.Join(dir, "backup", "planting.txt"))

	// similar: the same document with a few words changed. Different bytes, so
	// only the text fingerprint can find it.
	copyInto(t, filepath.Join(docs, "report.txt"), filepath.Join(dir, "report.txt"))
	copyInto(t, filepath.Join(docs, "report_edited.txt"), filepath.Join(dir, "report_v2.txt"))

	// related: two files nothing can read inside, with matching names and sizes.
	opaque := strings.Repeat("\x00\x01\x02\x03", 8192)
	write(t, filepath.Join(dir, "informe.bin"), opaque)
	write(t, filepath.Join(dir, "informe (1).bin"), opaque+"tail")

	// and one document that resembles nothing at all.
	write(t, filepath.Join(dir, "alone.txt"), strings.Repeat("a sentence with entirely its own words and nothing shared. ", 40))

	an, err := Run(t.Context(), Options{Root: dir, Kind: "docs", Similar: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	byType := map[dedupe.MatchType][]dedupe.Group{}
	for _, g := range an.Groups {
		byType[g.Type] = append(byType[g.Type], g)
	}

	for _, want := range []dedupe.MatchType{dedupe.Exact, dedupe.Similar, dedupe.Related} {
		if len(byType[want]) == 0 {
			t.Errorf("no %q group found; the tier is not reachable end to end", want)
		}
	}

	// No file in two groups.
	seen := map[string]string{}
	for _, g := range an.Groups {
		for _, f := range g.Files {
			if prev, dup := seen[f.Path]; dup {
				t.Errorf("%s appears in both a %q group and a %q group; it would be offered for deletion twice on different evidence",
					filepath.Base(f.Path), prev, g.Type)
			}
			seen[f.Path] = string(g.Type)
		}
	}

	// A document resembling nothing must be in no group at all.
	for path := range seen {
		if filepath.Base(path) == "alone.txt" {
			t.Error("alone.txt was grouped with something; it shares nothing with the other documents")
		}
	}

	// Confident groups come first, so a speculative match can never push a
	// certain one below the fold.
	sawRelated := false
	for _, g := range an.Groups {
		if g.Type == dedupe.Related {
			sawRelated = true
			continue
		}
		if sawRelated {
			t.Error("a confident group was ordered after a related one; the review view would bury a certain finding under a guess")
		}
	}

	// The headline must exclude the guesses.
	if an.Stats.RelatedGroups == 0 {
		t.Error("Stats.RelatedGroups = 0 although a related group was produced")
	}
	if an.Stats.Kind != "docs" {
		t.Errorf("Stats.Kind = %q, want docs", an.Stats.Kind)
	}
}

// TestRunDocsScansEveryExtension checks the no-blind-spots promise.
func TestRunDocsScansEveryExtension(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.mp3", "b.zip", "c.doc", "d.unknown"} {
		write(t, filepath.Join(dir, name), "distinct content for "+name)
	}

	an, err := Run(t.Context(), Options{Root: dir, Kind: "docs"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if an.Stats.FilesScanned != 4 {
		t.Errorf("scanned %d files, want 4: documents mode must look at every extension or the scan has blind spots", an.Stats.FilesScanned)
	}
}

func TestRunRejectsUnknownKind(t *testing.T) {
	if _, err := Run(t.Context(), Options{Root: t.TempDir(), Kind: "videos"}); err == nil {
		t.Error("Run accepted a kind that does not exist")
	}
}

func copyInto(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	write(t, dst, string(data))
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestRunDocsMergesIdenticalAndSimilarIntoOneGroup pins behaviour inherited
// from the photo side that is easy to mistake for a bug.
//
// A similarity scan unions on both signals at once, so a document, a
// byte-identical backup of it and an edited revision all land in one group
// rather than being reported as two overlapping findings. The group is then
// labelled "similar", because that is the weakest claim that covers every file
// in it — calling it exact would overstate what is known about the revision.
func TestRunDocsMergesIdenticalAndSimilarIntoOneGroup(t *testing.T) {
	dir := t.TempDir()
	docs := filepath.Join("..", "..", "testdata", "docs", "text")

	copyInto(t, filepath.Join(docs, "report.txt"), filepath.Join(dir, "report.txt"))
	copyInto(t, filepath.Join(docs, "report_copy.txt"), filepath.Join(dir, "backup", "report.txt"))
	copyInto(t, filepath.Join(docs, "report_edited.txt"), filepath.Join(dir, "report_v2.txt"))

	an, err := Run(t.Context(), Options{Root: dir, Kind: "docs", Similar: true})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if len(an.Groups) != 1 {
		t.Fatalf("got %d groups, want 1: the identical pair and the revision should be one decision, not two", len(an.Groups))
	}
	if n := len(an.Groups[0].Files); n != 3 {
		t.Errorf("group holds %d files, want 3", n)
	}
	if an.Groups[0].Type != dedupe.Similar {
		t.Errorf("Type = %q, want %q: not every file in the group is byte-identical, so calling it exact would overstate it", an.Groups[0].Type, dedupe.Similar)
	}
}
