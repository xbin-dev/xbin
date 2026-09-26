package server

import (
	"errors"
	"html"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

// Tile asset gating modes (--tile-assets; plans/tile-asset-auth.md).
//
//   - legacy (default this release): today's /c/ plane — a sandboxed
//     frame's credential-less subresource loads pass on the Fetch-Metadata
//     fingerprint plus a recently-authenticated source IP.
//   - tokens (mechanism B): no credential-less path. A tile document's
//     injection adds <base href="/c/~<asset-token>/<tile>/<dir>/"> and
//     remaps /c/<tile>/ in its import map, so relative URLs (and their
//     transitive loads) carry a path-scoped asset token.
//   - origins (mechanism A): no credential-less path. Each tile's frontend
//     lives on its own origin t-<id>.<tiles-domain>, where a cookie set by a
//     navigation-time frame-token exchange credentials every load
//     (tileorigin.go).
//
// Both strict modes check every /c/ request against live RBAC.
const (
	TileAssetsLegacy  = "legacy"
	TileAssetsTokens  = "tokens"
	TileAssetsOrigins = "origins"
)

// ValidTileAssetsMode reports whether m names a mode ("" = legacy).
func ValidTileAssetsMode(m string) bool {
	switch m {
	case "", TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins:
		return true
	}
	return false
}

func (s *Server) assetMode() string {
	if s.TileAssets == "" {
		return TileAssetsLegacy
	}
	return s.TileAssets
}

// strictAssets: a strict mode is on — no credential-less /c/ path.
func (s *Server) strictAssets() bool { return s.assetMode() != TileAssetsLegacy }

// strictLiveRead is the strict modes' extra read check: an element
// principal driven by a user reads a tile only while that USER still may
// (the element self-pass alone would keep a revoked user's open tile
// loading its files). Humans, owner-driven elements and backends are
// already checked by CanReadTile; legacy adds nothing.
func (s *Server) strictLiveRead(p auth.Principal, owner string) bool {
	if !s.strictAssets() || p.Component == "" || p.UserID == "" {
		return true
	}
	return s.Auth.UserCanReadTile(p.UserID, owner) || s.codeGranted(p, owner)
}

// docSandboxExtras: a document's sandbox tokens beyond the base — its grants'
// (ND11), plus allow-same-origin for a tile's own document served on its own
// origin (origins mode, decision 4 of the plan: the tile origin is the
// isolation boundary, and the cookie credential needs a real origin). Never
// on the workspace origin: allow-same-origin there would void the sandbox.
func (s *Server) docSandboxExtras(r *http.Request, compPath string) []string {
	extras := s.sandboxExtras(compPath)
	if t := tileOriginOf(r); t != "" && t == compPath {
		extras = append(append([]string(nil), extras...), "allow-same-origin")
	}
	return extras
}

// isNavigation: a browser loading a document (not a fetch or subresource).
// Without Fetch Metadata, an HTML Accept stands in.
func isNavigation(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	if m := r.Header.Get("Sec-Fetch-Mode"); m != "" {
		return m == "navigate"
	}
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

// isHTMLName: the documents the injection serves (by extension, as legacy).
func isHTMLName(name string) bool {
	return strings.HasSuffix(name, ".html") || strings.HasSuffix(name, ".htm")
}

// documentDest: Fetch Metadata says the browser will render this as a
// document (a navigation, a frame, an embed). The asset-token plane refuses
// these whatever the file — defense in depth on top of never serving HTML.
func documentDest(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Dest") {
	case "document", "iframe", "frame", "embed", "object", "fencedframe":
		return true
	}
	return r.Header.Get("Sec-Fetch-Mode") == "navigate"
}

// --- strict /c/ serving ---

// openStrict opens a /c/ file beneath its owning tile's directory (the dev
// overlay's copy first): no symlink met on the way may leave the tile — a
// tile's writers must not be able to point xbind at .xbin/secret, another
// tile, or the host (fsutil.OpenBeneath).
func (s *Server) openStrict(owner, cleaned string) (*os.File, os.FileInfo, error) {
	rel := strings.TrimPrefix(strings.TrimPrefix(cleaned, owner), "/")
	open := func(base string) (*os.File, os.FileInfo, error) {
		f, err := fsutil.OpenBeneath(filepath.Join(base, filepath.FromSlash(owner)), rel)
		if err != nil {
			return nil, nil, err
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			return nil, nil, err
		}
		return f, fi, nil
	}
	if s.Overlay != "" {
		switch path.Base(cleaned) {
		case "xbin.json", "scope.json": // manifests are never overlaid
		default:
			if f, fi, err := open(s.Overlay); err == nil {
				if fi.Mode().IsRegular() {
					return f, fi, nil
				}
				f.Close()
			}
		}
	}
	f, fi, err := open(s.Reg.Root)
	if errors.Is(err, fsutil.ErrEscapes) {
		slog.Warn("static: refused a path escaping its tile", "path", cleaned)
	}
	return f, fi, err
}

// serveStrictStatic is handleComponentStatic's tail under a strict mode:
// same directory/index/injection rules, files opened by openStrict, and the
// mode's gate (assetGate) before anything is written.
func (s *Server) serveStrictStatic(w http.ResponseWriter, r *http.Request, cleaned, owner string) {
	f, fi, err := s.openStrict(owner, cleaned)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dirIndex, name := false, cleaned
	if fi.IsDir() {
		f.Close()
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		dirIndex, name = true, path.Join(cleaned, "index.html")
		if f, fi, err = s.openStrict(owner, name); err != nil {
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()
	if !fi.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	comp, _, _ := s.Reg.Resolve(cleaned)
	isDoc := isHTMLName(name)
	if s.assetGate(w, r, owner, comp, isDoc) {
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if isDoc {
		if comp == nil || comp.Manifest.Inject == nil || *comp.Manifest.Inject {
			body, err := io.ReadAll(f)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			s.injectHTML(w, r, body, comp, cleaned, dirIndex)
			return
		}
		s.sandboxDocument(w, r, owner, comp)
	}
	http.ServeContent(w, r, name, fi.ModTime(), f)
}

// assetGate applies the strict mode's rules for one /c/ response and reports
// whether it answered the request itself:
//
//   - on a tile origin (origins mode), another tile's files are served only
//     as sandboxed non-documents — a tile origin runs only its own tile's
//     documents, and cross-tile content must never execute as it;
//   - on the workspace origin (origins mode), a browser navigating to a
//     sandboxed tile's document is sent to the tile's origin with a fresh
//     frame token to exchange (direct-tab opens, old links, bx-frame's
//     fallback);
//   - on the workspace origin, a sandboxed tile's non-document files carry
//     CSP sandbox (inert if ignored as a subresource; scriptless if an SVG
//     or XML file is navigated to — it would otherwise run as the
//     workspace origin, beside the session cookie). PDFs are left alone:
//     browsers refuse to render them sandboxed and they cannot script.
func (s *Server) assetGate(w http.ResponseWriter, r *http.Request, owner string, comp *registry.Component, isDoc bool) bool {
	if t := tileOriginOf(r); t != "" {
		if owner != t {
			if isDoc {
				http.Error(w, "a tile origin serves only its own tile's documents", http.StatusForbidden)
				return true
			}
			w.Header().Set("Content-Security-Policy", "sandbox")
		}
		return false
	}
	if isChrome(owner) || !sandboxedFrame(owner, comp) {
		return false
	}
	if isDoc {
		return s.assetMode() == TileAssetsOrigins && s.redirectToTileOrigin(w, r, owner)
	}
	if !strings.HasPrefix(mime.TypeByExtension(path.Ext(r.URL.Path)), "application/pdf") {
		w.Header().Set("Content-Security-Policy", "sandbox")
	}
	return false
}

// --- tokens mode: the asset-token plane ---

// withAssetTokens routes /c/~<token>/… to the asset-token plane in tokens
// mode; in every other mode (and for every other path) it is next.
func (s *Server) withAssetTokens(next http.Handler) http.Handler {
	if s.assetMode() != TileAssetsTokens {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/c/~") {
			s.serveAssetToken(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// serveAssetToken serves GET /c/~<token>/<path>: a non-document static file
// of a tile the token's user can read — checked live, for the token's own
// tile and for the tile being loaded (cross-tile loads work exactly when the
// user can read the other tile). Never HTML, never a directory, never
// workspace chrome, never a document destination, never /api. Answers carry
// CSP sandbox and no-referrer, so a stylesheet's onward requests don't
// repeat the token to third parties.
func (s *Server) serveAssetToken(w http.ResponseWriter, r *http.Request) {
	tok, rel, ok := strings.Cut(strings.TrimPrefix(r.URL.Path, "/c/~"), "/")
	if !ok || tok == "" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "asset tokens are read-only", http.StatusMethodNotAllowed)
		return
	}
	g, ok := s.Auth.VerifyAssetToken(tok)
	if !ok {
		http.Error(w, "asset token invalid or expired — reload the tile (docs/elements.md#asset-urls)", http.StatusUnauthorized)
		return
	}
	_, cleaned, err := util.SafeJoin(s.Reg.Root, rel)
	if err != nil || !pathAllowed(cleaned) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	owner := s.owningComponent(cleaned)
	if isChrome(owner) {
		http.Error(w, "asset tokens never serve workspace chrome", http.StatusForbidden)
		return
	}
	if !s.Auth.UserCanReadTile(g.UserID, g.Tile) || !s.Auth.UserCanReadTile(g.UserID, owner) {
		http.Error(w, "not permitted to use this tile", http.StatusForbidden)
		return
	}
	if documentDest(r) || isHTMLName(cleaned) {
		http.Error(w, "documents never load through an asset token — link to them with a relative URL (xbin-client navigates it) or xbin.url()", http.StatusForbidden)
		return
	}
	f, fi, err := s.openStrict(owner, cleaned)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	if !fi.Mode().IsRegular() {
		http.Error(w, "documents never load through an asset token", http.StatusForbidden)
		return
	}
	h := w.Header()
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Content-Security-Policy", "sandbox")
	h.Set("Referrer-Policy", "no-referrer")
	http.ServeContent(w, r, cleaned, fi.ModTime(), f)
}

// assetHead is the strict modes' addition to the D4 injection, placed
// before the import map (the <base> must precede every URL in <head>):
//
//   - tokens: <base href="/c/~<tok>/<doc-dir>/" data-xbin-assets> and the
//     mode meta; imports is rewritten in place — every /c/ value moves under
//     the token and /c/<tile>/ itself is remapped, so absolute self-imports
//     in modules keep working. A document with its own <base> keeps its
//     choice: ours then points at the token form of that base when it is a
//     /c/ URL, and is left out when it points elsewhere.
//   - origins: the mode meta (only documents on their tile origin are
//     injected there; the cookie does the rest).
//   - legacy: "".
func (s *Server) assetHead(r *http.Request, body []byte, compPath, userID string, imports map[string]string) string {
	switch s.assetMode() {
	case TileAssetsOrigins:
		return "<meta name=\"xbin-tile-assets\" content=\"origins\">\n"
	case TileAssetsTokens:
	default:
		return ""
	}
	tok := s.Auth.MintAssetToken(compPath, userID)
	under := func(p string) string { return "/c/~" + tok + "/" + strings.TrimPrefix(p, "/c/") }
	for k, v := range imports {
		if strings.HasPrefix(v, "/c/") && !strings.HasPrefix(v, "/c/~") && !isChrome(firstSeg(v[3:])) {
			imports[k] = under(v)
		}
	}
	self := "/c/" + compPath + "/"
	if _, ok := imports[self]; !ok {
		imports[self] = under(self)
	}
	out := ""
	if base, ok := tokenBase(r, body); ok {
		out = "<base href=\"" + htmlEscape((&url.URL{Path: under(base)}).EscapedPath()) + "\" data-xbin-assets>\n"
	}
	return out + "<meta name=\"xbin-tile-assets\" content=\"tokens\">\n"
}

var baseTagRe = regexp.MustCompile(`(?is)<base\b[^>]*?\bhref\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s>]+))`)

// tokenBase returns the /c/ path the injected <base> must stand for: the
// document's directory, or — when the document declares its own <base> — the
// /c/ URL that base resolves to (ok=false when it points off /c/).
func tokenBase(r *http.Request, body []byte) (string, bool) {
	dir := r.URL.Path[:strings.LastIndexByte(r.URL.Path, '/')+1]
	m := baseTagRe.FindSubmatch(body)
	if m == nil {
		return dir, true
	}
	href := html.UnescapeString(string(m[1]) + string(m[2]) + string(m[3]))
	u, err := (&url.URL{Path: r.URL.Path}).Parse(href)
	if err != nil || u.Scheme != "" || u.Host != "" || !strings.HasPrefix(u.Path, "/c/") || strings.HasPrefix(u.Path, "/c/~") {
		return "", false
	}
	return u.Path, true
}

func firstSeg(p string) string {
	if i := strings.IndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// redactAssetToken hides an asset token in a logged /c/~<tok>/… path.
func redactAssetToken(p string) string {
	if !strings.HasPrefix(p, "/c/~") {
		return p
	}
	if i := strings.IndexByte(p[4:], '/'); i >= 0 {
		return "/c/~…" + p[4+i:]
	}
	return "/c/~…"
}
