package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"time"

	"photocull/internal/dedupe"
	"photocull/internal/imageutil"
	"photocull/internal/osdialog"
	"photocull/internal/pipeline"
	"photocull/internal/report"
	"photocull/internal/scanner"
)

// reportPayload is what the page renders. Paths are sent both absolute (for the
// thumbnail and delete calls) and relative (for display).
type reportPayload struct {
	Loaded bool         `json:"loaded"`
	Root   string       `json:"root"`
	Stats  report.Stats `json:"stats"`
	Groups []groupView  `json:"groups"`
}

type groupView struct {
	ID        string     `json:"id"`
	Type      string     `json:"type"`
	KeepIndex int        `json:"keepIndex"`
	Files     []fileView `json:"files"`
}

type fileView struct {
	Path    string `json:"path"`    // absolute; used for thumb + delete
	RelPath string `json:"relPath"` // shown to the user
	Size    int64  `json:"size"`
	Width   int    `json:"width"`
	Height  int    `json:"height"`
	ModTime string `json:"modTime"`
	Decoded bool   `json:"decoded"`
}

func (s *Server) handleReport(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	payload := s.buildPayload()
	s.mu.Unlock()

	writeJSON(w, http.StatusOK, payload)
}

// scanRequest is the body of POST /api/scan: the mode and folder chosen on the
// launcher page.
type scanRequest struct {
	Path      string `json:"path"`
	Similar   bool   `json:"similar"`
	Threshold int    `json:"threshold"`
}

// handleScan starts a scan in the background and returns immediately, so a
// long scan (a whole disk can take many minutes) never blocks on one request.
// The page then polls /api/scan/status for progress.
func (s *Server) handleScan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req scanRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Path == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "please choose a folder"})
		return
	}
	if !req.Similar {
		req.Threshold = 0
	} else if req.Threshold <= 0 {
		req.Threshold = dedupe.DefaultThreshold
	}

	s.scanMu.Lock()
	if s.job != nil && !s.job.done {
		s.scanMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a scan is already running"})
		return
	}

	// The scan outlives this request, so it gets its own cancellable context
	// rather than the request's, which is cancelled the moment we respond.
	ctx, cancel := context.WithCancel(context.Background())
	job := &scanJob{
		progress:  &scanner.Progress{},
		cancel:    cancel,
		startedAt: time.Now(),
		similar:   req.Similar,
	}
	s.job = job
	s.scanMu.Unlock()

	go s.runScanJob(ctx, job, req)

	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

// runScanJob performs the scan and records the outcome on the job.
func (s *Server) runScanJob(ctx context.Context, job *scanJob, req scanRequest) {
	defer job.cancel()

	an, err := pipeline.Run(ctx, pipeline.Options{
		Root:      req.Path,
		Similar:   req.Similar,
		Threshold: req.Threshold,
		Progress:  job.progress,
	})

	s.scanMu.Lock()
	job.done = true
	if err != nil {
		job.err = err.Error()
	}
	s.scanMu.Unlock()

	if err == nil {
		s.mu.Lock()
		s.load(an.Root, an.Groups, an.Stats)
		s.mu.Unlock()
	}
}

// scanStatus is the progress report the page polls for.
type scanStatus struct {
	Running    bool   `json:"running"`
	Done       bool   `json:"done"`
	Error      string `json:"error,omitempty"`
	Phase      string `json:"phase"` // "scanning", "grouping", "done" or "error"
	Discovered int64  `json:"discovered"`
	Processed  int64  `json:"processed"`
	Bytes      int64  `json:"bytes"`
	WalkDone   bool   `json:"walkDone"`
	ElapsedSec int    `json:"elapsedSec"`
}

