//go:build !windows

package osdialog

// PickFolder has no native implementation off Windows; the web launcher falls
// back to a typed path.
func PickFolder(title string) (string, error) {
	return "", ErrUnsupported
}
