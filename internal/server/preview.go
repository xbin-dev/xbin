package server

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/preview"
)

// preview.go — "see what the user sees" (D234, docs/protocol/preview.md):
// POST /api/xbin/preview and the preview browser's transport.
//
// A render is read-only twice over, each half sufficient alone:
//
//   - the CREDENTIAL: the render's session is a preview session
//     (auth/preview.go) — Principal.ReadOnly, so authed, the tile-origin
//     plane, path tickets and the terminal gates refuse its writes, and so
//     do the frame tokens and tile cookies minted under it;
//   - the TRANSPORT: previewTransport serves the render's own unix socket.
//     Whatever credential a request on it carries — the page is untrusted
//     and may send any it has — nothing but GET/HEAD/OPTIONS passes, no
//     terminal socket, no sign-in route, and the session is set here, never
//     taken from the browser.
//
// And it is SCOPED (previewScope): the socket admits what the render shows —
// one tile, or the shell and the tiles of one screen — so a page that took
// over the browser reads no more than the picture was going to hold.

const (
	previewRefusal = "read-only: this is a preview render — nothing it loads can change anything"
	previewScoped  = "outside this preview: it shows %s, and its browser reaches nothing else"
)

type previewCtxKey struct{}

// previewOf is the scope of the render r belongs to, nil for every other
// request.
func previewOf(r *http.Request) *previewScope {
	sc, _ := r.Context().Value(previewCtxKey{}).(*previewScope)
	return sc
}

// previewScope is what one render's socket admits.
type previewScope struct {
	what  string   // for refusals: "the tile apps/x", "the screen Home"
	tiles []string // the tiles the render shows (their nested components with them)
	// person: the person's own plane is open — the shell's reads on a
	// screen render, a trusted chrome tile's on its own render. Off for a
	// sandboxed tile's render: there the session cookie loads the tile's
	// document and nothing else, and every call is the tile's frame's.
	person bool
	shell  bool // root and shell documents (a screen render)
	// gen is the render's own session's generation: the only one whose
	// credentials its socket honours (set when the session is opened).
	gen string
	// admin: the person is a workspace admin — a screen render then leaves
	// out the reads whose answer to an admin is the whole workspace's
	admin bool

	mu      sync.Mutex
	leftOut []string // person-plane reads the page asked for and a preview doesn't serve
}

func (sc *previewScope) leaveOut(p string) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	if len(sc.leftOut) < 24 && !slices.Contains(sc.leftOut, p) && len(p) <= 200 {
		sc.leftOut = append(sc.leftOut, p)
	}
}

func (sc *previewScope) has(tile string) bool {
	for _, t := range sc.tiles {
		if tile == t || strings.HasPrefix(tile, t+"/") {
			return true
		}
	}
	return false
}

