package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"photocull/internal/osdialog"
	"photocull/internal/trash"
)

// launcherServer starts empty, the way a double-clicked binary does.
func launcherServer() *Server {
	s := NewLauncher(&fakeMover{})
	// Default to "no native dialog" so no test can accidentally pop one.
	s.pickFolder = func(string) (string, error) { return "", osdialog.ErrUnsupported }
	return s
}

// startScan posts a scan request and returns the HTTP status code.
func startScan(t *testing.T, s *Server, req scanRequest) int {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/scan", bytes.NewReader(body)))
	return rec.Code
}

// waitForScan polls the status endpoint until the background scan finishes.
// The fixtures are tiny, so this settles almost immediately.
func waitForScan(t *testing.T, s *Server) scanStatus {
	t.Helper()
	for range 300 {
		rec := httptest.NewRecorder()
		bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/scan/status", nil))
		var st scanStatus
		if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
			t.Fatalf("status decode: %v", err)
		}
		if st.Done {
			return st
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("scan did not finish in time")
	return scanStatus{}
}

func getReport(t *testing.T, s *Server) reportPayload {
	t.Helper()
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report", nil))
	var p reportPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("report decode: %v", err)
	}
	return p
}

func TestReportBeforeAnyScan(t *testing.T) {
	s := launcherServer()
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/report", nil))

	var payload reportPayload
	json.Unmarshal(rec.Body.Bytes(), &payload)
	if payload.Loaded {
		t.Error("a fresh launcher should report loaded=false")
	}
	if len(payload.Groups) != 0 {
		t.Errorf("fresh launcher had %d groups, want 0", len(payload.Groups))
	}
}

func TestScanFromLauncher(t *testing.T) {
	s := launcherServer()
	dir, _ := filepath.Abs("../../testdata/exact")

	if code := startScan(t, s, scanRequest{Path: dir}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}

	st := waitForScan(t, s)
	if st.Error != "" {
		t.Fatalf("scan reported an error: %s", st.Error)
	}
	if st.Phase != "done" {
		t.Errorf("final phase = %q, want done", st.Phase)
	}

	// The scanned result is now served by /api/report.
	payload := getReport(t, s)
	if !payload.Loaded {
		t.Error("after a scan the report should be loaded")
	}
	// testdata/exact has one exact-duplicate pair.
	if len(payload.Groups) != 1 || payload.Groups[0].Type != "exact" {
		t.Errorf("unexpected groups after scan: %+v", payload.Groups)
	}
}

func TestScanSimilarMode(t *testing.T) {
	s := launcherServer()
	dir, _ := filepath.Abs("../../testdata/similar")

	if code := startScan(t, s, scanRequest{Path: dir, Similar: true, Threshold: 8}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}

	payload := getReport(t, s)
	if len(payload.Groups) != 1 || payload.Groups[0].Type != "similar" {
		t.Errorf("similar scan should find one similar group, got %+v", payload.Groups)
	}
}

func TestScanReportsProgress(t *testing.T) {
	s := launcherServer()
	dir, _ := filepath.Abs("../../testdata/exact")

	if code := startScan(t, s, scanRequest{Path: dir}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}
	st := waitForScan(t, s)

	// testdata/exact holds three image files; all should be discovered and
	// processed, and the walk must be marked complete.
	if st.Discovered != 3 {
		t.Errorf("discovered = %d, want 3", st.Discovered)
	}
	if st.Processed != 3 {
		t.Errorf("processed = %d, want 3", st.Processed)
	}
	if !st.WalkDone {
		t.Error("walkDone should be true once the scan has finished")
	}
	if st.Bytes <= 0 {
		t.Error("bytes read should be greater than zero")
	}
}

func TestScanRejectsEmptyPath(t *testing.T) {
	s := launcherServer()
	if code := startScan(t, s, scanRequest{Path: ""}); code != http.StatusBadRequest {
		t.Errorf("empty path = %d, want 400", code)
	}
}

func TestScanReportsBadFolder(t *testing.T) {
	s := launcherServer()
	if code := startScan(t, s, scanRequest{Path: filepath.Join("does", "not", "exist")}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d, want 202", code)
	}

	// A missing folder is surfaced through the status endpoint, not a crash.
	st := waitForScan(t, s)
	if st.Error == "" {
		t.Error("a bad folder should finish with an error")
	}
	if st.Phase != "error" {
		t.Errorf("phase = %q, want error", st.Phase)
	}
}

func TestScanRejectsConcurrentScans(t *testing.T) {
	s := launcherServer()
	dir, _ := filepath.Abs("../../testdata/similar")

	// Start one scan and, before draining it, try to start another.
	if code := startScan(t, s, scanRequest{Path: dir, Similar: true}); code != http.StatusAccepted {
		t.Fatalf("first scan = %d, want 202", code)
	}
	second := startScan(t, s, scanRequest{Path: dir, Similar: true})
	if second != http.StatusConflict && second != http.StatusAccepted {
		// 409 if the first is still running, 202 if it already finished — both
		// are correct; a 500 or silent overwrite would not be.
		t.Errorf("second concurrent scan = %d, want 409 or 202", second)
	}
	waitForScan(t, s)
}

func TestBrowseReturnsPickedPath(t *testing.T) {
	s := launcherServer()
	s.pickFolder = func(string) (string, error) { return `C:\photos`, nil }

	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/browse", nil))

	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["path"] != `C:\photos` {
		t.Errorf("browse returned %q, want C:\\photos", resp["path"])
	}
}

func TestBrowseCancelled(t *testing.T) {
	s := launcherServer()
	s.pickFolder = func(string) (string, error) { return "", osdialog.ErrCancelled }

	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/browse", nil))

	if rec.Code != http.StatusOK {
		t.Errorf("cancelled browse = %d, want 200 with empty path", rec.Code)
	}
	var resp map[string]string
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp["path"] != "" {
		t.Errorf("cancelled browse returned a path: %q", resp["path"])
	}
}

func TestBrowseUnsupported(t *testing.T) {
	s := launcherServer()
	s.pickFolder = func(string) (string, error) { return "", osdialog.ErrUnsupported }

	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/browse", nil))

	if rec.Code != http.StatusNotImplemented {
		t.Errorf("unsupported browse = %d, want 501 so the UI hides the button", rec.Code)
	}
}

// Ensure the trash.SystemBin default is wired for real launches (compile-time
// guard that NewLauncher takes a real Mover).
var _ trash.Mover = trash.SystemBin{}
