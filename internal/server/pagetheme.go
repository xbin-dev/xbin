package server

// pagetheme.go — xbind's own server-rendered pages on the Base Two product
// tokens (D184): the sign-in, invite and "Continue as" pages, the request-
// access page, the partition switch page, the tile-navigation hop and the
// tile-origin refusal. None of them is a tile document, so none gets the D4
// injection (static.go); each opts in itself:
//
//   - <html data-bx-theme="auto">: the page follows the person's theme
//     (/vendor/theme.css turns a document light only when it opts in);
//   - <meta name="xbin-theme" content="light|dark"> from the xbin_theme
//     hint cookie, which /vendor/bx-theme.js keeps equal to the person's
//     choice in this browser — before sign-in it is all there is. Exactly
//     "light" or "dark"; anything else (or none) writes no meta and the page
//     follows the system. A UI hint, never a credential: the page only ever
//     writes one of two constant strings;
//   - the /vendor/theme.css link, after the meta, so the right theme is
//     there at first paint (its :has() rules read the meta).
//
// They look like the product's Work volume (brand §12): concrete and ink, a
// plate (pageCSS), the workspace's branding (D76) or the one-colour
// wordmark "xbin", no fields, tokens only. Controls come from theme.css's
// .bx rules (product-ui §6: 28 px, border-strong, the focus ring; a primary
// button in the accent with its ink). Status is an icon, words and colour
// (R2): the glyphs are /vendor/bx-icons.js's, inline, since these pages run
// no module (TestPageIconsMatchBxIcons keeps the copies in step).
//
// A page with a CSP allows styles and fonts from 'self' for the sheet and
// its fonts ('self' is the URL's origin, also in a sandboxed page); script
// and frame rules are as they were.

import (
	"net/http"
	"strings"
)

// themeCookie is the appearance hint cookie (D184; docs/protocol.md).
const themeCookie = "xbin_theme"

// pageTheme is the person's theme as the hint cookie in r says it: "light",
// "dark", or "" (follow the system) for no cookie or any other value. It
// reads the header as /vendor/theme-boot.js reads document.cookie: the
// first xbin_theme whose value is exactly light or dark (no unquoting), so
// a page rendered here and one booted there agree.
func pageTheme(r *http.Request) string {
	for _, line := range r.Header.Values("Cookie") {
		for _, pair := range strings.Split(line, ";") {
			name, v, ok := strings.Cut(strings.TrimLeft(pair, " \t"), "=")
			if ok && name == themeCookie && (v == "light" || v == "dark") {
				return v
			}
		}
	}
	return ""
}

// themeHead is what a page puts in its <head>: the theme meta (when the
// cookie names one) and the theme.css link.
func themeHead(r *http.Request) string {
	meta := ""
	if t := pageTheme(r); t != "" {
		meta = `<meta name="xbin-theme" content="` + t + `">` + "\n"
	}
	return meta + `<link rel="stylesheet" href="/vendor/theme.css">`
}

// themed fills a page template's {{THEME}} with r's theme head.
func themed(page string, r *http.Request) string {
	return strings.Replace(page, "{{THEME}}", themeHead(r), 1)
}

// pageOpen starts a page: the doctype, <html> opted in, the charset and
// viewport. A template continues with {{THEME}}, its <title> and its
// <style> (pageCSS plus its own rules).
const pageOpen = `<!doctype html>
<html lang="en" data-bx-theme="auto"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
`

