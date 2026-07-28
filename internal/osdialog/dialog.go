// Package osdialog opens the operating system's native folder picker.
//
// It lets the web launcher offer a real "Browse…" button instead of asking the
// user to paste a path. Only a folder picker is needed, and only Windows has a
// native implementation here; elsewhere PickFolder reports that it is
// unsupported and the launcher falls back to a typed path.
package osdialog

import "errors"

// ErrUnsupported means this platform has no native folder picker wired up.
var ErrUnsupported = errors.New("native folder picker not supported on this platform")

// ErrCancelled means the user closed the dialog without choosing a folder.
var ErrCancelled = errors.New("folder selection cancelled")
