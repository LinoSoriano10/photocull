package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"photocull/internal/hashing"
	"photocull/internal/scanner"
)

// ImportSubdir is the folder created inside the library where new photos are
// copied, so the additions are grouped and easy to review rather than scattered
// through the existing structure.
const ImportSubdir = "photocull_added"

// MergeOptions configures an "add to library" comparison.
type MergeOptions struct {
	Base      string // the library that is kept and added to
	Source    string // the folder whose new photos we want to bring in
	Similar   bool   // also treat look-alikes as "already have it"
	Threshold int
	Progress  *scanner.Progress
}

// MergeAnalysis is the outcome: which photos in Source are genuinely new.
type MergeAnalysis struct {
	BaseRoot     string              `json:"baseRoot"`
	SourceRoot   string              `json:"sourceRoot"`
	New          []scanner.FileMeta  `json:"new"`        // in Source, not in Base
	Duplicates   int                 `json:"duplicates"` // in Source, already in Base (or repeated in Source)
	BaseImages   int                 `json:"baseImages"`
	SourceImages int                 `json:"sourceImages"`
	Errors       []scanner.ScanError `json:"errors,omitempty"`
}

// Merge scans the library and the source folder and works out which photos in
// the source are not already in the library — by exact content, and optionally
// by visual similarity. It reads only; nothing is copied here.
func Merge(ctx context.Context, opts MergeOptions) (*MergeAnalysis, error) {
	if opts.Threshold < 0 || opts.Threshold > 64 {
		return nil, fmt.Errorf("threshold must be between 0 and 64, got %d", opts.Threshold)
	}

	base, err := filepath.Abs(opts.Base)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve library folder %q: %w", opts.Base, err)
	}
	source, err := filepath.Abs(opts.Source)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve source folder %q: %w", opts.Source, err)
	}
	if base == source {
		return nil, fmt.Errorf("the library and the source folder are the same")
	}

	// Index the library first, then walk the source. Both feed the same
	// progress counters, so the UI shows steady movement across the whole job.
	baseRes, err := scanner.Scan(ctx, scanner.Options{Root: base, ComputePHash: opts.Similar, Progress: opts.Progress})
	if err != nil {
		return nil, err
	}
	if opts.Progress != nil {
		// The source walk is a fresh discovery phase, not a continuation of the
		// library's, so let the counters keep climbing without claiming the
		// total is already known.
		opts.Progress.WalkDone.Store(false)
	}
	srcRes, err := scanner.Scan(ctx, scanner.Options{Root: source, ComputePHash: opts.Similar, Progress: opts.Progress})
	if err != nil {
		return nil, err
	}

	baseHashes := make(map[string]bool, len(baseRes.Files))
	var basePHashes []uint64
	for _, f := range baseRes.Files {
		if f.SHA256 != "" {
			baseHashes[f.SHA256] = true
		}
		if f.HasPHash {
			basePHashes = append(basePHashes, f.PHash)
		}
	}

	analysis := &MergeAnalysis{
		BaseRoot:     base,
		SourceRoot:   source,
		BaseImages:   len(baseRes.Files),
		SourceImages: len(srcRes.Files),
		Errors:       append(baseRes.Errors, srcRes.Errors...),
	}

	// Track what we have already accepted as new, so two copies of the same
	// new photo in the source do not both get imported.
	acceptedHashes := make(map[string]bool)
	var acceptedPHashes []uint64

	for _, f := range srcRes.Files {
		// Already in the library?
		if f.SHA256 != "" && baseHashes[f.SHA256] {
			analysis.Duplicates++
			continue
		}
		if opts.Similar && f.HasPHash && nearAny(f.PHash, basePHashes, opts.Threshold) {
			analysis.Duplicates++
			continue
		}
		// Already accounted for by an earlier source photo?
		if f.SHA256 != "" && acceptedHashes[f.SHA256] {
			analysis.Duplicates++
			continue
		}
		if opts.Similar && f.HasPHash && nearAny(f.PHash, acceptedPHashes, opts.Threshold) {
			analysis.Duplicates++
			continue
		}

		analysis.New = append(analysis.New, f)
		if f.SHA256 != "" {
			acceptedHashes[f.SHA256] = true
		}
		if f.HasPHash {
			acceptedPHashes = append(acceptedPHashes, f.PHash)
		}
	}

	sort.Slice(analysis.New, func(i, j int) bool {
		return analysis.New[i].Path < analysis.New[j].Path
	})
	sort.Slice(analysis.Errors, func(i, j int) bool {
		return analysis.Errors[i].Path < analysis.Errors[j].Path
	})

	return analysis, nil
}

// nearAny reports whether h is within threshold of any hash in hs.
func nearAny(h uint64, hs []uint64, threshold int) bool {
	for _, b := range hs {
		if hashing.Distance(h, b) <= threshold {
			return true
		}
	}
	return false
}

// CopyInto copies the given files into an "added" subfolder of the library,
// keeping the originals where they are. Names that collide get a numeric
// suffix, and timestamps are preserved so a later scan keeps its bearings.
//
// It returns how many were copied and the paths it could not copy.
func CopyInto(baseDir string, paths []string) (copied int, failed []string, err error) {
	dest := filepath.Join(baseDir, ImportSubdir)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return 0, nil, fmt.Errorf("cannot create %q: %w", dest, err)
	}

	for _, src := range paths {
		target := freeName(filepath.Join(dest, filepath.Base(src)))
		if copyErr := copyFile(src, target); copyErr != nil {
			failed = append(failed, src)
			continue
		}
		copied++
	}
	return copied, failed, nil
}

// freeName returns target, or target with " (2)", " (3)", … inserted before the
// extension until it does not clash with an existing file.
func freeName(target string) string {
	if _, err := os.Stat(target); os.IsNotExist(err) {
		return target
	}
	dir := filepath.Dir(target)
	ext := filepath.Ext(target)
	base := target[:len(target)-len(ext)]
	base = filepath.Base(base)
	for n := 2; ; n++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, n, ext))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}

	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}

	// Preserve the original modification time; the access time is unimportant.
	if info, err := os.Stat(src); err == nil {
		_ = os.Chtimes(dst, time.Now(), info.ModTime())
	}
	return nil
}
