package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"photocull/internal/dedupe"
	"photocull/internal/report"
	"photocull/internal/scanner"
	"photocull/internal/trash"
)

// fakeMover records deletions instead of touching the recycle bin.
type fakeMover struct{ moved []string }

func (m *fakeMover) Move(paths ...string) error {
	m.moved = append(m.moved, paths...)
	return nil
}

// fixtureServer builds a server over the committed similar/ images: a group of
// source.jpg (keeper) plus two look-alikes.
func fixtureServer(t *testing.T) (*Server, *fakeMover) {
	t.Helper()
	root, err := filepath.Abs("../../testdata/similar")
	if err != nil {
		t.Fatal(err)
	}
	mover := &fakeMover{}
	return fixtureServerWith(t, root, mover), mover
}

// fixtureServerWith is the same fixture with a mover of the caller's choosing,
// so a test can make the recycle bin slow, or failing, on purpose.
func fixtureServerWith(t *testing.T, root string, mover trash.Mover) *Server {
	t.Helper()

	mod := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	mk := func(name string) scanner.FileMeta {
		return scanner.FileMeta{
			Path:    filepath.Join(root, name),
			Size:    1000,
			Width:   640,
			Height:  480,
			Decoded: true,
			ModTime: mod,
			SHA256:  name, // distinct content hashes; similarity is faked by grouping
		}
	}
	group := dedupe.Group{
		ID:        "test",
		Type:      dedupe.Similar,
		KeepIndex: 0,
		Files:     []scanner.FileMeta{mk("source.jpg"), mk("source_resized.jpg"), mk("source_recompressed.jpg")},
	}
	stats := report.Stats{FilesScanned: 3, Groups: 1, SimilarGroups: 1, DuplicateFiles: 2, ReclaimableBytes: 2000}

	return New(root, []dedupe.Group{group}, stats, mover)
}

func TestReportEndpoint(t *testing.T) {
	s, _ := fixtureServer(t)
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var payload reportPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(payload.Groups) != 1 || len(payload.Groups[0].Files) != 3 {
		t.Fatalf("unexpected groups: %+v", payload.Groups)
	}
	if payload.Groups[0].Files[0].RelPath != "source.jpg" {
		t.Errorf("relPath = %q, want source.jpg", payload.Groups[0].Files[0].RelPath)
	}
}

func TestThumbnailServesScannedFile(t *testing.T) {
	s, _ := fixtureServer(t)
	abs, _ := filepath.Abs("../../testdata/similar/source.jpg")

	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/thumb?path="+url.QueryEscape(abs), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("content-type = %q, want image/jpeg", ct)
	}
	if rec.Body.Len() == 0 {
		t.Error("empty thumbnail body")
	}
}

func TestPreviewServesAndGuards(t *testing.T) {
	s, _ := fixtureServer(t)
	abs, _ := filepath.Abs("../../testdata/similar/source.jpg")

	// A scanned file is served as a JPEG.
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/preview?path="+url.QueryEscape(abs), nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("preview status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("preview content-type = %q, want image/jpeg", ct)
	}

	// An unscanned path is refused, exactly like the thumbnail endpoint.
	rec2 := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/api/preview?path="+url.QueryEscape(`C:\Windows\win.ini`), nil))
	if rec2.Code == http.StatusOK {
		t.Error("preview served an unscanned path")
	}
}

// TestThumbnailRejectsPathTraversal is the security-critical test: a server
// that serves any file the query string names would leak the whole disk. Only
// files that were part of the scan may be served.
func TestThumbnailRejectsPathTraversal(t *testing.T) {
	s, _ := fixtureServer(t)

	attempts := []string{
		"/api/thumb?path=" + filepath.Join("..", "..", "..", "..", "Windows", "win.ini"),
		"/api/thumb?path=/etc/passwd",
		"/api/thumb?path=C:\\Windows\\System32\\drivers\\etc\\hosts",
		"/api/thumb", // missing path
	}
	for _, url := range attempts {
		rec := httptest.NewRecorder()
		bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code == http.StatusOK {
			t.Errorf("%s was served (status 200); it must be refused", url)
		}
	}
}

func TestDeleteMovesOnlyScannedFiles(t *testing.T) {
	s, mover := fixtureServer(t)
	dupAbs, _ := filepath.Abs("../../testdata/similar/source_resized.jpg")

	body, _ := json.Marshal(deleteRequest{Paths: []string{dupAbs}})
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, postJSON("/api/delete", bytes.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if len(mover.moved) != 1 || mover.moved[0] != dupAbs {
		t.Fatalf("moved %v, want just %s", mover.moved, dupAbs)
	}

	// The group had 3 files; after removing one, 2 remain, so the group stays.
	var resp deleteResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Moved != 1 {
		t.Errorf("reported moved = %d, want 1", resp.Moved)
	}
	if len(resp.Report.Groups) != 1 || len(resp.Report.Groups[0].Files) != 2 {
		t.Errorf("group not updated after delete: %+v", resp.Report.Groups)
	}
}

// TestDeleteRefusesUnscannedPath makes sure the delete endpoint has the same
// guard as thumbnails: a crafted path must never reach the recycle bin.
func TestDeleteRefusesUnscannedPath(t *testing.T) {
	s, mover := fixtureServer(t)

	body, _ := json.Marshal(deleteRequest{Paths: []string{"C:\\Windows\\System32\\notepad.exe"}})
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, postJSON("/api/delete", bytes.NewReader(body)))

	if len(mover.moved) != 0 {
		t.Fatalf("an unscanned path was moved: %v", mover.moved)
	}
	var resp deleteResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if len(resp.Failed) != 1 {
		t.Errorf("failed list = %v, want the rejected path", resp.Failed)
	}
}

func TestDeleteRejectsGet(t *testing.T) {
	s, _ := fixtureServer(t)
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/delete", nil))
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET /api/delete = %d, want 405", rec.Code)
	}
}

func TestServesIndexPage(t *testing.T) {
	s, _ := fixtureServer(t)
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "photocull") {
		t.Error("index page did not render")
	}
}