// previewTransport wraps the workspace's handler for one render.
func (s *Server) previewTransport(main http.Handler, sessionID string, sc *previewScope) http.Handler {
	baseHost, baseOrigin := "", "-"
	if u, err := url.Parse(s.previewBase); err == nil {
		baseHost, baseOrigin = strings.ToLower(u.Host), u.Scheme+"://"+u.Host
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			s.refusePreview(w, r, http.StatusForbidden, previewRefusal)
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/ws/term") || strings.HasPrefix(p, "/login") || strings.HasPrefix(p, "/logout") || strings.HasPrefix(p, "/enroll") {
			s.refusePreview(w, r, http.StatusForbidden, previewRefusal)
			return
		}
		// Only the workspace's own names resolve in the sandbox, and to
		// this socket (preview.planOf); whatever else arrives here — a
		// loopback literal, a name a page made up — is not the workspace.
		origins := s.assetMode() == TileAssetsOrigins && s.TilesDomain != ""
		label, tileHost := "", false
		if origins {
			label, tileHost = s.tileHostOf(r.Host)
		}
		if !tileHost && !strings.EqualFold(r.Host, baseHost) {
			s.refusePreview(w, r, http.StatusMisdirectedRequest, "a preview's browser reaches this workspace only, not "+r.Host)
			return
		}
		// Nothing the browser sends names an identity or a client address.
		r.Header.Del("Authorization")
		for k := range r.Header {
			if ck := http.CanonicalHeaderKey(k); strings.HasPrefix(ck, "X-Forwarded-") || ck == "Forwarded" || ck == "X-Real-Ip" {
				r.Header.Del(k)
			}
		}
		// The session rides only where a browser would send its cookie —
		// the transport stands in for the cookie jar, so it applies the
		// jar's rules, which the workspace has always leaned on:
		//   - never to a tile origin (a host-only cookie; the tile gets its
		//     own by the ticket exchange, as in any browser);
		//   - never on a request a sandboxed tile frame starts. Such a frame
		//     is credentialless and its origin opaque: Chromium marks its
		//     requests Sec-Fetch-Site: cross-site, and page script can't
		//     change that header. In origins mode tile origins are same-site
		//     and their navigations do carry the cookie (the ticket exchange
		//     needs it); withoutTileInitiatedCookies judges those.
		// No Fetch Metadata at all is refused the session: this browser
		// always sends it.
		// A WebSocket handshake carries no Fetch Metadata; its Origin says
		// who opened it (never page-settable either): the workspace's own
		// origin is the shell, "null" a sandboxed frame.
		site := r.Header.Get("Sec-Fetch-Site")
		if site == "" && r.Header.Get("Upgrade") != "" && strings.EqualFold(r.Header.Get("Origin"), baseOrigin) {
			site = "same-origin"
		}
		if !tileHost && (site == "same-origin" || site == "none" || origins && site == "same-site") {
			s.Auth.SetRequestSession(r, sessionID)
		} else {
			auth.StripRequestSession(r)
		}
		switch why := s.previewAdmits(r, sc, label, tileHost); {
		case why == previewEmpty && adminWide[r.URL.Path] != "":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(adminWide[r.URL.Path]))
			return
		case why == previewEmpty:
			s.refusePreview(w, r, http.StatusForbidden, "left out of a preview: an admin's render draws the shell as a member sees it")
			return
		case why != "":
			s.refusePreview(w, r, http.StatusForbidden, why)
			return
		}
		main.ServeHTTP(w, r.WithContext(contextWithPreview(r, sc)))
	})
}

