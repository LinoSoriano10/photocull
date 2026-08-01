package pipeline

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"photocull/internal/fingerprint"
	"photocull/internal/hashing"
	"photocull/internal/scanner"
)

// ImportSubdir is the folder created inside the library where new photos are
// copied, so the additions are grouped and easy to review rather than scattered
// through the existing structure.
const ImportSubdir = "photocull_added"

// MergeOptions configures an "add to library" comparison.
type MergeOptions struct {
	Base       string   // the library that is kept and added to
	Source     string   // the folder whose new files we want to bring in
	Extensions []string // empty means whatever the kind looks at

	// Kind selects what is being merged. Empty means photos, so every caller
	// written before documents existed keeps working unchanged.
	Kind string

	Similar   bool // also treat look-alikes as "already have it"
	Threshold int
	Progress  *scanner.Progress
}

// MergeAnalysis is the outcome: which files in Source are genuinely new.
type MergeAnalysis struct {
	BaseRoot    string              `json:"baseRoot"`
	SourceRoot  string              `json:"sourceRoot"`
	New         []scanner.FileMeta  `json:"new"`        // in Source, not in Base
	Duplicates  int                 `json:"duplicates"` // in Source, already in Base (or repeated in Source)
	BaseFiles   int                 `json:"baseFiles"`
	SourceFiles int                 `json:"sourceFiles"`
	Errors      []scanner.ScanError `json:"errors,omitempty"`

	// Kind records what was compared, so a caller can describe the result in
	// the user's terms without having to remember what it asked for. Stats
	// carries the same field after a scan, for the same reason.
	Kind string `json:"kind,omitempty"`
}

// Merge scans the library and the source folder and works out which files in
// the source are not already in the library — by exact content, and optionally
// by content similarity. It reads only; nothing is copied here.
//
// The low-confidence related tier is deliberately absent, and its absence is a
// decision rather than an omission. Merge answers "do I already have this?",
// and a guess based on nothing but a matching name and size answering *yes*
// means a genuinely new document is never imported while the user is told the
// import was complete. A wrong guess costs a file here; in a duplicate review
// it only costs a second look.
func Merge(ctx context.Context, opts MergeOptions) (*MergeAnalysis, error) {
	if opts.Threshold < 0 || opts.Threshold > 64 {
		return nil, fmt.Errorf("threshold must be between 0 and 64, got %d", opts.Threshold)
	}

	extractor, err := resolveKind(opts.Kind)
	if err != nil {
		return nil, err
	}
	threshold := opts.Threshold
	if opts.Similar && threshold <= 0 {
		threshold = fingerprint.DefaultsFor(extractor.Kind()).Threshold
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
	//
	// Both scans get the same extractor, and that is the load-bearing part: the
	// two sides are compared by Hamming distance, so a library fingerprinted as
	// photos against a source fingerprinted as documents would produce distances
	// that mean nothing at all.
	scan := func(root string) (*scanner.Result, error) {
		return scanner.Scan(ctx, scanner.Options{
			Root:       root,
			Extensions: opts.Extensions,
			Extractor:  extractor,
			DeepScan:   opts.Similar,
			Progress:   opts.Progress,
		})
	}

	baseRes, err := scan(base)
	if err != nil {
		return nil, err
	}
	if opts.Progress != nil {
		// The source walk is a fresh discovery phase, not a continuation of the
		// library's, so let the counters keep climbing without claiming the
		// total is already known.
		opts.Progress.WalkDone.Store(false)
	}
	srcRes, err := scan(source)
	if err != nil {
		return nil, err
	}

	// The library is indexed rather than kept as a flat list, because otherwise
	// every source file is compared against every library fingerprint: a small
	// import into a large library costs sources × library. See hashing.Index —
	// it narrows the field and the real distance still decides, so this is the
	// same answer, found sooner.
	baseHashes := make(map[string]bool, len(baseRes.Files))
	baseIndex := hashing.NewIndex(threshold, len(baseRes.Files))
	baseSearch := baseIndex.Search()
	for _, f := range baseRes.Files {
		if f.SHA256 != "" {
			baseHashes[f.SHA256] = true
		}
		if f.HasFingerprint {
			baseIndex.Add(baseIndex.Len(), f.Fingerprint)
		}
	}

	analysis := &MergeAnalysis{
		BaseRoot:    base,
		SourceRoot:  source,
		BaseFiles:   len(baseRes.Files),
		SourceFiles: len(srcRes.Files),
		Errors:      append(baseRes.Errors, srcRes.Errors...),
		Kind:        string(extractor.Kind()),
	}

	// Track what we have already accepted as new, so two copies of the same
	// new photo in the source do not both get imported. This index grows as the
	// loop runs, which is the one case where a Searcher outlives an Add.
	acceptedHashes := make(map[string]bool)
	acceptedIndex := hashing.NewIndex(threshold, len(srcRes.Files))
	acceptedSearch := acceptedIndex.Search()

	for _, f := range srcRes.Files {
		// Already in the library?
		if f.SHA256 != "" && baseHashes[f.SHA256] {
			analysis.Duplicates++
			continue
		}
		if opts.Similar && f.HasFingerprint && nearAny(baseSearch, f.Fingerprint) {
			analysis.Duplicates++
			continue
		}
		// Already accounted for by an earlier source photo?
		if f.SHA256 != "" && acceptedHashes[f.SHA256] {
			analysis.Duplicates++
			continue
		}
		if opts.Similar && f.HasFingerprint && nearAny(acceptedSearch, f.Fingerprint) {
			analysis.Duplicates++
			continue
		}

		analysis.New = append(analysis.New, f)
		if f.SHA256 != "" {
			acceptedHashes[f.SHA256] = true
		}
		if f.HasFingerprint {
			acceptedIndex.Add(acceptedIndex.Len(), f.Fingerprint)
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

// nearAny reports whether the index behind search holds any fingerprint within
// its threshold of h.
//
// Near has no early exit — it offers every neighbour — but the question here is
// only whether there is one, and stopping at the first is what a duplicate
// check wants. The flag is set from inside the callback rather than returned,
// which reads oddly and is the price of a visitor that cannot be broken out of.
func nearAny(search *hashing.Searcher, h uint64) bool {
	found := false
	search.Near(h, func(int) { found = true })
	return found
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
