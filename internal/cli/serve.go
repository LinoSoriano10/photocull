package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"photocull/internal/trash"
	"photocull/internal/webui"
)

func newServeCmd(global *globalFlags) *cobra.Command {
	var (
		match matchFlags
		serve serveOptions
	)

	cmd := &cobra.Command{
		Use:   "serve <directory>",
		Short: "Review duplicates in the app window before removing them",
		Long: `Serve scans a directory and opens the app on it, so the duplicates can be
reviewed before anything is removed.

This is the safe way to handle everything photocull is not certain about. For
photographs, a thumbnail tells you in a glance what a file path cannot, and two
copies can be laid over each other, blinked between, or diffed pixel by pixel.
For documents, the exact words that differ between one and the next are shown,
with the identical stretches collapsed — nobody is going to read thirty pages
twice to find a changed adjective.

Selected files are moved to the recycle bin, never deleted permanently.

The server binds to localhost only.`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()

			fmt.Fprintf(out, "Scanning %s ...\n", args[0])
			a, err := analyze(cmd, args[0], global, &match)
			if err != nil {
				return err
			}
			fmt.Fprint(out, a.stats.Summary())

			srv := webui.New(a.root, a.groups, a.stats, trash.SystemBin{})
			return runApp(cmd.Context(), out, srv, serve)
		},
	}

	match.register(cmd)
	serve.register(cmd.Flags())

	return cmd
}
