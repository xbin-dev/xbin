package server

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/fsutil"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

const frameTokenTTL = 15 * time.Minute

// sandboxCSP confines a non-chrome tile document to an opaque origin
// (plans/auth.md §6, ND8): scripts run, forms/modals/downloads work, but
// never allow-same-origin — that plus allow-scripts would void the sandbox.
// Downloads are allowed (ND10): they cross no workspace/session/tile
// boundary and the browser's download UI mediates. This is the BASE list;
// a tile's grants may unlock more (ND11: cap:open-links → allow-popups
// allow-popups-to-escape-sandbox) through the SandboxExtras hook. Must
// match bx-frame's iframe sandbox attribute — browsers intersect the two —
// which is why bx-frame appends the extras /components reports rather than
// keeping a list of its own.
const sandboxCSP = "sandbox allow-scripts allow-forms allow-modals allow-downloads"

// sandboxHeader composes the CSP for one document: the base plus whatever
// its grants unlock.
func sandboxHeader(extras []string) string {
	if len(extras) == 0 {
		return sandboxCSP
	}
	return sandboxCSP + " " + strings.Join(extras, " ")
}

// sandboxExtras: the Policy's extra sandbox tokens for a component.
func (s *Server) sandboxExtras(comp string) []string { return s.policy().SandboxExtras(comp) }

// sandboxDocument sets the CSP sandbox header on a non-chrome document (the
// one place both emission sites go through) and reports whether it did.
// Never Cross-Origin-Opener-Policy here: a top-level response with a
// sandboxed origin AND a COOP other than unsafe-none is a network error per
// the HTML spec, which would break every direct-tab open of /c/<tile>/.
// Opener severing lives on the popup targets instead (chrome pages and
// /docs/ send COOP; tile authors use rel="noopener").
func (s *Server) sandboxDocument(w http.ResponseWriter, r *http.Request, compPath string, comp *registry.Component) bool {
	if !sandboxedFrame(compPath, comp) {
		return false
	}
	s.setDocCSP(w, r, sandboxHeader(s.docSandboxExtras(r, compPath)))
	return true
}