// handleScanStatus reports how far the current scan has got.
func (s *Server) handleScanStatus(w http.ResponseWriter, r *http.Request) {
	s.scanMu.Lock()
	job := s.job
	s.scanMu.Unlock()

	if job == nil {
		writeJSON(w, http.StatusOK, scanStatus{Phase: "idle"})
		return
	}

	discovered := job.progress.Discovered.Load()
	processed := job.progress.Processed.Load()
	walkDone := job.progress.WalkDone.Load()

	status := scanStatus{
		Running:    !job.done,
		Done:       job.done,
		Error:      job.err,
		Discovered: discovered,
		Processed:  processed,
		Bytes:      job.progress.Bytes.Load(),
		WalkDone:   walkDone,
		ElapsedSec: int(time.Since(job.startedAt).Seconds()),
	}

	switch {
	case job.err != "":
		status.Phase = "error"
	case job.done:
		status.Phase = "done"
	case walkDone && processed >= discovered && discovered > 0:
		// Reading is finished; the remaining work is comparing hashes.
		status.Phase = "grouping"
	default:
		status.Phase = "scanning"
	}

	writeJSON(w, http.StatusOK, status)
}

// handleScanCancel stops the current scan. This is also the user's "it's taking
// too long, get me out" button.
func (s *Server) handleScanCancel(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s.scanMu.Lock()
	if s.job != nil && !s.job.done {
		s.job.cancel()
	}
	s.scanMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

// handleBrowse opens the native folder picker and returns the chosen path.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path, err := s.pickFolder("Choose the photo folder to scan")
	switch {
	case errors.Is(err, osdialog.ErrCancelled):
		writeJSON(w, http.StatusOK, map[string]string{"path": ""})
	case errors.Is(err, osdialog.ErrUnsupported):
		http.Error(w, "native folder picker unavailable; type the path instead", http.StatusNotImplemented)
	case err != nil:
		http.Error(w, "could not open folder picker", http.StatusInternalServerError)
	default:
		writeJSON(w, http.StatusOK, map[string]string{"path": path})
	}
}

// buildPayload snapshots the current groups. Callers must hold s.mu.
func (s *Server) buildPayload() reportPayload {
	groups := make([]groupView, 0, len(s.groups))
	for _, g := range s.groups {
		files := make([]fileView, 0, len(g.Files))
		for _, f := range g.Files {
			abs, _ := filepath.Abs(f.Path)
			rel, err := filepath.Rel(s.root, f.Path)
			if err != nil {
				rel = f.Path
			}
			files = append(files, fileView{
				Path:    abs,
				RelPath: rel,
				Size:    f.Size,
				Width:   f.Width,
				Height:  f.Height,
				ModTime: f.ModTime.Format("2006-01-02 15:04"),
				Decoded: f.Decoded,
			})
		}
		groups = append(groups, groupView{
			ID:        g.ID,
			Type:      string(g.Type),
			KeepIndex: g.KeepIndex,
			Files:     files,
		})
	}
	return reportPayload{Loaded: s.loaded, Root: s.root, Stats: s.stats, Groups: groups}
}

// handleThumb serves a JPEG thumbnail for one of the scanned files.
//
// It will only serve a path that was part of the scan (s.allowed). That single
// check is what stops "/api/thumb?path=C:\Windows\...\secret" or a "../.."
// escape from reading anything off the disk the user did not ask photocull to
// look at.
func (s *Server) handleThumb(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	s.serveImage(w, raw, imageutil.GridSize)
}

// handlePreview serves a larger version of a scanned photo, for the close-up
// comparison view. Same guard as thumbnails.
func (s *Server) handlePreview(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}
	s.serveImage(w, raw, imageutil.PreviewSize)
}

