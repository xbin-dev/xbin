package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/branding"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers D184 — the hint cookie names a theme only when its value is
// exactly light or dark, read as /vendor/theme-boot.js reads it: the first
// xbin_theme with such a value, never unquoted, never another cookie's.
func TestPageThemeCookie(t *testing.T) {
	for raw, want := range map[string]string{
		"":                                   "",
		"xbin_theme=light":                   "light",
		"xbin_theme=dark":                    "dark",
		"a=1; xbin_theme=dark; b=2":          "dark",
		"xbin_theme=dark; xbin_theme=light":  "dark",
		"xbin_theme=sepia; xbin_theme=light": "light",
		"xbin_theme=sepia":                   "",
		"xbin_theme=LIGHT":                   "",
		`xbin_theme="dark"`:                  "",
		"xbin_theme=":                        "",
		"xbin_theme=dark ":                   "",
		"xbin_themes=dark":                   "",
		"x_xbin_theme=light":                 "",
		"theme=dark":                         "",
	} {
		r := httptest.NewRequest("GET", "/login", nil)
		if raw != "" {
			r.Header.Set("Cookie", raw)
		}
		if got := pageTheme(r); got != want {
			t.Errorf("Cookie %q: %q, want %q", raw, got, want)
		}
		head := themeHead(r)
		if !strings.HasSuffix(head, `<link rel="stylesheet" href="/vendor/theme.css">`) {
			t.Errorf("Cookie %q: the head lacks the sheet: %q", raw, head)
		}
		if meta := strings.Contains(head, `<meta name="xbin-theme" content="`+want+`">`); meta != (want != "") || (want == "" && strings.Contains(head, "xbin-theme")) {
			t.Errorf("Cookie %q: head %q", raw, head)
		}
	}
}

// rawCookie appends a raw Cookie header fragment (garbage included: Go's
// AddCookie would sanitize it).
func rawCookie(v string) reqOpt {
	return func(r *http.Request) {
		if v == "" {
			return
		}
		if c := r.Header.Get("Cookie"); c != "" {
			v = c + "; " + v
		}
		r.Header.Set("Cookie", v)
	}
}

// pageCase is one of xbind's own pages, rendered for a request carrying
// the raw cookie fragment, with branding (title + icon) set or not.
type pageCase struct {
	name string
	// render serves the page; it fails the test when the page isn't there
	render func(t *testing.T, cookie string, branded bool) *httptest.ResponseRecorder
	// csp is the page's whole Content-Security-Policy ("": it has none)
	csp string
	// icon: the brand's icon shows (the sign-in family; not the pages
	// that load no image)
	icon bool
	// clientTheme: the page reads the cookie in the browser
	// (/vendor/theme-boot.js) — it gets no request to read it from
	clientTheme bool
	// status: the status glyphs the page shows
	status []string
}

const testBrandTitle = "Acme <Ops>"

// setBrand sets (or clears) s's branding: testBrandTitle and testPNG.
func setBrand(t *testing.T, s *Server, on bool) {
	t.Helper()
	if s.Brand == nil {
		s.Brand = branding.New(filepath.Join(t.TempDir(), "branding.json"))
	}
	title, icon := "", ""
	if on {
		title, icon = testBrandTitle, testPNG
	}
	if _, err := s.Brand.Apply(branding.Patch{Title: &title, Icon: &icon}); err != nil {
		t.Fatal(err)
	}
}

