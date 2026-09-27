package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// zsPage is every fixture document: a head for the injection to follow.
const zsPage = "<!doctype html><html><head><title>t</title></head><body>t</body></html>\n"

// newZeroStateWS is the zero-state fixture: tiles that never opted in, a
// tile whose own path holds +, a directory named like a qualified path
// inside a tile, a nested component, a c++ tile, an inject:false tile and
// the shell. ana reads the tiles; nobody holds a deployment record (none
// exists yet). Its own files, so the goldens change only with behaviour.
func newZeroStateWS(t *testing.T, mode string) *assetWS {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"xbin.json":                  `{"importMap":{"lit":"/vendor/lit-all.min.js","@lib/":"/c/apps/b/"},"lifecycle":{"apps/raw":"disabled"}}`,
		"apps/a/xbin.json":           `{"runtime":"node","expose":{"roles":{"reader":"reads"}},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"]}`,
		"apps/a/index.html":          zsPage,
		"apps/a/app.js":              "export const a = 1;\n",
		"apps/a/native.js":           "export default {};\n",
		"apps/a/x+y/file.txt":        "plus dir\n",
		"apps/b/xbin.json":           `{}`,
		"apps/b/lib.js":              "export const lib = 1;\n",
		"notes+ideas/xbin.json":      `{}`,
		"notes+ideas/index.html":     zsPage,
		"notes+ideas/n.js":           "export const n = 1;\n",
		"apps/shop/xbin.json":        `{}`,
		"apps/shop/index.html":       zsPage,
		"apps/shop/admin/xbin.json":  `{}`,
		"apps/shop/admin/index.html": zsPage,
		"c++/xbin.json":              `{}`,
		"c++/index.html":             zsPage,
		"apps/raw/xbin.json":         `{"inject":false}`,
		"apps/raw/index.html":        zsPage,
		"shell/index.html":           zsPage,
		"team/scope.json":            `{}`,
		"team/notes/xbin.json":       `{}`,
		"team/notes/index.html":      zsPage,
	}
	for rel, c := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	a, err := auth.Load(root, false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	read := map[string]string{}
	for _, tile := range []string{"apps/a", "apps/b", "notes+ideas", "apps/shop", "apps/shop/admin", "c++", "apps/raw", "team/notes"} {
		read[tile] = users.LevelRead
	}
	if _, err := st.Upsert(users.User{ID: "ana", Role: users.RoleUser, Tiles: read}, "password1"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a, TileAssets: mode, ExternalURL: "http://xbin.localhost:9260",
		ComponentAPI: http.HandlerFunc(zsComponentAPI)}
	if mode == TileAssetsOrigins {
		s.TilesDomain = "xbin.localhost"
		a.SetHostCookies(true)
	}
	a.SetClientIP(s.ClientIP)
	return &assetWS{t: t, s: s, a: a, st: st, root: root}
}

// zsComponentAPI stands in for the proxy behind /api/<tile>/…: it answers
// with what the server handed it — the path, untouched, and the principal.
func zsComponentAPI(w http.ResponseWriter, r *http.Request) {
	p := auth.PrincipalOf(r)
	w.Header().Set("Content-Type", "text/plain")
	fmt.Fprintf(w, "component api: %s %s from=%q via=%q user=%q imp=%q\n", r.Method, r.URL.RequestURI(), p.From(), p.Via, p.UserID, p.Impersonator)
}

var (
	zsFrameTok = regexp.MustCompile(`(<meta name="xbin-frame-token" content=")([^"]*)(">)`)
	zsAssetTok = regexp.MustCompile(`/c/~([^/"]+)/`)
	zsQueryTok = regexp.MustCompile(`((?:xbin_ticket|xbin_begin|xbin_state|frame)=)[^&"\s]+`)
)

// zsMask replaces the values two runs of today's xbind never share — frame
// and asset tokens, tile origin labels, exchange tickets and states — with
// what they stand for, after checking them where they can be checked.
func (w *assetWS) zsMask(s string) string {
	s = zsFrameTok.ReplaceAllStringFunc(s, func(m string) string {
		g := zsFrameTok.FindStringSubmatch(m)
		if g[2] == "" {
			return m
		}
		comp, uid, ok := w.a.VerifyFrameToken(g[2])
		parts := strings.Split(g[2], "|")
		if !ok || len(parts) != 5 {
			return g[1] + "<INVALID frame token>" + g[3]
		}
		return g[1] + "<frame token " + comp + "|" + uid + ">" + g[3]
	})
	s = zsAssetTok.ReplaceAllStringFunc(s, func(m string) string {
		tok := zsAssetTok.FindStringSubmatch(m)[1]
		if g, ok := w.a.VerifyAssetToken(tok); ok {
			return "/c/~<asset token " + g.Tile + "|" + g.UserID + ">/"
		}
		return "/c/~<not an asset token>/"
	})
	for _, tile := range []string{"apps/a", "apps/b", "notes+ideas", "apps/shop", "apps/shop/admin", "c++", "apps/raw", "team/notes", "shell"} {
		s = strings.ReplaceAll(s, w.a.TileHostID(tile), "t-<"+tile+">")
	}
	return zsQueryTok.ReplaceAllString(s, "$1<masked>")
}

