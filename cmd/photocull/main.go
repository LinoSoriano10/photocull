// Command photocull finds duplicate photos in a directory tree and helps you
// remove them without ever deleting anything permanently.
package main

import (
	"errors"
	"fmt"
	"os"
	"runtime"

	"photocull/internal/cli"
)

func init() {
	// The native window's message loop must run on the main OS thread. Locking
	// it here — before any goroutine can be scheduled onto it — keeps that
	// thread reserved for the goroutine that ends up driving the window. It is
	// harmless for the plain CLI subcommands.
	runtime.LockOSThread()
}

func main() {
	if err := cli.Execute(); err != nil {
		// A cancelled context means the user pressed Ctrl+C. That is a normal
		// way to end a long scan, not a failure worth a stack of red text.
		if errors.Is(err, cli.ErrInterrupted) {
			fmt.Fprintln(os.Stderr, "photocull: interrupted")
			os.Exit(130)
		}
		fmt.Fprintln(os.Stderr, "photocull:", err)
		os.Exit(1)
	}
}