// previewAdmits is the scope gate: "" when the render may make request r,
// else the refusal. r already carries the session the cookie-jar rules gave
// it (or none).
//
// First, WHOSE credential: only this render's own session, and what was
// minted under it, count on its socket — on every host. A frame token, a
// tile-origin cookie or an asset token of any other session (the person's
// real one, which a tile kept and handed its page; another render's) is
// refused before it is read for anything else: it would not be read-only,
// or not this render's to scope.
func (s *Server) previewAdmits(r *http.Request, sc *previewScope, label string, tileHost bool) string {
	refuse := fmt.Sprintf(previewScoped, sc.what)
	p := r.URL.Path
	if strings.HasPrefix(p, "/vendor/") {
		return "" // xbind's own shipped code, public by design
	}
	ft := r.Header.Get(auth.FrameTokenHeader)
	if ft == "" {
		ft = r.URL.Query().Get("frame")
	}
	if ft != "" {
		if fp, ok := s.Auth.FramePrincipal(ft); ok && fp.Gen != sc.gen {
			return refuse
		}
	}
	for _, c := range r.Cookies() {
		if c.Name == auth.TileCookieName || c.Name == auth.HostTileCookieName {
			if g, ok := s.Auth.VerifyTileCookie(c.Value); ok && g.FrameGen != sc.gen {
				return refuse
			}
		}
	}
	if tok, _, ok := strings.Cut(strings.TrimPrefix(p, "/c/~"), "/"); ok && strings.HasPrefix(p, "/c/~") {
		if g, ok := s.Auth.VerifyAssetToken(tok); ok && g.Gen != sc.gen {
			return refuse
		}
	}
	if strings.HasPrefix(p, "/api/~") {
		return refuse // a path ticket: none can be minted in a render (a POST), so none of its own exists
	}
	if tileHost { // a tile origin: its calls are that tile's; files it loads are judged below
		if tile, _, ok := s.originOf(label); !ok || !sc.has(tile) {
			return refuse
		}
		if !strings.HasPrefix(p, "/c/") {
			return ""
		}
	}
	if rel, ok := strings.CutPrefix(p, "/c/"); ok {
		if strings.HasPrefix(rel, "~") { // tokens mode: /c/~<asset token>/<tile>/…
			_, rel, _ = strings.Cut(rel, "/")
		}
		owner := s.owningComponent(strings.TrimPrefix(path.Clean("/"+rel), "/"))
		if isChrome(owner) && sc.shell || sc.has(owner) {
			return ""
		}
		return refuse
	}
	who, ok := s.Auth.FromRequest(r)
	switch {
	case !ok:
		return "" // nobody: the workspace answers 401, as it would any browser
	case who.Gen != sc.gen:
		return refuse // (a credential FromRequest read that the checks above did not)
	case who.Component != "" && !(sc.shell && isChrome(s.owningTile(who.Component))):
		// a tile's frame: the tile's own authority, for a tile the render shows
		if sc.has(s.owningTile(who.Component)) {
			return ""
		}
		return refuse
	case !sc.person:
		return refuse // a sandboxed tile's render: the person loads its document, nothing more
	}
	// (The shell's own frame token — the root document carries one for some
	// people — is the shell: it is judged with the person's plane.)
	// The person's own plane (a screen's shell): an ALLOW-list — exactly the
	// reads the shell and the frame machinery make to draw a screen
	// (shellReads; TestPreviewShellReads pins it against a real render). A
	// read not on it is refused and named in the answer's leftOut.
	switch {
	case p == "/" || p == "/api/xbin/frame-token":
		if c := r.URL.Query().Get("component"); p == "/" || c == "" || sc.has(s.owningTile(c)) || isChrome(c) && sc.shell {
			return ""
		}
		return refuse
	case shellReads[p] || strings.HasPrefix(p, "/api/xbin/prefs/"):
		if _, wide := adminWide[p]; wide && sc.admin {
			// an admin's answer there is the whole workspace's, not a
			// member's view: the transport answers it empty (adminWide)
			sc.leaveOut(p)
			return previewEmpty
		}
		return ""
	case strings.HasPrefix(p, "/api/") && !strings.HasPrefix(p, "/api/xbin/"): // a tile's API, in person (a chrome tile on the screen)
		if sc.has(s.owningTile(strings.TrimPrefix(p, "/api/"))) {
			return ""
		}
		return refuse
	}
	sc.leaveOut(p)
	return "left out of a preview: it draws " + sc.what + " as a member sees it, and that needs no " + p
}

// shellReads are the person's own reads a screen render makes: what the
// scaffold's shell and xbind's frame elements ask for to draw a screen, as
// traced from a real render — each answered per person (what THEY may see).
// /api/xbin/prefs/<key> and /api/xbin/frame-token (for a tile on the screen)
// are admitted beside it. Deliberately absent, though the shell asks:
// /status, /backends and /vault-status (admin-only: a member's shell gets
// 403 for them anyway).
var shellReads = map[string]bool{
	"/api/xbin/whoami": true, "/api/xbin/components": true, "/api/xbin/screens": true, "/api/xbin/branding": true,
	"/api/xbin/tile-report": true, "/api/xbin/limits": true, "/api/xbin/alerts": true, "/api/xbin/prefs": true,
	"/api/xbin/term/sessions": true, "/api/xbin/code/prs/summary": true,
	"/api/xbin/access-requests": true, "/api/xbin/grants": true, "/api/xbin/bindings": true,
	"/ws/events": true,
}