// zsRender renders one exchange: the request line and who made it, the
// status, every response header (sorted; volatile values masked), the body.
func (w *assetWS) zsRender(label string, rec *httptest.ResponseRecorder) string {
	var b strings.Builder
	fmt.Fprintf(&b, "== %s\n%d\n", label, rec.Code)
	keys := make([]string, 0, len(rec.Header()))
	for k := range rec.Header() {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		for _, v := range rec.Header()[k] {
			switch k {
			case "Last-Modified":
				v = "<mtime>"
			case "Set-Cookie":
				if name, rest, ok := strings.Cut(v, "="); ok {
					_, attrs, _ := strings.Cut(rest, ";")
					v = name + "=<masked>;" + attrs
				}
			}
			fmt.Fprintf(&b, "%s: %s\n", k, w.zsMask(v))
		}
	}
	b.WriteString("\n" + w.zsMask(rec.Body.String()))
	if !strings.HasSuffix(b.String(), "\n") {
		b.WriteString("\n")
	}
	return b.String()
}

// zsDiff names the first differing line of two renderings.
func zsDiff(got, want string) string {
	g, wl := strings.Split(got, "\n"), strings.Split(want, "\n")
	for i := 0; i < len(g) || i < len(wl); i++ {
		var a, b string
		if i < len(g) {
			a = g[i]
		}
		if i < len(wl) {
			b = wl[i]
		}
		if a != b {
			return fmt.Sprintf("line %d:\n  got  %q\n  want %q", i+1, a, b)
		}
	}
	return "identical"
}

// zsOwner is the bootstrap owner token as a bearer.
func (w *assetWS) zsOwner() reqOpt { return hdr("Authorization", "Bearer "+w.a.OwnerTokenValue()) }

