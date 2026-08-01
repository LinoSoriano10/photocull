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

// TestStatsExcludeRelatedFromTheHeadline is the honest-reporting test.
//
// It is tempting to fold related groups into "N files could be removed, freeing
// X" because it makes the tool look more useful. They are guesses, and a
// headline built on guesses is a lie that gets acted on.
func TestStatsExcludeRelatedFromTheHeadline(t *testing.T) {
	confident := dedupe.Group{
		Type:      dedupe.Exact,
		KeepIndex: 0,
		Files: []scanner.FileMeta{
			{Path: "a.txt", Size: 1000},
			{Path: "b.txt", Size: 1000},
		},
	}
	guess := dedupe.Group{
		Type:      dedupe.Related,
		KeepIndex: 0,
		Files: []scanner.FileMeta{
			{Path: "c.pdf", Size: 5000},
			{Path: "d.pdf", Size: 5000},
		},
	}

	counts := CountGroups([]dedupe.Group{confident, guess})

	if counts.ReclaimableBytes != 1000 {
		t.Errorf("ReclaimableBytes = %d, want 1000: the related group's bytes were counted as reclaimable", counts.ReclaimableBytes)
	}
	if counts.DuplicateFiles != 1 {
		t.Errorf("DuplicateFiles = %d, want 1: the related group's files were counted as removable", counts.DuplicateFiles)
	}
	if counts.Related != 1 || counts.RelatedFiles != 1 || counts.RelatedBytes != 5000 {
		t.Errorf("related tally = %d groups / %d files / %d bytes, want 1 / 1 / 5000", counts.Related, counts.RelatedFiles, counts.RelatedBytes)
	}
	if counts.Exact != 1 {
		t.Errorf("Exact = %d, want 1: a related group was counted as an exact one", counts.Exact)
	}
}

// TestSummaryReportsRelatedSeparately checks the wording as well as the
// arithmetic: the user has to be told these were not read, not just shown a
// smaller number.
func TestSummaryReportsRelatedSeparately(t *testing.T) {
	s := Stats{
		FilesScanned:     4,
		Groups:           2,
		ExactGroups:      1,
		DuplicateFiles:   1,
		ReclaimableBytes: 1000,
		RelatedGroups:    1,
		RelatedFiles:     1,
		RelatedBytes:     5000,
	}

	got := s.Summary()
	if !strings.Contains(got, "1 duplicate group") {
		t.Errorf("summary counts related groups in the duplicate total:\n%s", got)
	}
	if !strings.Contains(got, "Nothing is pre-selected") {
		t.Errorf("summary does not warn that the related groups are unreviewed:\n%s", got)
	}
}

// TestRenderGroupsMarksRelatedForReviewNotDeletion guards a one-word promise.
//
// clean refuses to touch a related group, so a table that printed "delete"
// beside those files would be telling the user something the tool deliberately
// will not do — and inviting them to go and do it by hand.
func TestRenderGroupsMarksRelatedForReviewNotDeletion(t *testing.T) {
	groups := []dedupe.Group{{
		Type:      dedupe.Related,
		KeepIndex: 0,
		Files: []scanner.FileMeta{
			{Path: "informe.pdf", Size: 4000, ModTime: time.Now()},
			{Path: "informe (1).pdf", Size: 4010, ModTime: time.Now()},
		},
	}}

	var buf strings.Builder
	RenderGroups(&buf, "", groups)
	got := buf.String()

	if strings.Contains(got, "delete") {
		t.Errorf("a related group is marked for deletion; clean will not act on it:\n%s", got)
	}
	if !strings.Contains(got, "review") {
		t.Errorf("a related group is not marked for review:\n%s", got)
	}
	if !strings.Contains(got, "if they turn out to be copies") {
		t.Errorf("the header states the bytes as reclaimable rather than as a guess:\n%s", got)
	}
}

// TestRenderGroupsDropsTheDimensionsColumnForDocuments keeps the table
// readable. On a documents scan nothing has a resolution, so the column would
// be a row of question marks down the whole report — which is worse than no
// column, because it reads as missing data rather than an inapplicable one.
func TestRenderGroupsDropsTheDimensionsColumnForDocuments(t *testing.T) {
	docs := []dedupe.Group{{
		Type:      dedupe.Similar,
		KeepIndex: 0,
		Files: []scanner.FileMeta{
			{Path: "report.txt", Size: 1000, Decoded: true},
			{Path: "report_v2.txt", Size: 1010, Decoded: true},
		},
	}}
	var docBuf strings.Builder
	RenderGroups(&docBuf, "", docs)
	if strings.Contains(docBuf.String(), "?") {
		t.Errorf("documents were given a dimensions column of question marks:\n%s", docBuf.String())
	}

	// A photo scan must still get it.
	photos := []dedupe.Group{{
		Type:      dedupe.Similar,
		KeepIndex: 0,
		Files:     []scanner.FileMeta{file("a.jpg", "h", 1000), file("b.jpg", "h", 1000)},
	}}
	var photoBuf strings.Builder
	RenderGroups(&photoBuf, "", photos)
	if !strings.Contains(photoBuf.String(), "100x100") {
		t.Errorf("photos lost their dimensions column:\n%s", photoBuf.String())
	}
}

// TestRecountKeepsScanTotalsAndRefreshesTheRest covers what happens after a
// delete in the review window. The scan-time totals describe a walk that
// already happened and must not change; everything derived from the groups has
// to follow them down, or the page shows figures for files that are gone.
func TestRecountKeepsScanTotalsAndRefreshesTheRest(t *testing.T) {
	before := Stats{
		FilesScanned:     6,
		TotalBytes:       9000,
		ReadErrors:       1,
		Groups:           2,
		ExactGroups:      2,
		DuplicateFiles:   3,
		ReclaimableBytes: 2000,
	}

	// One group survives the delete; the other is gone entirely.
	after := Recount(before, []dedupe.Group{{
		Type:      dedupe.Exact,
		KeepIndex: 0,
		Files:     []scanner.FileMeta{file("c.jpg", "h2", 500), file("d.jpg", "h2", 500)},
	}})

	if after.FilesScanned != 6 || after.TotalBytes != 9000 || after.ReadErrors != 1 {
		t.Errorf("scan-time totals changed: %+v", after)
	}
	if after.Groups != 1 || after.ExactGroups != 1 {
		t.Errorf("group counts = %d/%d, want 1/1", after.Groups, after.ExactGroups)
	}
	if after.DuplicateFiles != 1 || after.ReclaimableBytes != 500 {
		t.Errorf("derived figures = %d files / %d bytes, want 1 / 500: the page would "+
			"still be offering space from files already in the recycle bin",
			after.DuplicateFiles, after.ReclaimableBytes)
	}
}
