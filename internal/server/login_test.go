package server

import (
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// ClientIP identity: X-Forwarded-For is honored only from configured trusted
// proxies — an untrusted client must never pick its own throttle /
// attribution identity (rotating XFF bypassed the login throttle before
// --trusted-proxies existed).
func TestClientIPTrustedProxies(t *testing.T) {
	req := func(remote, xff string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = remote
		if xff != "" {
			r.Header.Set("X-Forwarded-For", xff)
		}
		return r
	}

	s := &Server{} // no trusted proxies: the peer is always authoritative
	if got := s.ClientIP(req("203.0.113.9:5000", "198.51.100.7")); got != "203.0.113.9" {
		t.Fatalf("untrusted peer's XFF must be ignored, got %q", got)
	}
	if got := s.ClientIP(req("203.0.113.9:5000", "")); got != "203.0.113.9" {
		t.Fatalf("no XFF: want peer, got %q", got)
	}

	// Behind a trusted proxy, the RIGHTMOST untrusted hop is the client —
	// that's the address the proxy itself appended and vouched for.
	s.TrustedProxies = []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/32")}
	if got := s.ClientIP(req("203.0.113.9:5000", "198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("trusted proxy XFF: want 198.51.100.7, got %q", got)
	}
	if got := s.ClientIP(req("203.0.113.9:5000", " 198.51.100.7 , 10.0.0.1")); got != "10.0.0.1" {
		t.Fatalf("multi-hop XFF: want the RIGHTMOST untrusted hop, got %q", got)
	}
	// The attack the walk direction exists for: a client behind the proxy
	// sends its own XFF, the appending proxy adds the real address after it —
	// the forged left entry must lose to the proxy-appended right one.
	if got := s.ClientIP(req("203.0.113.9:5000", "6.6.6.6, 198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("client-forged XFF prefix must be ignored, got %q", got)
	}
	// A chain of trusted proxies: skip their own addresses, keep walking to
	// the first hop a proxy vouched for.
	if got := s.ClientIP(req("203.0.113.9:5000", "198.51.100.7, 203.0.113.10")); got != "198.51.100.7" {
		t.Fatalf("trusted-chain XFF: want the client behind the chain, got %q", got)
	}
	// All hops trusted (or none untrusted before garbage): fall back to peer.
	if got := s.ClientIP(req("203.0.113.9:5000", "203.0.113.10")); got != "203.0.113.9" {
		t.Fatalf("all-trusted XFF: want the peer, got %q", got)
	}
	// A hop that isn't an IP poisons the walk — fall back to the peer rather
	// than admit attacker-chosen strings into the throttle/warm keyspaces.
	if got := s.ClientIP(req("203.0.113.9:5000", "198.51.100.7, not-an-ip")); got != "203.0.113.9" {
		t.Fatalf("garbage XFF hop: want the peer, got %q", got)
	}
	// …and a peer outside the trusted set still can't spoof at all.
	if got := s.ClientIP(req("203.0.114.1:5000", "198.51.100.7")); got != "203.0.114.1" {
		t.Fatalf("peer outside trusted range: XFF must be ignored, got %q", got)
	}
	// IPv6 peer in a trusted v6 range vouching for a v6 client outside it.
	if got := s.ClientIP(req("[2001:db8::5]:5000", "2001:db9::1")); got != "2001:db9::1" {
		t.Fatalf("trusted v6 proxy: got %q", got)
	}
	// Non-host:port peers (the gateway unix socket) pass through untouched.
	if got := s.ClientIP(req("@", "198.51.100.7")); got != "@" {
		t.Fatalf("unix-socket peer must be used as-is, got %q", got)
	}
}