// zsDocs runs a mode's URL matrix and renders it.
func (w *assetWS) zsDocs(mode string) string {
	ana := w.session("ana")
	w.a.NewSession("ana", "192.0.2.1") // the warm IP the legacy subresource rule wants
	var out strings.Builder
	add := func(label string, rec *httptest.ResponseRecorder) { out.WriteString(w.zsRender(label, rec)) }
	doc := func(label, url string, opts ...reqOpt) { add(label, w.do(url, opts...)) }

	switch mode {
	case TileAssetsLegacy, TileAssetsTokens:
		doc("GET /c/apps/a/ as ana", "/c/apps/a/", ana)
		doc("GET /c/apps/a as ana (no slash)", "/c/apps/a", ana)
		doc("GET /c/apps/a/?q=a+main as ana (a + in the query)", "/c/apps/a/?q=a+main", ana)
		doc("GET /c/apps/a/?native=1 as ana", "/c/apps/a/?native=1", ana)
		doc("GET /c/apps/a/?native=1 as ana, from the xbin app", "/c/apps/a/?native=1", ana, hdr(AppClientHeader, "app/ios 1.0"))
		doc("GET /c/apps/a/app.js as ana", "/c/apps/a/app.js", ana)
		doc("GET /c/apps/a/app.js by the tile's frame token", "/c/apps/a/app.js", w.frame("apps/a", "ana"))
		doc("GET /c/apps/a/app.js credential-less subresource", "/c/apps/a/app.js", subresource...)
		doc("GET /c/apps/a/x+y/file.txt as ana (a + directory inside a tile)", "/c/apps/a/x+y/file.txt", ana)
		doc("GET /c/apps/a+main/ as ana", "/c/apps/a+main/", ana)
		doc("GET /c/apps/a+main/ as the owner", "/c/apps/a+main/", w.zsOwner())
		doc("GET /c/apps/a+main/app.js as the owner", "/c/apps/a+main/app.js", w.zsOwner())
		doc("GET /c/notes+ideas/ as ana (a tile whose own path holds +)", "/c/notes+ideas/", ana)
		doc("GET /c/notes+ideas/n.js as ana", "/c/notes+ideas/n.js", ana)
		doc("GET /c/notes+ideas+main/ as the owner", "/c/notes+ideas+main/", w.zsOwner())
		doc("GET /c/apps/shop/admin/ as ana (a nested component)", "/c/apps/shop/admin/", ana)
		doc("GET /c/apps/shop/admin+dev/ as the owner", "/c/apps/shop/admin+dev/", w.zsOwner())
		doc("GET /c/c++/ as ana", "/c/c++/", ana)
		doc("GET /c/apps/raw/ as ana (inject:false)", "/c/apps/raw/", ana)
		doc("GET /c/shell/ as ana (chrome)", "/c/shell/", ana)
		if mode == TileAssetsTokens {
			for _, c := range []struct{ doc, file string }{{"apps/a", "app.js"}, {"notes+ideas", "n.js"}} {
				tok, base := assetTokenFrom(w.t, w.do("/c/"+c.doc+"/", ana).Body.String())
				at := "/c/~" + tok + "/"
				doc("GET /c/~TOKEN/"+base+c.file+" (the asset plane)", at+base+c.file, subresource...)
				doc("GET /c/~TOKEN/"+c.doc+"+main/"+c.file+" (the asset plane)", at+c.doc+"+main/"+c.file, subresource...)
				if c.doc == "apps/a" {
					doc("GET /c/~TOKEN/apps/a/x+y/file.txt (the asset plane)", at+"apps/a/x+y/file.txt", subresource...)
				}
			}
		}
	case TileAssetsOrigins:
		doc("GET /c/apps/a/ as ana, the shell's frame (workspace origin)", "/c/apps/a/", append(shellNav, ana)...)
		doc("GET /c/apps/a+main/ as the owner (workspace origin)", "/c/apps/a+main/", w.zsOwner())
		doc("GET /c/notes+ideas/ as ana, the shell's frame (workspace origin)", "/c/notes+ideas/", append(shellNav, ana)...)
		doc("GET /c/shell/ as ana (chrome, workspace origin)", "/c/shell/", ana)
		for _, tile := range []string{"apps/a", "notes+ideas"} {
			c, rec := w.exchange(tile, "ana", "/c/"+tile+"/")
			if c == nil {
				w.t.Fatalf("exchange for %s: %d %v", tile, rec.Code, rec.Header())
			}
			on := []reqOpt{host(w.originHost(tile)), cookie(c.Name, c.Value)}
			doc("GET /c/"+tile+"/ on its origin (tile cookie)", "/c/"+tile+"/", append(on, hopNav...)...)
			doc("GET /c/"+tile+"/?native=1 on its origin (tile cookie)", "/c/"+tile+"/?native=1", append(on, hopNav...)...)
			doc("GET /api/"+tile+"/x on its origin (tile cookie)", "/api/"+tile+"/x?y=1", append(on, sameOrig)...)
		}
		c, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
		on := []reqOpt{host(w.originHost("apps/a")), cookie(c.Name, c.Value)}
		doc("GET /c/apps/a/app.js on its origin (tile cookie)", "/c/apps/a/app.js", append(on, hdr("Sec-Fetch-Site", "same-origin"), hdr("Sec-Fetch-Dest", "script"))...)
		doc("GET /c/apps/a+main/ on apps/a's origin (tile cookie)", "/c/apps/a+main/", append(on, hopNav...)...)
	}
	// /api/<tile>/…: what the server hands the component API, per mode.
	doc("GET /api/apps/a/x?y=a+b as ana", "/api/apps/a/x?y=a+b", ana)
	doc("GET /api/apps/a/x by the tile's frame token", "/api/apps/a/x", w.frame("apps/a", "ana"))
	doc("GET /api/apps/a+main/x as the owner", "/api/apps/a+main/x", w.zsOwner())
	doc("GET /api/notes+ideas/x by its frame token", "/api/notes+ideas/x", w.frame("notes+ideas", "ana"))
	doc("POST /api/apps/shop/admin+dev/x as the owner", "/api/apps/shop/admin+dev/x", method("POST"), w.zsOwner())
	return out.String()
}

