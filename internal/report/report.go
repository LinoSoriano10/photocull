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

	// The related tier is reported separately from the figures above, because
	// it is a set of guesses and the headline must stay honest.
	RelatedGroups int   `json:"relatedGroups,omitempty"`
	RelatedFiles  int   `json:"relatedFiles,omitempty"`
	RelatedBytes  int64 `json:"relatedBytes,omitempty"`

	// Kind records what was scanned, so the review UI can present itself
	// correctly without being told separately.
	Kind string `json:"kind,omitempty"`
}

// Report bundles the stats with the groups behind them, for --json output and
// for the web UI.
type Report struct {
	Root   string              `json:"root"`
	Stats  Stats               `json:"stats"`
	Groups []dedupe.Group      `json:"groups"`
	Errors []scanner.ScanError `json:"errors,omitempty"`
}

// Counts is what a set of groups adds up to.
//
// It exists because two places need the answer: the report built after a scan,
// and the web UI's refresh after files are deleted. They used to each carry
// their own copy of the tally, which meant every new kind of group had to be
// remembered in two files or the numbers would quietly disagree.
//
// Related groups are counted separately from the other two, and that separation
// is the point rather than bookkeeping. A related group is a guess, and the
// headline figure — "241 files could be removed, freeing 9.3 GB" — must not be
// inflated with guesses. It is tempting to fold them in because it makes the
// tool look better; that temptation is exactly why the split is written down.
type Counts struct {
	Exact, Similar, Related int

	// DuplicateFiles and ReclaimableBytes cover the confident tiers only.
	DuplicateFiles   int
	ReclaimableBytes int64

	// RelatedFiles and RelatedBytes are what *might* be freed, after a person
	// has looked.
	RelatedFiles int
	RelatedBytes int64
}

// CountGroups tallies what the given groups add up to.
func CountGroups(groups []dedupe.Group) Counts {
	var c Counts
	for _, g := range groups {
		if g.Type == dedupe.Related {
			c.Related++
			c.RelatedFiles += len(g.Files) - 1
			c.RelatedBytes += g.ReclaimableBytes()
			continue
		}
		switch g.Type {
		case dedupe.Exact:
			c.Exact++
		case dedupe.Similar:
			c.Similar++
		}
		c.DuplicateFiles += len(g.Files) - 1
		c.ReclaimableBytes += g.ReclaimableBytes()
	}
	return c
}

// applyTo writes a tally into the group-derived fields of stats, leaving the
// scan-time totals alone.
func (c Counts) applyTo(stats *Stats, groups []dedupe.Group) {
	stats.Groups = len(groups)
	stats.ExactGroups = c.Exact
	stats.SimilarGroups = c.Similar
	stats.RelatedGroups = c.Related
	stats.DuplicateFiles = c.DuplicateFiles
	stats.ReclaimableBytes = c.ReclaimableBytes
	stats.RelatedFiles = c.RelatedFiles
	stats.RelatedBytes = c.RelatedBytes
}

// Build derives the summary from a scan and its grouping.
func Build(res *scanner.Result, groups []dedupe.Group) Stats {
	stats := Stats{
		FilesScanned: len(res.Files),
		TotalBytes:   res.TotalBytes(),
		ReadErrors:   res.ReadErrors(),
		DecodeErrors: res.DecodeErrors(),
		Duration:     res.Duration,
	}
	CountGroups(groups).applyTo(&stats, groups)
	return stats
}

// Recount refreshes the group-derived figures after files have been deleted,
// leaving the scan-time totals (files scanned, bytes read) untouched.
func Recount(base Stats, groups []dedupe.Group) Stats {
	CountGroups(groups).applyTo(&base, groups)
	return base
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

	confident := s.Groups - s.RelatedGroups
	fmt.Fprintf(&b, "Found %s: %d exact, %d similar\n",
		plural(confident, "duplicate group", "duplicate groups"), s.ExactGroups, s.SimilarGroups)
	fmt.Fprintf(&b, "%s could be removed, freeing %s\n",
		plural(s.DuplicateFiles, "file", "files"), HumanBytes(s.ReclaimableBytes))

	// Kept out of the sentence above on purpose: these are guesses, and folding
	// them into the headline would overstate what photocull actually knows.
	if s.RelatedGroups > 0 {
		fmt.Fprintf(&b, "\nAlso found %s where the names and sizes line up but photocull could not read inside the files.\nAbout %s might be freed after a look. Nothing is pre-selected and clean will not touch them.\n",
			plural(s.RelatedGroups, "group", "groups"), HumanBytes(s.RelatedBytes))
	}

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
		fmt.Fprintf(&b, "%s could not be interpreted and %s compared by content only\n",
			plural(s.DecodeErrors, "file", "files"), wasWere(s.DecodeErrors))
	}
	return b.String()
}

// RenderGroups prints every duplicate group as a table, with the file
// photocull suggests keeping marked.
func RenderGroups(w io.Writer, root string, groups []dedupe.Group) {
	for i, g := range groups {
		if g.Type == dedupe.Related {
			fmt.Fprintf(w, "\nGroup %d  [%s]  %d files, %s if they turn out to be copies\n",
				i+1, g.Type, len(g.Files), HumanBytes(g.ReclaimableBytes()))
		} else {
			fmt.Fprintf(w, "\nGroup %d  [%s]  %d files, %s reclaimable\n",
				i+1, g.Type, len(g.Files), HumanBytes(g.ReclaimableBytes()))
		}

		// The dimensions column is dropped when nothing in the group has any.
		// On a documents scan it would otherwise be a column of question marks
		// down the whole report, which is worse than no column at all.
		showDimensions := anyDecodedImage(g.Files)

		tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
		for j, f := range g.Files {
			// "review", not "delete": clean refuses to act on a related group,
			// so a column that said delete would be promising something the
			// tool deliberately will not do.
			marker := "delete"
			if g.Type == dedupe.Related {
				marker = "review"
			}
			if j == g.KeepIndex {
				marker = "KEEP"
			}
			if showDimensions {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\t%s\n",
					marker, relativeTo(root, f.Path), HumanBytes(f.Size), dimensions(f), f.ModTime.Format("2006-01-02"))
			} else {
				fmt.Fprintf(tw, "  %s\t%s\t%s\t%s\n",
					marker, relativeTo(root, f.Path), HumanBytes(f.Size), f.ModTime.Format("2006-01-02"))
			}
		}
		tw.Flush()
	}
}

func anyDecodedImage(files []scanner.FileMeta) bool {
	for _, f := range files {
		if f.Decoded && f.Width > 0 && f.Height > 0 {
			return true
		}
	}
	return false
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
