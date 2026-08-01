package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func writeImage(t *testing.T, srcFixture, dst string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("../../testdata", srcFixture))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
}

func startMerge(t *testing.T, s *Server, req mergeRequest) int {
	t.Helper()
	body, _ := json.Marshal(req)
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/merge", bytes.NewReader(body)))
	return rec.Code
}

func waitForMerge(t *testing.T, s *Server) scanStatus {
	t.Helper()
	for range 300 {
		rec := httptest.NewRecorder()
		bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/merge/status", nil))
		var st scanStatus
		json.Unmarshal(rec.Body.Bytes(), &st)
		if st.Done {
			return st
		}
	}
	t.Fatal("merge did not finish")
	return scanStatus{}
}

func mergeResult(t *testing.T, s *Server) mergeResultPayload {
	t.Helper()
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/merge/result", nil))
	var p mergeResultPayload
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("decode merge result: %v", err)
	}
	return p
}

func TestMergeEndpointFindsNewAndCopies(t *testing.T) {
	s := launcherServer()
	base := t.TempDir()
	source := t.TempDir()

	writeImage(t, "exact/original.jpg", filepath.Join(base, "have.jpg"))
	writeImage(t, "exact/original.jpg", filepath.Join(source, "dup.jpg"))  // already in library
	writeImage(t, "exact/unrelated.jpg", filepath.Join(source, "new.jpg")) // new

	if code := startMerge(t, s, mergeRequest{Base: base, Source: source}); code != http.StatusAccepted {
		t.Fatalf("merge start = %d, want 202", code)
	}
	if st := waitForMerge(t, s); st.Error != "" {
		t.Fatalf("merge errored: %s", st.Error)
	}

	result := mergeResult(t, s)
	if len(result.New) != 1 || result.Duplicates != 1 {
		t.Fatalf("result: %d new, %d dup; want 1 and 1", len(result.New), result.Duplicates)
	}

	// A thumbnail of a new (source) photo should be served — it is readable.
	newPath := result.New[0].Path
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/thumb?path="+url.QueryEscape(newPath), nil))
	if rec.Code != http.StatusOK {
		t.Errorf("thumbnail of a new photo = %d, want 200", rec.Code)
	}

	// But it must NOT be deletable via /api/delete (only copyable).
	delBody, _ := json.Marshal(deleteRequest{Paths: []string{newPath}})
	delRec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(delRec, httptest.NewRequest(http.MethodPost, "/api/delete", bytes.NewReader(delBody)))
	var delResp deleteResponse
	json.Unmarshal(delRec.Body.Bytes(), &delResp)
	if delResp.Moved != 0 {
		t.Error("a source photo from 'add to library' was deletable; it must not be")
	}

	// Copy it into the library.
	copyBody, _ := json.Marshal(deleteRequest{Paths: []string{newPath}})
	copyRec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(copyRec, httptest.NewRequest(http.MethodPost, "/api/merge/copy", bytes.NewReader(copyBody)))
	var copyResp mergeCopyResponse
	json.Unmarshal(copyRec.Body.Bytes(), &copyResp)
	if copyResp.Copied != 1 {
		t.Fatalf("copied %d, want 1", copyResp.Copied)
	}

	// The file now exists in the library's import folder.
	entries, _ := os.ReadDir(filepath.Join(base, "photocull_added"))
	if len(entries) != 1 {
		t.Errorf("library import folder has %d files, want 1", len(entries))
	}
}

func TestMergeCopyRejectsUnlistedPath(t *testing.T) {
	s := launcherServer()
	base := t.TempDir()
	source := t.TempDir()
	writeImage(t, "exact/original.jpg", filepath.Join(source, "new.jpg"))

	startMerge(t, s, mergeRequest{Base: base, Source: source})
	waitForMerge(t, s)

	// A path the comparison never flagged must not be copied.
	body, _ := json.Marshal(deleteRequest{Paths: []string{`C:\Windows\notepad.exe`}})
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/merge/copy", bytes.NewReader(body)))
	var resp mergeCopyResponse
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if resp.Copied != 0 {
		t.Errorf("copied an unlisted path; want 0")
	}
}

func TestMergeRejectsMissingFolders(t *testing.T) {
	s := launcherServer()
	if code := startMerge(t, s, mergeRequest{Base: "", Source: ""}); code != http.StatusBadRequest {
		t.Errorf("empty folders = %d, want 400", code)
	}
}