// covers PO-1 PO-10 Z2 SC-ZERO — every URL of a zero-state workspace
// answers with today's status, headers and bytes, in the legacy, tokens
// and origins asset modes: tile documents with today's D4 injection (no
// deployment meta, no deployment import-map entry), ?native=1, files,
// the asset plane, the tile origins, /api/<tile>/…; and the + matrix — a
// tile whose own path holds +, a + directory inside a tile, c++, a +
// in the query, and <tile>+main or a nested <tile>+<name> on a tile with
// no record, which resolve exactly as any other unknown path does today.
// Run-to-run values (tokens, origin labels, tickets, mtimes) are masked.
// Hand-maintained goldens: changing one is a compat change (12-compat.md).
func TestZeroStateDocumentUnchanged(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newZeroStateWS(t, mode)
			got := w.zsDocs(mode)
			want := zsDocGoldens[mode]
			if got != want {
				t.Errorf("%s mode differs from today at %s\n--- got ---\n%s", mode, zsDiff(got, want), got)
			}
		})
	}
}

// covers PO-14 Z7 SC-ZERO — /components keeps its rows (one per
// component, never one per deployment) and each row's keys and values;
// /components/{path} likewise, a + path included; <tile>+main is no
// component. Hand-maintained goldens.
func TestZeroStateComponentsEntry(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newZeroStateWS(t, mode)
			var out strings.Builder
			for _, c := range []struct {
				label, url string
				as         reqOpt
			}{
				{"GET /api/xbin/components as ana", "/api/xbin/components", w.session("ana")},
				{"GET /api/xbin/components as the owner", "/api/xbin/components", w.zsOwner()},
				{"GET /api/xbin/components/apps/a as the owner", "/api/xbin/components/apps/a", w.zsOwner()},
				{"GET /api/xbin/components/notes+ideas as the owner", "/api/xbin/components/notes+ideas", w.zsOwner()},
				{"GET /api/xbin/components/apps/shop/admin as the owner", "/api/xbin/components/apps/shop/admin", w.zsOwner()},
				{"GET /api/xbin/components/apps/a+main as the owner", "/api/xbin/components/apps/a+main", w.zsOwner()},
			} {
				out.WriteString(w.zsRender(c.label, w.do(c.url, c.as)))
			}
			if got, want := out.String(), zsComponentGoldens[mode]; got != want {
				t.Errorf("%s mode differs from today at %s\n--- got ---\n%s", mode, zsDiff(got, want), got)
			}
		})
	}
}

// The goldens: today's answers, masked as zsMask says. Hand-maintained —
// never regenerated; a change here is a compat change (12-compat.md).
var zsDocGoldens = map[string]string{
	TileAssetsLegacy:  zsDocLegacy,
	TileAssetsTokens:  zsDocTokens,
	TileAssetsOrigins: zsDocOrigins,
}

// /components answers alike in legacy and tokens modes; origins mode adds
// each sandboxed tile's origin.
var zsComponentGoldens = map[string]string{
	TileAssetsLegacy:  zsComponentsLegacy,
	TileAssetsTokens:  zsComponentsLegacy,
	TileAssetsOrigins: zsComponentsOrigins,
}

const zsDocLegacy = `== GET /c/apps/a/ as ana
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/a as ana (no slash)
301
Content-Type: text/html; charset=utf-8
Location: /c/apps/a/

<a href="/c/apps/a/">Moved Permanently</a>.

== GET /c/apps/a/?q=a+main as ana (a + in the query)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/a/?native=1 as ana
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>apps/a (native)</title>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<meta name="xbin-native" content="1">
<script type="module">
import { boot } from '/vendor/xb-native.js';
await boot("./native.js");
</script>
</head><body></body></html>
== GET /c/apps/a/?native=1 as ana, from the xbin app
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>apps/a (native)</title>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<meta name="xbin-ws-origin" content="ws://xbin.localhost:9260">
<script type="module" src="/vendor/xbin-client.js"></script>
<meta name="xbin-native" content="1">
<script type="module">
import { boot } from '/vendor/xb-native.js';
await boot("./native.js");
</script>
</head><body></body></html>
== GET /c/apps/a/app.js as ana
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/apps/a/app.js by the tile's frame token
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/apps/a/app.js credential-less subresource
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/apps/a/x+y/file.txt as ana (a + directory inside a tile)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 9
Content-Security-Policy: sandbox
Content-Type: text/plain; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

plus dir
== GET /c/apps/a+main/ as ana
403
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

not permitted to use this tile
== GET /c/apps/a+main/ as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/apps/a+main/app.js as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/notes+ideas/ as ana (a tile whose own path holds +)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="notes+ideas">
<meta name="xbin-frame-token" content="<frame token notes+ideas|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/notes+ideas/n.js as ana
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const n = 1;
== GET /c/notes+ideas+main/ as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/apps/shop/admin/ as ana (a nested component)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/shop/admin">
<meta name="xbin-frame-token" content="<frame token apps/shop/admin|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/shop/admin+dev/ as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/c++/ as ana
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="c++">
<meta name="xbin-frame-token" content="<frame token c++|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/raw/ as ana (inject:false)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 72
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

<!doctype html><html><head><title>t</title></head><body>t</body></html>
== GET /c/shell/ as ana (chrome)
200
Cache-Control: no-store
Content-Type: text/html; charset=utf-8
Cross-Origin-Opener-Policy: same-origin
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="shell">
<meta name="xbin-frame-token" content="">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /api/apps/a/x?y=a+b as ana
200
Content-Type: text/plain

component api: GET /api/apps/a/x?y=a+b from="user:ana" via="session" user="ana" imp=""
== GET /api/apps/a/x by the tile's frame token
200
Content-Type: text/plain

component api: GET /api/apps/a/x from="apps/a" via="frame" user="ana" imp=""
== GET /api/apps/a+main/x as the owner
200
Content-Type: text/plain

component api: GET /api/apps/a+main/x from="owner" via="bearer" user="" imp=""
== GET /api/notes+ideas/x by its frame token
200
Content-Type: text/plain

component api: GET /api/notes+ideas/x from="notes+ideas" via="frame" user="ana" imp=""
== POST /api/apps/shop/admin+dev/x as the owner
200
Content-Type: text/plain

component api: POST /api/apps/shop/admin+dev/x from="owner" via="bearer" user="" imp=""
`