func getPage(h http.Handler, target string, opts ...reqOpt) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", target, nil)
	for _, o := range opts {
		o(r)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func wantPage(t *testing.T, w *httptest.ResponseRecorder, code int, text string) *httptest.ResponseRecorder {
	t.Helper()
	if w.Code != code || !strings.Contains(w.Body.String(), text) {
		t.Fatalf("want %d with %q, got %d:\n%s", code, text, w.Code, w.Body.String())
	}
	return w
}

// serverPages: xbind's own pages, each rendered for a cookie and a brand.
func serverPages() []pageCase {
	return []pageCase{
		{name: "sign-in", icon: true, status: []string{"error"}, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			h, s := impServer(t)
			setBrand(t, s, b)
			return wantPage(t, getPage(h, "/login?sso_err=failed", rawCookie(c)), 200, `action="/login"`)
		}},
		{name: "invite", icon: true, status: []string{"warning"}, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			h, s := impServer(t)
			setBrand(t, s, b)
			if _, err := s.Auth.Users.UpsertInvited(users.User{ID: "erin"}); err != nil {
				t.Fatal(err)
			}
			tok, err := s.Auth.Users.CreateInvite("erin", 0)
			if err != nil {
				t.Fatal(err)
			}
			// signed in as someone else: the page warns
			sid := s.Auth.NewSession("bob", "")
			return wantPage(t, getPage(h, "/login?invite="+url.QueryEscape(tok), cookie(auth.CookieName, sid), rawCookie(c)), 200, "Welcome, erin")
		}},
		{name: "invite-invalid", icon: true, status: []string{"error"}, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			h, s := impServer(t)
			setBrand(t, s, b)
			return wantPage(t, getPage(h, "/login?invite=nope", rawCookie(c)), 403, "This invite link is invalid")
		}},
		{name: "continue-as", icon: true, status: []string{"warning"}, csp: webConfirmCSP, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			h, s := impServer(t)
			setBrand(t, s, b)
			_, dev := webDevice(t, s, "bob")
			code, out := mintWeb(h, dev, "", `{"next":"/c/apps/x/"}`)
			if code != 200 {
				t.Fatalf("mint: %d %v", code, out)
			}
			return wantPage(t, redeemWeb(h, out["url"].(string), "", "10.1.0.1", map[string]string{"Cookie": c}), 200, "Continue as bob")
		}},
		{name: "request-access", icon: true, clientTheme: true, status: []string{"info", "ok", "error"}, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			w := newAssetWS(t, TileAssetsLegacy)
			setBrand(t, w.s, b)
			return wantPage(t, w.do("/c/apps/a/", w.session("bob"), hdr("Accept", "text/html"), rawCookie(c)), 403, "No access to <code>apps/a</code>")
		}},
		{name: "partition-switch", status: []string{"warning"}, csp: partitionPageCSP, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			w := newAssetWS(t, TileAssetsLegacy)
			setBrand(t, w.s, b)
			pendWS(t, w, registry.PartitionSpec{}, registry.PartitionSpec{User: true})
			return wantPage(t, w.do("/c/apps/a/", w.session("ana"), hdr("Sec-Fetch-Dest", "iframe"), rawCookie(c)), 409, "Partition mode switch requested")
		}},
		{name: "partition-switch-on-tile-origin", status: []string{"warning"},
			csp: partitionPageCSP + "; frame-ancestors 'self' http://xbin.localhost:9260", render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
				w := newAssetWS(t, TileAssetsOrigins)
				setBrand(t, w.s, b)
				tc, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
				pendWS(t, w, registry.PartitionSpec{}, registry.PartitionSpec{User: true})
				return wantPage(t, w.do("/c/apps/a/", append(hopNav, host(w.originHost("apps/a")), cookie(tc.Name, tc.Value), rawCookie(c))...), 409, "Partition mode switch requested")
			}},
		{name: "tile-navigation", csp: tileNavCSP, render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
			w := newAssetWS(t, TileAssetsOrigins)
			setBrand(t, w.s, b)
			return wantPage(t, w.do("/c/apps/a/", w.session("ana"), hdr("Sec-Fetch-Site", "cross-site"), hdr("Sec-Fetch-Mode", "navigate"),
				hdr("Sec-Fetch-Dest", "document"), rawCookie(c)), 200, "Opening the tile")
		}},
		{name: "tile-origin-refusal", status: []string{"warning"},
			csp: "sandbox; default-src 'none'; style-src 'self' 'unsafe-inline'; font-src 'self'; frame-ancestors 'self' http://xbin.localhost:9260",
			render: func(t *testing.T, c string, b bool) *httptest.ResponseRecorder {
				w := newAssetWS(t, TileAssetsOrigins)
				setBrand(t, w.s, b)
				return wantPage(t, w.do("/c/apps/a/?xbin_retry=1", host(w.originHost("apps/a")), hdr("Sec-Fetch-Site", "same-origin"),
					hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "document"), rawCookie(c)), 401, "Open it from the workspace")
			}},
	}
}

