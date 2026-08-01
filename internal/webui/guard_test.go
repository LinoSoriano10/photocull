package webui

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// loopbackAddr stands in for the listener the CLI would have opened.
type loopbackAddr string

func (a loopbackAddr) Network() string { return "tcp" }
func (a loopbackAddr) String() string  { return string(a) }

const testPort = "8099"

// bound gives s a session if it has none, and returns a handler that talks to
// it the way the app's own window does: right Host, right cookie, and JSON on
// POSTs. Every test that means to exercise an endpoint goes through here.
//
// Tests that mean to exercise the guard itself call s.Handler() directly, so
// that what they are checking is the doorman and not this helper.
func bound(t *testing.T, s *Server) http.Handler {
	t.Helper()

	token, port := s.session()
	if token == "" {
		if _, err := s.Bind(loopbackAddr("127.0.0.1:" + testPort)); err != nil {
			t.Fatalf("Bind: %v", err)
		}
		token, port = s.session()
	}

	inner := s.Handler()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Host = "127.0.0.1:" + port
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
		if r.Method == http.MethodPost && r.Header.Get("Content-Type") == "" {
			r.Header.Set("Content-Type", "application/json")
		}
		inner.ServeHTTP(w, r)
	})
}

// session starts a server with a session and hands back its parts.
func session(t *testing.T) (*Server, string) {
	t.Helper()
	s := launcherServer()
	if _, err := s.Bind(loopbackAddr("127.0.0.1:" + testPort)); err != nil {
		t.Fatalf("Bind: %v", err)
	}
	token, _ := s.session()
	return s, token
}

// get builds a request the guard should accept, so each test can spoil exactly
// one thing about it and watch that one thing be refused.
func get(path, token string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, path, nil)
	r.Host = "127.0.0.1:" + testPort
	if token != "" {
		r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	}
	return r
}

// TestAPIRejectsForeignHostHeader is the DNS-rebinding defence.
//
// The attack: a page on evil.com is served normally, then evil.com is
// re-pointed at 127.0.0.1. The browser goes on believing the page is talking to
// its own origin, so the same-origin policy stops protecting anything and the
// page can read every file path photocull found. What gives it away is the Host
// header, which still carries the name the browser was told to fetch.
func TestAPIRejectsForeignHostHeader(t *testing.T) {
	s, token := session(t)

	hostile := []string{
		"evil.com",
		"evil.com:" + testPort,
		"127.0.0.1.nip.io:" + testPort,
		"127.0.0.1:9999", // right host, wrong port: not this server
		"localhost",      // no port at all
	}
	for _, host := range hostile {
		t.Run(host, func(t *testing.T) {
			r := get("/api/report", token)
			r.Host = host
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, r)
			if rec.Code == http.StatusOK {
				t.Errorf("Host: %s was served; a rebound request would have read the "+
					"whole report, every file path included", host)
			}
		})
	}
}

func TestAPIAcceptsEveryLoopbackSpelling(t *testing.T) {
	s, token := session(t)

	for _, host := range []string{"127.0.0.1:" + testPort, "localhost:" + testPort, "[::1]:" + testPort} {
		t.Run(host, func(t *testing.T) {
			r := get("/api/report", token)
			r.Host = host
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, r)
			if rec.Code != http.StatusOK {
				t.Errorf("Host: %s = %d, want 200; the browser picks the spelling, not us", host, rec.Code)
			}
		})
	}
}

// TestAPIRejectsRequestWithoutSession checks that the token is actually load
// bearing on every endpoint rather than on the ones somebody remembered.
func TestAPIRejectsRequestWithoutSession(t *testing.T) {
	s, _ := session(t)

	paths := []string{
		"/api/report", "/api/scan/status", "/api/browse",
		"/api/thumb?path=x", "/api/preview?path=x", "/api/snippet?path=x",
		"/api/compare?a=x&b=y", "/api/imagediff?a=x&b=y",
		"/api/merge/status", "/api/merge/result",
	}
	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, get(path, ""))
			if rec.Code != http.StatusUnauthorized {
				t.Errorf("%s without a session = %d, want 401", path, rec.Code)
			}
		})
	}
}

func TestAPIRejectsWrongToken(t *testing.T) {
	s, _ := session(t)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, get("/api/report", "0123456789abcdef0123456789abcdef"))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("status = %d, want 401", rec.Code)
	}
}

// TestTokenHandshakeSetsCookieAndStripsTheToken covers the path a real launch
// takes, and the reason for the redirect: the token must not survive in the
// address bar, the history or a Referer header.
func TestTokenHandshakeSetsCookieAndStripsTheToken(t *testing.T) {
	s, token := session(t)

	r := httptest.NewRequest(http.MethodGet, "/?"+tokenParam+"="+token, nil)
	r.Host = "127.0.0.1:" + testPort
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)

	if rec.Code != http.StatusSeeOther {
		t.Fatalf("status = %d, want 303", rec.Code)
	}
	if loc := rec.Header().Get("Location"); loc != "/" {
		t.Errorf("Location = %q, want /: the token has to leave the URL", loc)
	}

	var got *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			got = c
		}
	}
	if got == nil {
		t.Fatal("no session cookie was set, so the page could never load")
	}
	if got.Value != token {
		t.Errorf("cookie value = %q, want the session token", got.Value)
	}
	if !got.HttpOnly {
		t.Error("cookie is readable from script; a cross-site read would hand over the session")
	}
	if got.SameSite != http.SameSiteStrictMode {
		t.Error("cookie is not SameSite=Strict, which is the whole CSRF defence: " +
			"anything weaker travels on requests that started on another site")
	}
}