// serveImage renders one scanned file at the requested size, refusing any path
// that was not part of the scan.
func (s *Server) serveImage(w http.ResponseWriter, raw string, size int) {
	abs, err := filepath.Abs(raw)
	if err != nil || !s.allowed[abs] {
		// Do not reveal whether the file exists; just refuse.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	data, err := s.thumb.Get(abs, size)
	if err != nil {
		http.Error(w, "cannot render image", http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "image/jpeg")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

// deleteRequest is the body of POST /api/delete.
type deleteRequest struct {
	Paths []string `json:"paths"`
}

type deleteResponse struct {
	Moved  int           `json:"moved"`
	Failed []string      `json:"failed,omitempty"`
	Report reportPayload `json:"report"`
}

// handleDelete moves the requested files to the recycle bin, then returns the
// refreshed report so the page can update without a full re-scan.
func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req deleteRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Only delete files that a duplicate scan flagged — so a crafted request
	// cannot move an arbitrary file to the recycle bin, and "add to library"
	// source photos (readable but not deletable) are never trashed.
	var permitted []string
	var failed []string
	for _, p := range req.Paths {
		abs, err := filepath.Abs(p)
		if err != nil || !s.deletable[abs] {
			failed = append(failed, p)
			continue
		}
		permitted = append(permitted, abs)
	}

	moved := 0
	if len(permitted) > 0 {
		if err := s.mover.Move(permitted...); err != nil {
			// wastebasket moves what it can; treat the batch as best-effort and
			// still drop the moved files from the view below.
			failed = append(failed, err.Error())
		}
		moved = len(permitted)
		s.forget(permitted)
	}

	writeJSON(w, http.StatusOK, deleteResponse{
		Moved:  moved,
		Failed: failed,
		Report: s.buildPayload(),
	})
}

// forget removes deleted files from the groups and disallows their paths.
// Groups that fall to a single remaining file are dropped: nothing left to
// decide. Callers must hold s.mu.
func (s *Server) forget(paths []string) {
	gone := make(map[string]bool, len(paths))
	for _, p := range paths {
		gone[p] = true
		delete(s.allowed, p)
		delete(s.deletable, p)
	}

	kept := s.groups[:0]
	for _, g := range s.groups {
		remaining := g.Files[:0]
		for _, f := range g.Files {
			abs, _ := filepath.Abs(f.Path)
			if !gone[abs] {
				remaining = append(remaining, f)
			}
		}
		g.Files = remaining
		if len(g.Files) < 2 {
			continue
		}
		// Keep index may have shifted; fall back to the first file if the
		// previously-suggested keeper is gone.
		if g.KeepIndex >= len(g.Files) {
			g.KeepIndex = 0
		}
		kept = append(kept, g)
	}
	s.groups = kept
	s.stats = recomputeStats(s.stats, s.groups)
}

// recomputeStats refreshes the reclaimable figures after a deletion, leaving
// the scan-time totals (files scanned, bytes) untouched.
func recomputeStats(base report.Stats, groups []dedupe.Group) report.Stats {
	base.Groups = len(groups)
	base.ExactGroups, base.SimilarGroups = 0, 0
	base.DuplicateFiles = 0
	base.ReclaimableBytes = 0
	for _, g := range groups {
		switch g.Type {
		case dedupe.Exact:
			base.ExactGroups++
		case dedupe.Similar:
			base.SimilarGroups++
		}
		base.DuplicateFiles += len(g.Files) - 1
		base.ReclaimableBytes += g.ReclaimableBytes()
	}
	return base
}

// --- Add to library (merge) ---

type mergeRequest struct {
	Base      string `json:"base"`
	Source    string `json:"source"`
	Similar   bool   `json:"similar"`
	Threshold int    `json:"threshold"`
}

// handleMerge starts, in the background, the comparison of a source folder
// against a library to find the photos that are not already in the library.
func (s *Server) handleMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req mergeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Base == "" || req.Source == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "choose both a library folder and a source folder"})
		return
	}
	if !req.Similar {
		req.Threshold = 0
	} else if req.Threshold <= 0 {
		req.Threshold = dedupe.DefaultThreshold
	}

	s.scanMu.Lock()
	if s.mergeJob != nil && !s.mergeJob.done {
		s.scanMu.Unlock()
		writeJSON(w, http.StatusConflict, map[string]string{"error": "a comparison is already running"})
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	job := &scanJob{progress: &scanner.Progress{}, cancel: cancel, startedAt: time.Now(), similar: req.Similar}
	s.mergeJob = job
	s.scanMu.Unlock()

	go s.runMergeJob(ctx, job, req)
	writeJSON(w, http.StatusAccepted, map[string]bool{"started": true})
}

func (s *Server) runMergeJob(ctx context.Context, job *scanJob, req mergeRequest) {
	defer job.cancel()

	result, err := pipeline.Merge(ctx, pipeline.MergeOptions{
		Base:      req.Base,
		Source:    req.Source,
		Similar:   req.Similar,
		Threshold: req.Threshold,
		Progress:  job.progress,
	})

	s.scanMu.Lock()
	job.done = true
	if err != nil {
		job.err = err.Error()
	} else {
		s.mergeResult = result
	}
	s.scanMu.Unlock()

	if err != nil {
		return
	}

	// The new photos live in the source folder; make them readable (for
	// thumbnails) and copyable, but never deletable.
	s.mu.Lock()
	for _, f := range result.New {
		if abs, aerr := filepath.Abs(f.Path); aerr == nil {
			s.allowed[abs] = true
		}
	}
	s.mu.Unlock()

	s.scanMu.Lock()
	s.mergeCopy = make(map[string]bool, len(result.New))
	for _, f := range result.New {
		if abs, aerr := filepath.Abs(f.Path); aerr == nil {
			s.mergeCopy[abs] = true
		}
	}
	s.scanMu.Unlock()
}