// handleComponentStatic serves /c/<component-path>/<file> from the workspace.
// HTML responses get the single sanctioned transform (decision D4): the merged
// import map, component identity meta tags, a frame token, and the
// xbin-client module are injected into <head>. Everything else is served
// byte-exact. Cache-Control is no-store throughout: this is a live system.
func (s *Server) handleComponentStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/c/")
	_, cleaned, err := util.SafeJoin(s.Reg.Root, rel)
	if err != nil || !pathAllowed(cleaned) {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}

	// Tile-level RBAC: a user may load only tiles they can read (D16 — read is
	// the visibility level). Chrome (root, shell) is always viewable; the shell
	// then shows only the tiles the user may see. A signed-in HUMAN navigating
	// to an unreadable tile gets a request-access page instead of a bare 403
	// (D36) — the tile exists (they have the link), so hiding it buys nothing
	// and "ask an admin out of band" was the review's biggest sharing gap.
	//
	// A sandboxed tile frame's SUBRESOURCE loads (module scripts, CSS, images)
	// arrive with NO credential at all: opaque origins strip cookies (both
	// directions, verified in Chromium) and the Referer downgrades to nothing
	// (strict-origin-when-cross-origin against an unserializable origin), and
	// headers can't be attached to tag loads anyway. So they're authorized by
	// the one signal the browser still produces: the opaque-origin
	// Fetch-Metadata fingerprint (see tileSubresource).
	//
	// An element principal holding a code[:<owner>] grant reads sibling
	// source here too (the grant's whole point — tooling backends fetching
	// files); the 2026-08-02 element read clamp governs everything else.
	// Note the D4 injection mints a frame token only for a human or the tile
	// itself (mayMintFrameToken), so element reads — code grants, a user's
	// RBAC through another tile's frame token — never leak the OTHER tile's
	// credential.
	//
	// Strict asset gating (--tile-assets=tokens|origins, tileassets.go) has
	// no credential-less path at all, re-checks the DRIVING USER's live
	// access for a tile's own frame principal too, and serves through
	// serveStrictStatic (no symlink leaves the tile).
	owner := s.owningComponent(cleaned)
	if !isChrome(owner) {
		if p := auth.PrincipalOf(r); (!p.CanReadTile(owner) && !s.codeGranted(p, owner) && !s.tileSubresourceAuthed(r)) || !s.strictLiveRead(p, owner) {
			if p.User != nil && p.Component == "" && strings.Contains(r.Header.Get("Accept"), "text/html") {
				s.serveRequestAccessPage(w, owner)
				return
			}
			http.Error(w, "not permitted to use this tile", http.StatusForbidden)
			return
		}
	}
	// A native runtime document (?native=1 on a tile's directory URL) is
	// generated, not a file — authorized above exactly like index.html, in
	// every asset mode.
	if nativeRuntimeRequest(r) && s.serveNativeRoute(w, r, cleaned) {
		return
	}
	if s.strictAssets() {
		s.serveStrictStatic(w, r, cleaned, owner)
		return
	}

	f, fi, err := s.openLegacy(cleaned)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	name, dirIndex := cleaned, false
	if fi.IsDir() {
		f.Close()
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		dirIndex = true
		// `cleaned` stays the directory: it is the component path the
		// injection attributes a not-yet-scanned component to.
		name = path.Join(cleaned, "index.html")
		if f, fi, err = s.openLegacy(name); err != nil || !fi.Mode().IsRegular() {
			if err == nil {
				f.Close()
			}
			http.NotFound(w, r)
			return
		}
	}
	defer f.Close()

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	comp, _, _ := s.Reg.Resolve(cleaned)
	if isHTMLName(name) {
		if comp == nil || comp.Manifest.Inject == nil || *comp.Manifest.Inject {
			body, err := io.ReadAll(f)
			if err != nil {
				http.NotFound(w, r)
				return
			}
			s.injectHTML(w, r, body, comp, cleaned, dirIndex)
			return
		}
		// inject:false serves byte-exact, but non-chrome HTML is confined
		// regardless (ND8) — bx-frame sandboxes it when framed; this header
		// covers direct-tab opens. Without injection it holds no frame token
		// either, so its frontend has no identity at all.
		s.sandboxDocument(w, r, s.owningComponent(cleaned), comp)
		if strings.HasSuffix(r.URL.Path, "/index.html") { // as http.ServeFile always did
			localRedirect(w, r, "./")
			return
		}
	} else {
		s.inertNonDocument(w, r, owner, comp)
	}
	http.ServeContent(w, r, path.Base(name), fi.ModTime(), f)
}

// openLegacy opens a file for the legacy /c/ plane — the dev overlay's copy
// first (a trusted source tree: a scaffold file added there shows up without
// re-initialising the dev workspace; never manifests, the registry reads the
// real tree) — through fsutil.OpenResolved: symlinks between tiles keep
// working, but the resolved target must stay inside the workspace and
// outside its reserved trees (.xbin holds the HMAC secret and owner token;
// data/ and homes/ every tile's state), and what is served is the very file
// that was checked. Tile directories are written by sandboxes: a symlink
// there (../../.xbin/secret), even one swapped in while a request is in
// flight, never points xbind at its own credentials; a FIFO never blocks it.
// The strict modes go further (openStrict: nothing leaves the tile).
func (s *Server) openLegacy(cleaned string) (*os.File, os.FileInfo, error) {
	open := func(root string, allow func(string) bool) (*os.File, os.FileInfo, error) {
		f, _, err := fsutil.OpenResolved(root, cleaned, allow)
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
		case "xbin.json", "scope.json":
		default:
			if f, fi, err := open(s.Overlay, nil); err == nil {
				if fi.Mode().IsRegular() {
					return f, fi, nil
				}
				f.Close()
			}
		}
	}
	return open(s.Reg.Root, pathAllowed)
}

// localRedirect is net/http's (unexported) relative redirect, which
// http.ServeFile answers …/index.html with.
func localRedirect(w http.ResponseWriter, r *http.Request, newPath string) {
	if q := r.URL.RawQuery; q != "" {
		newPath += "?" + q
	}
	w.Header().Set("Location", newPath)
	w.WriteHeader(http.StatusMovedPermanently)
}

