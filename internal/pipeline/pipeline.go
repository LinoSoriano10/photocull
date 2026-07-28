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
	"photocull/internal/fingerprint"
	"photocull/internal/report"
	"photocull/internal/scanner"
)

// Options configures one run.
type Options struct {
	Root       string
	Workers    int      // 0 means one worker per CPU core
	Extensions []string // empty means whatever the kind looks at

	// Kind selects what photocull is looking for. Empty means photos, so every
	// caller written before documents existed keeps working unchanged.
	Kind string

	Similar   bool              // also match files that are alike, not just identical
	Threshold int               // fingerprint distance for --similar (0-64)
	Strategy  string            // which copy to suggest keeping; "" means the kind's default
	Progress  *scanner.Progress // optional; updated live during the scan
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

	extractor, err := resolveKind(opts.Kind)
	if err != nil {
		return nil, err
	}
	policy := fingerprint.DefaultsFor(extractor.Kind())

	strategy := opts.Strategy
	if strategy == "" {
		strategy = policy.Strategy
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
		Root:       root,
		Workers:    opts.Workers,
		Extensions: opts.Extensions,
		Extractor:  extractor,
		DeepScan:   opts.Similar,
		Progress:   opts.Progress,
	})
	if err != nil {
		return nil, err
	}

	var groups []dedupe.Group
	if opts.Similar {
		// A similarity scan with a zero tolerance would only match fingerprints
		// that are bit-for-bit equal, which is a stricter and slower version of
		// what GroupExact already does. Nobody means that, so an unset
		// threshold resolves to the kind's default here rather than silently
		// producing an empty result for a caller that forgot to set it.
		threshold := opts.Threshold
		if threshold <= 0 {
			threshold = policy.Threshold
		}
		groups = dedupe.GroupSimilar(result.Files, threshold, keep)
	} else {
		groups = dedupe.GroupExact(result.Files, keep)
	}

	// The low-confidence pass runs last, and its groups are appended rather
	// than merged into the sort. Confident groups come first whatever their
	// size: burying a certain 4 GB exact group beneath a speculative 8 GB
	// related one would be exactly backwards for somebody reviewing by eye.
	//
	// Composition lives here rather than in dedupe, so that package stays free
	// of any knowledge about documents.
	if policy.Related {
		groups = append(groups, dedupe.GroupRelated(result.Files, dedupe.PathsIn(groups), keep)...)
	}

	stats := report.Build(result, groups)
	stats.Kind = string(extractor.Kind())

	return &Analysis{
		Root:   root,
		Result: result,
		Groups: groups,
		Stats:  stats,
	}, nil
}

// resolveKind turns a --kind value into an extractor.
func resolveKind(kind string) (fingerprint.Extractor, error) {
	if kind == "" {
		return fingerprint.Default(), nil
	}
	return fingerprint.Lookup(kind)
}
