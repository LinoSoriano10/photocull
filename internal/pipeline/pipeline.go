// Package pipeline ties the scan, grouping and stats steps into one call.
//
// It is the single place that turns "a folder plus some options" into "the
// duplicate groups and the summary". Both the CLI and the web launcher use it,
// so the two can never drift in how they detect duplicates.
package pipeline

import (
	"context"
	"fmt"
	"path/filepath"

	"photocull/internal/dedupe"
	"photocull/internal/report"
	"photocull/internal/scanner"
)

// Options configures one run.
type Options struct {
	Root       string
	Workers    int               // 0 means one worker per CPU core
	Extensions []string          // empty means imageutil.DefaultExtensions
	Similar    bool              // also match visually similar photos, not just exact copies
	Threshold  int               // perceptual distance for --similar (0-64)
	Strategy   string            // which copy to suggest keeping; "" means "default"
	Progress   *scanner.Progress // optional; updated live during the scan
}

// Analysis is a completed run: the raw scan plus everything derived from it.
type Analysis struct {
	Root   string
	Result *scanner.Result
	Groups []dedupe.Group
	Stats  report.Stats
}

// Report packages the analysis for JSON output and the web UI.
func (a *Analysis) Report() report.Report {
	return report.Report{
		Root:   a.Root,
		Stats:  a.Stats,
		Groups: a.Groups,
		Errors: a.Result.Errors,
	}
}

// Run scans Root and groups the duplicates it finds.
func Run(ctx context.Context, opts Options) (*Analysis, error) {
	if opts.Threshold < 0 || opts.Threshold > 64 {
		return nil, fmt.Errorf("threshold must be between 0 and 64, got %d", opts.Threshold)
	}

	strategy := opts.Strategy
	if strategy == "" {
		strategy = "default"
	}
	keep, err := dedupe.LookupStrategy(strategy)
	if err != nil {
		return nil, err
	}

	root, err := filepath.Abs(opts.Root)
	if err != nil {
		return nil, fmt.Errorf("cannot resolve %q: %w", opts.Root, err)
	}

	result, err := scanner.Scan(ctx, scanner.Options{
		Root:         root,
		Workers:      opts.Workers,
		Extensions:   opts.Extensions,
		ComputePHash: opts.Similar,
		Progress:     opts.Progress,
	})
	if err != nil {
		return nil, err
	}

	var groups []dedupe.Group
	if opts.Similar {
		groups = dedupe.GroupSimilar(result.Files, opts.Threshold, keep)
	} else {
		groups = dedupe.GroupExact(result.Files, keep)
	}

	return &Analysis{
		Root:   root,
		Result: result,
		Groups: groups,
		Stats:  report.Build(result, groups),
	}, nil
}