// inertNonDocument: a sandboxed tile's non-document file served on the
// workspace origin carries CSP sandbox, in every mode. As a subresource
// (script, style, image, font, fetch) the header is ignored; navigated to —
// an SVG or XML file, an .xhtml/.shtml page, an extensionless file sniffed
// as HTML — it renders scriptless in an opaque origin instead of running
// tile-written script as the workspace origin, beside the session cookie.
// PDFs are left alone: browsers refuse to render them sandboxed and they
// cannot script. Chrome is trusted and never gated.
func (s *Server) inertNonDocument(w http.ResponseWriter, r *http.Request, owner string, comp *registry.Component) {
	if tileOriginOf(r) != "" || !sandboxedFrame(owner, comp) {
		return
	}
	if strings.HasPrefix(mime.TypeByExtension(path.Ext(r.URL.Path)), "application/pdf") {
		return
	}
	w.Header().Set("Content-Security-Policy", "sandbox")
}

// codeGranted reports whether an element principal holds a code[:<target>]
// source-read grant on target (Policy.CodeReadGrant). Element principals
// only — humans use their per-tile RBAC (CanReadTile).
func (s *Server) codeGranted(p auth.Principal, target string) bool {
	return p.Component != "" && s.policy().CodeReadGrant(p.Component, target)
}

// tileSubresource reports whether r is a credential-less subresource load
// from a sandboxed tile frame, authorized by the opaque-origin
// Fetch-Metadata fingerprint instead of credentials: a GET/HEAD for a
// non-HTML file with Sec-Fetch-Site cross-site (same-site accepted for
// engines that compute it differently — neither can be produced by
// unsandboxed same-origin JS, which always yields same-origin) and a
// genuine subresource destination — module scripts, styles, images, fonts,
// media, workers: the loads a tile page performs but cannot attach
// credentials to. Documents, frames, and fetch()/XHR (Dest: empty) are
// excluded: HTML navigates with the cookie or bootstrap token, and
// xbin.fetch carries the frame token.
//
// Honest scope: the URL names the tile, not the requester — any sandboxed
// tile can tag-load (execute/render, not fetch-read) another's assets, and
// headers are client-settable, so the fingerprint alone is spoofable by any
// NON-browser client. That's why serving additionally requires a
// recently-authenticated source IP (tileSubresourceAuthed): drive-by
// scanners with no login get 401, and only a client sharing an egress IP
// with a real signed-in session can read tile source this way. This
// confines tile JS; it is not a substitute for the vault. Never put secrets
// in source (D30).

// tileSubresourceAuthed is the /c/ credential-less exception in full: the
// Fetch-Metadata subresource fingerprint AND a recently-authenticated
// source IP (auth.RecentlyAuthed). Used identically by authedStatic
// (admission) and handleComponentStatic (authorization) so the two never
// disagree.
//
// Strict asset gating (--tile-assets=tokens|origins) has no such
// exception: every /c/ request carries a credential.
func (s *Server) tileSubresourceAuthed(r *http.Request) bool {
	return !s.strictAssets() && tileSubresource(r) && s.Auth.RecentlyAuthed(s.ClientIP(r))
}

var tileSubresourceDests = map[string]bool{
	"script": true, "style": true, "image": true, "font": true,
	"audio": true, "video": true, "track": true, "worker": true,
	"manifest": true,
}

func tileSubresource(r *http.Request) bool {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		return false
	}
	// HTML documents are never subresources — they navigate (Dest:
	// document/iframe, excluded below) with the cookie or bootstrap token.
	if isHTMLName(r.URL.Path) {
		return false
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
	default:
		return false
	}
	return tileSubresourceDests[r.Header.Get("Sec-Fetch-Dest")]
}

// sandboxedFrame reports whether a component's documents run in a sandboxed
// opaque origin (plans/auth.md §6): everything except implicit chrome
// (root, shell — they ARE the workspace UI) and components whose manifest
// carries the host-set trust flag `chrome: true`.
func sandboxedFrame(compPath string, comp *registry.Component) bool {
	if isChrome(compPath) {
		return false
	}
	if comp != nil && comp.Manifest.Chrome {
		return false
	}
	return true
}

