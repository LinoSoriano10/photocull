// Package cli wires photocull's subcommands together.
package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"

	"github.com/spf13/cobra"

	"photocull/internal/fingerprint"
	"photocull/internal/trash"
	"photocull/internal/webui"
)

// version is overridden at build time with -ldflags "-X photocull/internal/cli.version=v1.0.0".
var version = "dev"

// ErrInterrupted reports that the user stopped the run with Ctrl+C.
var ErrInterrupted = errors.New("interrupted")

// globalFlags are the options every subcommand shares.
type globalFlags struct {
	workers    int
	extensions []string
	kind       string
}

// Execute runs photocull, returning the error the command produced.
func Execute() error {
	// A scan over a large drive takes a while, so Ctrl+C has to stop it
	// cleanly rather than leaving goroutines mid-read.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	err := newRootCmd().ExecuteContext(ctx)
	if errors.Is(err, context.Canceled) {
		return ErrInterrupted
	}
	return err
}

func newRootCmd() *cobra.Command {
	global := &globalFlags{}
	var (
		port    int
		host    string
		browser bool
	)

	root := &cobra.Command{
		Use:   "photocull",
		Short: "Find and remove duplicate photos and documents, safely",
		Long: `photocull scans a directory for duplicates and helps you remove them.

With --kind photos (the default) it compares pictures by what they look like,
so a photo and its resized, re-compressed or format-converted copy land in one
group. With --kind docs it compares documents by what they say, so a report and
a later draft of it do — and every other file in the folder is still checked for
byte-identical copies, because a scan with blind spots is worse than no scan.

Files photocull cannot read inside at all — a scanned PDF, a legacy .doc, a
.zip — are grouped by name and size instead, and marked "related". That tier is
a hint for a person: nothing in it is ever pre-selected, and clean will not
touch it.

Nothing is ever deleted permanently. Duplicates go to the system recycle bin,
and only after you confirm.

Run photocull with no command (or double-click the app) to open a window where
you pick what to look through, a mode and a folder. Or use the scan / clean /
serve commands directly.`,
		Version:       version,
		SilenceUsage:  true,
		SilenceErrors: true,
		Args:          cobra.NoArgs,
		// With no subcommand, open the launcher: a local page to pick a folder
		// and mode. This is what a double-click on the binary runs.
		RunE: func(cmd *cobra.Command, args []string) error {
			out := cmd.OutOrStdout()
			fmt.Fprintln(out, "Starting photocull - pick a folder and mode in the window that opens.")
			srv := webui.NewLauncher(trash.SystemBin{})
			return runApp(cmd.Context(), out, srv, host, port, browser)
		},
	}

	root.PersistentFlags().IntVar(&global.workers, "workers", 0,
		"number of files to process concurrently (0 = one per CPU core)")
	root.PersistentFlags().StringVar(&global.kind, "kind", string(fingerprint.Photos),
		"what to look for: "+strings.Join(fingerprint.Kinds(), " or "))

	// The default is nil rather than a list, so "unset" resolves to whatever the
	// chosen kind looks at. Documents deliberately look at everything, so there
	// is no list that could stand in for them here — and expressing the default
	// this way means nothing has to ask whether the flag was set.
	root.PersistentFlags().StringSliceVar(&global.extensions, "ext", nil,
		"file extensions to scan (default: photo formats; with --kind docs, every file)")

	root.Flags().IntVar(&port, "port", 8080, "port for the launcher web page")
	root.Flags().StringVar(&host, "host", "127.0.0.1", "address to bind the launcher to")
	root.Flags().BoolVar(&browser, "browser", false, "open in the web browser instead of a native window")

	root.AddCommand(
		newScanCmd(global),
		newCleanCmd(global),
		newServeCmd(global),
	)

	return root
}
