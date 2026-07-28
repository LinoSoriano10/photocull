// Package report turns scan results into something a person can read, or a
// script can parse.
package report

import (
	"encoding/json"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"photocull/internal/dedupe"
	"photocull/internal/scanner"
)

// Stats is the summary photocull prints at the end of a run — and the numbers
// worth quoting when talking about what the tool actually did.
type Stats struct {
	FilesScanned     int           `json:"filesScanned"`
	TotalBytes       int64         `json:"totalBytes"`
	Groups           int           `json:"groups"`
	ExactGroups      int           `json:"exactGroups"`
	SimilarGroups    int           `json:"similarGroups"`
	DuplicateFiles   int           `json:"duplicateFiles"`
	ReclaimableBytes int64         `json:"reclaimableBytes"`
	ReclaimedBytes   int64         `json:"reclaimedBytes,omitempty"`
	ReadErrors       int           `json:"readErrors"`
	DecodeErrors     int           `json:"decodeErrors"`
	Duration         time.Duration `json:"durationNanos"`
}

// Report bundles the stats with the groups behind them, for --json output and
// for the web UI.
type Report struct {
	Root   string              `json:"root"`
	Stats  Stats               `json:"stats"`
	Groups []dedupe.Group      `json:"groups"`
	Errors []scanner.ScanError `json:"errors,omitempty"`
}

// Build derives the summary from a scan and its grouping.
func Build(res *scanner.Result, groups []dedupe.Group) Stats {
	stats := Stats{
		FilesScanned: len(res.Files),
		TotalBytes:   res.TotalBytes(),
		Groups:       len(groups),
		ReadErrors:   res.ReadErrors(),
		DecodeErrors: res.DecodeErrors(),
		Duration:     res.Duration,
	}

	for _, g := range groups {
		switch g.Type {
		case dedupe.Exact:
			stats.ExactGroups++
		case dedupe.Similar:
			stats.SimilarGroups++
		}
		stats.DuplicateFiles += len(g.Files) - 1
		stats.ReclaimableBytes += g.ReclaimableBytes()
	}

	return stats
}

// JSON renders the report for scripts and for the web UI.
func (r Report) JSON() ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// Summary is the closing paragraph of a run.
func (s Stats) Summary() string {
	var b strings.Builder

	fmt.Fprintf(&b, "Scanned %s in %s (%s)\n",
		plural(s.FilesScanned, "file", "files"), HumanBytes(s.TotalBytes), s.Duration.Round(time.Millisecond))

	if s.Groups == 0 {
		b.WriteString("No duplicates found.\n")
		return b.String() + errorLines(s)
	}

	fmt.Fprintf(&b, "Found %s: %d exact, %d similar\n",
		plural(s.Groups, "duplicate group", "duplicate groups"), s.ExactGroups, s.SimilarGroups)
	fmt.Fprintf(&b, "%s could be removed, freeing %s\n",
		plural(s.DuplicateFiles, "file", "files"), HumanBytes(s.ReclaimableBytes))

	if s.ReclaimedBytes > 0 {
		fmt.Fprintf(&b, "Freed %s\n", HumanBytes(s.ReclaimedBytes))
	}

	return b.String() + errorLines(s)
}

func errorLines(s Stats) string {
	var b strings.Builder
	if s.ReadErrors > 0 {
		fmt.Fprintf(&b, "%s could not be read and %s skipped\n",
			plural(s.ReadErrors, "file", "files"), wasWere(s.ReadErrors))
	}
	if s.DecodeErrors > 0 {
		fmt.Fprintf(&b, "%s could not be decoded and %s compared by content only\n",
			plural(s.DecodeErrors, "file", "files"), wasWere(s.DecodeErrors))
	}
	return b.String()
}

// RenderGroups prints every duplicate group as a table, with the file
// photocull suggests keeping marked.
func RenderGroups(w io.Writer, root string, groups []dedupe.Group) {
	for i, g := range groups {
		fmt.Fprintf(w, "\nGroup %d  [%s]  %d files, %s reclaimable\n",
			i+1, g.Type, len(g.Files), HumanBytes(g.ReclaimableBytes()))

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for j, f := range g.Files {
			marker := "delete"
			if j == g.KeepIndex {
				marker = "KEEP"
			}
			fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
				marker,
				relativeTo(root, f.Path),
				HumanBytes(f.Size),
				dimensions(f),
				f.ModTime.Format("2006-01-02"),
			)
		}
		tw.Flush()
	}
}

func dimensions(f scanner.FileMeta) string {
	if !f.Decoded || f.Width == 0 || f.Height == 0 {
		return "?"
	}
	return fmt.Sprintf("%dx%d", f.Width, f.Height)
}

// relativeTo shortens paths against the scanned root so the table stays
// readable on deeply nested directories.
func relativeTo(root, path string) string {
	if root == "" {
		return path
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return path
	}
	return rel
}

// HumanBytes formats a byte count the way a person would say it.
func HumanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for size := n / unit; size >= unit; size /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func plural(n int, singular, pluralForm string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, singular)
	}
	return fmt.Sprintf("%d %s", n, pluralForm)
}

func wasWere(n int) string {
	if n == 1 {
		return "was"
	}
	return "were"
}
