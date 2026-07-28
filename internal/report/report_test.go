package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"photocull/internal/dedupe"
	"photocull/internal/scanner"
)

func file(path, sha string, size int64) scanner.FileMeta {
	return scanner.FileMeta{Path: path, SHA256: sha, Size: size, Width: 100, Height: 100, Decoded: true}
}

func sampleResult() *scanner.Result {
	return &scanner.Result{
		Files: []scanner.FileMeta{
			file("a.jpg", "h1", 1000), file("b.jpg", "h1", 1000),
			file("c.jpg", "h2", 500), file("d.jpg", "h2", 500), file("e.jpg", "h2", 500),
			file("solo.jpg", "h3", 200),
		},
		Errors: []scanner.ScanError{
			{Path: "bad.jpg", Kind: scanner.ErrorRead},
			{Path: "broken.jpg", Kind: scanner.ErrorDecode},
		},
		Duration: 1500 * time.Millisecond,
	}
}

func TestBuildStats(t *testing.T) {
	res := sampleResult()
	groups := dedupe.GroupExact(res.Files, dedupe.DefaultKeepStrategy)
	s := Build(res, groups)

	if s.FilesScanned != 6 {
		t.Errorf("FilesScanned = %d, want 6", s.FilesScanned)
	}
	if s.Groups != 2 {
		t.Errorf("Groups = %d, want 2", s.Groups)
	}
	if s.ExactGroups != 2 {
		t.Errorf("ExactGroups = %d, want 2", s.ExactGroups)
	}
	// Group h1: 2 files -> 1 duplicate; group h2: 3 files -> 2 duplicates.
	if s.DuplicateFiles != 3 {
		t.Errorf("DuplicateFiles = %d, want 3", s.DuplicateFiles)
	}
	// Reclaimable: one 1000-byte copy + two 500-byte copies = 2000.
	if s.ReclaimableBytes != 2000 {
		t.Errorf("ReclaimableBytes = %d, want 2000", s.ReclaimableBytes)
	}
	if s.ReadErrors != 1 || s.DecodeErrors != 1 {
		t.Errorf("errors: read=%d decode=%d, want 1 and 1", s.ReadErrors, s.DecodeErrors)
	}
}

func TestSummaryMentionsKeyNumbers(t *testing.T) {
	res := sampleResult()
	groups := dedupe.GroupExact(res.Files, dedupe.DefaultKeepStrategy)
	out := Build(res, groups).Summary()

	for _, want := range []string{"6 files", "2 duplicate groups", "could be removed"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary missing %q:\n%s", want, out)
		}
	}
}

func TestSummaryNoDuplicates(t *testing.T) {
	res := &scanner.Result{Files: []scanner.FileMeta{file("a.jpg", "h1", 100)}}
	out := Build(res, nil).Summary()

	if !strings.Contains(out, "No duplicates") {
		t.Errorf("summary should report no duplicates:\n%s", out)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		0:          "0 B",
		512:        "512 B",
		1024:       "1.0 KB",
		1536:       "1.5 KB",
		1048576:    "1.0 MB",
		1073741824: "1.0 GB",
	}
	for n, want := range cases {
		if got := HumanBytes(n); got != want {
			t.Errorf("HumanBytes(%d) = %q, want %q", n, got, want)
		}
	}
}

func TestReportJSONRoundTrips(t *testing.T) {
	res := sampleResult()
	groups := dedupe.GroupExact(res.Files, dedupe.DefaultKeepStrategy)
	rep := Report{Root: "/photos", Stats: Build(res, groups), Groups: groups, Errors: res.Errors}

	data, err := rep.JSON()
	if err != nil {
		t.Fatalf("JSON: %v", err)
	}

	var back Report
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back.Root != "/photos" || back.Stats.Groups != 2 {
		t.Errorf("round trip lost data: %+v", back.Stats)
	}
}
