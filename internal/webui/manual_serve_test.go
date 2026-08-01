package webui

import (
	"net"
	"net/http"
	"os"
	"testing"
)

// TestManualServe is a throwaway harness for looking at the page in a real
// browser. It is skipped unless PHOTOCULL_MANUAL is set, so it never runs in CI
// or in an ordinary `go test ./...`.
//
// It exists because the Go tests exercise the handlers and never execute a line
// of app.js: two rounds of UI work here passed everything and still shipped
// bugs that were obvious the moment the page was open. Run it with
//
//	PHOTOCULL_MANUAL=1 go test ./internal/webui -run TestManualServe -v
//
// and it prints a URL with a session token to paste into a browser.
func TestManualServe(t *testing.T) {
	if os.Getenv("PHOTOCULL_MANUAL") == "" {
		t.Skip("set PHOTOCULL_MANUAL=1 to serve the UI for a manual look")
	}

	s, _ := fixtureServer(t)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	url, err := s.Bind(ln.Addr())
	if err != nil {
		t.Fatal(err)
	}
	t.Log("open this, it carries the one-time token:", url)

	if err := http.Serve(ln, s.Handler()); err != nil {
		t.Log("server stopped:", err)
	}
}