// adminWide are the shellReads whose answer to an ADMIN is the whole
// workspace's (every pending grant, binding and access request; every
// event): an admin's render leaves them out, so the picture is the shell as
// a member sees it and a hijacked browser reads no more. Each is answered
// as for a member with nothing pending — an empty list in the route's own
// shape, so the shell draws on — and the event socket is refused.
var adminWide = map[string]string{
	"/api/xbin/grants":          `{"grants":[],"pending":[],"scope":"mine"}`,
	"/api/xbin/bindings":        `{"bindings":[],"pending":[]}`,
	"/api/xbin/access-requests": `{"requests":[]}`,
	"/ws/events":                "",
}

// previewEmpty is previewAdmits's verdict for an adminWide read.
const previewEmpty = "\x00empty"

func contextWithPreview(r *http.Request, sc *previewScope) context.Context {
	return context.WithValue(r.Context(), previewCtxKey{}, sc)
}

func (s *Server) refusePreview(w http.ResponseWriter, r *http.Request, code int, msg string) {
	if r.Header.Get("Origin") == "null" { // a sandboxed tile frame reads the refusal, as nullOriginCORS lets it read any answer
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", "null")
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		WriteError(w, code, msg, "/docs/protocol/preview.md")
		return
	}
	http.Error(w, msg, code)
}

// previewHost is the preview.Host of this server.
type previewHost struct {
	s    *Server
	main http.Handler
}

func (h previewHost) OpenPreview(userID string, ttl time.Duration, scope any) (http.Handler, func(), error) {
	sc, ok := scope.(*previewScope)
	if !ok || sc == nil {
		return nil, nil, errors.New("preview: no scope")
	}
	id, err := h.s.Auth.NewPreviewSession(userID, ttl)
	if err != nil {
		return nil, nil, err
	}
	sc.gen = h.s.Auth.PreviewSessionGen(id)
	return h.s.previewTransport(h.main, id, sc), func() { h.s.Auth.EndPreviewSession(id) }, nil
}

// EnablePreview sets the preview service up (boot). base is the address a
// browser opens this workspace at: --external-url, else the listener's.
// Call before Handler; SetPreviewHandler completes it.
func (s *Server) EnablePreview(svc *preview.Service, base string) {
	if u, err := url.Parse(base); err == nil {
		host, port, perr := net.SplitHostPort(u.Host)
		if ip := net.ParseIP(host); perr == nil && (host == "" || ip != nil && ip.IsUnspecified()) {
			u.Host = net.JoinHostPort("127.0.0.1", port) // a wildcard listener is opened on loopback
			base = u.String()
		}
	}
	s.previewBase = strings.TrimRight(base, "/")
	s.Preview = svc
}

// SetPreviewHandler gives the preview service the workspace's own handler
// (what Handler returned): a render's requests enter where a browser's do.
func (s *Server) SetPreviewHandler(main http.Handler) {
	if s.Preview != nil {
		s.Preview.Host = previewHost{s, main}
	}
}

func (s *Server) registerPreviewAPI() {
	s.RegisterAPI("POST /preview", s.apiPreview)
	s.RegisterAPI("GET /preview", s.apiPreviewStatus)
}

// PreviewTarget names what to render: a tile's own page, or a screen of the
// shell (D220) by id or name — "" with no tile is the person's active
// screen.
type PreviewTarget struct {
	Tile     string `json:"tile,omitempty"`
	Screen   string `json:"screen,omitempty"`
	At       string `json:"at,omitempty"` // the tile's fragment
	Viewport struct {
		Width  int `json:"width,omitempty"`
		Height int `json:"height,omitempty"`
	} `json:"viewport"`
	Wait     int  `json:"wait,omitempty"` // extra ms after the page is drawn
	FullPage bool `json:"fullPage,omitempty"`
	// shell: with Tile, show the tile in the shell on its screen rather
	// than alone (set by a screen target; Screen may then be "").
	shell bool
}