var (
	reStyle      = regexp.MustCompile(`(?s)<style>(.*?)</style>`)
	reColour     = regexp.MustCompile(`(?i)#[0-9a-f]{3,8}\b|\b(?:rgba?|hsla?|hwb|lab|lch|oklab|oklch|color)\(|(?:^|[\s:,(])(?:white|black)(?:$|[\s;},)!])`)
	reRadius     = regexp.MustCompile(`border(?:-[a-z]+)?-radius:([^;}]*)`)
	reFontDecl   = regexp.MustCompile(`(?:^|[;{\s])font(?:-family)?:([^;}]*)`)
	reFontFamily = regexp.MustCompile(`(?i)system-ui|sans-serif|serif|monospace|arial|helvetica|segoe|-apple-system|roboto|menlo|consolas`)
)

// covers D184 (B2 Q11) — each of xbind's own pages (serverPages) opts in
// to the theme (<html data-bx-theme="auto">), links /vendor/theme.css,
// carries the theme the hint cookie names (light, dark; none for no cookie
// or another value) before the sheet, inside <head>; keeps its CSP's script
// and frame rules, styles and fonts from 'self'; shows xbin's mark and
// wordmark (D183) or the workspace's brand (D76), never X/BIN; draws its
// status as bx-icons' glyphs; and styles with tokens alone — no colour,
// font stack or radius literal but the mark's own.
func TestServerPagesTheme(t *testing.T) {
	for _, pc := range serverPages() {
		t.Run(pc.name, func(t *testing.T) {
			for _, c := range []struct{ cookie, theme string }{
				{"", ""}, {"xbin_theme=light", "light"}, {"xbin_theme=dark", "dark"}, {"xbin_theme=sepia", ""},
			} {
				w := pc.render(t, c.cookie, false)
				body, csp := w.Body.String(), w.Header().Get("Content-Security-Policy")
				if !strings.HasPrefix(body, "<!doctype html>\n<html lang=\"en\" data-bx-theme=\"auto\"><head>") {
					t.Errorf("cookie %q: not opted in:\n%.200s", c.cookie, body)
				}
				head, _, ok := strings.Cut(body, "</head>")
				link := strings.Index(head, `<link rel="stylesheet" href="/vendor/theme.css">`)
				if !ok || link < 0 || strings.Count(body, "/vendor/theme.css") != 1 {
					t.Errorf("cookie %q: the sheet isn't linked once in <head>:\n%s", c.cookie, body)
				}
				meta := strings.Index(head, `<meta name="xbin-theme"`)
				switch {
				case pc.clientTheme:
					boot := strings.Index(head, `<script src="/vendor/theme-boot.js"></script>`)
					if meta >= 0 || boot < 0 || boot > link {
						t.Errorf("cookie %q: theme-boot.js must run before the sheet, and no meta be rendered:\n%s", c.cookie, head)
					}
				case c.theme == "":
					if strings.Contains(body, "xbin-theme") {
						t.Errorf("cookie %q: a theme meta without a light or dark cookie:\n%s", c.cookie, head)
					}
				default:
					if meta < 0 || meta > link || !strings.Contains(head, `<meta name="xbin-theme" content="`+c.theme+`">`) || strings.Count(body, "xbin-theme") != 1 {
						t.Errorf("cookie %q: want the %s meta before the sheet:\n%s", c.cookie, c.theme, head)
					}
				}
				if csp != pc.csp {
					t.Errorf("cookie %q: CSP %q, want %q", c.cookie, csp, pc.csp)
				}
			}

			// xbin's own: the mark and wordmark (D183), the default favicon on
			// the pages that carry one; the mark's colours are the only ones
			// the page paints outside the tokens. Branded: the title
			// (escaped) and, where images load, the icon
			body := pc.render(t, "", false).Body.String()
			if !strings.Contains(body, `<div class="logo">`+brandLockup+`</div>`) || strings.Contains(body, "X/BIN") || strings.Contains(body, "f5a623") {
				t.Errorf("unbranded: want xbin's lockup alone in the logo:\n%s", body)
			}
			for _, m := range regexp.MustCompile(`\s(?:fill|stroke|color|stop-color)="([^"]*)"`).FindAllStringSubmatch(strings.ReplaceAll(body, markTile, ""), -1) {
				if m[1] != "none" && m[1] != "currentColor" {
					t.Errorf("unbranded: a colour other than the mark's or a token's: %s", m[0])
				}
			}
			body = pc.render(t, "", true).Body.String()
			name := `<span class="name">Acme &lt;Ops&gt;</span>`
			if pc.icon {
				name = `<img class="mark" src="` + testPNG + `" alt="">` + name
			}
			if !strings.Contains(body, `<div class="logo">`+name+`</div>`) || strings.Contains(body, `class="lockup"`) || strings.Contains(body, markTile) ||
				pc.icon != strings.Contains(body, "data:image/png") {
				t.Errorf("branded: want %s in the logo:\n%s", name, body)
			}

			// status: bx-icons' glyphs, in currentColor
			for _, k := range pc.status {
				if !strings.Contains(body, statusIcons[k]) {
					t.Errorf("no %s glyph:\n%s", k, body)
				}
			}

			// tokens only: no colour, font stack or radius literal; no inline
			// style attribute
			for _, m := range reStyle.FindAllStringSubmatch(body, -1) {
				css := m[1]
				if bad := reColour.FindString(css); bad != "" {
					t.Errorf("a colour literal in the page's CSS: %q", bad)
				}
				for _, r := range reRadius.FindAllStringSubmatch(css, -1) {
					if v := strings.TrimSpace(r[1]); v != "var(--bx-radius)" && v != "0" {
						t.Errorf("a radius off the token: %q", v)
					}
				}
				for _, f := range reFontDecl.FindAllStringSubmatch(css, -1) {
					if bad := reFontFamily.FindString(f[1]); bad != "" || (!strings.Contains(f[1], "var(--bx-") && strings.TrimSpace(f[1]) != "inherit") {
						t.Errorf("a font off the tokens: %q", f[1])
					}
				}
			}
			if regexp.MustCompile(`\sstyle="`).MatchString(body) || reColour.MatchString(strings.Join(regexp.MustCompile(`<svg[^>]*>`).FindAllString(body, -1), "")) {
				t.Errorf("an inline style or a coloured glyph:\n%s", body)
			}
		})
	}
}

