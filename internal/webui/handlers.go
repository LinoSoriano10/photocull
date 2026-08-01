package webui

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"photocull/internal/doctext"
	"photocull/internal/fingerprint"
	"photocull/internal/imageutil"
	"photocull/internal/osdialog"
	"photocull/internal/pipeline"
	"photocull/internal/report"
	"photocull/internal/scanner"
	"photocull/internal/textdiff"
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

// scanRequest is the body of POST /api/scan: the kind, mode and folder chosen
// on the launcher page.
type scanRequest struct {
	Path string `json:"path"`

	// Kind is "photos" or "docs". Empty means photos — the page that shipped
	// before documents existed sent no such field, and it must keep working.
	Kind string `json:"kind"`

	// Ext narrows the scan to these extensions. Empty means whatever the kind
	// looks at, which for documents is deliberately *everything*.
	Ext []string `json:"ext"`

	Similar   bool `json:"similar"`
	Threshold int  `json:"threshold"`
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
	// An unset threshold is left at zero rather than filled in here, so the
	// pipeline resolves it from the kind. Photos and documents want different
	// numbers, and deciding that in two places is how they come to disagree.
	if !req.Similar {
		req.Threshold = 0
	}

	s.scanMu.Lock()
	if s.job != nil && !s.job.finished() {
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
		Root:       req.Path,
		Kind:       req.Kind,
		Extensions: req.Ext,
		Similar:    req.Similar,
		Threshold:  req.Threshold,
		Progress:   job.progress,
	})

	// Load the report first and mark the job done second. The page polls status
	// and fetches /api/report the moment it sees "done", so publishing in the
	// other order leaves a window where it would render the *previous* scan.
	if err == nil {
		s.mu.Lock()
		s.load(an.Root, an.Groups, an.Stats)
		s.mu.Unlock()
	}
	job.finish(err)
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
	done, failure := job.outcome()

	status := scanStatus{
		Running:    !done,
		Done:       done,
		Error:      failure,
		Discovered: discovered,
		Processed:  processed,
		Bytes:      job.progress.Bytes.Load(),
		WalkDone:   walkDone,
		ElapsedSec: int(time.Since(job.startedAt).Seconds()),
	}

	switch {
	case failure != "":
		status.Phase = "error"
	case done:
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
	if s.job != nil && !s.job.finished() {
		s.job.cancel()
	}
	s.scanMu.Unlock()
	writeJSON(w, http.StatusOK, map[string]bool{"cancelled": true})
}

// handleBrowse opens the native folder picker and returns the chosen path.
func (s *Server) handleBrowse(w http.ResponseWriter, r *http.Request) {
	path, err := s.pickFolder("Choose the folder to scan")
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

// lookup resolves raw to an absolute path and reports whether set contains it.
// Callers must hold s.mu.
func lookup(set map[string]bool, raw string) (string, bool) {
	abs, err := filepath.Abs(raw)
	if err != nil || !set[abs] {
		return "", false
	}
	return abs, true
}

// resolveAllowed turns a request's raw path into an absolute one and reports
// whether the scan ever touched it.
//
// Every endpoint that opens a file off the disk goes through here. Membership
// in s.allowed *is* photocull's entire file-access security model — there is no
// path-prefix check, which is also what makes a "../.." escape pointless — so
// it lives in one function precisely so it can be read, tested and pointed at.
//
// It takes the lock itself: the browser fires image and comparison requests
// concurrently, and a delete running at the same time rewrites these sets.
func (s *Server) resolveAllowed(raw string) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return lookup(s.allowed, raw)
}

