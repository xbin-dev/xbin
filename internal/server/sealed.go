package server

// sealed.go — sealed components on the HTTP planes (D227). A sealed
// component lives at xbin/<name>, inside the namespace of xbind's own API
// (/api/xbin/…) and outside what /c/ serves (pathAllowed refuses the
// reserved top-level names). Both planes ask the registry: a path owned by
// a registered sealed component is that component's, served from xbind's
// materialized tree (codeRoot) and proxied to its backend (handleAPI).

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// sealedOwned reports whether urlPath ("xbin/agent/x.js") lies at or below a
// registered sealed component.
func (s *Server) sealedOwned(urlPath string) bool {
	if s.Reg == nil || !util.SealedPath(urlPath) {
		return false
	}
	c, _, ok := s.Reg.Resolve(urlPath)
	return ok && c.Sealed
}

// WantsSealed reports whether a listing's caller asked for sealed
// components too (?sealed=1). Listings leave them out otherwise, so a shell,
// an admin console or an app that predates them never shows a tile it
// can't open a terminal, the code or a deployment of (docs/compat.md).
func WantsSealed(r *http.Request) bool {
	switch r.URL.Query().Get("sealed") {
	case "1", "true":
		return true
	}
	return false
}

// SealedNameClash names what of xbind's own a sealed component called name
// would shadow ("" = nothing): an /api/xbin route whose first path segment
// is the name (its API prefix is /api/xbin/<name>/…), or an xbind page
// /xbin/<name>. apiPatterns are RegisterAPI patterns, corePatterns the
// top-level mux's.
func SealedNameClash(name string, apiPatterns, corePatterns []string) string {
	for _, pat := range apiPatterns {
		seg, _, _ := strings.Cut(strings.TrimPrefix(patternPath(pat), "/"), "/")
		if seg == name || strings.HasPrefix(seg, "{") && !strings.HasPrefix(seg, "{$") {
			return fmt.Sprintf("xbind's API route %q", pat)
		}
	}
	for _, pat := range corePatterns {
		if p := patternPath(pat); p == "/xbin/"+name || strings.HasPrefix(p, "/xbin/"+name+"/") {
			return fmt.Sprintf("xbind's page %q", pat)
		}
	}
	return ""
}

// patternPath is a ServeMux pattern without its method.
func patternPath(pat string) string {
	if i := strings.IndexByte(pat, ' '); i >= 0 {
		return pat[i+1:]
	}
	return pat
}

// SealedAPIClash names the first registered sealed component whose name
// shadows something of xbind's own (SealedNameClash), "" when none does.
// Boot refuses to serve with one, after every route is registered: a
// sealed component can never take a route of the API over, and a new route
// can never be added under a sealed name unnoticed.
func (s *Server) SealedAPIClash() string {
	if s.Reg == nil {
		return ""
	}
	for _, c := range s.Reg.SealedComponents() {
		if what := SealedNameClash(strings.TrimPrefix(c.Path, util.SealedPrefix), s.apiPatterns, s.corePatterns); what != "" {
			return fmt.Sprintf("sealed component %s would shadow %s", c.Path, what)
		}
	}
	return ""
}

// A sealed component's code changes under its open pages when xbind
// restarts onto another release: the page in the browser is the old
// release's, the backend the new one's. Nothing checks a tile for that — a
// tile's code is one tree its authors move together — but a sealed
// component's two halves are a release's, and its frontend is written
// against exactly that release's backend. So the page says which code it
// is, and xbind turns away a request from a page of other code:
//
//   - every document of a sealed component carries its code's id (the
//     xbin-code meta, sealedCodeMeta);
//   - the injected client sends it with each call to the component's own
//     API: the X-XBin-Code header on xbin.fetch, the xbin-code query
//     parameter on the URLs it builds for sockets and tags (consumed here,
//     never forwarded);
//   - a request that names other code than the running release's answers
//     409 with X-XBin-Reload: <the current id>, and a reload event for the
//     component goes out, so every open frame of it loads the new page
//     (bx-frame reloads on that event as it does for a tile's save; the
//     client reloads a page opened by itself).
//
// A request that names no code — a backend, a script, an admin's call — is
// not a page and passes. The check needs no state across restarts: the id
// is in the page.
const (
	// SealedCodeHeader carries the id of the code a sealed component's page
	// was served from.
	SealedCodeHeader = "X-XBin-Code"
	// SealedCodeQuery is the same on a URL (a socket, a tag-driven request).
	SealedCodeQuery = "xbin-code"
	// SealedReloadHeader, on the 409, names the code the release runs now.
	SealedReloadHeader = "X-XBin-Reload"
)

// sealedCodeMeta is the head injection's xbin-code meta for a sealed
// component's document; "" for every other.
func sealedCodeMeta(c *registry.Component) string {
	if c == nil || !c.Sealed || c.SealedID == "" {
		return ""
	}
	return fmt.Sprintf("<meta name=\"xbin-code\" content=\"%s\">\n", htmlEscape(c.SealedID))
}

// sealedStale answers a call to a sealed component's API from a page of
// other code than the running release's (true: answered), and takes the
// xbin-code parameter off a URL that carries the current one. rest is the
// path after /api/.
func (s *Server) sealedStale(w http.ResponseWriter, r *http.Request, rest string) bool {
	c, _, ok := s.Reg.Resolve(rest)
	if !ok || !c.Sealed {
		return false
	}
	code := r.Header.Get(SealedCodeHeader)
	q := r.URL.Query()
	if v := q.Get(SealedCodeQuery); v != "" {
		if code == "" {
			code = v
		}
		q.Del(SealedCodeQuery) // xbind's own, like ?frame=: the backend never sees it
		r.URL.RawQuery = q.Encode()
	}
	if code == "" || code == c.SealedID {
		return false
	}
	s.sealedReload(c.Path)
	w.Header().Set(SealedReloadHeader, c.SealedID)
	WriteError(w, http.StatusConflict, c.Path+" was updated with xbin: its page reloads onto the new version", SealedDocs)
	return true
}

// sealedReloads spaces the reload events of a component: a stale page may
// fire many requests before it is gone.
var sealedReloads struct {
	sync.Mutex
	last map[string]time.Time
}

// sealedReload tells the open frames of the sealed component at path to
// load its page again (at most once a second per component).
func (s *Server) sealedReload(path string) {
	if s.Hub == nil {
		return
	}
	sealedReloads.Lock()
	if sealedReloads.last == nil {
		sealedReloads.last = map[string]time.Time{}
	}
	recent := time.Since(sealedReloads.last[path]) < time.Second
	if !recent {
		sealedReloads.last[path] = time.Now()
	}
	sealedReloads.Unlock()
	if !recent {
		s.Hub.Publish(events.Event{Type: "reload", Component: path})
	}
}