// covers D184 — the status glyphs these pages inline are the ones
// /vendor/bx-icons.js draws (brand §4.6): the same paths and squares, so a
// redrawn glyph can't drift here unnoticed.
func TestPageIconsMatchBxIcons(t *testing.T) {
	src, err := os.ReadFile(filepath.Join("..", "..", "web", "bx-icons.js"))
	if err != nil {
		t.Fatal(err)
	}
	js := string(src)
	frame := regexp.MustCompile(`const FRAME = '([^']*)';`).FindStringSubmatch(js)
	if frame == nil {
		t.Fatal("bx-icons.js: no FRAME")
	}
	// one glyph's definition: p('…') / p(`…${FRAME}…`) and sq(x, y, w[, h]) joined by +
	part := regexp.MustCompile(`p\(['` + "`" + `]([^'` + "`" + `]*)['` + "`" + `]\)|sq\(([\d.]+), ([\d.]+), ([\d.]+)(?:, ([\d.]+))?\)`)
	for kind, glyph := range map[string]string{"error": glyphError, "warning": glyphWarning, "ok": glyphOK, "info": glyphInfo} {
		def := regexp.MustCompile(`(?m)^  ` + kind + `: (.*),$`).FindStringSubmatch(js)
		if def == nil {
			t.Fatalf("bx-icons.js: no %s glyph", kind)
		}
		var markup strings.Builder
		for _, m := range part.FindAllStringSubmatch(def[1], -1) {
			if m[2] == "" {
				markup.WriteString(`<path d="` + strings.ReplaceAll(m[1], "${FRAME}", frame[1]) + `"/>`)
				continue
			}
			h := m[5]
			if h == "" {
				h = m[4]
			}
			markup.WriteString(`<rect x="` + m[2] + `" y="` + m[3] + `" width="` + m[4] + `" height="` + h + `" fill="currentColor" stroke="none"/>`)
		}
		if markup.String() != glyph {
			t.Errorf("%s: bx-icons.js draws %s\nthe pages inline %s", kind, markup.String(), glyph)
		}
	}
	// the wrapper: bx-icons' stroke and grid, in currentColor
	for _, a := range []string{`viewBox="0 0 16 16"`, `fill="none"`, `stroke="currentColor"`, `stroke-width="1.5"`, `stroke-linecap="square"`, `stroke-linejoin="miter"`} {
		if !strings.Contains(js, a) || !strings.Contains(icoAttrs, a) {
			t.Errorf("the glyph wrapper lacks %s", a)
		}
	}
}
