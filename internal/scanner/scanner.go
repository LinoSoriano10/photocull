// Package scanner walks a directory tree and computes, concurrently, the
// fingerprints photocull needs to group duplicates.
//
// The pipeline has three stages connected by channels:
//
//	walker  ->  pathsCh  ->  worker pool  ->  resultsCh  ->  aggregator
//
// One goroutine lists files (cheap, and the filesystem serialises it anyway),
// a pool of workers does the expensive per-file reading and hashing, and the
// calling goroutine collects the results. This is where the concurrency
// actually pays off: on a directory with thousands of photos the workers keep
// the disk and every CPU core busy at once.
package scanner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/errgroup"

	"photocull/internal/fingerprint"
	"photocull/internal/hashing"
)

// channelBuffer keeps the walker slightly ahead of the workers so neither
// stage stalls waiting for the other on every single file.
const channelBuffer = 1024

// FileMeta is everything photocull learned about one file in a single read.
type FileMeta struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`

	// SHA256 is always present: it is what exact duplicate detection needs.
	SHA256 string `json:"sha256"`

	// Decoded reports whether the file's contents could be interpreted. A
	// corrupt JPEG is still deduplicated by SHA256, it just cannot take part in
	// similarity matching.
	Decoded bool `json:"decoded"`
	Width   int  `json:"width"`
	Height  int  `json:"height"`

	// Fingerprint is a 64-bit summary of what the file *contains*, as opposed
	// to which bytes it is made of. It is only set when the scan asked for one
	// and the file's contents could be read.
	//
	// Every file in a single scan is fingerprinted by the same algorithm. That
	// invariant is load-bearing rather than incidental: dedupe compares these
	// with a plain Hamming distance, and two fingerprints produced by different
	// algorithms sitting a short distance apart would mean nothing at all.
	Fingerprint    uint64 `json:"fingerprint,omitempty"`
	HasFingerprint bool   `json:"hasFingerprint"`
}

// Pixels is the resolution of the image, used to decide which copy to keep.
func (f FileMeta) Pixels() int64 {
	return int64(f.Width) * int64(f.Height)
}

// ErrorKind separates "photocull could not read this file at all" from "the
// file was read but its contents could not be interpreted".
type ErrorKind string

const (
	ErrorRead   ErrorKind = "read"
	ErrorDecode ErrorKind = "decode"
)

// ScanError records one file photocull could not fully process. A single bad
// file never aborts a scan: on a drive holding years of phone backups, some
// damaged files are expected, and the other few thousand photos still matter.
type ScanError struct {
	Path    string    `json:"path"`
	Kind    ErrorKind `json:"kind"`
	Message string    `json:"message"`
}

// Progress carries live counters a caller can poll while a scan runs, so a UI
// can show something better than an unmoving spinner. All fields are safe to
// read from another goroutine.
//
// A percentage only becomes meaningful once WalkDone is true: until the tree
// has been fully walked the total (Discovered) is still climbing, so a UI
// should show the raw counts during discovery and switch to Processed /
// Discovered once discovery is complete.
type Progress struct {
	Discovered atomic.Int64 // image files found so far
	Processed  atomic.Int64 // image files fingerprinted so far
	Bytes      atomic.Int64 // bytes read so far
	WalkDone   atomic.Bool  // the directory walk has finished; Discovered is final
}

// Options configures a scan.
type Options struct {
	Root       string
	Workers    int      // 0 means one worker per CPU core
	Extensions []string // empty means whatever the extractor looks at

	// Extractor decides what a fingerprint means for this run. Nil means
	// photos, so every caller written before the seam existed keeps its
	// behaviour exactly.
	Extractor fingerprint.Extractor

	// DeepScan asks for a similarity fingerprint as well as a content hash.
	// It costs a full read of every file, so it is off unless the caller
	// actually intends to match files that are alike rather than identical.
	DeepScan bool

	Progress *Progress // optional; updated live during the scan
}

// Result is the outcome of a scan.
type Result struct {
	Files    []FileMeta    `json:"files"`
	Errors   []ScanError   `json:"errors,omitempty"`
	Duration time.Duration `json:"duration"`
}

// ReadErrors counts files that could not be opened or read.
func (r *Result) ReadErrors() int {
	return r.countErrors(ErrorRead)
}

// DecodeErrors counts files that were read but whose image data was unusable.
func (r *Result) DecodeErrors() int {
	return r.countErrors(ErrorDecode)
}

func (r *Result) countErrors(kind ErrorKind) int {
	n := 0
	for _, e := range r.Errors {
		if e.Kind == kind {
			n++
		}
	}
	return n
}

// TotalBytes is the combined size of every file that was scanned.
func (r *Result) TotalBytes() int64 {
	var total int64
	for _, f := range r.Files {
		total += f.Size
	}
	return total
}

// workerOutput carries one file's outcome back to the aggregator. Both fields
// can be set at once: a photo whose pixels failed to decode still has a valid
// content hash, and is still worth reporting as a possible exact duplicate.
type workerOutput struct {
	meta    *FileMeta
	scanErr *ScanError
}

// Scan walks opts.Root and fingerprints every image it finds.
//
// It returns an error only when the scan as a whole could not run — a missing
// root directory, or cancellation via ctx. Problems with individual files are
// reported in Result.Errors.
func Scan(ctx context.Context, opts Options) (*Result, error) {
	start := time.Now()

	if opts.Root == "" {
		return nil, errors.New("scanner: no directory given")
	}
	info, err := os.Stat(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("scanner: cannot access %q: %w", opts.Root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("scanner: %q is not a directory", opts.Root)
	}

	workers := opts.Workers
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	extractor := opts.Extractor
	if extractor == nil {
		extractor = fingerprint.Default()
	}
	exts := opts.Extensions
	if len(exts) == 0 {
		exts = extractor.Extensions()
	}
	accepted := newExtSet(exts)

	paths := make(chan string, channelBuffer)
	results := make(chan workerOutput, channelBuffer)

	// errgroup gives us cancellation for free: if any stage fails, ctx is
	// cancelled and the rest wind down instead of blocking on a channel.
	group, ctx := errgroup.WithContext(ctx)

	// Stage 1 — walk the tree. Only this goroutine writes walkErrors, and it
	// is only read after group.Wait() returns, so no lock is needed.
	var walkErrors []ScanError
	group.Go(func() error {
		defer close(paths)
		if opts.Progress != nil {
			defer opts.Progress.WalkDone.Store(true)
		}
		var err error
		walkErrors, err = walk(ctx, opts.Root, accepted, paths, opts.Progress)
		return err
	})

	// Stage 2 — the worker pool.
	var wg sync.WaitGroup
	for range workers {
		wg.Add(1)
		group.Go(func() error {
			defer wg.Done()
			return work(ctx, paths, results, extractor, opts.DeepScan)
		})
	}
	go func() {
		wg.Wait()
		close(results)
	}()

	// Stage 3 — aggregate on the calling goroutine. Draining results here is
	// what keeps the workers from blocking on a full channel, and it is the one
	// place every finished file passes through, so it is where progress is
	// tallied.
	result := &Result{}
	var processed, bytesRead int64
	for out := range results {
		if out.meta != nil {
			result.Files = append(result.Files, *out.meta)
			bytesRead += out.meta.Size
		}
		if out.scanErr != nil {
			result.Errors = append(result.Errors, *out.scanErr)
		}
		processed++
		if opts.Progress != nil {
			opts.Progress.Processed.Store(processed)
			opts.Progress.Bytes.Store(bytesRead)
		}
	}

	if err := group.Wait(); err != nil {
		return nil, err
	}

	result.Errors = append(result.Errors, walkErrors...)

	// Workers finish in whatever order the disk hands files back. Sorting
	// makes the output stable, which matters for both tests and humans
	// re-running a scan.
	sort.Slice(result.Files, func(i, j int) bool {
		return result.Files[i].Path < result.Files[j].Path
	})
	sort.Slice(result.Errors, func(i, j int) bool {
		return result.Errors[i].Path < result.Errors[j].Path
	})

	result.Duration = time.Since(start)
	return result, nil
}

// skippedDirs are directories that never hold anything worth deduplicating, and
// that photocull must not touch on Windows.
//
// The operating-system entries matter more now than they used to. Documents
// mode looks at every extension, which makes "point it at C:\" a realistic
// thing for somebody to try — and hashing a Windows install is hours of I/O for
// no possible finding.
//
// The match is by directory name at any depth, so a user folder genuinely
// called "Windows" and full of documents would be skipped too. That is the same
// trade already accepted for $recycle.bin, and the alternative — matching
// absolute paths — breaks the moment the drive letter differs.
var skippedDirs = map[string]bool{
	"$recycle.bin":              true,
	"system volume information": true,
	"$windows.~bt":              true,
	"$windows.~ws":              true,
	"windows":                   true,
	"program files":             true,
	"program files (x86)":       true,
	"programdata":               true,
	"appdata":                   true,
	"node_modules":              true,
}

// extSet decides which files a scan looks at.
//
// A nil set means every file. That is not a convenience: the documents kind
// deliberately has no extension filter, because a .zip, an .mp3 or a legacy
// .doc still has to be compared by content hash, and a filter would leave blind
// spots exactly where the user assumed photocull was looking.
type extSet map[string]bool

func newExtSet(exts []string) extSet {
	if len(exts) == 0 {
		return nil
	}
	return fingerprint.NormaliseExtensions(exts)
}

func (s extSet) accepts(path string) bool {
	return s == nil || fingerprint.HasExtension(path, s)
}

func shouldSkipDir(name string) bool {
	if skippedDirs[strings.ToLower(name)] {
		return true
	}
	// Hidden directories: caches, version control, thumbnail stores.
	return len(name) > 1 && strings.HasPrefix(name, ".")
}

// walk lists candidate files, sending each one downstream. It returns the
// directories it could not read rather than failing the whole scan over one
// permission error.
func walk(ctx context.Context, root string, accepted extSet, out chan<- string, progress *Progress) ([]ScanError, error) {
	var problems []ScanError

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			problems = append(problems, ScanError{Path: path, Kind: ErrorRead, Message: err.Error()})
			if d != nil && d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			// Never skip the root itself, even if its name looks hidden.
			if path != root && shouldSkipDir(d.Name()) {
				return fs.SkipDir
			}
			return nil
		}

		// Symlinks and device files are not content to hash.
		if !d.Type().IsRegular() {
			return nil
		}
		if !accepted.accepts(path) {
			return nil
		}

		// Empty files are skipped. They are all byte-identical to each other, so
		// in documents mode — where every extension is scanned — every stray
		// zero-byte .log and placeholder on the disk would form one enormous
		// "exact duplicate" group worth nothing at all. A file that cannot
		// reclaim a single byte cannot be worth a person's attention.
		if info, err := d.Info(); err == nil && info.Size() == 0 {
			return nil
		}

		select {
		case out <- path:
			if progress != nil {
				progress.Discovered.Add(1)
			}
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})

	return problems, err
}

// work pulls paths until the channel closes or ctx is cancelled.
func work(ctx context.Context, paths <-chan string, out chan<- workerOutput, ex fingerprint.Extractor, deepScan bool) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case path, ok := <-paths:
			if !ok {
				return nil
			}
			select {
			case out <- process(path, ex, deepScan):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
}

// process reads one file exactly once and derives everything from that single
// pass.
func process(path string, ex fingerprint.Extractor, deepScan bool) workerOutput {
	f, err := os.Open(path)
	if err != nil {
		return workerOutput{scanErr: &ScanError{Path: path, Kind: ErrorRead, Message: err.Error()}}
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return workerOutput{scanErr: &ScanError{Path: path, Kind: ErrorRead, Message: err.Error()}}
	}

	hasher := hashing.NewSHA256()

	// The extractor and the hasher share one pass over the file. Reading every
	// file twice would double the I/O, and on an external drive holding
	// thousands of them the disk is the bottleneck, not the CPU.
	tee := io.TeeReader(f, hasher)

	meta := FileMeta{Path: path, Size: info.Size(), ModTime: info.ModTime()}
	var scanErr *ScanError

	out := ex.Fingerprint(tee, fingerprint.Input{Path: path, Size: info.Size(), Deep: deepScan})
	meta.Decoded = out.Understood
	meta.Width, meta.Height = out.Width, out.Height
	meta.Fingerprint, meta.HasFingerprint = out.Fingerprint, out.HasFingerprint
	if out.Err != nil {
		scanErr = &ScanError{Path: path, Kind: ErrorDecode, Message: out.Err.Error()}
	}

	// However much of the file the extractor consumed, the rest still has to
	// flow through the hasher — SHA-256 has to cover the whole file, not just
	// the part that happened to hold the content it understood.
	if _, err := io.Copy(io.Discard, tee); err != nil {
		return workerOutput{scanErr: &ScanError{Path: path, Kind: ErrorRead, Message: err.Error()}}
	}

	meta.SHA256 = hashing.HexSum(hasher)
	return workerOutput{meta: &meta, scanErr: scanErr}
}
