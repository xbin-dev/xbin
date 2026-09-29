package server

// pathticket.go — path tickets (D135; internal/auth/pathticket.go): a
// tile's page mints one for a prefix of its own backend's API, and
// /api/~<ticket>/<rest> reaches /api/<tile>/<prefix>/<rest> as that page's
// frame principal. It exists for documents a tile frames from its backend
// in an opaque-origin sandbox — the agent template's live preview of a
// sandbox's server — whose relative loads carry no token and no cookie.
// The framed content can read the ticket from its own URL, so a ticket
// reaches nothing but its prefix: never another route of the tile, another
// tile, /api/xbin or /ws. And it is used only from an address that signed
// in within the hour (the /c/ subresource rule's), so a ticket the framed
// page sends away is no use elsewhere.

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
)

// apiPathTicket answers POST /api/xbin/path-tickets {path}: a ticket for
// the calling page's own tile, {url: "/api/~<ticket>/", expires}.
func (s *Server) apiPathTicket(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	if p.Via != "frame" || p.Component == "" {
		WriteError(w, http.StatusForbidden, "a path ticket is minted by a tile's page, with its frame token", "/docs/auth.md")
		return
	}
	var body struct {
		Path string `json:"path"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		WriteError(w, http.StatusBadRequest, "bad JSON body: {path}", "/docs/protocol.md")
		return
	}
	prefix := strings.Trim(body.Path, "/")
	if !auth.ValidPathTicketPrefix(prefix) {
		WriteError(w, http.StatusBadRequest, "path is a relative path below the tile's API of unreserved characters (A-Z a-z 0-9 . _ ~ -), no . or .. segment, at most 256 bytes", "/docs/protocol.md")
		return
	}
	tok, exp, err := s.Auth.MintPathTicket(p, prefix)
	if err != nil {
		WriteError(w, http.StatusForbidden, err.Error(), "/docs/auth.md")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	WriteJSON(w, http.StatusOK, map[string]any{"url": "/api/~" + tok + "/", "expires": exp.UnixMilli()})
}

// pathTicketCSP is on every answer a path ticket reaches.
const pathTicketCSP = "sandbox allow-scripts allow-forms"

// isPathTicket: /api/~<ticket>/….
func isPathTicket(r *http.Request) bool { return strings.HasPrefix(r.URL.Path, "/api/~") }

// servePathTicket serves /api/~<ticket>/<rest> as the ticket's principal,
// on /api/<tile>/<prefix>/<rest>. originID is the tile origin the request
// came to ("" on the workspace origin): a ticket there must be that
// origin's tile's.
func (s *Server) servePathTicket(w http.ResponseWriter, r *http.Request, originID string) {
	esc := strings.TrimPrefix(r.URL.EscapedPath(), "/api/~")
	tok, rest, ok := strings.Cut(esc, "/")
	if !ok {
		// the document's own URL without its slash: relative loads need it
		http.Redirect(w, r, "/api/~"+tok+"/", http.StatusMovedPermanently)
		return
	}
	p, prefix, ok := s.Auth.VerifyPathTicket(tok)
	if !ok {
		WriteError(w, http.StatusUnauthorized, "this link has expired or its sign-in ended: reload the page that showed it", "/docs/auth.md")
		return
	}
	if originID != "" && !s.originIsTile(originID, p.Component) {
		WriteError(w, http.StatusForbidden, "a tile origin serves only its own tile's links", "/docs/auth.md")
		return
	}
	if !s.Auth.NoAuth() && !s.Auth.RecentlyAuthed(s.ClientIP(r)) {
		WriteError(w, http.StatusUnauthorized, "this link works only from where you are signed in", "/docs/auth.md")
		return
	}
	dec, err := url.PathUnescape(rest)
	if err != nil {
		WriteError(w, http.StatusBadRequest, "a bad escape in the path", "/docs/protocol.md")
		return
	}
	for _, seg := range strings.Split(dec, "/") {
		if seg == "." || seg == ".." {
			WriteError(w, http.StatusBadRequest, "the path has a dot segment", "/docs/protocol.md")
			return
		}
	}
	if p.ReadOnly() && !readOnlyAllowed(r) {
		refuseReadOnly(w, r)
		return
	}
	inner := "/" + p.Component + "/" + prefix + "/"
	if comp, _, found := s.Reg.Resolve(strings.TrimPrefix(inner+dec, "/")); !found || comp.Path != p.Component {
		WriteError(w, http.StatusNotFound, "no such tile", "/docs/protocol.md")
		return
	}
	r2 := r.Clone(auth.WithNoSetCookie(auth.WithPrincipal(r.Context(), p)))
	r2.URL.Path = "/api" + inner + dec
	r2.URL.RawPath = ""
	if raw := "/api" + inner + rest; raw != r2.URL.EscapedPath() {
		r2.URL.RawPath = raw
	}
	r2.RequestURI = r2.URL.RequestURI()
	for _, h := range []string{"Cookie", "Authorization", auth.FrameTokenHeader} {
		r2.Header.Del(h)
	}
	// Whatever a ticket reaches is framed where no credential rides: it
	// answers as an opaque origin whatever its backend sets (browsers
	// intersect this with the backend's own policy).
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Security-Policy", pathTicketCSP)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if s.ComponentAPI == nil {
		http.Error(w, `{"error":"component backends not enabled"}`, http.StatusNotImplemented)
		return
	}
	s.ComponentAPI.ServeHTTP(noSetCookie(w), r2)
}