// owningComponent returns the registered component that owns a /c/ path (its
// longest registered prefix), falling back to the first path segment when
// nothing is registered yet (a just-created dir before rescan).
func (s *Server) owningComponent(cleaned string) string {
	if c, _, ok := s.Reg.Resolve(cleaned); ok {
		return c.Path
	}
	if i := strings.IndexByte(cleaned, '/'); i >= 0 {
		return cleaned[:i]
	}
	return cleaned
}

// isChrome reports workspace-chrome components that every authenticated user
// may load (the shell frame itself); tile-access RBAC applies to the rest.
func isChrome(path string) bool {
	return path == "root" || path == "shell"
}

// pathAllowed blocks serving reserved trees and internals through /c/.
func pathAllowed(cleaned string) bool {
	if cleaned == "" {
		return false
	}
	first := strings.Split(cleaned, "/")[0]
	if util.ReservedTop[first] {
		return false
	}
	for _, part := range strings.Split(cleaned, "/") {
		if part == ".git" || part == ".xbin" {
			return false
		}
	}
	return true
}

var headRe = regexp.MustCompile(`(?i)<head[^>]*>`)

// injectHTML writes body — a document read through the plane's own opener
// (openLegacy, openStrict) — with the D4 injection.
func (s *Server) injectHTML(w http.ResponseWriter, r *http.Request, body []byte, comp *registry.Component, cleaned string, dirIndex bool) {
	compPath := ""
	switch {
	case comp != nil:
		compPath = comp.Path
	case dirIndex:
		// Not (yet) in the registry — a just-created dir before the rescan
		// lands. The directory itself is the component.
		compPath = cleaned
	default:
		// A bare .html file: attribute to its directory.
		compPath = strings.TrimSuffix(cleaned, "/"+filepath.Base(cleaned))
	}

	inject := s.headInjection(r, comp, compPath, body)

	var out []byte
	if loc := headRe.FindIndex(body); loc != nil {
		out = append(out, body[:loc[1]]...)
		out = append(out, []byte(inject)...)
		out = append(out, body[loc[1]:]...)
	} else {
		out = append([]byte(inject), body...)
	}

	s.documentHeaders(w, r, compPath, comp)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
}

// documentHeaders sets the headers of an injected document of compPath (a
// tile page or its native runtime document): the content type, and the
// sandbox — or, for trusted chrome, COOP (and, in origins mode,
// frame-ancestors).
func (s *Server) documentHeaders(w http.ResponseWriter, r *http.Request, compPath string, comp *registry.Component) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Browser-plane isolation (plans/auth.md §6): a non-chrome document runs
	// in an opaque origin — no parent/sibling DOM access, no storage, no
	// ambient credentials on subresources; its only credential is the injected
	// frame token. Delivered as a header (not just the iframe attribute) so
	// direct-tab opens of /c/<tile>/ are confined identically, with the same
	// grant-unlocked extras (ND11).
	if !s.sandboxDocument(w, r, compPath, comp) {
		// Trusted chrome: keep popups it opens (full-page tile views, docs)
		// in its own browsing-context group.
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		// Origins mode: tile origins are same-site, so their frames of the
		// shell would carry the session cookie in browsers without Fetch
		// Metadata (tilenav.go drops it where they send it) — chrome is
		// framed only by the workspace itself.
		if s.assetMode() == TileAssetsOrigins {
			w.Header().Set("Content-Security-Policy", "frame-ancestors 'self'")
		}
	}
}

