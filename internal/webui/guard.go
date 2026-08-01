package webui

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// The local server is the soft spot in a desktop app that talks HTTP to itself.
// It listens on the loopback interface with no user account and no password,
// which is fine right up until the user opens a browser — because from that
// moment any page they visit can send it requests. Two attacks matter, and this
// file exists for them:
//
// Cross-site request forgery. A form posted with enctype="text/plain" is a
// "simple request": the browser sends it without asking permission first. Go's
// json.Decoder reads the first JSON value and ignores whatever follows, so a
// body of {"path":"C:\\"}= parses cleanly. Without a defence, any web page could
// make photocull scan an entire drive.
//
// DNS rebinding, which is the serious one. An attacker points evil.com at their
// own address, serves a page, then re-points evil.com at 127.0.0.1. The browser
// still believes the page is talking to its own origin, so the same-origin
// policy stops protecting anything: the page can read /api/report — every file
// path on the disk — and then call /api/delete with real paths.
//
// The four checks below are deliberately independent. Any one of them closes
// both attacks on its own; together they mean a mistake in one is not a
// vulnerability.

// sessionCookie carries the per-run token. It is HttpOnly so no script can read
// it back out, and SameSite=Strict so the browser never attaches it to a
// request that started on another site — which is what kills CSRF without the
// page having to do anything at all.
const sessionCookie = "photocull_session"

// tokenParam is how the token reaches the page the first time: the app opens
// its own window at /?t=<token>, the handshake swaps it for the cookie, and a
// redirect strips it back out of the address bar so it does not linger in
// history or in a Referer header.
const tokenParam = "t"

// newToken returns 128 bits of randomness, hex encoded.
//
// crypto/rand, not math/rand: this is the only thing standing between a
// malicious page and the delete endpoint, and a predictable token is no token.
func newToken() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("webui: cannot generate a session token: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

// Bind tells the server which address it ended up listening on and starts a
// session. It returns the URL to open, token and all.
//
// It exists because the listener is opened by the caller, after the Server is
// built — the port is not known until then, and the host allowlist needs it.
// Calling this is not optional: Handler refuses every request until it has.
func (s *Server) Bind(addr net.Addr) (string, error) {
	token, err := newToken()
	if err != nil {
		return "", err
	}

	host, port, err := net.SplitHostPort(addr.String())
	if err != nil {
		return "", fmt.Errorf("webui: cannot read the port from %s: %w", addr, err)
	}

	s.mu.Lock()
	s.token = token
	s.port = port
	s.mu.Unlock()

	// A listener bound to the IPv6 loopback reports "[::1]", which has to keep
	// its brackets in a URL. Anything else is reachable as 127.0.0.1, including
	// the unspecified address — and the CLI refuses to bind anywhere but
	// loopback, so there is no third case to handle.
	dial := "127.0.0.1"
	if ip := net.ParseIP(host); ip != nil && ip.To4() == nil && ip.IsLoopback() {
		dial = "[::1]"
	}

	return fmt.Sprintf("http://%s:%s/?%s=%s", dial, port, tokenParam, token), nil
}

// session reads the token and port set by Bind.
func (s *Server) session() (token, port string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.token, s.port
}

// allowedHost reports whether the Host header names this server on loopback.
//
// This is the anti-rebinding check, and it is one comparison because that is
// all it needs to be: a rebound request arrives with the attacker's hostname in
// Host, because that is the name the browser was told to fetch. Only the three
// spellings of "this machine" are accepted.
func allowedHost(host, port string) bool {
	h, p, err := net.SplitHostPort(host)
	if err != nil {
		// No port in the header. A browser always sends one for a non-default
		// port, and this server never runs on 80, so this is not our traffic.
		return false
	}
	if p != port {
		return false
	}
	switch strings.ToLower(h) {
	case "127.0.0.1", "localhost", "::1", "[::1]":
		return true
	}
	return false
}

// sameOrigin reports whether an Origin header refers to this server.
//
// Origin is absent on plain navigations and present on fetches and form posts,
// so this is a check that only fires when there is something to check.
func sameOrigin(origin, port string) bool {
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" {
		return false
	}
	return allowedHost(u.Host, port)
}