// previewError is a refusal with its status.
type previewError struct {
	code int
	msg  string
}

func (e *previewError) Error() string { return e.msg }

func previewErr(code int, format string, a ...any) error {
	return &previewError{code, fmt.Sprintf(format, a...)}
}

// previewPerson is WHICH PERSON a call to the preview route renders as, or
// why it renders as nobody — the one place that decides it (D234):
//
//   - a person, for themselves: a browser session or the app's (device or
//     password) bearer;
//   - a person's terminal on a tile, for that tile's own page only: a
//     terminal's token is scoped to its tile, and a picture of another tile
//     would be a read of it;
//   - nobody else: not the root token (it is nobody's view), not a tile or
//     its frame, not a view-as session (an admin looks through view-as
//     itself), not a preview.
//
// The workspace agent's delegate (plans/workspace-agent.md §4) is added here
// and nowhere else when delegation lands: Via "delegate" renders as its
// person.
func previewPerson(p auth.Principal, t PreviewTarget) (userID string, err error) {
	switch {
	case p.ReadOnly():
		return "", previewErr(http.StatusForbidden, "a preview is a person's own view: not from a view-as session")
	case p.UserID == "":
		return "", previewErr(http.StatusForbidden, "a preview is a person's own view: sign in as a person (the root token is nobody's view)")
	case p.Component == "" && (p.Via == "session" || p.Via == "device" || p.Via == "app"):
		return p.UserID, nil
	case p.Via == "terminal" && p.Component != "":
		if t.Screen == "" && !t.shell && t.Tile != "" && (t.Tile == p.Component || strings.HasPrefix(t.Tile, p.Component+"/")) {
			return p.UserID, nil
		}
		return "", previewErr(http.StatusForbidden, "a tile's terminal previews its own tile only (%s): bx preview %s", p.Component, p.Component)
	}
	return "", previewErr(http.StatusForbidden, "a preview is a person's own view: a tile can't ask for one")
}

// RenderPreview renders t as userID. Callers decide who userID is
// (previewPerson); this resolves what the render shows and so what its
// socket admits.
func (s *Server) RenderPreview(r *http.Request, userID string, t PreviewTarget) (*preview.Result, error) {
	if s.Preview == nil {
		return nil, fmt.Errorf("%w: not set up", preview.ErrUnavailable)
	}
	if err := s.Preview.Available(); err != nil {
		return nil, err
	}
	host, ok := s.Preview.Host.(previewHost)
	if !ok {
		return nil, fmt.Errorf("%w: not set up", preview.ErrUnavailable)
	}
	page, sc, err := s.previewPlan(host.main, userID, t)
	if err != nil {
		return nil, err
	}
	res, err := s.Preview.Render(r.Context(), preview.Request{
		UserID: userID, URL: s.previewBase + page, Scope: sc, Hosts: s.previewHosts(),
		Width: t.Viewport.Width, Height: t.Viewport.Height, WaitMs: t.Wait, FullPage: t.FullPage,
	})
	if err != nil {
		return nil, err
	}
	sc.mu.Lock()
	res.LeftOut = slices.Clone(sc.leftOut)
	sc.mu.Unlock()
	if sc.admin && sc.person {
		res.Note = previewAdminNote
	}
	return res, nil
}

// previewAdminNote goes with an admin's screen render: what it leaves out.
const previewAdminNote = "You are a workspace admin: a preview draws the shell as a member sees it. The admin-only reads (status, backends, the vault's state), the workspace-wide lists (pending grants, bindings, access requests) and live events are left out, so prompts and status marks an admin sees are missing here."

// previewHosts are the tile origins' names, for the sandbox's resolver rule.
func (s *Server) previewHosts() []string {
	if s.assetMode() != TileAssetsOrigins || s.TilesDomain == "" {
		return nil
	}
	td := s.TilesDomain
	if h, _, err := net.SplitHostPort(td); err == nil {
		td = h
	}
	return []string{"*." + strings.ToLower(td)}
}

