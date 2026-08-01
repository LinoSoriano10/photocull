package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/pflag"

	"photocull/internal/webui"
)

// serveOptions are the flags that decide how the app is served. Both the bare
// launcher and `serve <dir>` take the same set, so they live together and are
// registered once rather than being spelled out twice and drifting.
type serveOptions struct {
	port        int
	host        string
	browser     bool
	allowRemote bool
}

func (o *serveOptions) register(flags *pflag.FlagSet) {
	// Zero means "ask the operating system for a free one". See listen.
	flags.IntVar(&o.port, "port", 0, "port to serve the app on (0 picks a free one)")
	flags.StringVar(&o.host, "host", "127.0.0.1", "address to bind to; must be loopback unless --allow-remote")
	flags.BoolVar(&o.browser, "browser", false, "open in the web browser instead of a native window")
	flags.BoolVar(&o.allowRemote, "allow-remote", false,
		"permit binding to a non-loopback address, exposing your files to the network with no password")
}

// isLoopback reports whether host names this machine and nothing else.
//
// A hostname other than "localhost" is refused rather than resolved: resolution
// depends on DNS and on the hosts file, both of which can point a
// friendly-looking name at a public address, and this decision is the one
// standing between a private photo library and the local network.
func isLoopback(host string) bool {
	if host == "" {
		// An empty host binds to every interface, which is the opposite of what
		// this function is asked to confirm.
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// checkBindAddress refuses to expose the app to anything but this machine.
//
// photocull's whole access-control model assumes a single user in front of the
// screen: the session token lives in a cookie handed to a window it opened
// itself, and there is no account, no password and no transport encryption.
// Serving that to a network is not a configuration, it is a mistake — so it
// takes a second, explicit flag to do, and the error says what it costs.
func checkBindAddress(host string, allowRemote bool) error {
	if isLoopback(host) || allowRemote {
		return nil
	}
	return fmt.Errorf(
		"refusing to bind to %q: anyone who can reach that address could browse and delete your files, "+
			"because photocull has no password and no encryption. Use --host 127.0.0.1, or pass --allow-remote "+
			"if you genuinely mean to expose it", host)
}

// listen binds host:port, falling back to any free port if it is taken. On an
// unfamiliar PC — the whole point of the portable build — a busy port should
// not stop the app from starting.
//
// Port 0, the default, asks the operating system for a free port. A fixed
// well-known port is guessable, and a guessable port is the first thing an
// attack against a local server needs; the app opens its own window, so nobody
// types the address anyway.
func listen(host string, port int, out io.Writer) (net.Listener, error) {
	addr := net.JoinHostPort(host, strconv.Itoa(port))
	l, err := net.Listen("tcp", addr)
	if err == nil {
		return l, nil
	}

	fallback, ferr := net.Listen("tcp", net.JoinHostPort(host, "0"))
	if ferr != nil {
		return nil, fmt.Errorf("cannot start server on %s (%v) and no free port was available: %w", addr, err, ferr)
	}
	fmt.Fprintf(out, "Port %d was busy; using a free port instead.\n", port)
	return fallback, nil
}

// runApp starts the web server and shows the UI. It opens a native window when
// it can (Windows with the WebView2 runtime) and otherwise falls back to the
// browser. It returns when the window is closed or the context is cancelled.
func runApp(ctx context.Context, out io.Writer, srv *webui.Server, opts serveOptions) error {
	if err := checkBindAddress(opts.host, opts.allowRemote); err != nil {
		return err
	}

	listener, err := listen(opts.host, opts.port, out)
	if err != nil {
		return err
	}

	// Start a session before serving. The URL that comes back carries a
	// single-use token; the server refuses every request until this has run,
	// so it cannot be skipped by accident.
	url, err := srv.Bind(listener.Addr())
	if err != nil {
		listener.Close()
		return err
	}

	httpServer := &http.Server{
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpServer.Serve(listener) }()

	// Ctrl+C (or any context cancellation) shuts the server down.
	go func() {
		<-ctx.Done()
		httpServer.Close()
	}()

	if !opts.browser {
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
