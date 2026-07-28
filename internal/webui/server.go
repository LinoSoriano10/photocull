// Package webui serves a small local page for reviewing duplicate photos by
// eye before deleting them.
//
// It runs in two shapes. Given a folder on the command line (`photocull serve
// <dir>`), it starts with the scan already done. Launched bare (a double-click
// on the binary), it starts empty and shows a page to pick a mode and a folder,
// then scans on demand. Either way the review UI is the same.
//
// The whole point is trust: exact duplicates are safe to delete on faith, but
// "similar" ones are a judgement call, and a wall of file paths is a poor way
// to make it. Thumbnails side by side are a good one.
package webui

import (
	"context"
	"embed"
	"io/fs"
	"net/http"
	"path/filepath"
	"sync"
	"time"

	"photocull/internal/dedupe"
	"photocull/internal/imageutil"
	"photocull/internal/osdialog"
	"photocull/internal/pipeline"
	"photocull/internal/report"
	"photocull/internal/scanner"
	"photocull/internal/trash"
)

//go:embed static
var staticFiles embed.FS

// Server holds the state behind the review UI.
type Server struct {
	mover trash.Mover
	thumb *imageutil.ThumbCache

	// pickFolder opens the native folder dialog. It is a field so tests can
	// swap in a fake and never pop a real dialog.
	pickFolder func(title string) (string, error)

	// mu guards everything below, which changes when the user scans a new
	// folder or deletes files.
	mu     sync.Mutex
	loaded bool
	root   string
	groups []dedupe.Group
	stats  report.Stats

	// allowed is the set of absolute file paths the UI may READ (thumbnails and
	// previews). deletable is the subset it may move to the recycle bin. They
	// are separate because "add to library" needs to show source photos without
	// making them deletable — only files that were part of a duplicate scan can
	// be recycled. A path outside these sets can be neither read nor touched.
	allowed   map[string]bool
	deletable map[string]bool

	// scanMu guards the scan and merge jobs, which run in the background so the
	// page can poll their progress rather than block on one long request.
	scanMu      sync.Mutex
	job         *scanJob
	mergeJob    *scanJob
	mergeResult *pipeline.MergeAnalysis
	mergeCopy   map[string]bool // source paths the copy step is allowed to touch
}

// scanJob is a running or finished background scan.
//
// It carries its own mutex rather than borrowing the server's. The job outlives
// the request that created it, and the goroutine writing its outcome is not the
// one reading it — so tying its safety to a server field invites exactly the bug
// that used to live here: the status handler copied the job *pointer* under
// scanMu, released the lock, and then read the fields outside it. Holding a lock
// on one side of a shared write is the same as holding no lock at all.
//
// progress, cancel, startedAt and similar are written once before the goroutine
// starts, so the `go` statement already orders them and they need no lock.
type scanJob struct {
	progress  *scanner.Progress
	cancel    context.CancelFunc
	startedAt time.Time
	similar   bool

	mu   sync.Mutex
	done bool
	err  string // non-empty if the scan failed or was cancelled
}

// finish records the outcome of the job.
//
// Callers must publish whatever the job produced *before* calling this: once
// done is set, a page polling for status will immediately ask for the result.
func (j *scanJob) finish(err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.done = true
	if err != nil {
		j.err = err.Error()
	}
}

// outcome reports how far the job has got. Both values come back from one
// critical section, so a status report can never pair a stale "done" with a
// fresh error.
func (j *scanJob) outcome() (done bool, failure string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.done, j.err
}

// finished reports whether the job has completed.
func (j *scanJob) finished() bool {
	done, _ := j.outcome()
	return done
}

// New builds a server with the scan already done — used by `serve <dir>`.
func New(root string, groups []dedupe.Group, stats report.Stats, mover trash.Mover) *Server {
	s := NewLauncher(mover)
	s.load(root, groups, stats)
	return s
}

// NewLauncher builds a server that starts empty and scans on demand — used
// when the binary is launched with no folder to review yet.
func NewLauncher(mover trash.Mover) *Server {
	return &Server{
		mover:      mover,
		thumb:      imageutil.NewThumbCache(),
		pickFolder: osdialog.PickFolder,
		allowed:    make(map[string]bool),
		deletable:  make(map[string]bool),
		mergeCopy:  make(map[string]bool),
	}
}

// load replaces the current analysis. Callers must hold s.mu, except New which
// runs before the server is reachable.
func (s *Server) load(root string, groups []dedupe.Group, stats report.Stats) {
	s.root = root
	s.groups = groups
	s.stats = stats
	s.loaded = true
	s.allowed = make(map[string]bool)
	s.deletable = make(map[string]bool)
	for _, g := range groups {
		for _, f := range g.Files {
			if abs, err := filepath.Abs(f.Path); err == nil {
				s.allowed[abs] = true
				s.deletable[abs] = true // scanned duplicates may be recycled
			}
		}
	}
}

// Handler returns the HTTP routes: the embedded page at the root, and the
// JSON/thumbnail API under /api/.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	static, _ := fs.Sub(staticFiles, "static")
	mux.Handle("/", http.FileServer(http.FS(static)))

	mux.HandleFunc("/api/report", s.handleReport)
	mux.HandleFunc("/api/scan", s.handleScan)
	mux.HandleFunc("/api/scan/status", s.handleScanStatus)
	mux.HandleFunc("/api/scan/cancel", s.handleScanCancel)
	mux.HandleFunc("/api/browse", s.handleBrowse)
	mux.HandleFunc("/api/thumb", s.handleThumb)
	mux.HandleFunc("/api/preview", s.handlePreview)
	mux.HandleFunc("/api/compare", s.handleCompare)
	mux.HandleFunc("/api/imagediff", s.handleImageDiff)
	mux.HandleFunc("/api/delete", s.handleDelete)
	mux.HandleFunc("/api/merge", s.handleMerge)
	mux.HandleFunc("/api/merge/status", s.handleMergeStatus)
	mux.HandleFunc("/api/merge/result", s.handleMergeResult)
	mux.HandleFunc("/api/merge/copy", s.handleMergeCopy)

	return mux
}