// pageCSS is the pages' shared layout, on tokens only: the concrete page,
// the plate (an ink rule on top, a hairline under the logo), the logo, the
// body type, status alerts and icons. Controls are theme.css's .bx rules.
const pageCSS = `
body.bx{box-sizing:border-box;min-height:100vh;margin:0;padding:16px;display:flex;align-items:center;justify-content:center;
  background:var(--bx-bg);color:var(--bx-text);font:var(--bx-font)}
.plate{box-sizing:border-box;width:min(360px,100%);margin:0;background:var(--bx-panel);
  border:1px solid var(--bx-border-strong);border-top:3px solid var(--bx-text);border-radius:var(--bx-radius)}
.plate.wide{width:min(480px,100%)}
.logo{display:flex;align-items:center;gap:12px;margin:0;padding:14px 20px;border-bottom:1px solid var(--bx-border);
  color:var(--bx-text);font:var(--bx-font-hero);letter-spacing:var(--bx-tracking-hero);overflow-wrap:anywhere}
.logo img.mark{flex:none;width:32px;height:32px;object-fit:contain;border-radius:var(--bx-radius)}
.notice .logo{padding:12px 20px;font:var(--bx-font-heading);font-weight:800}
.main{padding:16px 20px 20px}
.main>:first-child{margin-top:0}
.main>:last-child{margin-bottom:0}
h1{margin:0 0 8px;font:var(--bx-font-title);overflow-wrap:anywhere}
h1.status{display:flex;align-items:flex-start;gap:8px}
h1.status svg.ico{margin-top:3px}
p{margin:8px 0;overflow-wrap:anywhere}
.muted{color:var(--bx-muted)}
a{color:var(--bx-link)}
b,strong{font-weight:600}
code{padding:0 4px;background:var(--bx-code-bg);border:1px solid var(--bx-border);border-radius:var(--bx-radius);
  color:var(--bx-text);font:var(--bx-font-code);overflow-wrap:anywhere}
label{display:block;margin:12px 0 4px;font:var(--bx-font-ui);font-weight:600}
.bx input:not([type=hidden]),.bx select{width:100%}
.bx button{width:100%;margin-top:16px}
svg.ico{flex:none;width:16px;height:16px}
.ico.error{color:var(--bx-danger)}
.ico.warning{color:var(--bx-warn)}
.ico.ok{color:var(--bx-ok)}
.ico.info{color:var(--bx-info)}
.alert{display:flex;align-items:flex-start;gap:8px;margin:12px 0;padding:8px 10px;border-radius:var(--bx-radius);
  background:var(--bx-info-bg);color:var(--bx-text)}
.alert[hidden]{display:none}
.alert svg.ico{margin-top:1px}
.alert.error{background:var(--bx-danger-bg)}
.alert.warning{background:var(--bx-warn-bg)}
.alert.ok{background:var(--bx-ok-bg)}
`

// The status glyphs (brand §4.6: ok a check in a square, warning a
// triangle, error an octagon, info an i in a square), drawn as
// /vendor/bx-icons.js draws them, for these script-less pages.
const (
	icoAttrs = `viewBox="0 0 16 16" width="16" height="16" focusable="false" fill="none" stroke="currentColor" ` +
		`stroke-width="1.5" stroke-linecap="square" stroke-linejoin="miter" stroke-miterlimit="10"`
	glyphError   = `<path d="M5.25 1.75h5.5l3.5 3.5v5.5l-3.5 3.5h-5.5l-3.5-3.5v-5.5zM4.75 7.75h6"/>`
	glyphWarning = `<path d="M7.75 2L14.25 13.25H1.25zM7.75 6.25v3.25"/><rect x="7" y="10.5" width="1.5" height="1.5" fill="currentColor" stroke="none"/>`
	glyphOK      = `<path d="M2.75 2.75h10.5v10.5h-10.5zM5 7.75l2 2 4-4"/>`
	glyphInfo    = `<path d="M2.75 2.75h10.5v10.5h-10.5zM7.75 7v4.25"/><rect x="7" y="4.25" width="1.5" height="1.5" fill="currentColor" stroke="none"/>`
)

// The glyphs as inline SVG in the currentColor of their status (pageCSS
// .ico.<kind>), each named for a screen reader: the status word.
const (
	svgError   = `<svg class="ico error" role="img" aria-label="Error" ` + icoAttrs + `>` + glyphError + `</svg>`
	svgWarning = `<svg class="ico warning" role="img" aria-label="Warning" ` + icoAttrs + `>` + glyphWarning + `</svg>`
	svgOK      = `<svg class="ico ok" role="img" aria-label="Done" ` + icoAttrs + `>` + glyphOK + `</svg>`
	svgInfo    = `<svg class="ico info" role="img" aria-label="Note" ` + icoAttrs + `>` + glyphInfo + `</svg>`
)

var statusIcons = map[string]string{"error": svgError, "warning": svgWarning, "ok": svgOK, "info": svgInfo}

// notePage is a page of one plate: the brand (its title alone — these pages
// load no image, and the hop shouldn't carry an icon's bytes) over body,
// HTML the caller escaped. head opens <head> (a refresh, or "").
func (s *Server) notePage(r *http.Request, head, body string) string {
	b := s.brand()
	return pageOpen + head + themeHead(r) + "\n<title>" + pageTitle(b) + "</title>\n<style>" + pageCSS + "</style></head><body class=\"bx\">\n" +
		`<div class="plate notice"><div class="logo">` + brandLogo(b, false) + `</div><div class="main">` + body + "</div></div></body></html>"
}

// pageAlert is a status line: kind's icon (error, warning, ok, info) and
// msg (HTML the caller has escaped) on kind's tint. An error or a warning
// is announced (role alert), the others politely (role status).
func pageAlert(kind, msg string) string {
	role := "status"
	if kind == "error" || kind == "warning" {
		role = "alert"
	}
	return `<div class="alert ` + kind + `" role="` + role + `">` + statusIcons[kind] + `<span>` + msg + `</span></div>`
}