const zsDocTokens = `== GET /c/apps/a/ as ana
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<base href="/c/~<asset token apps/a|ana>/apps/a/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/apps/a/":"/c/~<asset token apps/a|ana>/apps/a/","@lib/":"/c/~<asset token apps/a|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/a as ana (no slash)
301
Content-Type: text/html; charset=utf-8
Location: /c/apps/a/

<a href="/c/apps/a/">Moved Permanently</a>.

== GET /c/apps/a/?q=a+main as ana (a + in the query)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<base href="/c/~<asset token apps/a|ana>/apps/a/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/apps/a/":"/c/~<asset token apps/a|ana>/apps/a/","@lib/":"/c/~<asset token apps/a|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/a/?native=1 as ana
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>apps/a (native)</title>
<base href="/c/~<asset token apps/a|ana>/apps/a/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/apps/a/":"/c/~<asset token apps/a|ana>/apps/a/","@lib/":"/c/~<asset token apps/a|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<meta name="xbin-native" content="1">
<script type="module">
import { boot } from '/vendor/xb-native.js';
await boot("./native.js");
</script>
</head><body></body></html>
== GET /c/apps/a/?native=1 as ana, from the xbin app
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>apps/a (native)</title>
<base href="/c/~<asset token apps/a|ana>/apps/a/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/apps/a/":"/c/~<asset token apps/a|ana>/apps/a/","@lib/":"/c/~<asset token apps/a|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<meta name="xbin-ws-origin" content="ws://xbin.localhost:9260">
<script type="module" src="/vendor/xbin-client.js"></script>
<meta name="xbin-native" content="1">
<script type="module">
import { boot } from '/vendor/xb-native.js';
await boot("./native.js");
</script>
</head><body></body></html>
== GET /c/apps/a/app.js as ana
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/apps/a/app.js by the tile's frame token
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/apps/a/app.js credential-less subresource
401
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

unauthorized — strict tile asset gating: tile files load only with a credential; use relative URLs (bx fix assets <tile>) — docs/elements.md#asset-urls
== GET /c/apps/a/x+y/file.txt as ana (a + directory inside a tile)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 9
Content-Security-Policy: sandbox
Content-Type: text/plain; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

plus dir
== GET /c/apps/a+main/ as ana
403
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

not permitted to use this tile
== GET /c/apps/a+main/ as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/apps/a+main/app.js as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/notes+ideas/ as ana (a tile whose own path holds +)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<base href="/c/~<asset token notes+ideas|ana>/notes+ideas/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/notes+ideas/":"/c/~<asset token notes+ideas|ana>/notes+ideas/","@lib/":"/c/~<asset token notes+ideas|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="notes+ideas">
<meta name="xbin-frame-token" content="<frame token notes+ideas|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/notes+ideas/n.js as ana
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const n = 1;
== GET /c/notes+ideas+main/ as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/apps/shop/admin/ as ana (a nested component)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<base href="/c/~<asset token apps/shop/admin|ana>/apps/shop/admin/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/apps/shop/admin/":"/c/~<asset token apps/shop/admin|ana>/apps/shop/admin/","@lib/":"/c/~<asset token apps/shop/admin|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/shop/admin">
<meta name="xbin-frame-token" content="<frame token apps/shop/admin|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/shop/admin+dev/ as the owner
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/c++/ as ana
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<base href="/c/~<asset token c++|ana>/c++/" data-xbin-assets>
<meta name="xbin-tile-assets" content="tokens">
<script type="importmap">{"imports":{"/c/c++/":"/c/~<asset token c++|ana>/c++/","@lib/":"/c/~<asset token c++|ana>/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="c++">
<meta name="xbin-frame-token" content="<frame token c++|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/raw/ as ana (inject:false)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 72
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads
Content-Type: text/html; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

<!doctype html><html><head><title>t</title></head><body>t</body></html>
== GET /c/shell/ as ana (chrome)
200
Cache-Control: no-store
Content-Type: text/html; charset=utf-8
Cross-Origin-Opener-Policy: same-origin
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="shell">
<meta name="xbin-frame-token" content="">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/~TOKEN/apps/a/app.js (the asset plane)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/~TOKEN/apps/a+main/app.js (the asset plane)
403
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

not permitted to use this tile
== GET /c/~TOKEN/apps/a/x+y/file.txt (the asset plane)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 9
Content-Security-Policy: sandbox
Content-Type: text/plain; charset=utf-8
Last-Modified: <mtime>
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff

plus dir
== GET /c/~TOKEN/notes+ideas/n.js (the asset plane)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: sandbox
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
Referrer-Policy: no-referrer
X-Content-Type-Options: nosniff

export const n = 1;
== GET /c/~TOKEN/notes+ideas+main/n.js (the asset plane)
403
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

not permitted to use this tile
== GET /api/apps/a/x?y=a+b as ana
200
Content-Type: text/plain

component api: GET /api/apps/a/x?y=a+b from="user:ana" via="session" user="ana" imp=""
== GET /api/apps/a/x by the tile's frame token
200
Content-Type: text/plain

component api: GET /api/apps/a/x from="apps/a" via="frame" user="ana" imp=""
== GET /api/apps/a+main/x as the owner
200
Content-Type: text/plain

component api: GET /api/apps/a+main/x from="owner" via="bearer" user="" imp=""
== GET /api/notes+ideas/x by its frame token
200
Content-Type: text/plain

component api: GET /api/notes+ideas/x from="notes+ideas" via="frame" user="ana" imp=""
== POST /api/apps/shop/admin+dev/x as the owner
200
Content-Type: text/plain

component api: POST /api/apps/shop/admin+dev/x from="owner" via="bearer" user="" imp=""
`