// headInjection is the D4 <head> block of one of compPath's documents
// (body: the document it goes into — a tile page, or nil for the generated
// native runtime document): strict asset gating's head (assetHead), the
// merged import map, the component and frame-token metas (a token only for
// a human or the tile itself that may read it — mayMintFrameToken — bound
// to the login that opened it), bound interfaces, the sandbox token list,
// the WebSocket origin for app WebViews (appWSOriginMeta), and the
// xbin-client module.
func (s *Server) headInjection(r *http.Request, comp *registry.Component, compPath string, body []byte) string {
	imports := s.Reg.ImportMapFor(comp)

	frameTok, assetHead := "", ""
	if p := auth.PrincipalOf(r); s.mayMintFrameToken(r, p, compPath) {
		frameTok = s.Auth.MintFrameTokenFor(p, compPath, frameTokenTTL) // bound to p's login (frametoken.go)
		// Strict asset gating: tokens mode's <base> + import-map remap
		// (which rewrites imports in place), origins mode's mode meta;
		// "" in legacy, so the injection below is byte-for-byte unchanged.
		assetHead = s.assetHead(r, body, compPath, comp, p, imports)
	}
	im, _ := json.Marshal(map[string]any{"imports": imports})

	ifaceMeta := ""
	if ifaces := s.policy().Interfaces(compPath); len(ifaces) > 0 {
		j, _ := json.Marshal(ifaces)
		ifaceMeta = fmt.Sprintf("<meta name=\"xbin-interfaces\" content=\"%s\">\n", htmlEscape(string(j)))
	}

	// The sandbox this document runs in (full token list; absent for chrome)
	// — the injected client reads it to tell "unsandboxed" from "sandboxed
	// without popups" and say which grant a blocked target=_blank needs.
	sandboxMeta := ""
	if sandboxedFrame(compPath, comp) {
		tokens := strings.TrimPrefix(sandboxHeader(s.docSandboxExtras(r, compPath)), "sandbox ")
		sandboxMeta = fmt.Sprintf("<meta name=\"xbin-sandbox\" content=\"%s\">\n", htmlEscape(tokens))
	}

	return fmt.Sprintf(
		"\n%s<script type=\"importmap\">%s</script>\n"+
			"<meta name=\"xbin-component\" content=\"%s\">\n"+
			"<meta name=\"xbin-frame-token\" content=\"%s\">\n"+
			"%s%s%s"+
			"<script type=\"module\" src=\"/vendor/xbin-client.js\"></script>\n",
		assetHead, im, htmlEscape(compPath), frameTok, ifaceMeta, sandboxMeta, appWSOriginMeta(r))
}

// mayMintFrameToken: the injection mints compPath's frame token only for a
// principal that may read the tile AND is not another tile — a human
// (cookie, bearer) or the tile itself (its own frame or terminal principal,
// including an xbin.window sub-path token like apps/x/editor, whose owning
// component is apps/x) — and never for a backend (backendPrincipal).
// Without the second half, any tile's
// frontend could xbin.fetch('/c/<other>/') — or '/c/<other>/?native=1', a
// native runtime document, which exists even for inject:false tiles and
// tiles with no index.html — and lift the other tile's token out of the
// HTML whenever its user can read that tile — e.g. the admin tile's
// (xbin:admin grant) from any tile an admin opens. Element
// principals reading other tiles' documents (code grants, the user's RBAC)
// get the HTML without a token, as code-grant reads always did.
//
// One exception: a NAVIGATION within one tile tree (sameTileTree) — a
// multi-page tile moving its frame between its own pages when a sub-page
// directory holding index.html is registered as a nested component of its
// own (settings/ → apps/a/settings). The initiator cannot read a document it
// navigates to (an opaque or foreign origin), so there is no token to lift;
// across trees it stays refused.
func (s *Server) mayMintFrameToken(r *http.Request, p auth.Principal, compPath string) bool {
	if backendPrincipal(p) || !p.CanReadTile(compPath) {
		return false
	}
	if p.Component == "" || p.Component == compPath {
		return true
	}
	own := s.owningComponent(p.Component)
	return own == compPath || (isNavigation(r) && s.sameTileTree(own, compPath))
}

// backendPrincipal: a tile's backend — its instance token, or a cron or bus
// delivery acting for it: an element principal with no person behind it.
// Its own reach is self-only (tileLevel), but a frame or asset token minted
// for it would name no user and so read as an owner-driven frame — owner
// reach on every tile. A backend has no document to put a token in: it
// never gets one (the <head> injection, /api/xbin/frame-token).
func backendPrincipal(p auth.Principal) bool {
	return p.Component != "" && p.Via != "frame" && p.Via != "terminal"
}