func (s *Server) handleMergeStatus(w http.ResponseWriter, r *http.Request) {
	s.scanMu.Lock()
	job := s.mergeJob
	s.scanMu.Unlock()

	if job == nil {
		writeJSON(w, http.StatusOK, scanStatus{Phase: "idle"})
		return
	}

	status := scanStatus{
		Running:    !job.done,
		Done:       job.done,
		Error:      job.err,
		Discovered: job.progress.Discovered.Load(),
		Processed:  job.progress.Processed.Load(),
		Bytes:      job.progress.Bytes.Load(),
		ElapsedSec: int(time.Since(job.startedAt).Seconds()),
	}
	switch {
	case job.err != "":
		status.Phase = "error"
	case job.done:
		status.Phase = "done"
	default:
		status.Phase = "scanning"
	}
	writeJSON(w, http.StatusOK, status)
}

// mergeResultPayload is the list of new photos plus the counts around them.
type mergeResultPayload struct {
	BaseRoot     string     `json:"baseRoot"`
	SourceRoot   string     `json:"sourceRoot"`
	New          []fileView `json:"new"`
	Duplicates   int        `json:"duplicates"`
	BaseImages   int        `json:"baseImages"`
	SourceImages int        `json:"sourceImages"`
	ImportSubdir string     `json:"importSubdir"`
}

func (s *Server) handleMergeResult(w http.ResponseWriter, r *http.Request) {
	s.scanMu.Lock()
	result := s.mergeResult
	s.scanMu.Unlock()

	if result == nil {
		writeJSON(w, http.StatusOK, mergeResultPayload{})
		return
	}

	newViews := make([]fileView, 0, len(result.New))
	for _, f := range result.New {
		abs, _ := filepath.Abs(f.Path)
		rel, err := filepath.Rel(result.SourceRoot, f.Path)
		if err != nil {
			rel = f.Path
		}
		newViews = append(newViews, fileView{
			Path:    abs,
			RelPath: rel,
			Size:    f.Size,
			Width:   f.Width,
			Height:  f.Height,
			ModTime: f.ModTime.Format("2006-01-02 15:04"),
			Decoded: f.Decoded,
		})
	}

	writeJSON(w, http.StatusOK, mergeResultPayload{
		BaseRoot:     result.BaseRoot,
		SourceRoot:   result.SourceRoot,
		New:          newViews,
		Duplicates:   result.Duplicates,
		BaseImages:   result.BaseImages,
		SourceImages: result.SourceImages,
		ImportSubdir: pipeline.ImportSubdir,
	})
}

type mergeCopyResponse struct {
	Copied int      `json:"copied"`
	Failed []string `json:"failed,omitempty"`
	Dest   string   `json:"dest"`
}

// handleMergeCopy copies the chosen new photos into the library.
func (s *Server) handleMergeCopy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req deleteRequest // reuse: a list of paths
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}

	s.scanMu.Lock()
	result := s.mergeResult
	allowedCopy := s.mergeCopy
	s.scanMu.Unlock()

	if result == nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "no comparison has been run"})
		return
	}

	// Only copy paths the comparison flagged as new.
	var permitted []string
	var failed []string
	for _, p := range req.Paths {
		abs, err := filepath.Abs(p)
		if err != nil || !allowedCopy[abs] {
			failed = append(failed, p)
			continue
		}
		permitted = append(permitted, abs)
	}

	copied, copyFailed, err := pipeline.CopyInto(result.BaseRoot, permitted)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]string{"error": err.Error()})
		return
	}
	failed = append(failed, copyFailed...)

	writeJSON(w, http.StatusOK, mergeCopyResponse{
		Copied: copied,
		Failed: failed,
		Dest:   filepath.Join(result.BaseRoot, pipeline.ImportSubdir),
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
