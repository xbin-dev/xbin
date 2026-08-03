package server

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/util"
)

const frameTokenTTL = 15 * time.Minute

// sandboxCSP confines a non-chrome tile document to an opaque origin
// (plans/auth.md §6, ND8): scripts run, forms/modals work, but never
// allow-same-origin — that plus allow-scripts would void the sandbox.
const sandboxCSP = "sandbox allow-scripts allow-forms allow-modals"

// handleComponentStatic serves /c/<component-path>/<file> from the workspace.
// HTML responses get the single sanctioned transform (decision D4): the merged
// import map, component identity meta tags, a frame token, and the
// xbin-client module are injected into <head>. Everything else is served
// byte-exact. Cache-Control is no-store throughout: this is a live system.
func (s *Server) handleComponentStatic(w http.ResponseWriter, r *http.Request) {
	rel := strings.TrimPrefix(r.URL.Path, "/c/")
	full, cleaned, err := util.SafeJoin(s.Reg.Root, rel)
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
	if owner := s.owningComponent(cleaned); !isChrome(owner) {
		if p := auth.PrincipalOf(r); !p.CanReadTile(owner) && !tileSubresource(r) {
			if p.User != nil && p.Component == "" && strings.Contains(r.Header.Get("Accept"), "text/html") {
				s.serveRequestAccessPage(w, owner)
				return
			}
			http.Error(w, "not permitted to use this tile", http.StatusForbidden)
			return
		}
	}

	fi, err := os.Stat(full)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	dirIndex := false
	if fi.IsDir() {
		if !strings.HasSuffix(r.URL.Path, "/") {
			http.Redirect(w, r, r.URL.Path+"/", http.StatusMovedPermanently)
			return
		}
		dirIndex = true
		full = filepath.Join(full, "index.html")
		if _, err := os.Stat(full); err != nil {
			http.NotFound(w, r)
			return
		}
	}

	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")

	if strings.HasSuffix(full, ".html") || strings.HasSuffix(full, ".htm") {
		comp, _, _ := s.Reg.Resolve(cleaned)
		if comp == nil || comp.Manifest.Inject == nil || *comp.Manifest.Inject {
			s.serveInjectedHTML(w, r, full, comp, cleaned, dirIndex)
			return
		}
		// inject:false serves byte-exact, but non-chrome HTML is confined
		// regardless (ND8) — bx-frame sandboxes it when framed; this header
		// covers direct-tab opens. Without injection it holds no frame token
		// either, so its frontend has no identity at all.
		if owner := s.owningComponent(cleaned); sandboxedFrame(owner, comp) {
			w.Header().Set("Content-Security-Policy", sandboxCSP)
		}
	}
	http.ServeFile(w, r, full)
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
// headers are client-settable, so a determined NON-browser client can spoof
// this to read tile source under /c/. This confines tile JS; it is not a
// substitute for the vault. Never put secrets in source (D30).
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
	if strings.HasSuffix(r.URL.Path, ".html") || strings.HasSuffix(r.URL.Path, ".htm") {
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

func (s *Server) serveInjectedHTML(w http.ResponseWriter, r *http.Request, file string, comp *registry.Component, cleaned string, dirIndex bool) {
	body, err := os.ReadFile(file)
	if err != nil {
		http.NotFound(w, r)
		return
	}

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

	imports := s.Reg.ImportMapFor(comp)
	im, _ := json.Marshal(map[string]any{"imports": imports})

	frameTok := ""
	if p := auth.PrincipalOf(r); p.CanReadTile(compPath) {
		frameTok = s.Auth.MintFrameToken(compPath, p.UserID, frameTokenTTL)
	}

	ifaceMeta := ""
	if s.Interfaces != nil {
		if ifaces := s.Interfaces(compPath); len(ifaces) > 0 {
			j, _ := json.Marshal(ifaces)
			ifaceMeta = fmt.Sprintf("<meta name=\"xbin-interfaces\" content=\"%s\">\n", htmlEscape(string(j)))
		}
	}

	inject := fmt.Sprintf(
		"\n<script type=\"importmap\">%s</script>\n"+
			"<meta name=\"xbin-component\" content=\"%s\">\n"+
			"<meta name=\"xbin-frame-token\" content=\"%s\">\n"+
			"%s"+
			"<script type=\"module\" src=\"/vendor/xbin-client.js\"></script>\n",
		im, htmlEscape(compPath), frameTok, ifaceMeta)

	var out []byte
	if loc := headRe.FindIndex(body); loc != nil {
		out = append(out, body[:loc[1]]...)
		out = append(out, []byte(inject)...)
		out = append(out, body[loc[1]:]...)
	} else {
		out = append([]byte(inject), body...)
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if sandboxedFrame(compPath, comp) {
		// Browser-plane isolation (plans/auth.md §6): the document runs in an
		// opaque origin — no parent/sibling DOM access, no storage, no ambient
		// credentials on subresources; its only credential is the injected
		// frame token. Delivered as a header (not just the iframe attribute)
		// so direct-tab opens of /c/<tile>/ are confined identically.
		w.Header().Set("Content-Security-Policy", sandboxCSP)
	} else {
		// Trusted chrome: keep popups it opens (full-page tile views, docs)
		// in its own browsing-context group.
		w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	}
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(out)
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