// topTile is the outermost registered component on p's path (p's own first
// segment when none is registered): the root of the directory tree whose
// writers can write everything under it, nested components included.
func (s *Server) topTile(p string) string {
	segs := strings.Split(strings.Trim(p, "/"), "/")
	for i := 1; i <= len(segs); i++ {
		if _, ok := s.Reg.Component(strings.Join(segs[:i], "/")); ok {
			return strings.Join(segs[:i], "/")
		}
	}
	return segs[0]
}

// sameTileTree: a and b are components of one tile tree (never chrome).
func (s *Server) sameTileTree(a, b string) bool {
	return !isChrome(a) && !isChrome(b) && s.topTile(a) == s.topTile(b)
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")
	return r.Replace(s)
}

// handleVendor serves core elements (web/*) and vendored deps (web/vendor/*)
// under one /vendor/ prefix, so import maps and element imports have a single
// stable root.
func (s *Server) handleVendor(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/vendor/")
	if name == "" || strings.Contains(name, "..") {
		http.NotFound(w, r)
		return
	}
	for _, p := range []string{name, "vendor/" + name} {
		b, err := fs.ReadFile(s.WebFS, p)
		if err != nil {
			continue
		}
		ct := mime.TypeByExtension(filepath.Ext(p))
		if ct == "" {
			ct = "application/octet-stream"
		}
		w.Header().Set("Content-Type", ct)
		w.Header().Set("Cache-Control", "no-cache") // revalidate; vendor changes on upgrade
		w.Header().Set("X-Content-Type-Options", "nosniff")
		_, _ = w.Write(b)
		return
	}
	http.NotFound(w, r)
}

// handleDocs serves the embedded builder docs. Markdown files are wrapped in
// a small client-side viewer; ?raw=1 (and non-browser Accept) returns bytes.
func (s *Server) handleDocs(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/docs/")
	if rel == "" {
		rel = "index.md"
	}
	b, err := fs.ReadFile(s.DocsFS, rel)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if strings.HasSuffix(rel, ".md") && r.URL.Query().Get("raw") == "" &&
		strings.Contains(r.Header.Get("Accept"), "text/html") {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		// The popup target of every shipped tile's links (ND11): its own
		// browsing-context group, so a tile that opened it keeps no handle.
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
		fmt.Fprintf(w, docViewerHTML, htmlEscape(rel))
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

const docViewerHTML = `<!doctype html><html><head><meta charset="utf-8">
<title>xbin docs — %[1]s</title>
<link rel="icon" type="image/svg+xml" href="/vendor/favicon.svg">
<link rel="stylesheet" href="/vendor/theme.css">
<style>body{max-width:52rem;margin:1.5rem auto;padding:0 1rem;font:14px/1.65 -apple-system,"Segoe UI",system-ui,sans-serif;color:var(--bx-text,#33414e);background:var(--bx-panel,#fff)}
pre{background:var(--bx-panel-2,#f7f8fa);border:1px solid var(--bx-border,#e4e8ed);padding:.7rem .9rem;border-radius:6px;overflow-x:auto;font-size:12px;line-height:1.55}
code{background:var(--bx-panel-2,#f7f8fa);border:1px solid var(--bx-border,#e4e8ed);padding:0 .3em;border-radius:3px;font-size:12px}
pre code{padding:0;border:0;background:none}table{border-collapse:collapse;font-size:13px}td,th{border:1px solid var(--bx-border,#e4e8ed);padding:.25em .6em}
th{background:var(--bx-panel-2,#f7f8fa);text-align:left}a{color:var(--bx-accent,#f5a623);text-decoration:none}a:hover{text-decoration:underline}
h1,h2,h3{line-height:1.25}h1{font-size:1.5rem}h2{font-size:1.15rem;margin-top:2rem}h3{font-size:1rem}
.crumb{font-size:12px;color:var(--bx-muted,#8794a1)}</style></head><body>
<p class="crumb"><a href="/docs/index.md">← docs index</a></p><div id="doc">loading…</div>
<script type="module">
import {marked} from '/vendor/marked.esm.js';
const md = await (await fetch('/docs/%[1]s?raw=1')).text();
document.getElementById('doc').innerHTML = marked.parse(md);
</script></body></html>`
