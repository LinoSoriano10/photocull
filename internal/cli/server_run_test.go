package cli

import (
	"bytes"
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestBindAddressRefusesTheNetwork is a safety test, not a plumbing one.
//
// photocull's access control assumes one person at one screen: the session
// token lives in a cookie handed to a window the app opened itself, and there
// is no account, no password and no TLS. Bound to a network address, all of
// that becomes "anyone on this Wi-Fi can browse and delete your files". The
// flag that permits it has to be typed on purpose.
func TestBindAddressRefusesTheNetwork(t *testing.T) {
	loopback := []string{"127.0.0.1", "localhost", "LOCALHOST", "::1", "[::1]", "127.0.0.53"}
	for _, host := range loopback {
		if err := checkBindAddress(host, false); err != nil {
			t.Errorf("checkBindAddress(%q) = %v, want it allowed", host, err)
		}
	}

	exposed := []string{"0.0.0.0", "", "192.168.1.10", "::", "example.com", "0.0.0.0"}
	for _, host := range exposed {
		err := checkBindAddress(host, false)
		if err == nil {
			t.Errorf("checkBindAddress(%q) was allowed; that address is reachable from "+
				"the network and photocull has no password", host)
			continue
		}
		// The message has to say what it costs, or the reflex is to reach
		// straight for the override.
		if !strings.Contains(err.Error(), "--allow-remote") {
			t.Errorf("error for %q does not mention the override: %v", host, err)
		}
	}

	// ...and the override works, because refusing outright would be deciding
	// for somebody who may have a reason.
	if err := checkBindAddress("0.0.0.0", true); err != nil {
		t.Errorf("--allow-remote did not permit 0.0.0.0: %v", err)
	}
}

// TestBindAddressRejectsHostnamesWithoutResolving records a deliberate choice.
// Resolving a name would put this decision in the hands of DNS and the hosts
// file, either of which can point a friendly-looking name at a public address.
func TestBindAddressRejectsHostnamesWithoutResolving(t *testing.T) {
	if err := checkBindAddress("my-laptop.local", false); err == nil {
		t.Error("a hostname other than localhost was accepted; that would make the " +
			"bind decision depend on whatever DNS happens to answer")
	}
}

// TestListenPicksAFreePortByDefault covers the change from a fixed 8080. A
// well-known port is the first thing an attack on a local server needs, and
// nobody types this address anyway — the app opens its own window.
func TestListenPicksAFreePortByDefault(t *testing.T) {
	var out bytes.Buffer
	l, err := listen("127.0.0.1", 0, &out)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer l.Close()

	_, port, err := net.SplitHostPort(l.Addr().String())
	if err != nil {
		t.Fatalf("SplitHostPort(%s): %v", l.Addr(), err)
	}
	if port == "0" || port == "" {
		t.Errorf("listener reports port %q; the OS should have assigned a real one", port)
	}
	if out.Len() != 0 {
		t.Errorf("asking for a free port printed a warning: %q", out.String())
	}
}

// TestListenFallsBackWhenThePortIsTaken keeps the behaviour the portable build
// relies on: on somebody else's PC a busy port must not stop the app starting.
func TestListenFallsBackWhenThePortIsTaken(t *testing.T) {
	first, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	_, portStr, _ := net.SplitHostPort(first.Addr().String())

	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	var out bytes.Buffer
	second, err := listen("127.0.0.1", port, &out)
	if err != nil {
		t.Fatalf("listen on a busy port: %v", err)
	}
	defer second.Close()

	if second.Addr().String() == first.Addr().String() {
		t.Error("two listeners on the same address")
	}
	if !strings.Contains(out.String(), "busy") {
		t.Errorf("the fallback happened silently: %q", out.String())
	}
}
