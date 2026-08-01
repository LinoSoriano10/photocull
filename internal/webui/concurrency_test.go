package webui

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
)

// TestScanStatusIsSafeUnderConcurrentPolling is a race-detector test: it asserts
// almost nothing on its own, and is meaningful only under `go test -race`.
//
// It exists because the bug it guards against was invisible without one. The
// status handler used to copy the running job's pointer under scanMu, release
// the lock, and then read the job's fields outside it — a lock held on one side
// of a shared write, which is the same as no lock at all. The ordinary tests
// polled status too, so CI caught it eventually, but only by the luck of the
// timing. This makes the window wide on purpose.
func TestScanStatusIsSafeUnderConcurrentPolling(t *testing.T) {
	s := launcherServer()
	root, err := filepath.Abs("../../testdata/similar")
	if err != nil {
		t.Fatal(err)
	}

	if code := startScan(t, s, scanRequest{Path: root, Similar: true, Threshold: 8}); code != http.StatusAccepted {
		t.Fatalf("start scan: status %d", code)
	}

	// Hammer every endpoint a real page polls while a scan is in flight.
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 50 {
				rec := httptest.NewRecorder()
				bound(t, s).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/scan/status", nil))

				var st scanStatus
				if err := json.Unmarshal(rec.Body.Bytes(), &st); err != nil {
					continue
				}
				// A finished job must never report itself as still running.
				if st.Done && st.Running {
					t.Error("status reported Done and Running at once; the two were read from different critical sections")
				}
				// Once done, the report must already be in place — the page
				// fetches it the moment it sees this flag.
				if st.Done {
					rep := httptest.NewRecorder()
					bound(t, s).ServeHTTP(rep, httptest.NewRequest(http.MethodGet, "/api/report", nil))
					var payload reportPayload
					if err := json.Unmarshal(rep.Body.Bytes(), &payload); err == nil && !payload.Loaded {
						t.Error("the scan reported done but no report was loaded yet; a page polling status would render the previous scan")
					}
				}
			}
		}()
	}

	// And a concurrent starter, which reads the job's state to decide whether
	// one is already running.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for range 50 {
			startScan(t, s, scanRequest{Path: root})
		}
	}()

	wg.Wait()
	waitForScan(t, s)
}
