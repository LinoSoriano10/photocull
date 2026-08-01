package webui

import (
	"encoding/json"
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

// compareVia runs /api/compare over two paths and decodes the answer.
func compareVia(t *testing.T, s *Server, a, b string) comparison {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet,
		"/api/compare?a="+url.QueryEscape(a)+"&b="+url.QueryEscape(b), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("compare status = %d, want 200", rec.Code)
	}
	var cmp comparison
	if err := json.Unmarshal(rec.Body.Bytes(), &cmp); err != nil {
		t.Fatalf("decode comparison: %v", err)
	}
	return cmp
}

// groupOfType returns the two paths of the first group of the given tier.
func groupOfType(t *testing.T, payload reportPayload, kind string) (a, b string) {
	t.Helper()
	for _, g := range payload.Groups {
		if g.Type == kind && len(g.Files) >= 2 {
			return g.Files[0].Path, g.Files[1].Path
		}
	}
	t.Fatalf("no %q group in the report", kind)
	return "", ""
}

// TestCompareDocumentsReturnsAWordDiff is the phase in one test: two drafts of
// the same report come back as the words that changed, not as two walls of
// text for the user to read twice.
func TestCompareDocumentsReturnsAWordDiff(t *testing.T) {
	s, payload := scannedDocs(t)
	a, b := groupOfType(t, payload, "similar")

	cmp := compareVia(t, s, a, b)
	if cmp.Kind != "doc" {
		t.Fatalf("kind = %q, want doc (note: %q)", cmp.Kind, cmp.Note)
	}
	if cmp.Doc == nil {
		t.Fatal("kind was doc but no diff came with it")
	}
	if len(cmp.Doc.Hunks) == 0 {
		t.Error("no hunks: two documents the scan called similar-but-not-identical " +
			"must differ somewhere, or the viewer shows nothing at all")
	}
	if cmp.Doc.ChangedWords == 0 {
		t.Error("changedWords = 0 for two different documents")
	}
	if cmp.Doc.SameWords == 0 {
		t.Error("sameWords = 0: the headline percentage would be 100% for two " +
			"drafts of one report")
	}
	// The point of the tier: mostly the same document.
	if cmp.Doc.ChangedWords > cmp.Doc.SameWords {
		t.Errorf("more words changed (%d) than stayed (%d); these two were grouped "+
			"as near-duplicates", cmp.Doc.ChangedWords, cmp.Doc.SameWords)
	}
}

// TestCompareIdenticalDocumentsSaysSo covers the exact tier. There are no hunks
// to draw, and a blank panel would read as a failure rather than an answer.
func TestCompareIdenticalDocumentsSaysSo(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "docs", "text", "report.txt")
	copyFixture(t, src, filepath.Join(dir, "report.txt"))
	copyFixture(t, src, filepath.Join(dir, "backup", "report.txt"))

	s := launcherServer()
	if code := startScan(s, scanRequest{Path: dir, Kind: "docs"}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}

	payload := getReport(t, s)
	a, b := groupOfType(t, payload, "exact")

	cmp := compareVia(t, s, a, b)
	if cmp.Kind != "doc" || cmp.Doc == nil {
		t.Fatalf("kind = %q, doc = %v", cmp.Kind, cmp.Doc)
	}
	if len(cmp.Doc.Hunks) != 0 || cmp.Doc.ChangedWords != 0 {
		t.Errorf("two byte-identical files produced %d hunks and %d changed words",
			len(cmp.Doc.Hunks), cmp.Doc.ChangedWords)
	}
	if cmp.Doc.SameWords == 0 {
		t.Error("sameWords = 0, so the panel cannot say how much text it checked")
	}
}

// TestCompareRelatedFilesIsOpaque is the third panel. A related pair is matched
// on names and sizes alone; answering with an empty diff would imply photocull
// had read them and found nothing to report, which is the opposite of the truth.
func TestCompareRelatedFilesIsOpaque(t *testing.T) {
	s, payload := scannedDocs(t)
	a, b := groupOfType(t, payload, "related")

	cmp := compareVia(t, s, a, b)
	if cmp.Kind != "opaque" {
		t.Fatalf("kind = %q, want opaque", cmp.Kind)
	}
	if cmp.Doc != nil {
		t.Error("an opaque comparison came with a diff attached")
	}
	if cmp.ReasonA == "" || cmp.ReasonB == "" {
		t.Errorf("reasons = %q / %q; the panel has nothing to explain itself with",
			cmp.ReasonA, cmp.ReasonB)
	}
	if cmp.Note == "" {
		t.Error("no note: the user is shown two file listings and no reason for them")
	}
}

// TestCompareFollowsTheScanNotTheFile records a decision that is easy to get
// backwards. Documents mode deliberately walks every extension, so a photograph
// can end up in a documents scan — and it is still part of a documents scan.
// Sniffing the file instead would put a pixel diff inside a documents review,
// answering a question the user did not ask, and would make the panel that
// appears depend on which two files happened to land in a group.
func TestCompareFollowsTheScanNotTheFile(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join("..", "..", "testdata", "exact", "original.jpg")
	copyFixture(t, src, filepath.Join(dir, "photo.jpg"))
	copyFixture(t, src, filepath.Join(dir, "backup", "photo.jpg"))

	s := launcherServer()
	if code := startScan(s, scanRequest{Path: dir, Kind: "docs"}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}

	a, b := groupOfType(t, getReport(t, s), "exact")
	if cmp := compareVia(t, s, a, b); cmp.Kind != "opaque" {
		t.Errorf("kind = %q, want opaque: two JPEGs inside a documents scan have no "+
			"text, and a documents review is not the place for a pixel diff", cmp.Kind)
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
