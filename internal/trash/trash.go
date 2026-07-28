// Package trash moves files to the operating system's recycle bin.
//
// photocull never deletes a photo outright. Everything it removes goes to the
// recycle bin, where the operating system already knows how to restore it —
// so a wrong call from the user, or a bug in photocull, costs a few clicks
// rather than a photo.
package trash

import (
	"errors"
	"fmt"

	"github.com/Bios-Marcel/wastebasket/v2"
)

// Mover sends files to the recycle bin. It is an interface so the rest of
// photocull can be tested against a fake that records calls instead of
// touching the real recycle bin — no test should ever move a real file.
type Mover interface {
	Move(paths ...string) error
}

// SystemBin is the real recycle bin, backed by the OS. On Windows it uses the
// Shell32 API; on Linux it follows the FreeDesktop trash specification.
type SystemBin struct{}

// Move sends paths to the recycle bin, one at a time so that a single
// unwritable file does not sink the whole batch: the good files still make it,
// and the failures are reported together.
func (SystemBin) Move(paths ...string) error {
	var failures error
	for _, p := range paths {
		if err := wastebasket.Trash(p); err != nil {
			failures = errors.Join(failures, fmt.Errorf("trash %q: %w", p, err))
		}
	}
	return failures
}
