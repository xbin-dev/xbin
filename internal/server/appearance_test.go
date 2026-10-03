package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"mime"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/prefsfile"
)

// shellPrefs writes user's shell bucket (data/prefs/<user>/root.json, the
// one the shell's PUT /prefs/theme writes) as the obs plane would.
func shellPrefs(t *testing.T, root, user string, m map[string]any) string {
	t.Helper()
	raw := map[string]json.RawMessage{}
	for k, v := range m {
		b, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		raw[k] = b
	}
	p := prefsfile.Path(root, user, prefsfile.Root)
	if err := prefsfile.Write(p, raw); err != nil {
		t.Fatal(err)
	}
	return p
}

var appearanceMeta = regexp.MustCompile(`<meta name="xbin-(theme|density)" content="([^"]*)">`)

// appearanceIn is the appearance metas in a served document: name → content
// (each at most once, or the test fails).
func appearanceIn(t *testing.T, body string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, m := range appearanceMeta.FindAllStringSubmatch(body, -1) {
		if _, dup := out[m[1]]; dup {
			t.Fatalf("two xbin-%s metas in:\n%s", m[1], body)
		}
		out[m[1]] = m[2]
	}
	return out
}

func sameMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

// covers D184 §2.3 — the person's appearance rides the D4 injection: a
// person who never chose (or whose bucket holds anything but the values the
// theme defines) gets today's bytes; light, dark and comfortable add their
// meta in the block, between the component meta and the frame token, before
// the document's own <head> content; the owner token reads root's bucket.
func TestAppearanceInjection(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	ana := w.session("ana")
	get := func(url string, opts ...reqOpt) string {
		t.Helper()
		rec := w.do(url, opts...)
		if rec.Code != 200 {
			t.Fatalf("%s: %d %s", url, rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}

	// No bucket at all: today's injection — the component meta is followed
	// by the frame token's, as before the appearance existed.
	today := w.do("/c/apps/a/", ana)
	body := today.Body.String()
	if len(appearanceIn(t, body)) != 0 || !strings.Contains(body, `<meta name="xbin-component" content="apps/a">`+"\n"+`<meta name="xbin-frame-token" content="`) {
		t.Fatalf("no pref: not today's injection:\n%s", body)
	}

	// Anything but a defined value is the default: byte for byte today's.
	for _, junk := range []map[string]any{
		{"layout": map[string]any{"screens": []any{}}, "fontSize": 13},
		{"theme": "system"},
		{"theme": "purple"},
		{"theme": 5},
		{"theme": `light"><script>alert(1)</script>`},
		{"theme": []string{"light"}},
		{"density": "compact"},
		{"density": "huge"},
		{"density": true},
	} {
		shellPrefs(t, w.root, "ana", junk)
		if rec := w.do("/c/apps/a/", ana); !same(rec, today) {
			t.Errorf("bucket %v: not today's bytes:\n%s", junk, rec.Body.String())
		}
	}
	p := prefsfile.Path(w.root, "ana", prefsfile.Root)
	if err := os.WriteFile(p, []byte(`{"theme":"light"`), 0o644); err != nil { // half a file: unreadable JSON
		t.Fatal(err)
	}
	if rec := w.do("/c/apps/a/", ana); !same(rec, today) {
		t.Errorf("a broken bucket: not today's bytes:\n%s", rec.Body.String())
	}

	for _, c := range []struct {
		prefs map[string]any
		want  map[string]string
	}{
		{map[string]any{"theme": "light"}, map[string]string{"theme": "light"}},
		{map[string]any{"theme": "dark", "layout": "x"}, map[string]string{"theme": "dark"}},
		{map[string]any{"density": "comfortable"}, map[string]string{"density": "comfortable"}},
		{map[string]any{"theme": "light", "density": "comfortable"}, map[string]string{"theme": "light", "density": "comfortable"}},
	} {
		shellPrefs(t, w.root, "ana", c.prefs)
		body := get("/c/apps/a/", ana)
		if got := appearanceIn(t, body); !sameMap(got, c.want) {
			t.Errorf("bucket %v: metas %v, want %v", c.prefs, got, c.want)
		}
		comp := strings.Index(body, `<meta name="xbin-component"`)
		tok := strings.Index(body, `<meta name="xbin-frame-token"`)
		own := strings.Index(body, "<title>a</title>")
		for _, m := range appearanceMeta.FindAllStringIndex(body, -1) {
			if m[0] < comp || m[0] > tok || m[0] > own {
				t.Errorf("bucket %v: a meta outside the block, or after the document's own head:\n%s", c.prefs, body)
			}
		}
	}

	// The owner token (and its frames) reads root's bucket; the session reads
	// its own person's, never root's.
	shellPrefs(t, w.root, "ana", map[string]any{"theme": "light"})
	shellPrefs(t, w.root, prefsfile.Root, map[string]any{"theme": "dark", "density": "comfortable"})
	owner := cookie(auth.CookieName, w.a.OwnerTokenValue())
	if got := appearanceIn(t, get("/c/apps/a/", owner)); !sameMap(got, map[string]string{"theme": "dark", "density": "comfortable"}) {
		t.Errorf("the owner token: %v, want root's dark + comfortable", got)
	}
	if got := appearanceIn(t, get("/c/shell/", owner)); got["theme"] != "dark" {
		t.Errorf("the owner's shell (chrome): %v, want dark", got)
	}
	if got := appearanceIn(t, get("/c/apps/a/", ana)); !sameMap(got, map[string]string{"theme": "light"}) {
		t.Errorf("ana's session: %v, want her light", got)
	}
	if got := appearanceIn(t, get("/c/apps/a/", w.frame("apps/a", "ana"))); !sameMap(got, map[string]string{"theme": "light"}) {
		t.Errorf("ana's frame token: %v, want her light", got)
	}
	if got := appearanceIn(t, get("/c/apps/a/", w.frame("apps/a", ""))); got["theme"] != "dark" {
		t.Errorf("the owner's frame token: %v, want root's dark", got)
	}
	if got := appearanceIn(t, get("/c/apps/a/", hdr("Authorization", "Bearer "+w.a.MintTerminal("apps/a", "ana")))); got["theme"] != "light" {
		t.Errorf("ana's terminal on the tile: %v, want her light", got)
	}
	// bob has no bucket: the defaults, whatever ana and root chose.
	if got := appearanceIn(t, get("/c/apps/b/", w.session("bob"))); len(got) != 0 {
		t.Errorf("bob (no bucket): %v", got)
	}
}

// covers D184 §2.3 — while an admin views as someone (D64) a document looks
// as that person sees it: their appearance, not the admin's; a tile's
// backend gets none (it has no document to paint), nor does a
// credential-less subresource load.
func TestAppearancePerson(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	shellPrefs(t, w.root, prefsfile.Root, map[string]any{"theme": "dark"})
	shellPrefs(t, w.root, "ana", map[string]any{"theme": "light", "density": "comfortable"})

	tk, err := w.a.NewImpersonationTicket(auth.Principal{Owner: true, Via: "cookie"}, "ana")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := w.a.RedeemImpersonation(tk, auth.Principal{Owner: true, Via: "cookie"}, "", "192.0.2.1")
	if err != nil {
		t.Fatal(err)
	}
	rec := w.do("/c/apps/a/", cookie(auth.CookieName, sid))
	if got := appearanceIn(t, rec.Body.String()); rec.Code != 200 || !sameMap(got, map[string]string{"theme": "light", "density": "comfortable"}) {
		t.Errorf("view-as ana: %d %v, want ana's light + comfortable", rec.Code, got)
	}

	w.a.RegisterInstance("inst-a", "apps/a")
	rec = w.do("/c/apps/a/", hdr("Authorization", "Bearer inst-a"))
	if rec.Code != 200 || len(appearanceIn(t, rec.Body.String())) != 0 {
		t.Errorf("the tile's backend: %d, metas %v — want the document without any", rec.Code, appearanceIn(t, rec.Body.String()))
	}

	for _, c := range []struct {
		name string
		p    auth.Principal
		want string
		ok   bool
	}{
		{"a session", auth.Principal{UserID: "ana", Via: "session"}, "ana", true},
		{"the app's device", auth.Principal{UserID: "ana", Via: "device"}, "ana", true},
		{"view-as", auth.Principal{UserID: "ana", Via: "session", Impersonator: "owner"}, "ana", true},
		{"ana's frame", auth.Principal{UserID: "ana", Component: "apps/a", Via: "frame"}, "ana", true},
		{"ana's terminal", auth.Principal{UserID: "ana", Component: "apps/a", Via: "terminal"}, "ana", true},
		{"the owner token", auth.Principal{Owner: true, Via: "bearer"}, prefsfile.Root, true},
		{"--no-auth", auth.Principal{Owner: true, Via: "dev"}, prefsfile.Root, true},
		{"the owner's frame", auth.Principal{Component: "apps/a", Via: "frame"}, prefsfile.Root, true},
		{"an instance", auth.Principal{Component: "apps/a", Via: "instance"}, "", false},
		{"a cron tick", auth.Principal{Component: "apps/a", Via: "cron"}, "", false},
		{"a credential-less subresource", auth.Principal{}, "", false},
	} {
		got, ok := appearancePerson(c.p)
		if got != c.want || ok != c.ok {
			t.Errorf("%s: (%q, %v), want (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

// covers D184 §2.3 — every injected document carries it: a deployment URL
// (/c/<tile>+<name>/), a partitioned tile's document (beside its partition
// meta) and the native runtime document (?native=1).
func TestAppearanceEveryDocument(t *testing.T) {
	t.Run("deployment", func(t *testing.T) {
		w := newDepWS(t, TileAssetsLegacy)
		shellPrefs(t, w.root, "wes", map[string]any{"theme": "light"})
		for _, url := range []string{"/c/apps/a/", "/c/apps/a+dev/", "/c/apps/a+dev/?native=1"} {
			rec := w.do(url, w.session("wes"))
			if got := appearanceIn(t, rec.Body.String()); rec.Code != 200 || got["theme"] != "light" {
				t.Errorf("%s: %d %v, want wes's light", url, rec.Code, got)
			}
		}
	})
	t.Run("partitioned", func(t *testing.T) {
		w := partServer(t)
		shellPrefs(t, w.root, "ana", map[string]any{"theme": "dark", "density": "comfortable"})
		body := w.do("/c/apps/a/", w.session("ana")).Body.String()
		if got := appearanceIn(t, body); !sameMap(got, map[string]string{"theme": "dark", "density": "comfortable"}) || !strings.Contains(body, `<meta name="xbin-partition" content="user:ana">`) {
			t.Errorf("a partitioned tile: %v in\n%s", got, body)
		}
	})
	t.Run("native runtime document", func(t *testing.T) {
		w := newAssetWS(t, TileAssetsLegacy)
		writeWS(t, w.root, map[string]string{"apps/a/native.js": `export default function tile() {}`})
		if err := w.s.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		shellPrefs(t, w.root, "ana", map[string]any{"theme": "light"})
		rec := w.do("/c/apps/a/?native=1", w.session("ana"))
		if got := appearanceIn(t, rec.Body.String()); rec.Code != 200 || got["theme"] != "light" || !strings.Contains(rec.Body.String(), `name="xbin-native"`) {
			t.Errorf("the native runtime document: %d %v\n%s", rec.Code, got, rec.Body.String())
		}
	})
	for _, mode := range []string{TileAssetsTokens, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			shellPrefs(t, w.root, "ana", map[string]any{"theme": "light"})
			if got := appearanceIn(t, injectedFor(t, w, "apps/a", "ana")); got["theme"] != "light" {
				t.Errorf("%s: %v, want ana's light", mode, got)
			}
		})
	}
}

// injectedFor is the D4 block of tile's index.html as uid opens it, in any
// asset mode (headInjection itself: the origins mode's document is reached
// through its tile origin's ticket exchange, which tileorigin_test.go
// covers).
func injectedFor(t *testing.T, w *assetWS, tile, uid string) string {
	t.Helper()
	comp, ok := w.s.Reg.Component(tile)
	if !ok {
		t.Fatalf("%s not registered", tile)
	}
	r := httptest.NewRequest("GET", "/c/"+tile+"/", nil)
	r.Header.Set("Sec-Fetch-Mode", "navigate")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: uid, Via: "session"}))
	return w.s.headInjection(r, comp, tile, []byte(assetPage))
}

// covers D184 §2.3 — the bucket is cached by the file (identity, size and
// modification time): a load is a stat, and every rewrite is seen — a
// replacement (the plane's atomic write), and a rewrite in place of the
// same size.
func TestAppearanceCache(t *testing.T) {
	root := t.TempDir()
	p := shellPrefs(t, root, "ana", map[string]any{"theme": "light"})
	if a := bucketAppearance(p); a.theme != "light" {
		t.Fatalf("first read: %+v", a)
	}
	// A hit: the cached answer, the file not read again.
	appearanceCache.Lock()
	e := appearanceCache.m[p]
	e.a = appearance{theme: "cached"}
	appearanceCache.m[p] = e
	appearanceCache.Unlock()
	if a := bucketAppearance(p); a.theme != "cached" {
		t.Fatalf("an unchanged file was read again: %+v", a)
	}
	// A replacement (prefsfile.Write renames a new file into place).
	shellPrefs(t, root, "ana", map[string]any{"theme": "dark"})
	if a := bucketAppearance(p); a.theme != "dark" {
		t.Fatalf("after a replacement: %+v", a)
	}
	// In place, the same size, a later modification time.
	if err := os.WriteFile(p, []byte(`{"theme":"dark","x":"a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if a := bucketAppearance(p); a.theme != "dark" {
		t.Fatalf("in place: %+v", a)
	}
	if err := os.WriteFile(p, []byte(`{"theme":"light","x":""}`), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(2 * time.Second)
	if err := os.Chtimes(p, later, later); err != nil {
		t.Fatal(err)
	}
	if a := bucketAppearance(p); a.theme != "light" {
		t.Fatalf("a same-size rewrite in place: %+v", a)
	}
	// Gone: the defaults.
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if a := bucketAppearance(p); a != (appearance{}) {
		t.Fatalf("no bucket: %+v", a)
	}
}

// covers D184 §2.7 — the docs viewer follows the person: it opts in,
// carries their metas (none when they never chose), and styles itself from
// tokens only: no colour, font stack or radius of its own.
func TestDocViewerAppearance(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	w.s.DocsFS = fstest.MapFS{"index.md": {Data: []byte("# docs\n")}}
	view := func(opts ...reqOpt) string {
		t.Helper()
		rec := w.do("/docs/index.md", append([]reqOpt{hdr("Accept", "text/html")}, opts...)...)
		if rec.Code != 200 {
			t.Fatalf("/docs/index.md: %d %s", rec.Code, rec.Body.String())
		}
		return rec.Body.String()
	}
	body := view(w.session("ana"))
	if !strings.Contains(body, `<html lang="en" data-bx-theme="auto">`) || !strings.Contains(body, `<link rel="stylesheet" href="/vendor/theme.css">`) || len(appearanceIn(t, body)) != 0 {
		t.Fatalf("the viewer, no pref: not opted in, or metas:\n%s", body)
	}
	head, _, _ := strings.Cut(body, "<body")
	style := head[strings.Index(head, "<style>"):]
	for _, bad := range []*regexp.Regexp{
		regexp.MustCompile(`#[0-9a-fA-F]{3,8}\b`), regexp.MustCompile(`\brgba?\(`),
		regexp.MustCompile(`system-ui|-apple-system|sans-serif|monospace|Segoe`),
		regexp.MustCompile(`border-radius:\s*[1-9]`), regexp.MustCompile(`var\(--bx-[a-z0-9-]+,`),
	} {
		if m := bad.FindString(style); m != "" {
			t.Errorf("the viewer's styles hold %q (tokens only)", m)
		}
	}
	shellPrefs(t, w.root, "ana", map[string]any{"theme": "light", "density": "comfortable"})
	body = view(w.session("ana"))
	if got := appearanceIn(t, body); !sameMap(got, map[string]string{"theme": "light", "density": "comfortable"}) {
		t.Errorf("the viewer: %v, want ana's light + comfortable", got)
	}
	if i, j := strings.Index(body, `name="xbin-theme"`), strings.Index(body, `href="/vendor/theme.css"`); i < 0 || i > j {
		t.Errorf("the theme meta must come before the stylesheet:\n%s", body)
	}
}

// covers D184 §2.8 — the fonts are served from /vendor/fonts/ to anyone,
// as fonts, and a sandboxed frame's cross-origin font load (Origin: null)
// gets its CORS answer on the workspace origin in the legacy and tokens
// modes (in origins mode a tile's document is its own origin, so its fonts
// are same-origin).
func TestVendorFonts(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens} {
		w := newAssetWS(t, mode)
		w.s.WebFS = os.DirFS(filepath.Join("..", "..", "web"))
		for _, f := range []string{"instrument-sans-400.woff2", "instrument-sans-600-ext.woff2", "jetbrains-mono-400.woff2", "bricolage-grotesque-800.woff2"} {
			rec := w.do("/vendor/fonts/"+f, hdr("Origin", "null"), hdr("Sec-Fetch-Mode", "cors"), hdr("Sec-Fetch-Site", "cross-site"), hdr("Sec-Fetch-Dest", "font"))
			if rec.Code != 200 || rec.Header().Get("Content-Type") != "font/woff2" || rec.Header().Get("Access-Control-Allow-Origin") != "null" || !strings.HasPrefix(rec.Body.String(), "wOF2") {
				t.Errorf("%s: /vendor/fonts/%s: %d %q ACAO %q", mode, f, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Access-Control-Allow-Origin"))
			}
		}
		if rec := w.do("/vendor/fonts/OFL-instrument-sans.txt"); rec.Code != 200 || !strings.Contains(rec.Body.String(), "SIL Open Font License") {
			t.Errorf("%s: the licence: %d", mode, rec.Code)
		}
	}
	// theme.css names only files that exist, each with its own version: the
	// immutable answer below is reachable, and a font whose bytes change
	// can't hide behind its old URL in a browser's cache.
	css, err := os.ReadFile(filepath.Join("..", "..", "web", "theme.css"))
	if err != nil {
		t.Fatal(err)
	}
	urls := regexp.MustCompile(`url\("(fonts/[^"?]+)\?v=([0-9a-f]+)"\)`).FindAllStringSubmatch(string(css), -1)
	if n := len(regexp.MustCompile(`url\("fonts/`).FindAllString(string(css), -1)); len(urls) < 13 || len(urls) != n {
		t.Fatalf("theme.css names %d font files, %d of them with a ?v= version", n, len(urls))
	}
	for _, u := range urls {
		b, err := os.ReadFile(filepath.Join("..", "..", "web", "vendor", u[1]))
		if err != nil {
			t.Errorf("theme.css names %s: %v", u[1], err)
			continue
		}
		sum := sha256.Sum256(b)
		if want := FontVersion(hex.EncodeToString(sum[:])); u[2] != want {
			t.Errorf("theme.css names %s?v=%s, but its bytes are version %s — write that in its url()", u[1], u[2], want)
		}
	}
}

// covers D184 (the review's font flash) — /vendor/ answers carry a strong
// ETag under no-cache, so a reload revalidates to a 304 without the bytes; a
// font asked for with its version is immutable (no round trip at all), any
// other version or none is not; HEAD and ranges still work; the fonts' type
// doesn't depend on the host's MIME files.
func TestVendorCaching(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	w.s.WebFS = os.DirFS(filepath.Join("..", "..", "web"))
	if got := mime.TypeByExtension(".woff2"); got != "font/woff2" {
		t.Fatalf(".woff2 is %q, want font/woff2 whatever the host's mime.types says", got)
	}
	css := w.do("/vendor/theme.css")
	etag := css.Header().Get("ETag")
	if css.Code != 200 || css.Header().Get("Cache-Control") != "no-cache" || !regexp.MustCompile(`^"[0-9a-f]{32}"$`).MatchString(etag) {
		t.Fatalf("/vendor/theme.css: %d Cache-Control %q ETag %q", css.Code, css.Header().Get("Cache-Control"), etag)
	}
	if again := w.do("/vendor/theme.css", hdr("If-None-Match", etag)); again.Code != http.StatusNotModified || again.Body.Len() != 0 {
		t.Errorf("a revalidation with the ETag: %d, %d bytes — want 304 and none", again.Code, again.Body.Len())
	}
	if other := w.do("/vendor/theme.css", hdr("If-None-Match", `"0000"`)); other.Code != 200 || other.Body.Len() == 0 {
		t.Errorf("a stale ETag: %d, want the file", other.Code)
	}
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "vendor", "fonts", "instrument-sans-400.woff2"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(b)
	v := FontVersion(hex.EncodeToString(sum[:]))
	for _, c := range []struct{ q, want string }{
		{"?v=" + v, fontImmutable},
		{"?v=00000000", "no-cache"},
		{"", "no-cache"},
	} {
		rec := w.do("/vendor/fonts/instrument-sans-400.woff2"+c.q, hdr("Origin", "null"))
		if rec.Code != 200 || rec.Header().Get("Cache-Control") != c.want || rec.Header().Get("Content-Type") != "font/woff2" || rec.Header().Get("Access-Control-Allow-Origin") != "null" {
			t.Errorf("font %q: %d Cache-Control %q type %q ACAO %q", c.q, rec.Code, rec.Header().Get("Cache-Control"), rec.Header().Get("Content-Type"), rec.Header().Get("Access-Control-Allow-Origin"))
		}
	}
	if rec := w.do("/vendor/bx-theme.js?v=" + v); rec.Header().Get("Cache-Control") != "no-cache" {
		t.Errorf("a ?v= outside fonts/ is not a version: %q", rec.Header().Get("Cache-Control"))
	}
	if rec := w.do("/vendor/fonts/instrument-sans-400.woff2", hdr("Range", "bytes=0-3")); rec.Code != http.StatusPartialContent || rec.Body.String() != "wOF2" {
		t.Errorf("a range: %d %q", rec.Code, rec.Body.String())
	}
	// the page's own copy and a sandboxed frame's differ (CORS headers), so
	// every answer varies by Origin: a cached immutable font of the page's
	// is never handed to the frame
	for _, o := range []string{"", "null"} {
		var opts []reqOpt
		if o != "" {
			opts = append(opts, hdr("Origin", o))
		}
		rec := w.do("/vendor/fonts/instrument-sans-400.woff2?v="+v, opts...)
		if vary := rec.Header().Values("Vary"); len(vary) != 1 || vary[0] != "Origin" {
			t.Errorf("Origin %q: Vary %q, want exactly Origin", o, vary)
		}
	}
}