const zsDocOrigins = `== GET /c/apps/a/ as ana, the shell's frame (workspace origin)
302
Cache-Control: no-store
Content-Type: text/html; charset=utf-8
Location: http://t-<apps/a>.xbin.localhost:9260/c/apps/a/?xbin_begin=<masked>
Referrer-Policy: no-referrer

<a href="http://t-<apps/a>.xbin.localhost:9260/c/apps/a/?xbin_begin=<masked>">Found</a>.

== GET /c/apps/a+main/ as the owner (workspace origin)
404
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

404 page not found
== GET /c/notes+ideas/ as ana, the shell's frame (workspace origin)
302
Cache-Control: no-store
Content-Type: text/html; charset=utf-8
Location: http://t-<notes+ideas>.xbin.localhost:9260/c/notes+ideas/?xbin_begin=<masked>
Referrer-Policy: no-referrer

<a href="http://t-<notes+ideas>.xbin.localhost:9260/c/notes+ideas/?xbin_begin=<masked>">Found</a>.

== GET /c/shell/ as ana (chrome, workspace origin)
200
Cache-Control: no-store
Content-Security-Policy: frame-ancestors 'self'
Content-Type: text/html; charset=utf-8
Cross-Origin-Opener-Policy: same-origin
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="shell">
<meta name="xbin-frame-token" content="">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/a/ on its origin (tile cookie)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads allow-same-origin; frame-ancestors 'self' http://xbin.localhost:9260
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<meta name="xbin-tile-assets" content="origins">
<meta name="xbin-workspace-origin" content="http://xbin.localhost:9260">
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/apps/a/?native=1 on its origin (tile cookie)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads allow-same-origin; frame-ancestors 'self' http://xbin.localhost:9260
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>apps/a (native)</title>
<meta name="xbin-tile-assets" content="origins">
<meta name="xbin-workspace-origin" content="http://xbin.localhost:9260">
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="apps/a">
<meta name="xbin-frame-token" content="<frame token apps/a|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin">
<script type="module" src="/vendor/xbin-client.js"></script>
<meta name="xbin-native" content="1">
<script type="module">
import { boot } from '/vendor/xb-native.js';
await boot("./native.js");
</script>
</head><body></body></html>
== GET /api/apps/a/x on its origin (tile cookie)
200
Content-Type: text/plain

component api: GET /api/apps/a/x?y=1 from="apps/a" via="frame" user="ana" imp=""
== GET /c/notes+ideas/ on its origin (tile cookie)
200
Cache-Control: no-store
Content-Security-Policy: sandbox allow-scripts allow-forms allow-modals allow-downloads allow-same-origin; frame-ancestors 'self' http://xbin.localhost:9260
Content-Type: text/html; charset=utf-8
X-Content-Type-Options: nosniff

<!doctype html><html><head>
<meta name="xbin-tile-assets" content="origins">
<meta name="xbin-workspace-origin" content="http://xbin.localhost:9260">
<script type="importmap">{"imports":{"@lib/":"/c/apps/b/","lit":"/vendor/lit-all.min.js"}}</script>
<meta name="xbin-component" content="notes+ideas">
<meta name="xbin-frame-token" content="<frame token notes+ideas|ana>">
<meta name="xbin-sandbox" content="allow-scripts allow-forms allow-modals allow-downloads allow-same-origin">
<script type="module" src="/vendor/xbin-client.js"></script>
<title>t</title></head><body>t</body></html>
== GET /c/notes+ideas/?native=1 on its origin (tile cookie)
404
Cache-Control: no-store
Content-Security-Policy: frame-ancestors 'self' http://xbin.localhost:9260
Content-Type: text/plain; charset=utf-8
X-Content-Type-Options: nosniff

this tile has no native app UI (no native.js, and no "native" in its xbin.json)
== GET /api/notes+ideas/x on its origin (tile cookie)
200
Content-Type: text/plain

component api: GET /api/notes+ideas/x?y=1 from="notes+ideas" via="frame" user="ana" imp=""
== GET /c/apps/a/app.js on its origin (tile cookie)
200
Accept-Ranges: bytes
Cache-Control: no-store
Content-Length: 20
Content-Security-Policy: frame-ancestors 'self' http://xbin.localhost:9260
Content-Type: text/javascript; charset=utf-8
Last-Modified: <mtime>
X-Content-Type-Options: nosniff

export const a = 1;
== GET /c/apps/a+main/ on apps/a's origin (tile cookie)
302
Cache-Control: no-store
Content-Type: text/html; charset=utf-8
Location: http://xbin.localhost:9260/c/apps/a+main/

<a href="http://xbin.localhost:9260/c/apps/a+main/">Found</a>.

== GET /api/apps/a/x?y=a+b as ana
200
Content-Type: text/plain

component api: GET /api/apps/a/x?y=a+b from="user:ana" via="session" user="ana" imp=""
== GET /api/apps/a/x by the tile's frame token
200
Content-Type: text/plain

component api: GET /api/apps/a/x from="apps/a" via="frame" user="ana" imp=""
== GET /api/apps/a+main/x as the owner
200
Content-Type: text/plain

component api: GET /api/apps/a+main/x from="owner" via="bearer" user="" imp=""
== GET /api/notes+ideas/x by its frame token
200
Content-Type: text/plain

component api: GET /api/notes+ideas/x from="notes+ideas" via="frame" user="ana" imp=""
== POST /api/apps/shop/admin+dev/x as the owner
200
Content-Type: text/plain

component api: POST /api/apps/shop/admin+dev/x from="owner" via="bearer" user="" imp=""
`