// previewPlan resolves t for userID: the page to open and the render's
// scope. It reads what the person's own shell would — their layout, the
// screens shared with them, the tiles they can read — through the
// workspace's handler as a short preview session of its own, so the answer
// is exactly what the render will be shown.
func (s *Server) previewPlan(main http.Handler, userID string, t PreviewTarget) (page string, sc *previewScope, err error) {
	sid, err := s.Auth.NewPreviewSession(userID, 30*time.Second)
	if err != nil {
		return "", nil, previewErr(http.StatusForbidden, "%v", err)
	}
	defer s.Auth.EndPreviewSession(sid)
	get := func(path string, v any) int {
		req := httptest.NewRequest(http.MethodGet, s.previewBase+path, nil)
		s.Auth.SetRequestSession(req, sid)
		rec := httptest.NewRecorder()
		main.ServeHTTP(rec, req)
		if rec.Code == http.StatusOK {
			_ = json.Unmarshal(rec.Body.Bytes(), v)
		}
		return rec.Code
	}
	var comps struct {
		Components []struct {
			Path string `json:"path"`
		} `json:"components"`
	}
	var list []struct {
		Path string `json:"path"`
	}
	admin := false
	if s.Auth.Users != nil {
		if u, ok := s.Auth.Users.Get(userID); ok {
			admin = u.IsAdmin()
		}
	}
	readable := map[string]bool{}
	if get("/api/xbin/components", &comps); len(comps.Components) == 0 {
		get("/api/xbin/components", &list)
		comps.Components = list
	}
	for _, c := range comps.Components {
		readable[c.Path] = true
	}

	if t.Tile != "" {
		clean, ok := cleanTilePath(t.Tile)
		if !ok {
			return "", nil, previewErr(http.StatusBadRequest, "tile: not a tile path")
		}
		t.Tile = clean
		if !readable[clean] || isChrome(clean) {
			return "", nil, previewErr(http.StatusNotFound, "no tile %s among the ones you can open", clean)
		}
	}
	if t.Tile != "" && t.Screen == "" && !t.shell { // the tile's own page, filling the viewport
		who, _ := s.Auth.FromRequest(func() *http.Request {
			req := httptest.NewRequest(http.MethodGet, s.previewBase+"/", nil)
			s.Auth.SetRequestSession(req, sid)
			return req
		}())
		if _, perr := s.addressedPartition(who, s.owningTile(t.Tile)); perr != nil {
			return "", nil, previewErr(http.StatusConflict, "%v", perr) // a partition never opened (D234), or one the tile can't give
		}
		comp, _ := s.Reg.Component(t.Tile)
		page = "/c/" + t.Tile + "/"
		if t.At != "" {
			page += "#" + strings.TrimPrefix(t.At, "#")
		}
		return page, &previewScope{what: "the tile " + t.Tile, tiles: []string{t.Tile}, person: s.trustedChrome(t.Tile, comp), admin: admin}, nil
	}

	// a screen of the shell
	type screen struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Tiles []struct {
			Path string `json:"path"`
		} `json:"tiles"`
	}
	var layout struct {
		Active  string   `json:"active"`
		Screens []screen `json:"screens"`
	}
	var shared struct {
		Org []screen `json:"org"`
	}
	hasLayout := get("/api/xbin/prefs/layout", &layout) == http.StatusOK && len(layout.Screens) > 0
	get("/api/xbin/screens", &shared)
	all := append(append([]screen{}, layout.Screens...), shared.Org...)
	var pick *screen
	want := t.Screen
	if want == "" {
		want = layout.Active
	}
	for _, byName := range []bool{false, true} { // an id first, then a name
		for i := range all {
			if pick == nil && want != "" && (!byName && all[i].ID == want || byName && strings.EqualFold(all[i].Name, want)) {
				pick = &all[i]
			}
		}
	}
	if pick == nil && t.Screen == "" && len(all) > 0 {
		pick = &all[0]
	}
	sc = &previewScope{person: true, shell: true, admin: admin}
	q := url.Values{}
	switch {
	case pick != nil:
		sc.what = "the screen " + pick.Name
		q.Set("screen", pick.ID)
		for _, tl := range pick.Tiles {
			if readable[tl.Path] {
				sc.tiles = append(sc.tiles, tl.Path)
			}
		}
	case t.Screen != "":
		var names []string
		for _, sn := range all {
			names = append(names, sn.Name)
		}
		return "", nil, previewErr(http.StatusNotFound, "no screen %q among yours (%s)", t.Screen, strings.Join(names, ", "))
	case !hasLayout:
		// a first visit: the shell seeds the screen from the workspace's
		// default and its own pins, filtered to what the person reads —
		// which is the most this render can show
		sc.what = "your first screen"
		for c := range readable {
			if !isChrome(c) {
				sc.tiles = append(sc.tiles, c)
			}
		}
	}
	if t.Tile != "" {
		q.Set("tile", t.Tile)
		if !sc.has(t.Tile) {
			sc.tiles = append(sc.tiles, t.Tile) // the link opens it on the screen
		}
		if t.At != "" {
			q.Set("at", strings.TrimPrefix(t.At, "#"))
		}
	}
	page = "/c/root/"
	if len(q) > 0 {
		page += "?" + q.Encode()
	}
	return page, sc, nil
}

