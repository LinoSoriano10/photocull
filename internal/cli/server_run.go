package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"photocull/internal/webui"
)

// listen binds host:port, falling back to any free port if it is taken. On an
// unfamiliar PC — the whole point of the portable build — a busy port should
// not stop the app from starting.
func listen(host string, port int, out io.Writer) (net.Listener, error) {
	addr := fmt.Sprintf("%s:%d", host, port)
	l, err := net.Listen("tcp", addr)
	if err == nil {
		return l, nil
	}

	fallback, ferr := net.Listen("tcp", fmt.Sprintf("%s:0", host))
	if ferr != nil {
		return nil, fmt.Errorf("cannot start server on %s (%v) and no free port was available: %w", addr, err, ferr)
	}
	fmt.Fprintf(out, "Port %d was busy; using a free port instead.\n", port)
	return fallback, nil
}

// runApp starts the web server and shows the UI. It opens a native window when
// it can (Windows with the WebView2 runtime) and otherwise falls back to the
// browser. It returns when the window is closed or the context is cancelled.
func runApp(ctx context.Context, out io.Writer, srv *webui.Server, host string, port int, forceBrowser bool) error {
	listener, err := listen(host, port, out)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	url := "http://" + listener.Addr().String()

	// Ctrl+C (or any context cancellation) shuts the server down.
	go func() {
		<-ctx.Done()
		httpServer.Close()
	}()

	if !forceBrowser {
		shown, werr := openWindow(url, "photocull")
		if werr != nil {
			fmt.Fprintf(out, "Could not open a native window (%v); using the browser instead.\n", werr)
		}
		if shown {
			// The user closed the window: stop the server and finish.
			httpServer.Close()
			<-serveErr
			return nil
		}
		// No native window here — fall through to the browser.
	}

	fmt.Fprintf(out, "\nphotocull is running at %s\nPress Ctrl+C to stop.\n", url)
	_ = webui.OpenBrowser(url)

	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
