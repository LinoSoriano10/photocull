package cli

import (
	"strings"

	"github.com/spf13/cobra"

	"photocull/internal/dedupe"
	"photocull/internal/fingerprint"
	"photocull/internal/pipeline"
	"photocull/internal/report"
	"photocull/internal/scanner"
)

// matchFlags configure how duplicates are detected. Both scan and clean take
// them, so they live here.
type matchFlags struct {
	similar   bool
	threshold int
	strategy  string
}

func (m *matchFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&m.similar, "similar", false,
		"also match files that are alike rather than identical (a resized photo, a re-saved document)")
	cmd.Flags().IntVar(&m.threshold, "threshold", dedupe.DefaultThreshold,
		"how different two files may be and still match, 0-64; lower is stricter (default: 8 for photos, 6 for documents)")
	cmd.Flags().StringVar(&m.strategy, "strategy", "",
		"which copy to suggest keeping: "+strings.Join(dedupe.StrategyNames(), ", ")+" (default: resolution for photos, newest for documents)")
}

// resolve fills in the defaults that depend on --kind.
//
// Both are left to the kind unless the user actually typed the flag, which is
// why this asks cobra rather than comparing against a sentinel: --threshold 8 on
// a documents scan is a deliberate choice and must survive.
func (m *matchFlags) resolve(cmd *cobra.Command, kind string) {
	if !cmd.Flags().Changed("threshold") {
		m.threshold = fingerprint.DefaultsFor(fingerprint.Kind(kind)).Threshold
	}
}

// analysis is a completed scan plus everything derived from it.
type analysis struct {
	root   string
	result *scanner.Result
	groups []dedupe.Group
	stats  report.Stats
}

// report packages the analysis for JSON output and for the web UI.
func (a analysis) report() report.Report {
	return report.Report{
		Root:   a.root,
		Stats:  a.stats,
		Groups: a.groups,
		Errors: a.result.Errors,
	}
}

// analyze scans a directory and groups the duplicates it finds. It is the step
// scan, clean and serve all begin with, and it delegates to the shared
// pipeline so the CLI and the web launcher stay in lockstep.
func analyze(cmd *cobra.Command, dir string, global *globalFlags, match *matchFlags) (*analysis, error) {
	// Resolve the kind-dependent defaults here, once, so scan, clean and serve
	// cannot drift apart on what --threshold means.
	match.resolve(cmd, global.kind)

	an, err := pipeline.Run(cmd.Context(), pipeline.Options{
		Root:       dir,
		Workers:    global.workers,
		Extensions: global.extensions,
		Kind:       global.kind,
		Similar:    match.similar,
		Threshold:  match.threshold,
		Strategy:   match.strategy,
	})
	if err != nil {
		return nil, err
	}
	return &analysis{
		root:   an.Root,
		result: an.Result,
		groups: an.Groups,
		stats:  an.Stats,
	}, nil
}