const zsComponentsLegacy = `== GET /api/xbin/components as ana
200
Content-Type: application/json

[{"path":"apps/a","runtime":"node","hasIndex":true,"roles":{"reader":"reads"},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"native":{"entry":"native.js"}},{"path":"apps/b","hasIndex":false},{"path":"apps/raw","hasIndex":true,"state":"disabled"},{"path":"apps/shop","hasIndex":true},{"path":"apps/shop/admin","hasIndex":true},{"path":"c++","hasIndex":true},{"path":"notes+ideas","hasIndex":true},{"path":"shell","hasIndex":true,"chrome":true},{"path":"team/notes","scope":"team","hasIndex":true}]
== GET /api/xbin/components as the owner
200
Content-Type: application/json

[{"path":"apps/a","runtime":"node","hasIndex":true,"roles":{"reader":"reads"},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"native":{"entry":"native.js"}},{"path":"apps/b","hasIndex":false},{"path":"apps/raw","hasIndex":true,"state":"disabled"},{"path":"apps/shop","hasIndex":true},{"path":"apps/shop/admin","hasIndex":true},{"path":"c++","hasIndex":true},{"path":"notes+ideas","hasIndex":true},{"path":"shell","hasIndex":true,"chrome":true},{"path":"team/notes","scope":"team","hasIndex":true}]
== GET /api/xbin/components/apps/a as the owner
200
Content-Type: application/json

{"apiDoc":"","component":{"path":"apps/a","runtime":"node","hasIndex":true,"roles":{"reader":"reads"},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"native":{"entry":"native.js"}}}
== GET /api/xbin/components/notes+ideas as the owner
200
Content-Type: application/json

{"apiDoc":"","component":{"path":"notes+ideas","hasIndex":true}}
== GET /api/xbin/components/apps/shop/admin as the owner
200
Content-Type: application/json

{"apiDoc":"","component":{"path":"apps/shop/admin","hasIndex":true}}
== GET /api/xbin/components/apps/a+main as the owner
404
Content-Type: application/json

{"docs":"/docs/protocol.md","error":"no such component"}
`

