// mcp_reach.go — what a direct MCP server's requests may reach (API.md §MCP
// servers, "Reach"). Every request for a direct server — the MCP calls, and
// the sign-in's discovery, registration and token requests, whose addresses
// the server itself names — goes through directHTTP:
//
//   - a person's own server: https only, and every connection to a public
//     address only, checked when it is dialled (so a hostname that resolves
//     to a private address, a redirect, or a DNS answer that changes after a
//     check reaches nothing inside the network). A person can't point the
//     agent's backend at the LAN, a cloud's metadata endpoint or the
//     backend's own loopback and read what comes back.
//   - a global server (added by a manager): whatever the agent's network
//     reaches — a server on the LAN, or on this machine over http.
//
// The two keep separate connection pools, so a connection made for a global
// server is never reused by a personal one. When the environment names an
// HTTP proxy, a personal server's request goes to it like any other, its
// host's addresses checked before it is sent (the proxy dials the server).
package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"sync"
	"syscall"
	"time"
)

// directHTTP reaches direct MCP servers and their authorization servers
// (outside the workspace: the agent's net slot). A var so tests can swap it.
var directHTTP = &http.Client{Timeout: 30 * time.Second, Transport: directRT{strict: directTransport(false), open: directTransport(true)}}

// directLocalOK lets a personal server reach loopback over http — the tests'
// servers (httptest) only; never set outside a test.
var directLocalOK = false

type directReachKey struct{}

// withDirectReach marks ctx's requests as a global server's (open: what the
// agent's network reaches) or a personal one's (public addresses only).
func withDirectReach(ctx context.Context, open bool) context.Context {
	return context.WithValue(ctx, directReachKey{}, open)
}

// reach is ctx for s's requests.
func (s *directServer) reach(ctx context.Context) context.Context {
	return withDirectReach(ctx, s.Scope == mcpGlobal)
}

// directOpen: ctx is a global server's. Unmarked is a personal one's.
func directOpen(ctx context.Context) bool {
	open, _ := ctx.Value(directReachKey{}).(bool)
	return open
}

// directRT sends a request through the open or the strict transport.
type directRT struct{ strict, open http.RoundTripper }

func (d directRT) RoundTrip(r *http.Request) (*http.Response, error) {
	if directOpen(r.Context()) {
		return d.open.RoundTrip(r)
	}
	if r.URL.Scheme != "https" && !directLocalOK {
		return nil, fmt.Errorf("%s: a personal MCP server is reached over https only", r.URL.Redacted())
	}
	if p, _ := http.ProxyFromEnvironment(r); p != nil {
		if err := checkPublicHost(r.Context(), r.URL.Hostname()); err != nil {
			return nil, err
		}
	}
	return d.strict.RoundTrip(r)
}

// directTransport is a transport of its own (its own pool); a strict one
// dials public addresses only — and the environment's proxies.
func directTransport(open bool) *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	d := &net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}
	if open {
		t.DialContext = d.DialContext
		return t
	}
	strict := *d
	strict.Control = func(_, address string, _ syscall.RawConn) error {
		host, _, _ := net.SplitHostPort(address)
		ip, err := netip.ParseAddr(host)
		if err != nil || !publicAddr(ip) && !(directLocalOK && ip.Unmap().IsLoopback()) {
			return fmt.Errorf("%s is not a public address: a personal MCP server reaches the internet only", host)
		}
		return nil
	}
	t.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if envProxyAddrs()[addr] {
			return d.DialContext(ctx, network, addr)
		}
		return strict.DialContext(ctx, network, addr)
	}
	return t
}

// envProxyAddrs are the host:port of the proxies the environment names.
var envProxyAddrs = sync.OnceValue(func() map[string]bool {
	out := map[string]bool{}
	for _, scheme := range []string{"https", "http"} {
		p, err := http.ProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: scheme, Host: "mcp.invalid"}})
		if err != nil || p == nil {
			continue
		}
		port := p.Port()
		if port == "" {
			port = map[string]string{"https": "443", "socks5": "1080"}[p.Scheme]
			if port == "" {
				port = "80"
			}
		}
		out[net.JoinHostPort(p.Hostname(), port)] = true
	}
	return out
})

// checkPublicHost: every address host resolves to is public.
func checkPublicHost(ctx context.Context, host string) error {
	if ip, err := netip.ParseAddr(host); err == nil {
		if !publicAddr(ip) {
			return fmt.Errorf("%s is not a public address: a personal MCP server reaches the internet only", host)
		}
		return nil
	}
	addrs, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return errors.New(host + ": no address")
	}
	for _, ip := range addrs {
		if !publicAddr(ip) {
			return fmt.Errorf("%s resolves to %s, not a public address: a personal MCP server reaches the internet only", host, ip)
		}
	}
	return nil
}

// notPublic are the ranges no personal server's request reaches beyond
// what netip's predicates say: this network, CGNAT, IETF protocol
// assignments, benchmarking, reserved, NAT64 (the class rule's list,
// docs/isolation.md "A class's internet is strict").
var notPublic = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("64:ff9b::/96"),
	netip.MustParsePrefix("64:ff9b:1::/48"),
}

// publicAddr: ip is a public unicast address.
func publicAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsMulticast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}
