package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"photocull/internal/report"
)

func newScanCmd(global *globalFlags) *cobra.Command {
	var (
		match  matchFlags
		asJSON bool
	)

	cmd := &cobra.Command{
		Use:   "scan <directory>",
		Short: "Report duplicate photos without changing anything",
		Long: `Scan walks a directory, fingerprints every photo and reports the duplicates
it finds. It only reads: no file is moved, renamed or deleted.

Use it first to see what is there, then reach for "photocull clean".`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			if !asJSON {
				fmt.Fprintf(out, "Scanning %s ...\n", args[0])
			}

			a, err := analyze(cmd.Context(), args[0], global, &match)
			if err != nil {
				return err
			}

			if asJSON {
				data, err := a.report().JSON()
				if err != nil {
					return fmt.Errorf("cannot render JSON: %w", err)
				}
				fmt.Fprintln(out, string(data))
				return nil
			}

			report.RenderGroups(out, a.root, a.groups)
			fmt.Fprintln(out)
			fmt.Fprint(out, a.stats.Summary())

			if a.stats.Groups > 0 {
				fmt.Fprintf(out, "\nRun \"photocull clean %s\" to review and remove them.\n", args[0])
			}
			return nil
		},
	}

	match.register(cmd)
	cmd.Flags().BoolVar(&asJSON, "json", false, "print the full report as JSON")

	return cmd
}
