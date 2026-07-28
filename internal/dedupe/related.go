package dedupe

import (
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"photocull/internal/scanner"
)

// Tuning for the related pass. These are all judgements about how people name
// files, not about hashing, and each one exists to keep a weak signal from
// producing confident-looking nonsense.
const (
	// MinStemRunes rejects stems too generic to mean anything: "a", "1", "img".
	MinStemRunes = 4

	// MaxRelatedGroup drops groups so large the stem is clearly generic. Forty
	// files sharing the name "scan" is not a finding; it is a word, and nobody
	// is going to review it.
	MaxRelatedGroup = 12

	// SizeTolerance is how much two files' sizes may differ and still be
	// plausible copies of one another.
	SizeTolerance = 0.10

	// SizeToleranceFloor keeps the proportional tolerance from collapsing to
	// nothing on small files, where a byte or two of metadata is a large
	// fraction of the whole.
	SizeToleranceFloor = 1024
)

// PathsIn collects every path the given groups already claim, so a later pass
// can avoid reporting the same file twice.
func PathsIn(groups []Group) map[string]bool {
	claimed := make(map[string]bool)
	for _, g := range groups {
		for _, f := range g.Files {
			claimed[f.Path] = true
		}
	}
	return claimed
}

// GroupRelated finds files photocull could not read inside at all, but whose
// names and sizes suggest they are copies of each other.
//
// It is a separate pass on purpose, and it deliberately does not go near
// GroupSimilar's union-find. Union-find is transitive: it follows a chain of
// pairwise links and hands the whole cluster over as one decision, which is
// right for a hash strong enough to trust. A filename match is not that signal.
// Fed into the same structure, "informe" would link to "informe (1)", which
// would link to "informe final", which would link to a completely different
// "informe" three folders away, and the user would be shown one garbage group
// of forty unrelated files and invited to delete most of it.
//
// exclude holds every path already placed in a hash or fingerprint group, so no
// file is ever reported twice.
func GroupRelated(files []scanner.FileMeta, exclude map[string]bool, keep KeepStrategy) []Group {
	// Only files with no fingerprint are eligible. Where the contents *were*
	// read, the fingerprint is a strictly better signal, and adding a name
	// match on top of it could only add false positives to a group that was
	// already decided correctly.
	buckets := make(map[string][]scanner.FileMeta)
	for _, f := range files {
		if f.HasFingerprint || exclude[f.Path] {
			continue
		}
		stem := normaliseStem(f.Path)
		if len([]rune(stem)) < MinStemRunes {
			continue
		}
		buckets[stem] = append(buckets[stem], f)
	}

	var groups []Group
	for _, bucket := range buckets {
		groups = append(groups, groupBySize(bucket, keep)...)
	}

	sort.Slice(groups, func(i, j int) bool {
		if a, b := groups[i].ReclaimableBytes(), groups[j].ReclaimableBytes(); a != b {
			return a > b
		}
		return groups[i].ID < groups[j].ID
	})
	return groups
}

// groupBySize splits one stem's files into groups of similar size.
//
// It seeds greedily from the largest ungrouped file and takes everything within
// tolerance of *that seed* — never chaining from one member to the next. Three
// files at 1.0, 1.05 and 1.5 MB therefore produce one group of two and leave
// the third out, rather than linking into a group of three whose ends have
// nothing to do with each other. Non-transitivity is the entire point of this
// function.
func groupBySize(files []scanner.FileMeta, keep KeepStrategy) []Group {
	sort.Slice(files, func(i, j int) bool {
		if files[i].Size != files[j].Size {
			return files[i].Size > files[j].Size
		}
		return files[i].Path < files[j].Path
	})

	var groups []Group
	used := make([]bool, len(files))

	for i := range files {
		if used[i] {
			continue
		}
		seed := files[i]
		members := []scanner.FileMeta{seed}
		used[i] = true

		tolerance := int64(float64(seed.Size) * SizeTolerance)
		if tolerance < SizeToleranceFloor {
			tolerance = SizeToleranceFloor
		}

		for j := i + 1; j < len(files); j++ {
			if used[j] {
				continue
			}
			if seed.Size-files[j].Size <= tolerance {
				members = append(members, files[j])
				used[j] = true
			}
		}

		if len(members) < 2 || len(members) > MaxRelatedGroup {
			continue
		}

		sort.Slice(members, func(a, b int) bool { return members[a].Path < members[b].Path })
		groups = append(groups, Group{
			ID:   groupID(members),
			Type: Related, // set directly: classify() would report exact or similar
			// A keeper is still suggested so the group reads like any other,
			// but nothing is pre-selected in the UI and clean refuses to touch
			// these. KeepIndex must be a real index — an out-of-range value
			// makes Duplicates() return every file in the group.
			KeepIndex: keep(members),
			Files:     members,
		})
	}
	return groups
}

// copySuffix matches the marks operating systems and people leave when they
// copy a file: " (1)", " - copia", "_v2", "-final".
//
// The Spanish forms are here because that is what this user's own drive
// contains; Windows in es-ES writes "Copia de X" and "X - copia". The list is
// per-locale by nature and extending it is one line.
// Each suffix pattern requires a real separator before the word it strips, or
// the start of the stem. Allowing the separator to be empty looks harmless and
// is not: "v\d+$" then matches the "v3" inside "rev3", and "borrador$" matches
// the tail of "elaborador". Both turn a stem into a shorter, wrong one, which
// is how a tier built on weak evidence starts inventing matches.
var (
	trailingCopyNumber = regexp.MustCompile(`\s*\(\d+\)$`)
	trailingCopyWord   = regexp.MustCompile(`(^|[\s_-]+)(copy|copia|copie|kopie)$`)
	leadingCopyWord    = regexp.MustCompile(`^(copia de|copy of|kopie von)\s+`)
	trailingVersion    = regexp.MustCompile(`(^|[\s_-]+)v\d+$`)
	trailingStage      = regexp.MustCompile(`(^|[\s_-]+)(final|draft|borrador|rev)\d*$`)

	// trailingShortNumber strips a lone one- or two-digit counter. Three or
	// more digits are years, invoice numbers and camera counters — stripping
	// those would collapse "budget-2023" and "budget-2024" onto one stem, which
	// is the worst false positive this tier can produce.
	trailingShortNumber = regexp.MustCompile(`[\s_-]\d{1,2}$`)

	nonAlphanumeric = regexp.MustCompile(`[^\p{L}\p{N}]+`)
)

// normaliseStem reduces a filename to the part that identifies the document.
func normaliseStem(path string) string {
	name := filepath.Base(path)
	stem := strings.ToLower(strings.TrimSuffix(name, filepath.Ext(name)))

	// Loop until nothing more strips, so "informe - copia (2)" reduces fully.
	for {
		before := stem
		for _, re := range []*regexp.Regexp{trailingCopyNumber, trailingCopyWord, leadingCopyWord, trailingVersion, trailingStage} {
			stem = strings.TrimSpace(re.ReplaceAllString(stem, ""))
		}
		if stem == before {
			break
		}
	}

	// Once only, and outside the loop: repeating it would eat "report 12 34"
	// down to "report", and two digits at a time is already the most that can
	// be assumed to be a counter.
	stem = trailingShortNumber.ReplaceAllString(stem, "")

	return strings.TrimSpace(nonAlphanumeric.ReplaceAllString(stem, " "))
}
