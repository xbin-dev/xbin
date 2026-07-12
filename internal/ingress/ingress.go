// Package ingress is the mechanism layer of plans/ingress.md: the public
// path allowlist, the Host-routed HTTP entry (shared by xbind's builtin
// listener and per-terminator forward sockets), and the L4 stream relay that
// carries host-port TCP/UDP into a tile's netns.
//
// It is deliberately dumb about the workspace: routes and stream specs are
// computed by the broker (which owns bindings/exposes/policy) and injected as
// functions/snapshots, so this package is testable with literals and knows
// nothing about components, grants, or orgs.
package ingress

import (
	"net/http"
	"path"
	"strings"
)

// Route is one resolved public route: the tile + expose slot a hostname maps
// to, with the tile's declared public-path allowlist.
type Route struct {
	Component string   `json:"component"`
	Slot      string   `json:"slot"`
	Paths     []string `json:"paths"`
	Source    string   `json:"source"`         // "runtime" or the terminator tile path
	Host      string   `json:"host"`           // the matched hostname
	Zone      string   `json:"zone,omitempty"` // set when admitted via a delegated zone
}

// PathAllowed is the public-path gate (ING-5): default-deny, patterns are
// exact paths or subtrees ("/x/*" covers /x and everything under it; "/*"
// publishes the whole surface). p must already be cleaned (CleanPath); a
// trailing slash matches like the path itself ("/docs/" passes a "/docs"
// pattern).
func PathAllowed(patterns []string, p string) bool {
	if p != "/" {
		p = strings.TrimSuffix(p, "/")
	}
	for _, pat := range patterns {
		if stem, ok := strings.CutSuffix(pat, "/*"); ok {
			if stem == "" { // "/*" — everything
				return true
			}
			if p == stem || strings.HasPrefix(p, stem+"/") {
				return true
			}
			continue
		}
		if p == pat {
			return true
		}
	}
	return false
}

// CleanPath normalizes a request path for matching AND forwarding: dot
// segments are resolved (so "/api/public/../../secret" can't slip past the
// allowlist and the backend both), a leading "/" is guaranteed, and a
// meaningful trailing slash survives Clean.
func CleanPath(p string) string {
	if p == "" {
		return "/"
	}
	trailing := strings.HasSuffix(p, "/")
	c := path.Clean("/" + p)
	if trailing && c != "/" {
		c += "/"
	}
	return c
}

// HostOnly lowercases and strips any :port from a Host header value
// (including bracketed IPv6).
func HostOnly(hostport string) string {
	h := strings.ToLower(strings.TrimSpace(hostport))
	if strings.HasPrefix(h, "[") { // [v6]:port
		if i := strings.LastIndexByte(h, ']'); i >= 0 {
			return h[1:i]
		}
	}
	if i := strings.LastIndexByte(h, ':'); i >= 0 && strings.IndexByte(h, ':') == i {
		return h[:i] // exactly one colon → host:port
	}
	return strings.TrimSuffix(h, ".")
}

// HostInZone reports whether host sits inside a delegated wildcard zone
// ("*.sites.example.com"): at least one extra label, never the apex itself.
func HostInZone(host, zone string) bool {
	suffix, ok := strings.CutPrefix(zone, "*.")
	if !ok {
		return false
	}
	return strings.HasSuffix(host, "."+suffix) && len(host) > len(suffix)+1
}

// ValidHost is the hostname shape accepted in bindings and runtime
// registrations: lowercase dns labels, no wildcard, no port.
func ValidHost(h string) bool {
	if h == "" || len(h) > 253 || strings.Contains(h, "*") {
		return false
	}
	for _, label := range strings.Split(h, ".") {
		if label == "" || len(label) > 63 {
			return false
		}
		for i, c := range label {
			switch {
			case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
			case c == '-' && i > 0 && i < len(label)-1:
			default:
				return false
			}
		}
	}
	return true
}

// ValidZone is the delegated-zone shape: "*." + a valid host.
func ValidZone(z string) bool {
	suffix, ok := strings.CutPrefix(z, "*.")
	return ok && ValidHost(suffix)
}

// HTTPHandler is the public HTTP(S) entry: route the Host to its one tile,
// gate the path against the tile's declared allowlist, and hand off to the
// forwarder. It runs OUTSIDE the authenticated middleware — everything it
// admits reaches exactly one tile backend as the anonymous `ingress`
// principal, and nothing else (no /api/xbin, no sibling tiles).
type HTTPHandler struct {
	// Source scopes lookups: "runtime" for the builtin listener, the
	// terminator tile's path for its forward socket — so a terminator can
	// only route hosts whose binding names IT as the source.
	Source string
	// Lookup resolves (source, bare lowercase host) to a route.
	Lookup func(source, host string) (Route, bool)
	// Forward proxies the (path-cleaned) request to the route's tile backend.
	Forward func(w http.ResponseWriter, r *http.Request, rt Route)
}

func (h *HTTPHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt, ok := h.Lookup(h.Source, HostOnly(r.Host))
	if !ok {
		http.Error(w, "no site is published at this hostname", http.StatusNotFound)
		return
	}
	p := CleanPath(r.URL.Path)
	if !PathAllowed(rt.Paths, p) {
		// 404, not 403: the path simply isn't published; don't map the allowlist.
		http.NotFound(w, r)
		return
	}
	if p != r.URL.Path {
		r = r.Clone(r.Context())
		r.URL.Path = p
		r.URL.RawPath = "" // re-encode from the cleaned path
	}
	h.Forward(w, r, rt)
}
