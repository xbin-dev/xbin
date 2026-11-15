package server

import (
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/preview"
)

// preview.go — "see what the user sees" (plans/wsagent/s3-preview.md): the
// preview browser's transport and POST /api/xbin/preview. SPIKE: the route
// is mounted only by EnablePreview (boot: XBIN_PREVIEW_SPIKE=1), so the
// route inventory, its class tables and the docs are untouched.
//
// The read-only guarantee has two independent halves:
//
//   - the CREDENTIAL: the render's session is a preview session
//     (auth/preview.go) — Principal.ReadOnly, so authed, the tile-origin
//     plane, path tickets and the terminal gates refuse its writes, and so
//     do the frame tokens and tile cookies minted under it;
//   - the TRANSPORT: previewTransport below serves the render's own unix
//     socket. Whatever credential a request on it carries — the page is
//     untrusted and may send any it has — nothing but GET/HEAD/OPTIONS
//     passes, no terminal socket, no sign-in route, and the session cookie
//     is set here, never taken from the browser.

const previewRefusal = "read-only: this is a preview render — nothing it loads can change anything"

// previewTransport wraps the workspace's handler for one render.
func (s *Server) previewTransport(main http.Handler, sessionID string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
		default:
			s.refusePreview(w, r)
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/ws/term") || strings.HasPrefix(p, "/login") || strings.HasPrefix(p, "/logout") || strings.HasPrefix(p, "/enroll") {
			s.refusePreview(w, r)
			return
		}
		// Nothing the browser sends names an identity or a client address.
		r.Header.Del("Authorization")
		for k := range r.Header {
			if strings.HasPrefix(http.CanonicalHeaderKey(k), "X-Forwarded-") || http.CanonicalHeaderKey(k) == "Forwarded" || http.CanonicalHeaderKey(k) == "X-Real-Ip" {
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
		origins := s.assetMode() == TileAssetsOrigins && s.TilesDomain != ""
		_, tileHost := s.tileHostOf(r.Host)
		site := r.Header.Get("Sec-Fetch-Site")
		if (!origins || !tileHost) && (site == "same-origin" || site == "none" || origins && site == "same-site") {
			s.Auth.SetRequestSession(r, sessionID)
		} else {
			auth.StripRequestSession(r)
		}
		main.ServeHTTP(w, r)
	})
}

func (s *Server) refusePreview(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Origin") == "null" { // a sandboxed tile frame reads the refusal, as nullOriginCORS lets it read any answer
		w.Header().Set("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", "null")
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		WriteError(w, http.StatusForbidden, previewRefusal, "/docs/auth.md")
		return
	}
	http.Error(w, previewRefusal, http.StatusForbidden)
}

// previewHost is the preview.Host of this server.
type previewHost struct {
	s    *Server
	main http.Handler
}

func (h previewHost) OpenPreview(userID string, ttl time.Duration) (http.Handler, func(), error) {
	id, err := h.s.Auth.NewPreviewSession(userID, ttl)
	if err != nil {
		return nil, nil, err
	}
	return h.s.previewTransport(h.main, id), func() { h.s.Auth.EndPreviewSession(id) }, nil
}

// EnablePreview mounts POST /preview. base is the address a browser opens
// this workspace at (--external-url, else the listener's); rootfs the base
// rootfs. Call before Handler; main is set by SetPreviewHandler once the
// handler exists.
func (s *Server) EnablePreview(base, rootfs string) {
	s.previewBase = strings.TrimRight(base, "/")
	s.Preview = &preview.Service{Rootfs: rootfs}
}

// mountPreview puts the spike's route on the core mux (Handler), beside
// /api/ and inside the same outer gates. NOT through RegisterAPI on purpose:
// a registered route needs its rows in the deployment and partition class
// tables, openapi.go and docs/protocol — the guards (internal/apicheck)
// insist — and those come with the production change, where the route moves
// to RegisterAPI and so gains handleAPI's gates and audit line.
func (s *Server) mountPreview(handle func(pattern string, h http.Handler)) {
	if s.Preview != nil {
		handle(previewRoute, s.authed(http.HandlerFunc(s.apiPreview)))
	}
}