const zsComponentsOrigins = `== GET /api/xbin/components as ana
200
Content-Type: application/json

[{"path":"apps/a","runtime":"node","hasIndex":true,"roles":{"reader":"reads"},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"native":{"entry":"native.js"},"origin":"http://t-<apps/a>.xbin.localhost:9260"},{"path":"apps/b","hasIndex":false,"origin":"http://t-<apps/b>.xbin.localhost:9260"},{"path":"apps/raw","hasIndex":true,"state":"disabled","origin":"http://t-<apps/raw>.xbin.localhost:9260"},{"path":"apps/shop","hasIndex":true,"origin":"http://t-<apps/shop>.xbin.localhost:9260"},{"path":"apps/shop/admin","hasIndex":true,"origin":"http://t-<apps/shop/admin>.xbin.localhost:9260"},{"path":"c++","hasIndex":true,"origin":"http://t-<c++>.xbin.localhost:9260"},{"path":"notes+ideas","hasIndex":true,"origin":"http://t-<notes+ideas>.xbin.localhost:9260"},{"path":"shell","hasIndex":true,"chrome":true},{"path":"team/notes","scope":"team","hasIndex":true,"origin":"http://t-<team/notes>.xbin.localhost:9260"}]
== GET /api/xbin/components as the owner
200
Content-Type: application/json

[{"path":"apps/a","runtime":"node","hasIndex":true,"roles":{"reader":"reads"},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"native":{"entry":"native.js"},"origin":"http://t-<apps/a>.xbin.localhost:9260"},{"path":"apps/b","hasIndex":false,"origin":"http://t-<apps/b>.xbin.localhost:9260"},{"path":"apps/raw","hasIndex":true,"state":"disabled","origin":"http://t-<apps/raw>.xbin.localhost:9260"},{"path":"apps/shop","hasIndex":true,"origin":"http://t-<apps/shop>.xbin.localhost:9260"},{"path":"apps/shop/admin","hasIndex":true,"origin":"http://t-<apps/shop/admin>.xbin.localhost:9260"},{"path":"c++","hasIndex":true,"origin":"http://t-<c++>.xbin.localhost:9260"},{"path":"notes+ideas","hasIndex":true,"origin":"http://t-<notes+ideas>.xbin.localhost:9260"},{"path":"shell","hasIndex":true,"chrome":true},{"path":"team/notes","scope":"team","hasIndex":true,"origin":"http://t-<team/notes>.xbin.localhost:9260"}]
== GET /api/xbin/components/apps/a as the owner
200
Content-Type: application/json

{"apiDoc":"","component":{"path":"apps/a","runtime":"node","hasIndex":true,"roles":{"reader":"reads"},"uses":[{"target":"apps/b","role":"reader"}],"deps":["apps/b"],"native":{"entry":"native.js"},"origin":"http://t-<apps/a>.xbin.localhost:9260"}}
== GET /api/xbin/components/notes+ideas as the owner
200
Content-Type: application/json

{"apiDoc":"","component":{"path":"notes+ideas","hasIndex":true,"origin":"http://t-<notes+ideas>.xbin.localhost:9260"}}
== GET /api/xbin/components/apps/shop/admin as the owner
200
Content-Type: application/json

{"apiDoc":"","component":{"path":"apps/shop/admin","hasIndex":true,"origin":"http://t-<apps/shop/admin>.xbin.localhost:9260"}}
== GET /api/xbin/components/apps/a+main as the owner
404
Content-Type: application/json

{"docs":"/docs/protocol.md","error":"no such component"}
`
