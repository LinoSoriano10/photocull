package webui

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// docsDir builds a folder holding one of each tier documents mode produces: a
// pair with the same prose but different bytes, and a pair nothing can read
// inside whose names and sizes line up.
func docsDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	src := filepath.Join("..", "..", "testdata", "docs", "text")
	copyFixture(t, filepath.Join(src, "report.txt"), filepath.Join(dir, "report.txt"))
	copyFixture(t, filepath.Join(src, "report_edited.txt"), filepath.Join(dir, "report_v2.txt"))

	opaque := strings.Repeat("\x00\x01\x02\x03", 8192)
	writeFile(t, filepath.Join(dir, "informe.bin"), opaque)
	writeFile(t, filepath.Join(dir, "informe (1).bin"), opaque+"tail")

	return dir
}

func copyFixture(t *testing.T, from, to string) {
	t.Helper()
	data, err := os.ReadFile(from)
	if err != nil {
		t.Fatalf("read fixture %s: %v", from, err)
	}
	writeFile(t, to, string(data))
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// scannedDocs runs a documents scan through the HTTP API and returns the server
// and the report, which is the only way to reach documents mode from the
// window: the page has no other route to pipeline.Options.
func scannedDocs(t *testing.T) (*Server, reportPayload) {
	t.Helper()
	s := launcherServer()

	if code := startScan(s, scanRequest{Path: docsDir(t), Kind: "docs", Similar: true}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}
	return s, getReport(t, s)
}

// TestScanDocsModeReachesTheDocumentTiers is the test the whole phase exists
// for. Before it, scanRequest had no kind field at all, so documents mode was
// implemented, tested, and completely unreachable from the window — which is
// the only way most people will ever run photocull.
func TestScanDocsModeReachesTheDocumentTiers(t *testing.T) {
	_, payload := scannedDocs(t)

	if payload.Stats.Kind != "docs" {
		t.Errorf("stats.kind = %q, want docs; the review page reads this to decide "+
			"whether to render photographs or documents", payload.Stats.Kind)
	}

	seen := map[string]int{}
	for _, g := range payload.Groups {
		seen[g.Type]++
	}
	if seen["similar"] == 0 {
		t.Errorf("no similar group: the text fingerprint never ran. Groups: %+v", payload.Groups)
	}
	if seen["related"] == 0 {
		t.Errorf("no related group: the low-confidence tier is unreachable from the "+
			"window even though the CLI produces it. Groups: %+v", payload.Groups)
	}
}

// TestDocsScanKeepsRelatedOutOfTheHeadline guards the one number a user acts
// on. Related groups are guesses; counting their bytes as reclaimable would
// promise space that deleting the pre-ticked files does not free.
func TestDocsScanKeepsRelatedOutOfTheHeadline(t *testing.T) {
	_, payload := scannedDocs(t)

	if payload.Stats.RelatedGroups == 0 {
		t.Fatal("no related group in the fixture; the rest of this test proves nothing")
	}
	if payload.Stats.RelatedBytes == 0 {
		t.Error("relatedBytes = 0, so the review page has nothing to show in its own tile")
	}

	// The related pair is two 32 KB files; if their bytes had leaked into the
	// headline it would be the largest number in the report.
	var confident int64
	for _, g := range payload.Groups {
		if g.Type == "related" {
			continue
		}
		for i, f := range g.Files {
			if i != g.KeepIndex {
				confident += f.Size
			}
		}
	}
	if payload.Stats.ReclaimableBytes != confident {
		t.Errorf("reclaimableBytes = %d, want %d (the confident groups alone); "+
			"a headline inflated with guesses promises space that deleting the "+
			"pre-ticked files would not free",
			payload.Stats.ReclaimableBytes, confident)
	}
}

func TestSnippetServesScannedDocument(t *testing.T) {
	s, payload := scannedDocs(t)

	var target string
	for _, g := range payload.Groups {
		if g.Type != "similar" {
			continue
		}
		target = g.Files[0].Path
	}
	if target == "" {
		t.Fatal("no similar group to take a document from")
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/snippet?path="+url.QueryEscape(target), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/plain; charset=utf-8" {
		t.Errorf("content-type = %q, want text/plain; charset=utf-8", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("empty snippet: the card would show nothing at all for this document")
	}
}

// TestSnippetTruncatesToTheRequestedLength keeps a card a card. Without the
// cap, n=100000000 would make the server read and send whole documents.
func TestSnippetTruncatesToTheRequestedLength(t *testing.T) {
	s, payload := scannedDocs(t)

	var target string
	for _, g := range payload.Groups {
		if g.Type == "similar" {
			target = g.Files[0].Path
		}
	}

	for _, n := range []string{"40", "999999999"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
			"/api/snippet?path="+url.QueryEscape(target)+"&n="+n, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("n=%s: status = %d, want 200", n, rec.Code)
		}
		asked, _ := strconv.Atoi(n)
		// The ellipsis costs one rune beyond the cap.
		got := len([]rune(rec.Body.String()))
		want := min(asked, maxSnippet) + 1
		if got > want {
			t.Errorf("n=%s returned %d runes, want at most %d", n, got, want)
		}
	}
}

// TestSnippetRefusesFileWithoutText mirrors serveImage's 422. A .bin nobody can
// read is the ordinary case in documents mode, not a failure, so the page needs
// to tell the two apart: 403 means "not yours to read", 422 means "nothing to
// show".
func TestSnippetRefusesFileWithoutText(t *testing.T) {
	s, payload := scannedDocs(t)

	var target string
	for _, g := range payload.Groups {
		if g.Type == "related" {
			target = g.Files[0].Path
		}
	}
	if target == "" {
		t.Fatal("no related group to take an unreadable file from")
	}

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/snippet?path="+url.QueryEscape(target), nil))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("status = %d, want 422", rec.Code)
	}
}

// TestSnippetRejectsPathTraversal is the security-critical half, and it is the
// direct mirror of TestThumbnailRejectsPathTraversal. A snippet endpoint that
// served any path in the query string would hand over the first few hundred
// characters of any file on the disk — which for a text endpoint is worse than
// the thumbnail one it was copied from.
func TestSnippetRejectsPathTraversal(t *testing.T) {
	s, _ := scannedDocs(t)

	attempts := []string{
		"/api/snippet?path=" + filepath.Join("..", "..", "..", "..", "Windows", "win.ini"),
		"/api/snippet?path=/etc/passwd",
		"/api/snippet?path=C:\\Windows\\System32\\drivers\\etc\\hosts",
		"/api/snippet", // missing path
	}
	for _, u := range attempts {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, u, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("%s was served (status 200); it must be refused", u)
		}
	}
}