// serveImage renders one scanned file at the requested size, refusing any path
// that was not part of the scan.
func (s *Server) serveImage(w http.ResponseWriter, raw string, size int) {
	abs, ok := s.resolveAllowed(raw)
	if !ok {
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

// maxSnippet caps how much text one card may ask for. A card is a glance, not
// a reader: past a few thousand characters the user is scrolling a document
// inside a thumbnail, which is the wrong tool for that job.
const maxSnippet = 4000

// defaultSnippet is what a card gets when it does not ask for a length.
const defaultSnippet = 400

// handleSnippet serves the opening text of a scanned document, so a card can
// show what is *in* the file rather than only its name.
//
// It is the documents answer to /api/thumb, and it goes through the same guard.
// Unlike thumbnails it is not cached: ThumbCache exists because decoding and
// resizing a JPEG is expensive, whereas this re-reads a few hundred characters
// off a file the operating system almost certainly still has in its page cache.
// Caching it would buy nothing and would have to be invalidated on delete.
func (s *Server) handleSnippet(w http.ResponseWriter, r *http.Request) {
	raw := r.URL.Query().Get("path")
	if raw == "" {
		http.Error(w, "missing path", http.StatusBadRequest)
		return
	}

	abs, ok := s.resolveAllowed(raw)
	if !ok {
		// Same as the image endpoints: do not reveal whether the file exists.
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	n := defaultSnippet
	if v, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && v > 0 {
		n = min(v, maxSnippet)
	}

	text, err := readSnippet(abs, n)
	if err != nil {
		// A file photocull cannot read inside is an ordinary outcome in
		// documents mode — a .zip, a scanned PDF, a legacy .doc — so this
		// mirrors serveImage's 422 rather than pretending something broke. The
		// page falls back to showing the file's details.
		http.Error(w, "no text in this file", http.StatusUnprocessableEntity)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	io.WriteString(w, text)
}

// documentText reads one file's text, or says why there is none.
//
// The two results are the two outcomes that matter to the page, and neither is
// an error: a file photocull cannot read inside is ordinary in documents mode,
// and the reason is the useful thing to show in its place. reason is empty
// exactly when text is usable.
func documentText(path string) (text, reason string) {
	f, err := os.Open(path)
	if err != nil {
		// Phrased for the person deciding, like doctext's own reasons. The
		// underlying error is not shown: it names an absolute path the page
		// already displays, and adds nothing to the decision.
		return "", "this file could not be opened"
	}
	defer f.Close()

	out, err := doctext.Extract(f, path)
	if err != nil {
		return "", "this file is damaged, or is not the format its name claims"
	}
	if !out.Usable {
		return "", out.Reason
	}
	return out.Text, ""
}

// readSnippet extracts a document's text and returns at most n runes of it.
func readSnippet(path string, n int) (string, error) {
	text, reason := documentText(path)
	if reason != "" {
		return "", errors.New("webui: " + reason)
	}

	// Cut on runes, not bytes: slicing UTF-8 by byte count lands mid-character
	// and the browser renders a replacement glyph at the end of every snippet.
	runes := []rune(text)
	if len(runes) > n {
		return string(runes[:n]) + "…", nil
	}
	return string(runes), nil
}

// comparison answers "is there anything here worth looking at?" before the page
// fetches a single pixel.
//
// It deliberately does not echo the two files' details back: the page already
// holds them from /api/report, and sending them twice would be one more place
// for the two views to disagree about what a file is.
type comparison struct {
	// Kind selects which panel the page renders: "image" for two photographs,
	// "doc" for two documents whose text came out, and "opaque" for a pair
	// photocull cannot read inside at all — a scanned PDF, a .zip, a legacy
	// .doc. The third is not a failure of the other two, it is the honest
	// answer for the tier that was matched on names and sizes alone.
	Kind  string     `json:"kind"`
	Image *imageDiff `json:"image,omitempty"`
	Doc   *docDiff   `json:"doc,omitempty"`

	// ReasonA and ReasonB say why each file yielded no text, for the opaque
	// panel. They are doctext's own wording, which is already phrased for the
	// person deciding whether to delete the file.
	ReasonA string `json:"reasonA,omitempty"`
	ReasonB string `json:"reasonB,omitempty"`

	// Note explains, in the user's words, why there is no comparison to show.
	Note string `json:"note,omitempty"`
}

// docDiff is the wire shape of a word-level comparison. It mirrors
// textdiff.Diff rather than reusing it, for the same reason imageDiff mirrors
// imageutil's result: the diff package has no business knowing what JSON the
// page happens to want this week.
type docDiff struct {
	Hunks        []hunkView `json:"hunks"`
	SameWords    int        `json:"sameWords"`
	ChangedWords int        `json:"changedWords"`

	// Trailing is the identical run after the last change, reported like the
	// runs between hunks so the page can say "nothing changed after this".
	TrailingWords int    `json:"trailingWords,omitempty"`
	Trailing      string `json:"trailing,omitempty"`

	Truncated bool `json:"truncated,omitempty"`
}

type hunkView struct {
	Before string `json:"before,omitempty"`
	Del    string `json:"del,omitempty"`
	Ins    string `json:"ins,omitempty"`
	After  string `json:"after,omitempty"`

	// SkippedWords counts the identical words collapsed before this hunk, and
	// Skipped holds them. The text is sent rather than fetched on demand
	// because expanding a collapsed run is a click on something already on
	// screen, and a round trip there feels like a fault.
	SkippedWords int    `json:"skippedWords,omitempty"`
	Skipped      string `json:"skipped,omitempty"`
}

type imageDiff struct {
	// Ratio is the fraction of pixels that differ, 0 to 1.
	Ratio float64 `json:"ratio"`

	// Width and Height are the grid both photos were scaled onto, so the page
	// can position the box over the heatmap.
	Width  int `json:"width"`
	Height int `json:"height"`

	// Box encloses the change. Absent when nothing differs.
	Box *diffBox `json:"box,omitempty"`
}

type diffBox struct {
	X int `json:"x"`
	Y int `json:"y"`
	W int `json:"w"`
	H int `json:"h"`
}

// resolvePair validates both halves of a comparison request.
//
// Both paths are checked. Validating only the one that happens to be read first
// would leave the other as an open door to any file on the disk, which is the
// easy mistake to make here and the reason this is not written inline twice.
func (s *Server) resolvePair(r *http.Request) (a, b string, ok bool) {
	a, okA := s.resolveAllowed(r.URL.Query().Get("a"))
	b, okB := s.resolveAllowed(r.URL.Query().Get("b"))
	return a, b, okA && okB
}

// scanKind reports what the loaded analysis was about.
func (s *Server) scanKind() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.stats.Kind
}

// handleCompare reports how two scanned files differ.
func (s *Server) handleCompare(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.resolvePair(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	// Which comparison to run is decided by what was scanned, not by what the
	// files look like. Documents mode deliberately walks every extension, so a
	// .jpg sitting in a documents folder is part of a documents scan and its
	// group is a documents group — offering a pixel diff there would be
	// answering a question nobody asked.
	if s.scanKind() == string(fingerprint.Docs) {
		s.compareDocuments(w, a, b)
		return
	}

	diff, err := imageutil.Compare(a, b)
	if err != nil {
		// A photo photocull could not decode is still a real duplicate
		// candidate — it was grouped by content hash — so this is a note, not
		// an error. The page falls back to comparing file details.
		writeJSON(w, http.StatusOK, comparison{
			Kind: "image",
			Note: "One of these files could not be decoded as an image, so they can only be compared by name, size and date.",
		})
		return
	}

	if diff.AspectMismatch {
		writeJSON(w, http.StatusOK, comparison{
			Kind: "image",
			Note: "These photos are framed differently — one of them is cropped — so they cannot be laid over each other. Compare them side by side instead.",
		})
		return
	}

	out := &imageDiff{Ratio: diff.Ratio, Width: diff.Width, Height: diff.Height}
	if !diff.Box.Empty() {
		out.Box = &diffBox{
			X: diff.Box.Min.X,
			Y: diff.Box.Min.Y,
			W: diff.Box.Dx(),
			H: diff.Box.Dy(),
		}
	}
	writeJSON(w, http.StatusOK, comparison{Kind: "image", Image: out})
}

// compareDocuments answers /api/compare for a documents scan.
//
// Both files are re-read here rather than remembered from the scan. Keeping
// every document's text would multiply a scan's memory by the size of the
// documents themselves — for text that is only ever looked at when somebody
// opens one pair out of hundreds. Two file reads in response to a click is the
// cheaper side of that trade by a wide margin.
func (s *Server) compareDocuments(w http.ResponseWriter, a, b string) {
	textA, reasonA := documentText(a)
	textB, reasonB := documentText(b)

	if reasonA != "" || reasonB != "" {
		// This is the related tier's own panel. There is no text to compare and
		// pretending otherwise would be worse than saying so: the page falls
		// back to laying the two files' details side by side, which for a pair
		// matched on name and size is exactly the evidence there is.
		writeJSON(w, http.StatusOK, comparison{
			Kind:    "opaque",
			ReasonA: reasonA,
			ReasonB: reasonB,
			Note:    "photocull cannot read text inside at least one of these files, so there is nothing to compare word by word.",
		})
		return
	}

	d := textdiff.Words(textA, textB)

	hunks := make([]hunkView, 0, len(d.Hunks))
	for _, h := range d.Hunks {
		hunks = append(hunks, hunkView{
			Before:       h.Before,
			Del:          h.Del,
			Ins:          h.Ins,
			After:        h.After,
			SkippedWords: h.SkippedWords,
			Skipped:      h.Skipped,
		})
	}

	writeJSON(w, http.StatusOK, comparison{
		Kind: "doc",
		Doc: &docDiff{
			Hunks:         hunks,
			SameWords:     d.SameWords,
			ChangedWords:  d.ChangedWords,
			TrailingWords: d.TrailingWords,
			Trailing:      d.Trailing,
			Truncated:     d.Truncated,
		},
	})
}

// handleImageDiff renders the difference map between two scanned photos.
//
// It is a separate endpoint from /api/compare, and only the heatmap tab asks
// for it, so the second decode-and-scale of both photos is paid for only when
// somebody actually looks.
func (s *Server) handleImageDiff(w http.ResponseWriter, r *http.Request) {
	a, b, ok := s.resolvePair(r)
	if !ok {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	diff, err := imageutil.Compare(a, b)
	if err != nil {
		http.Error(w, "cannot compare these images", http.StatusUnprocessableEntity)
		return
	}

	data, err := diff.HeatmapPNG()
	if err != nil {
		http.Error(w, "no difference map for these images", http.StatusUnprocessableEntity)
		return
	}

	// PNG, not JPEG: the map is flat colour over large areas, which JPEG would
	// smear into a halo around exactly the edges the user is trying to judge.
	w.Header().Set("Content-Type", "image/png")
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
		abs, ok := lookup(s.deletable, p)
		if !ok {
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
	s.stats = report.Recount(s.stats, s.groups)
}

// --- Add to library (merge) ---

type mergeRequest struct {
	Base   string `json:"base"`
	Source string `json:"source"`

	// Kind and Ext mean the same here as on a scan; see scanRequest.
	Kind string   `json:"kind"`
	Ext  []string `json:"ext"`

	Similar   bool `json:"similar"`
	Threshold int  `json:"threshold"`
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
	}

	s.scanMu.Lock()
	if s.mergeJob != nil && !s.mergeJob.finished() {
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
		Base:       req.Base,
		Source:     req.Source,
		Kind:       req.Kind,
		Extensions: req.Ext,
		Similar:    req.Similar,
		Threshold:  req.Threshold,
		Progress:   job.progress,
	})

	if err != nil {
		job.finish(err)
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
	s.mergeResult = result
	s.mergeCopy = make(map[string]bool, len(result.New))
	for _, f := range result.New {
		if abs, aerr := filepath.Abs(f.Path); aerr == nil {
			s.mergeCopy[abs] = true
		}
	}
	s.scanMu.Unlock()

	// Marked done last, once the result and both permission sets are in place:
	// the page asks for /api/merge/result as soon as it sees "done".
	job.finish(nil)
}

func (s *Server) handleMergeStatus(w http.ResponseWriter, r *http.Request) {
	s.scanMu.Lock()
	job := s.mergeJob
	s.scanMu.Unlock()

	if job == nil {
		writeJSON(w, http.StatusOK, scanStatus{Phase: "idle"})
		return
	}

	done, failure := job.outcome()

	status := scanStatus{
		Running:    !done,
		Done:       done,
		Error:      failure,
		Discovered: job.progress.Discovered.Load(),
		Processed:  job.progress.Processed.Load(),
		Bytes:      job.progress.Bytes.Load(),
		ElapsedSec: int(time.Since(job.startedAt).Seconds()),
	}
	switch {
	case failure != "":
		status.Phase = "error"
	case done:
		status.Phase = "done"
	default:
		status.Phase = "scanning"
	}
	writeJSON(w, http.StatusOK, status)
}

// mergeResultPayload is the list of new files plus the counts around them.
type mergeResultPayload struct {
	BaseRoot     string     `json:"baseRoot"`
	SourceRoot   string     `json:"sourceRoot"`
	New          []fileView `json:"new"`
	Duplicates   int        `json:"duplicates"`
	BaseFiles    int        `json:"baseFiles"`
	SourceFiles  int        `json:"sourceFiles"`
	ImportSubdir string     `json:"importSubdir"`

	// Kind lets the page label the result in the user's terms — photos or
	// documents — without having to remember what it asked for.
	Kind string `json:"kind,omitempty"`
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
		BaseFiles:    result.BaseFiles,
		SourceFiles:  result.SourceFiles,
		ImportSubdir: pipeline.ImportSubdir,
		Kind:         result.Kind,
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