func TestHandshakeRejectsAWrongToken(t *testing.T) {
	s, _ := session(t)
	r := httptest.NewRequest(http.MethodGet, "/?"+tokenParam+"=deadbeef", nil)
	r.Host = "127.0.0.1:" + testPort
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)

	if rec.Code == http.StatusSeeOther {
		t.Fatal("a wrong token completed the handshake")
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			t.Error("a session cookie was handed out for a wrong token")
		}
	}
}

// TestAPIRejectsForeignOrigin is the second CSRF defence, for the case where
// the browser tells us where the request came from.
func TestAPIRejectsForeignOrigin(t *testing.T) {
	s, token := session(t)

	for _, origin := range []string{"http://evil.com", "https://evil.com", "http://127.0.0.1:9999", "null"} {
		t.Run(origin, func(t *testing.T) {
			r := get("/api/report", token)
			r.Header.Set("Origin", origin)
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, r)
			if rec.Code == http.StatusOK {
				t.Errorf("Origin: %s was served", origin)
			}
		})
	}
}

// TestPostRejectsFormContentType closes the hole that makes CSRF possible
// without any preflight at all.
//
// A form posted with enctype="text/plain" is a "simple request": the browser
// sends it without asking the server for permission first. And Go's JSON
// decoder reads the first value and ignores the trailing junk a form adds, so
// the body parses cleanly. Requiring application/json is what stops it, because
// a form cannot produce that header and script setting it forces a preflight.
func TestPostRejectsFormContentType(t *testing.T) {
	s, token := session(t)

	// Exactly what <form enctype="text/plain"> puts on the wire.
	body := `{"path":"C:\\"}=` + "\n"
	for _, ct := range []string{"text/plain", "application/x-www-form-urlencoded", "multipart/form-data", ""} {
		t.Run(ct, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/api/scan", strings.NewReader(body))
			r.Host = "127.0.0.1:" + testPort
			r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
			if ct != "" {
				r.Header.Set("Content-Type", ct)
			}
			rec := httptest.NewRecorder()
			s.Handler().ServeHTTP(rec, r)

			if rec.Code != http.StatusUnsupportedMediaType {
				t.Errorf("Content-Type %q = %d, want 415: a plain form must not be "+
					"able to start a scan", ct, rec.Code)
			}
		})
	}
}

func TestPostAcceptsJSONWithCharset(t *testing.T) {
	s, token := session(t)
	r := httptest.NewRequest(http.MethodPost, "/api/scan", bytes.NewReader([]byte(`{"path":""}`)))
	r.Host = "127.0.0.1:" + testPort
	r.AddCookie(&http.Cookie{Name: sessionCookie, Value: token})
	r.Header.Set("Content-Type", "application/json; charset=utf-8")

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, r)
	// 400 for the empty path — the point is that it reached the handler at all.
	if rec.Code == http.StatusUnsupportedMediaType {
		t.Error("a charset parameter made a valid JSON post look like a form")
	}
}

// TestUnboundServerRefusesEverything covers the ordering mistake this design
// makes impossible to ship: a server that started listening before it had a
// session would be a server with no access control at all.
func TestUnboundServerRefusesEverything(t *testing.T) {
	s := launcherServer()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, get("/api/report", "anything"))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503 before Bind", rec.Code)
	}
}

func TestBindReturnsAUsableURL(t *testing.T) {
	s := launcherServer()
	url, err := s.Bind(loopbackAddr("127.0.0.1:" + testPort))
	if err != nil {
		t.Fatalf("Bind: %v", err)
	}
	token, _ := s.session()
	if !strings.HasPrefix(url, "http://127.0.0.1:"+testPort+"/?") {
		t.Errorf("url = %q, want it to point at the bound address", url)
	}
	if !strings.Contains(url, token) {
		t.Errorf("url = %q, want it to carry the session token", url)
	}
	if len(token) != 32 {
		t.Errorf("token is %d hex chars, want 32 (128 bits)", len(token))
	}
}

// TestTwoRunsGetDifferentTokens: a token that repeated across runs would be a
// password shipped in the source.
func TestTwoRunsGetDifferentTokens(t *testing.T) {
	a, _ := session(t)
	b := launcherServer()
	if _, err := b.Bind(loopbackAddr("127.0.0.1:" + testPort)); err != nil {
		t.Fatal(err)
	}
	ta, _ := a.session()
	tb, _ := b.session()
	if ta == tb {
		t.Error("two servers were given the same session token")
	}
}

func TestBindRejectsAnAddressWithNoPort(t *testing.T) {
	s := launcherServer()
	if _, err := s.Bind(loopbackAddr("127.0.0.1")); err == nil {
		t.Error("Bind accepted an address with no port")
	}
}

// Compile-time check that loopbackAddr really satisfies what Bind takes.
var _ net.Addr = loopbackAddr("")
