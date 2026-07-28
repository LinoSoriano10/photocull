package cli

import (
	"context"
	"strings"

	"github.com/spf13/cobra"

	"photocull/internal/dedupe"
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
		"also match photos that only look the same (resized, re-compressed, re-encoded)")
	cmd.Flags().IntVar(&m.threshold, "threshold", dedupe.DefaultThreshold,
		"how different two photos may look and still match, 0-64; lower is stricter")
	cmd.Flags().StringVar(&m.strategy, "strategy", "default",
		"which copy to suggest keeping: "+strings.Join(dedupe.StrategyNames(), ", "))
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
func analyze(ctx context.Context, dir string, global *globalFlags, match *matchFlags) (*analysis, error) {
	an, err := pipeline.Run(ctx, pipeline.Options{
		Root:       dir,
		Workers:    global.workers,
		Extensions: global.extensions,
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
