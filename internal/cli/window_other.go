//go:build !windows

package cli

// openWindow has no native implementation off Windows, so the app always uses
// the browser there.
func openWindow(url, title string) (shown bool, err error) {
	return false, nil
}
