package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/spf13/cobra"

	"photocull/internal/dedupe"
	"photocull/internal/fingerprint"
	"photocull/internal/report"
	"photocull/internal/trash"
)

// cleanOptions groups everything the clean command needs, so the logic can be
// unit-tested without building a cobra command.
type cleanOptions struct {
	global  *globalFlags
	match   *matchFlags
	confirm bool
	yes     bool
	mover   trash.Mover // injectable so tests never touch the real recycle bin
	in      io.Reader   // where the confirmation prompt reads from
	out     io.Writer
}

func newCleanCmd(global *globalFlags) *cobra.Command {
	opts := &cleanOptions{global: global, match: &matchFlags{}, mover: trash.SystemBin{}}

	cmd := &cobra.Command{
		Use:   "clean <directory>",
		Short: "Move duplicates to the recycle bin",
		Long: `Clean finds duplicates and moves them to the system recycle bin, keeping the
one copy photocull suggests in each group.

By default it only shows what it would do — nothing is touched. Add --confirm
to actually move the duplicates. Even then, they go to the recycle bin, never
to permanent deletion, so a mistake is always recoverable.

Groups marked "related" are never touched, whatever the flags say. Those files
were matched on their names and sizes because photocull could not read inside
them, which is a hint for a person rather than a finding to act on. Review them
with "photocull serve".`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts.in = cmd.InOrStdin()
			opts.out = cmd.OutOrStdout()
			return runClean(cmd, args[0], opts)
		},
	}

	opts.match.register(cmd)
	cmd.Flags().BoolVar(&opts.confirm, "confirm", false, "actually move duplicates to the recycle bin (default is a dry run)")
	cmd.Flags().BoolVar(&opts.yes, "yes", false, "skip the confirmation prompt; only meaningful together with --confirm")

	return cmd
}

func runClean(cmd *cobra.Command, dir string, opts *cleanOptions) error {
	out := opts.out

	// --yes without --confirm is almost certainly a mistake, and a dangerous
	// one to guess at, so refuse it rather than pick an interpretation.
	if opts.yes && !opts.confirm {
		return fmt.Errorf("--yes only makes sense with --confirm")
	}

	fmt.Fprintf(out, "Scanning %s ...\n", dir)
	a, err := analyze(cmd, dir, opts.global, opts.match)
	if err != nil {
		return err
	}

	if len(a.groups) == 0 {
		fmt.Fprint(out, a.stats.Summary())
		return nil
	}

	report.RenderGroups(out, a.root, a.groups)
	fmt.Fprintln(out)

	// Collect the duplicates: everything except the suggested keeper in each
	// group.
	//
	// Related groups are skipped entirely, and this is the security-critical
	// line in the command. Those files were matched on their name and size
	// alone, because photocull could not read inside them — that is a hint for
	// a person, not a finding to act on. Without this check, "clean --confirm
	// --yes" would recycle files whose only crime was being called something
	// similar.
	var toRemove []string
	skippedRelated := 0
	for _, g := range a.groups {
		if g.Type == dedupe.Related {
			skippedRelated++
			continue
		}
		for _, d := range g.Duplicates() {
			toRemove = append(toRemove, d.Path)
		}
	}

	relatedNote := ""
	if skippedRelated > 0 {
		relatedNote = fmt.Sprintf(
			"\n%d group(s) marked \"related\" are left alone: photocull could not read inside those files and only matched their names and sizes.\nReview them with \"photocull serve %s%s\".\n",
			skippedRelated, dir, kindFlagFor(opts.global))
	}

	// Dry run: the default. Show the plan and stop.
	if !opts.confirm {
		fmt.Fprint(out, a.stats.Summary())
		fmt.Fprintf(out, "\nThis was a dry run. Nothing was moved.\n")
		fmt.Fprintf(out, "Re-run with --confirm to move %s to the recycle bin.\n",
			pluralFiles(len(toRemove)))
		fmt.Fprint(out, relatedNote)
		return nil
	}

	// Real run: confirm unless explicitly waived.
	if !opts.yes {
		ok, err := confirmPrompt(opts.in, out, len(toRemove))
		if err != nil {
			return err
		}
		if !ok {
			fmt.Fprintln(out, "Cancelled. Nothing was moved.")
			return nil
		}
	}

	moveErr := opts.mover.Move(toRemove...)

	// Report what actually happened. Even on a partial failure the recycle bin
	// is recoverable, so this is informational, not alarming.
	moved := len(toRemove)
	if moveErr != nil {
		fmt.Fprintf(out, "\nSome files could not be moved:\n%v\n", moveErr)
	}
	a.stats.ReclaimedBytes = a.stats.ReclaimableBytes
	fmt.Fprintf(out, "\nMoved %s to the recycle bin, freeing about %s.\n",
		pluralFiles(moved), report.HumanBytes(a.stats.ReclaimedBytes))
	fmt.Fprintln(out, "They can be restored from the recycle bin if this was a mistake.")
	fmt.Fprint(out, relatedNote)

	return moveErr
}

// kindFlagFor echoes back the --kind the user gave, so the suggested command
// actually reproduces the scan they just ran.
func kindFlagFor(global *globalFlags) string {
	if global == nil || global.kind == "" || global.kind == string(fingerprint.Photos) {
		return ""
	}
	return " --kind " + global.kind
}

// confirmPrompt asks the user to type y/N before anything is moved.
func confirmPrompt(in io.Reader, out io.Writer, n int) (bool, error) {
	fmt.Fprintf(out, "Move %s to the recycle bin? [y/N]: ", pluralFiles(n))

	line, err := bufio.NewReader(in).ReadString('\n')
	if err != nil && line == "" {
		// EOF with no input (e.g. a closed stdin) means "no", not an error.
		return false, nil
	}

	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func pluralFiles(n int) string {
	if n == 1 {
		return "1 file"
	}
	return fmt.Sprintf("%d files", n)
}
