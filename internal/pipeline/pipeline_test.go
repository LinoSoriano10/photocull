package pipeline

import (
	"context"
	"path/filepath"
	"testing"
)

func TestRunExact(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/exact")
	a, err := Run(context.Background(), Options{Root: dir})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(a.Groups) != 1 || a.Groups[0].Type != "exact" {
		t.Fatalf("expected one exact group, got %+v", a.Groups)
	}
	if a.Stats.DuplicateFiles != 1 {
		t.Errorf("DuplicateFiles = %d, want 1", a.Stats.DuplicateFiles)
	}
	if !filepath.IsAbs(a.Root) {
		t.Errorf("Root %q should be absolute", a.Root)
	}
}

func TestRunSimilar(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/similar")
	a, err := Run(context.Background(), Options{Root: dir, Similar: true, Threshold: 8})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(a.Groups) != 1 || a.Groups[0].Type != "similar" {
		t.Fatalf("expected one similar group, got %+v", a.Groups)
	}
}

func TestRunReportRoundTrip(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/exact")
	a, err := Run(context.Background(), Options{Root: dir})
	if err != nil {
		t.Fatal(err)
	}
	rep := a.Report()
	if rep.Root != a.Root || rep.Stats.Groups != len(a.Groups) {
		t.Error("Report() did not reflect the analysis")
	}
}

func TestRunRejectsBadThreshold(t *testing.T) {
	for _, bad := range []int{-1, 65, 100} {
		if _, err := Run(context.Background(), Options{Root: ".", Threshold: bad}); err == nil {
			t.Errorf("threshold %d should be rejected", bad)
		}
	}
}

func TestRunRejectsBadStrategy(t *testing.T) {
	dir, _ := filepath.Abs("../../testdata/exact")
	if _, err := Run(context.Background(), Options{Root: dir, Strategy: "nonsense"}); err == nil {
		t.Error("an unknown strategy should be rejected")
	}
}

func TestRunRejectsMissingRoot(t *testing.T) {
	if _, err := Run(context.Background(), Options{Root: filepath.Join("no", "such", "dir")}); err == nil {
		t.Error("a missing root should error")
	}
}