func cleanTilePath(p string) (string, bool) {
	p = strings.Trim(p, "/")
	if p == "" || strings.ContainsAny(p, "?#\\~+") {
		return "", false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." {
			return "", false
		}
	}
	return p, true
}

// apiPreview: POST /api/xbin/preview (docs/protocol/preview.md).
func (s *Server) apiPreview(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PreviewTarget
		Shell bool `json:"shell,omitempty"` // with tile: in the shell, on its screen
	}
	if err := DecodeJSON(r, &body); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error(), "/docs/protocol/preview.md")
		return
	}
	t := body.PreviewTarget
	t.shell = body.Shell || t.Screen != "" || t.Tile == ""
	userID, err := previewPerson(auth.PrincipalOf(r), t)
	var res *preview.Result
	if err == nil {
		res, err = s.RenderPreview(r, userID, t)
	}
	var pe *previewError
	switch {
	case errors.As(err, &pe):
		WriteError(w, pe.code, pe.msg, "/docs/protocol/preview.md")
	case errors.Is(err, preview.ErrUnavailable):
		WriteError(w, http.StatusServiceUnavailable, err.Error(), "/docs/protocol/preview.md")
	case errors.Is(err, preview.ErrBusy):
		w.Header().Set("Retry-After", "5")
		WriteError(w, http.StatusTooManyRequests, err.Error(), "/docs/protocol/preview.md")
	case errors.Is(err, preview.ErrMemory):
		WriteError(w, http.StatusInsufficientStorage, err.Error(), "/docs/protocol/preview.md")
	case err != nil:
		WriteError(w, http.StatusBadGateway, err.Error(), "/docs/protocol/preview.md")
	default:
		res.PNGBase64 = base64.StdEncoding.EncodeToString(res.PNG)
		WriteJSON(w, http.StatusOK, res)
	}
}

// apiPreviewStatus: GET /api/xbin/preview — whether this xbind renders
// previews, why not, and the limits in force. Any signed-in caller: it is
// how `bx preview`, the workspace agent and the admin console learn it.
func (s *Server) apiPreviewStatus(w http.ResponseWriter, r *http.Request) {
	st := s.Preview.Status()
	if !s.admin(r) { // the browser build and what is running are the operator's to know
		st.Browser, st.Sandbox, st.Playwright, st.Running = "", "", "", 0
	}
	WriteJSON(w, http.StatusOK, st)
}