// guard wraps the whole mux. Nothing reaches a handler without passing here.
func (s *Server) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, port := s.session()
		if token == "" {
			// Bind was never called, so there is no session to check against.
			// Refusing everything is the only safe reading of that.
			http.Error(w, "server not ready", http.StatusServiceUnavailable)
			return
		}

		// 1. Host. Cheapest check, and the one that stops DNS rebinding.
		if !allowedHost(r.Host, port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// 2. Origin, when the browser sent one.
		if origin := r.Header.Get("Origin"); origin != "" && !sameOrigin(origin, port) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// 3. The handshake: /?t=<token> swaps the token for a cookie and
		// redirects to a clean URL. It has to come before the cookie check,
		// because it is how the cookie gets set in the first place.
		if r.URL.Path == "/" && r.URL.Query().Has(tokenParam) {
			s.handshake(w, r, token)
			return
		}

		// 4. Everything else needs the cookie. Static assets are guarded too,
		// not only /api/: the page itself lists nothing sensitive, but leaving
		// it open would hand an attacker a way to confirm photocull is running
		// and on which port.
		if !hasSession(r, token) {
			s.refuse(w, r)
			return
		}

		// 5. A state-changing request must be JSON. This is what the
		// enctype="text/plain" form cannot produce: setting Content-Type to
		// application/json from a form is impossible, and doing it from script
		// forces a preflight the server will fail.
		if r.Method == http.MethodPost {
			ct := r.Header.Get("Content-Type")
			if media, _, _ := strings.Cut(ct, ";"); strings.TrimSpace(media) != "application/json" {
				http.Error(w, "expected application/json", http.StatusUnsupportedMediaType)
				return
			}
		}

		next.ServeHTTP(w, r)
	})
}

// hasSession reports whether the request carries the right session cookie.
func hasSession(r *http.Request, token string) bool {
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return false
	}
	// Constant time, so a caller cannot learn the token a byte at a time by
	// measuring how long the comparison takes.
	return subtle.ConstantTimeCompare([]byte(c.Value), []byte(token)) == 1
}

// handshake exchanges a valid ?t= for the session cookie.
func (s *Server) handshake(w http.ResponseWriter, r *http.Request, token string) {
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get(tokenParam)), []byte(token)) != 1 {
		s.refuse(w, r)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		// Strict, not Lax: Lax still travels on top-level navigations, which is
		// exactly the shape of a link on a hostile page.
		SameSite: http.SameSiteStrictMode,
		// No Secure flag: this is plain HTTP on loopback, and setting it would
		// make the browser drop the cookie entirely.
	})

	// Redirect to the bare path so the token leaves the address bar, the
	// history and any Referer the page might later send.
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// refuse explains the problem instead of returning a bare 401.
//
// The person most likely to see this is the user themselves, having
// bookmarked the address or reopened a stale tab — the session token changes
// every run, so yesterday's URL will not work today. "Unauthorized" would leave
// them thinking the app is broken.
func (s *Server) refuse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if strings.HasPrefix(r.URL.Path, "/api/") {
		http.Error(w, "no session", http.StatusUnauthorized)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusUnauthorized)
	fmt.Fprint(w, refusalPage)
}

const refusalPage = `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><title>photocull</title>
<style>
 body{font:15px/1.6 system-ui,Segoe UI,sans-serif;background:#0f1115;color:#e6e8ec;
      display:grid;place-items:center;min-height:100vh;margin:0;padding:24px}
 div{max-width:44ch}h1{font-size:20px;margin:0 0 12px}p{color:#9aa1ad;margin:0 0 10px}
 @media(prefers-color-scheme:light){body{background:#f5f6f8;color:#1a1d23}p{color:#666e7a}}
</style></head><body><div>
<h1>This link has expired</h1>
<p>photocull gives every run its own single-use address, so a bookmarked or
reopened link stops working once the app is closed.</p>
<p>Start photocull again and it will open its own window.</p>
</div></body></html>`
