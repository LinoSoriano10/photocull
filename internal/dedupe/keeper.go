package dedupe

import (
	"fmt"
	"sort"
	"strings"

	"photocull/internal/scanner"
)

// KeepStrategy picks which file in a group is worth keeping, returning its
// index. It never deletes anything: the choice is a suggestion that arrives
// pre-selected in the CLI output and in the web UI, and a human still has to
// confirm it.
type KeepStrategy func(files []scanner.FileMeta) int

// KeepHighestResolution favours the copy with the most pixels — the one least
// likely to have been downscaled by a messaging app or a backup tool.
func KeepHighestResolution(files []scanner.FileMeta) int {
	return pick(files, func(a, b scanner.FileMeta) bool {
		return a.Pixels() > b.Pixels()
	})
}

// KeepOldest favours the earliest modification time, on the assumption that
// the original predates every copy of it.
func KeepOldest(files []scanner.FileMeta) int {
	return pick(files, func(a, b scanner.FileMeta) bool {
		return a.ModTime.Before(b.ModTime)
	})
}

// KeepLargest favours the largest file, a rough proxy for the least
// re-compressed copy.
func KeepLargest(files []scanner.FileMeta) int {
	return pick(files, func(a, b scanner.FileMeta) bool {
		return a.Size > b.Size
	})
}

// KeepNewest favours the most recently modified copy.
func KeepNewest(files []scanner.FileMeta) int {
	return pick(files, func(a, b scanner.FileMeta) bool {
		return a.ModTime.After(b.ModTime)
	})
}

// DocumentKeepStrategy prefers the newest copy, breaking ties with the larger
// file and then the shallowest path.
//
// This is the exact opposite of the photo default, and the inversion is the
// whole point rather than an oversight. A photograph's oldest copy is the
// original, and every later one is a re-export that has lost pixels. A
// document's newest copy is the revision the person actually worked on, and the
// older ones are superseded drafts. Applying the photo rule to documents would
// confidently suggest keeping the draft and recycling the final version.
//
// Newest-first applies only where the files actually differ. For byte-identical
// copies there is no "later revision" at all — only a copy somebody made later,
// which on any real disk is the backup rather than the original. Modification
// time there is noise that would actively pick the wrong file, so those groups
// fall back to the rule that still carries information: the shallower path is
// the one a person filed deliberately.
func DocumentKeepStrategy(files []scanner.FileMeta) int {
	if identicalContent(files) {
		return pick(files, neverBetter)
	}
	return pick(files, func(a, b scanner.FileMeta) bool {
		if !a.ModTime.Equal(b.ModTime) {
			return a.ModTime.After(b.ModTime)
		}
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		return shallower(a.Path, b.Path)
	})
}

// neverBetter makes pick fall through to its shallowest-path tiebreak, which is
// the whole ranking when nothing else distinguishes the files.
func neverBetter(_, _ scanner.FileMeta) bool { return false }

// identicalContent reports whether every file in the group carries the same
// content hash — that is, whether this is an exact-duplicate group.
func identicalContent(files []scanner.FileMeta) bool {
	if len(files) < 2 {
		return true
	}
	first := files[0].SHA256
	if first == "" {
		return false
	}
	for _, f := range files[1:] {
		if f.SHA256 != first {
			return false
		}
	}
	return true
}

// DefaultKeepStrategy prefers the highest resolution, breaking ties with the
// largest file and then the oldest timestamp.
//
// For byte-identical duplicates every one of those is a tie by definition, so
// the final tiebreak does the real work: the shallowest path wins. Originals
// tend to sit in the folder you actually organised, while copies accumulate
// under nested backup directories.
func DefaultKeepStrategy(files []scanner.FileMeta) int {
	return pick(files, func(a, b scanner.FileMeta) bool {
		if a.Pixels() != b.Pixels() {
			return a.Pixels() > b.Pixels()
		}
		if a.Size != b.Size {
			return a.Size > b.Size
		}
		if !a.ModTime.Equal(b.ModTime) {
			return a.ModTime.Before(b.ModTime)
		}
		return shallower(a.Path, b.Path)
	})
}

// pick returns the index of the file that beats every other under better.
// Ties fall back to path order so the result never depends on scan order.
func pick(files []scanner.FileMeta, better func(a, b scanner.FileMeta) bool) int {
	if len(files) == 0 {
		return 0
	}
	best := 0
	for i := 1; i < len(files); i++ {
		switch {
		case better(files[i], files[best]):
			best = i
		case better(files[best], files[i]):
			// current best stands
		case shallower(files[i].Path, files[best].Path):
			best = i
		}
	}
	return best
}

// shallower reports whether a sits at a less nested path than b, falling back
// to lexical order so the comparison is total and deterministic.
func shallower(a, b string) bool {
	da, db := depth(a), depth(b)
	if da != db {
		return da < db
	}
	return a < b
}

func depth(path string) int {
	return strings.Count(strings.ReplaceAll(path, "\\", "/"), "/")
}

// strategies maps the --strategy flag values to their implementations.
var strategies = map[string]KeepStrategy{
	"default":    DefaultKeepStrategy,
	"resolution": KeepHighestResolution,
	"oldest":     KeepOldest,
	"largest":    KeepLargest,
	"newest":     KeepNewest,
	"document":   DocumentKeepStrategy,
}

// StrategyNames lists the accepted --strategy values, sorted for help text.
func StrategyNames() []string {
	names := make([]string, 0, len(strategies))
	for name := range strategies {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// LookupStrategy resolves a --strategy value.
func LookupStrategy(name string) (KeepStrategy, error) {
	if s, ok := strategies[strings.ToLower(strings.TrimSpace(name))]; ok {
		return s, nil
	}
	return nil, fmt.Errorf("unknown keep strategy %q (available: %s)", name, strings.Join(StrategyNames(), ", "))
}