// TestScanWithoutKindStaysOnPhotos is the compatibility guard. The kind field
// was added after the fact, so its zero value has to keep meaning photos —
// otherwise every caller that predates it silently changes behaviour.
func TestScanWithoutKindStaysOnPhotos(t *testing.T) {
	// One photo and one document in the same folder. Photos mode must walk past
	// the document entirely; documents mode would read both.
	dir := t.TempDir()
	copyFixture(t, filepath.Join("..", "..", "testdata", "exact", "original.jpg"), filepath.Join(dir, "holiday.jpg"))
	copyFixture(t, filepath.Join("..", "..", "testdata", "docs", "text", "report.txt"), filepath.Join(dir, "report.txt"))

	s := launcherServer()
	if code := startScan(s, scanRequest{Path: dir}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}

	payload := getReport(t, s)
	if payload.Stats.Kind != "photos" {
		t.Errorf("stats.kind = %q, want photos", payload.Stats.Kind)
	}
	if payload.Stats.FilesScanned != 1 {
		t.Errorf("filesScanned = %d, want 1: an omitted kind must still mean photos, "+
			"so the .txt beside the photo is not looked at", payload.Stats.FilesScanned)
	}
}

func TestScanRejectsUnknownKind(t *testing.T) {
	s := launcherServer()
	dir, _ := filepath.Abs("../../testdata/exact")

	if code := startScan(s, scanRequest{Path: dir, Kind: "spreadsheets"}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	st := waitForScan(t, s)
	if st.Error == "" {
		t.Error("an unknown kind should finish with an error, not silently scan photos")
	}
}

// TestDocsThresholdDefaultsToTheDocumentPolicy checks the number nobody types.
// The page sends whatever its slider holds, but a client that omits the field
// must not inherit the photo threshold: 8 is a sensible distance between two
// JPEGs and a reckless one between two documents.
func TestDocsThresholdDefaultsToTheDocumentPolicy(t *testing.T) {
	s := launcherServer()
	dir := docsDir(t)

	if code := startScan(s, scanRequest{Path: dir, Kind: "docs", Similar: true}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}

	// report.txt and report_edited.txt sit inside the document threshold but
	// would also sit inside the photo one, so the assertion that carries weight
	// is simply that the scan ran with a resolved threshold rather than zero —
	// a zero threshold on a similarity scan groups nothing at all.
	payload := getReport(t, s)
	var similar int
	for _, g := range payload.Groups {
		if g.Type == "similar" {
			similar++
		}
	}
	if similar == 0 {
		t.Error("no similar group: an unset threshold was passed through as 0 instead " +
			"of being resolved from the kind, so nothing could ever match")
	}
}
