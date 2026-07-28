package webui

import (
	"os/exec"
	"runtime"
)

// OpenBrowser tries to open url in the user's default browser. It is
// best-effort: if it fails, the launcher just prints the URL and the user
// opens it themselves.
func OpenBrowser(url string) error {
	switch runtime.GOOS {
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return exec.Command("xdg-open", url).Start()
	}
}
