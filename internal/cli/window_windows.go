//go:build windows

package cli

import (
	webview "github.com/jchv/go-webview2"
)

// openWindow shows url in a native WebView2 window and blocks until the user
// closes it.
//
// It returns (false, nil) when a native window could not be created — most
// often because the WebView2 runtime is missing — so the caller can fall back
// to opening the browser instead of failing.
func openWindow(url, title string) (shown bool, err error) {
	w := webview.NewWithOptions(webview.WebViewOptions{
		Debug: false,
		WindowOptions: webview.WindowOptions{
			Title:  title,
			Width:  1150,
			Height: 820,
			Center: true,
		},
	})
	if w == nil {
		return false, nil
	}
	defer w.Destroy()

	w.Navigate(url)
	w.Run() // blocks until the window is closed
	return true, nil
}