const previewRoute = "POST /api/xbin/" + "preview"

// SetPreviewHandler gives the preview service the workspace's own handler
// (what Handler returned): a render's requests enter where a browser's do.
func (s *Server) SetPreviewHandler(main http.Handler) {
	if s.Preview != nil {
		s.Preview.Host = previewHost{s, main}
	}
}

// PreviewTarget names what to render.
type PreviewTarget struct {
	Tile     string `json:"tile,omitempty"`   // a tile's own page, filling the viewport
	Screen   string `json:"screen,omitempty"` // a screen of the shell (D220), with its tiles
	At       string `json:"at,omitempty"`     // the tile's fragment
	Viewport struct {
		Width  int `json:"width,omitempty"`
		Height int `json:"height,omitempty"`
	} `json:"viewport"`
	Wait     int  `json:"wait,omitempty"` // extra ms after the page went quiet
	FullPage bool `json:"fullPage,omitempty"`
}

// RenderPreview renders t as userID: the one entry both callers use — a
// person for themselves (apiPreview) and, later, their workspace agent's
// delegate, whose route table resolves the person before calling this.
func (s *Server) RenderPreview(r *http.Request, userID string, t PreviewTarget) (*preview.Result, error) {
	if s.Preview == nil {
		return nil, preview.ErrUnavailable
	}
	var page string
	switch {
	case t.Screen != "":
		q := url.Values{"screen": {t.Screen}}
		if t.Tile != "" {
			q.Set("tile", t.Tile)
		}
		if t.At != "" {
			q.Set("at", t.At)
		}
		page = "/c/root/?" + q.Encode()
	case t.Tile != "":
		clean, ok := cleanTilePath(t.Tile)
		if !ok {
			return nil, errors.New("tile: not a tile path")
		}
		page = "/c/" + clean + "/"
		if t.At != "" {
			page += "#" + strings.TrimPrefix(t.At, "#")
		}
	default:
		page = "/c/root/"
	}
	return s.Preview.Render(r.Context(), preview.Request{
		UserID: userID, URL: s.previewBase + page,
		Width: t.Viewport.Width, Height: t.Viewport.Height, WaitMs: t.Wait, FullPage: t.FullPage,
	})
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

// apiPreview: POST /api/xbin/preview {tile | screen, at?, viewport?, wait?,
// fullPage?} → {png (base64), width, height, snapshot, frames[], console[],
// page_errors[], requests_failed[], url, title, timing}. A person, for
// themselves: a signed-in human session — not the root token (it is nobody's
// view), not a tile, not a view-as session.
func (s *Server) apiPreview(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	var t PreviewTarget
	if err := DecodeJSON(r, &t); err != nil {
		WriteError(w, http.StatusBadRequest, err.Error())
		return
	}
	// A person, or a person's terminal on a tile — and that one for its own
	// tile's page only: a terminal's token is scoped to its tile, and a
	// picture of another tile would be a read of it (bx preview).
	person := p.Component == "" && p.UserID != "" && !p.ReadOnly()
	ownTile := p.Via == "terminal" && p.UserID != "" && !p.ReadOnly() && t.Screen == "" &&
		t.Tile != "" && (t.Tile == p.Component || strings.HasPrefix(t.Tile, p.Component+"/"))
	if !person && !ownTile {
		WriteError(w, http.StatusForbidden, "a preview is a person's own view: sign in as a person (not a tile, the root token or a view-as session); a tile's terminal previews its own tile", "/docs/auth.md")
		return
	}
	res, err := s.RenderPreview(r, p.UserID, t)
	switch {
	case errors.Is(err, preview.ErrUnavailable):
		WriteError(w, http.StatusServiceUnavailable, err.Error())
		return
	case errors.Is(err, preview.ErrBusy):
		WriteError(w, http.StatusTooManyRequests, err.Error())
		return
	case err != nil:
		WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	res.PNGBase64 = base64.StdEncoding.EncodeToString(res.PNG)
	WriteJSON(w, http.StatusOK, res)
}
