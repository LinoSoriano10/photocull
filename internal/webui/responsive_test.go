package webui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// slowMover imitates the recycle bin on a bad day: a call into the operating
// system that takes real time for a large batch.
type slowMover struct {
	delay time.Duration

	mu      sync.Mutex
	moved   []string
	started chan struct{} // closed once Move is under way
}

func newSlowMover(delay time.Duration) *slowMover {
	return &slowMover{delay: delay, started: make(chan struct{})}
}

func (m *slowMover) Move(paths ...string) error {
	close(m.started)
	time.Sleep(m.delay)
	m.mu.Lock()
	m.moved = append(m.moved, paths...)
	m.mu.Unlock()
	return nil
}

// TestDeleteDoesNotFreezeTheWindow is a responsiveness test, and it earns its
// place because the bug it covers is invisible in every other kind of check:
// the endpoints all returned the right answers, just not until the recycle bin
// had finished.
//
// handleDelete used to hold s.mu across mover.Move. That mutex is what every
// thumbnail, snippet and comparison request needs to resolve its path, so the
// whole window stopped repainting for as long as the move took — which for a
// few hundred files is seconds, at exactly the moment the user is watching to
// see whether it worked.
func TestDeleteDoesNotFreezeTheWindow(t *testing.T) {
	root, _ := filepath.Abs("../../testdata/similar")
	mover := newSlowMover(750 * time.Millisecond)
	s := fixtureServerWith(t, root, mover)
	h := bound(t, s)

	dup, _ := filepath.Abs("../../testdata/similar/source_resized.jpg")
	keeper, _ := filepath.Abs("../../testdata/similar/source.jpg")

	body, _ := json.Marshal(deleteRequest{Paths: []string{dup}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postJSON("/api/delete", bytes.NewReader(body)))
	}()

	// Wait until the move is genuinely in progress, then ask for a thumbnail of
	// a file that is *not* being deleted. It must come back while the move is
	// still running.
	<-mover.started

	served := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/thumb?path="+url.QueryEscape(keeper), nil))
		served <- rec.Code
	}()

	select {
	case code := <-served:
		if code != http.StatusOK {
			t.Errorf("thumbnail during a delete = %d, want 200", code)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("a thumbnail request was still blocked half a second into a delete: " +
			"the recycle-bin call is holding the lock the whole UI reads through")
	}

	<-done
}

// TestDeleteWithdrawsPermissionBeforeMoving closes the window between "this
// file is on its way to the bin" and "the server stops serving it".
func TestDeleteWithdrawsPermissionBeforeMoving(t *testing.T) {
	root, _ := filepath.Abs("../../testdata/similar")
	mover := newSlowMover(500 * time.Millisecond)
	s := fixtureServerWith(t, root, mover)
	h := bound(t, s)

	dup, _ := filepath.Abs("../../testdata/similar/source_resized.jpg")
	body, _ := json.Marshal(deleteRequest{Paths: []string{dup}})

	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, postJSON("/api/delete", bytes.NewReader(body)))
	}()
	<-mover.started

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/thumb?path="+url.QueryEscape(dup), nil))
	if rec.Code == http.StatusOK {
		t.Error("a file already on its way to the recycle bin was still being served")
	}
}

// TestScanClearsAPreviousMergeResult covers state that used to outlive what
// produced it. load() reset the groups and the permitted paths but left the
// merge alone, so after a merge followed by a scan the page could still ask for
// — and be given — the folders it had moved on from.
func TestScanClearsAPreviousMergeResult(t *testing.T) {
	s := launcherServer()
	base := t.TempDir()
	source := t.TempDir()
	writeImage(t, "exact/original.jpg", filepath.Join(base, "have.jpg"))
	writeImage(t, "exact/unrelated.jpg", filepath.Join(source, "new.jpg"))

	if code := startMerge(t, s, mergeRequest{Base: base, Source: source}); code != http.StatusAccepted {
		t.Fatalf("merge start = %d", code)
	}
	if st := waitForMerge(t, s); st.Error != "" {
		t.Fatalf("merge errored: %s", st.Error)
	}
	if got := mergeResult(t, s); len(got.New) == 0 {
		t.Fatal("the merge found nothing; the rest of this test proves nothing")
	}

	// Now scan something else entirely.
	dir, _ := filepath.Abs("../../testdata/exact")
	if code := startScan(t, s, scanRequest{Path: dir}); code != http.StatusAccepted {
		t.Fatalf("scan start = %d", code)
	}
	if st := waitForScan(t, s); st.Error != "" {
		t.Fatalf("scan errored: %s", st.Error)
	}

	if after := mergeResult(t, s); len(after.New) != 0 || after.SourceRoot != "" {
		t.Errorf("the merge result survived a new scan: %+v", after)
	}

	// And the copy endpoint must no longer accept the paths it had listed.
	body, _ := json.Marshal(deleteRequest{Paths: []string{filepath.Join(source, "new.jpg")}})
	rec := httptest.NewRecorder()
	bound(t, s).ServeHTTP(rec, postJSON("/api/merge/copy", bytes.NewReader(body)))
	var resp map[string]any
	json.Unmarshal(rec.Body.Bytes(), &resp)
	if copied, _ := resp["copied"].(float64); copied != 0 {
		t.Errorf("copied %v files from a merge that had been superseded", copied)
	}
}
