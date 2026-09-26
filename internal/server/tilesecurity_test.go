package server

import (
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

var allModes = []string{TileAssetsLegacy, TileAssetsTokens, TileAssetsOrigins}

// secretOf is the workspace HMAC secret — what every leak below would hand
// out (it forges any frame token or owner credential).
func (w *assetWS) secretOf() string {
	return string(mustRead(w.t, filepath.Join(w.root, ".xbin/secret")))
}

// assetReq: a credentialed /c/ load of path in mode, as uid.
func (w *assetWS) assetReq(mode, p, uid string) *http.Response {
	w.t.Helper()
	if mode != TileAssetsOrigins {
		return w.do(p, w.session(uid)).Result()
	}
	tile := w.s.owningComponent(strings.TrimPrefix(p, "/c/"))
	c, _ := w.exchange(tile, uid, "/c/"+tile+"/")
	if c == nil {
		w.t.Fatalf("no tile cookie for %s", tile)
	}
	return w.do(p, host(w.originHost(tile)), cookie(c.Name, c.Value), sameOrig).Result()
}

func body(r *http.Response) string {
	b := new(strings.Builder)
	_, _ = bufioCopy(b, r)
	return b.String()
}

func bufioCopy(b *strings.Builder, r *http.Response) (int64, error) {
	defer r.Body.Close()
	buf := make([]byte, 4096)
	var n int64
	for {
		k, err := r.Body.Read(buf)
		b.Write(buf[:k])
		n += int64(k)
		if err != nil {
			return n, nil
		}
	}
}

// PoC (review): a nested component (a sub-directory with index.html,
// registered as its own component) swapped for a symlink to ../../.xbin
// before the — trailing-debounced, so arbitrarily delayed — rescan made
// the strict planes open "the tile" at .xbin and serve the HMAC secret to
// its writer's session and asset token. The owner directory is now reached
// without any symlink (fsutil.OpenIn); legacy re-resolves the target.
func TestStaleNestedComponentSymlink(t *testing.T) {
	for _, mode := range allModes {
		w := newAssetWS(t, mode)
		sub := filepath.Join(w.root, "apps/a/sub2")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "index.html"), []byte(assetPage), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := w.s.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.Upsert(users.User{ID: "mal", Role: users.RoleUser, Tiles: map[string]string{"apps/a/*": users.LevelWrite}}, "password1"); err != nil {
			t.Fatal(err)
		}
		var ca *http.Cookie
		if mode == TileAssetsOrigins { // the cookie of the nested component's origin, got while it was real
			ca, _ = w.exchange("apps/a/sub2", "mal", "/c/apps/a/sub2/")
		}
		if err := os.RemoveAll(sub); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("../../.xbin", sub); err != nil {
			t.Fatal(err)
		}
		secret := w.secretOf()
		var rec *http.Response
		if mode == TileAssetsOrigins {
			rec = w.do("/c/apps/a/sub2/secret", host(w.originHost("apps/a/sub2")), cookie(ca.Name, ca.Value), sameOrig).Result()
		} else {
			rec = w.do("/c/apps/a/sub2/secret", w.session("mal")).Result()
		}
		if b := body(rec); rec.StatusCode == 200 || strings.Contains(b, secret) {
			t.Errorf("%s: stale nested component served %d %q", mode, rec.StatusCode, b)
		}
		if mode == TileAssetsTokens {
			tok := w.a.MintAssetToken("apps/a/sub2", "mal")
			rec := w.do("/c/~"+tok+"/apps/a/sub2/secret", hdr("Sec-Fetch-Dest", "script"))
			if rec.Code == 200 || strings.Contains(rec.Body.String(), secret) {
				t.Errorf("tokens: asset token served %d", rec.Code)
			}
		}
	}
}

// PoC (review): the legacy plane checked a path's symlink target and then
// re-opened it by name, so a tile writer looping a rename between a file
// and a symlink to ../../.xbin/secret won the race within a few requests
// and read the HMAC secret. Every plane now serves the very file it
// checked; the race must never win.
func TestSymlinkSwapRace(t *testing.T) {
	for _, mode := range allModes {
		w := newAssetWS(t, mode)
		dir := filepath.Join(w.root, "apps/a")
		target := filepath.Join(dir, "f.txt")
		if err := os.WriteFile(target, []byte("public"), 0o644); err != nil {
			t.Fatal(err)
		}
		secret := w.secretOf()
		// The credential first (origins: an exchange), then race.
		var get func() *http.Response
		if mode == TileAssetsOrigins {
			c, _ := w.exchange("apps/a", "ana", "/c/apps/a/")
			get = func() *http.Response {
				return w.do("/c/apps/a/f.txt", host(w.originHost("apps/a")), cookie(c.Name, c.Value), sameOrig).Result()
			}
		} else {
			sess := w.session("ana")
			get = func() *http.Response { return w.do("/c/apps/a/f.txt", sess).Result() }
		}
		w.do("/healthz") // builds the handler before the goroutines share it
		stop := make(chan struct{})
		var swapper sync.WaitGroup
		swapper.Add(1)
		go func() {
			defer swapper.Done()
			for i := 0; ; i++ {
				select {
				case <-stop:
					return
				default:
				}
				tmp := filepath.Join(dir, ".swap")
				if i%2 == 0 {
					_ = os.Symlink("../../.xbin/secret", tmp)
				} else {
					_ = os.WriteFile(tmp, []byte("public"), 0o644)
				}
				_ = os.Rename(tmp, target)
			}
		}()
		var served, leaked atomic.Int64
		var readers sync.WaitGroup
		deadline := time.Now().Add(700 * time.Millisecond)
		for g := 0; g < 4; g++ {
			readers.Add(1)
			go func() {
				defer readers.Done()
				for time.Now().Before(deadline) {
					r := get()
					b := body(r)
					if strings.Contains(b, secret) {
						leaked.Add(1)
					} else if r.StatusCode == 200 {
						served.Add(1)
					}
				}
			}()
		}
		readers.Wait()
		close(stop)
		swapper.Wait()
		if leaked.Load() > 0 {
			t.Errorf("%s: the swap race leaked the secret %d times", mode, leaked.Load())
		}
		if served.Load() == 0 {
			t.Errorf("%s: never served the regular file — the race wasn't exercised", mode)
		}
	}
}

// Legacy too: a sandboxed tile's non-document file is inert if navigated
// to (CSP sandbox) — an SVG, .xhtml, .shtml or an extensionless file
// sniffed as HTML would otherwise run tile-written script as the workspace
// origin, beside the session cookie. PDFs and chrome are untouched; a
// document is found by extension in any case (x.HTML is injected and
// sandboxed, never raw).
func TestNonDocumentsInertInEveryMode(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens} {
		w := newAssetWS(t, mode)
		for name, c := range map[string]string{
			"x.xhtml": `<html xmlns="http://www.w3.org/1999/xhtml"><script>alert(1)</script></html>`,
			"x.shtml": `<script>alert(1)</script>`,
			"noext":   `<!doctype html><script>alert(1)</script>`,
			"X.HTML":  assetPage,
		} {
			if err := os.WriteFile(filepath.Join(w.root, "apps/a", name), []byte(c), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for p, want := range map[string]string{
			"/c/apps/a/img.svg":      "sandbox",
			"/c/apps/a/x.xhtml":      "sandbox",
			"/c/apps/a/x.shtml":      "sandbox",
			"/c/apps/a/noext":        "sandbox",
			"/c/apps/a/app.js":       "sandbox",
			"/c/apps/a/doc.pdf":      "",
			"/c/shell/shell.js":      "",
			"/c/apps/a/X.HTML":       sandboxCSP,
			"/c/apps/a/":             sandboxCSP,
			"/c/apps/raw/index.html": sandboxCSP, // inject:false: ServeFile's /index.html → ./ redirect, kept
		} {
			rec := w.do(p, w.session("ana"))
			if got := rec.Header().Get("Content-Security-Policy"); got != want {
				t.Errorf("%s %s: CSP %q, want %q (%d)", mode, p, got, want, rec.Code)
			}
		}
		if rec := w.do("/c/apps/a/X.HTML", w.session("ana")); !strings.Contains(rec.Body.String(), "xbin-frame-token") {
			t.Errorf("%s: X.HTML not treated as a document", mode)
		}
		if rec := w.do("/c/apps/raw/index.html?q=1", w.session("ana")); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "./?q=1" {
			t.Errorf("%s: inject:false index.html: %d %q", mode, rec.Code, rec.Header().Get("Location"))
		}
	}
}

// The legacy plane keeps working symlinks between tiles and absolute
// symlinks inside the workspace, and refuses what resolves to xbind's own
// files, reserved trees or outside the workspace.
func TestLegacySymlinkTargets(t *testing.T) {
	w := newAssetWS(t, TileAssetsLegacy)
	for link, to := range map[string]string{
		"apps/a/shared.js":  "../b/lib.js",
		"apps/a/abs.js":     filepath.Join(w.root, "apps/b/lib.js"),
		"apps/a/host.txt":   "/etc/hostname",
		"apps/a/data.json":  "../../data/x.json",
		"apps/a/links":      "../b",
		"apps/a/deep/g.txt": "../../../.xbin/token",
	} {
		p := filepath.Join(w.root, link)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(to, p); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(w.root, "data"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.root, "data/x.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for p, want := range map[string]int{
		"/c/apps/a/leak.txt": 404, "/c/apps/a/host.txt": 404, "/c/apps/a/data.json": 404, "/c/apps/a/deep/g.txt": 404,
		"/c/apps/a/shared.js": 200, "/c/apps/a/abs.js": 200, "/c/apps/a/links/lib.js": 200, "/c/apps/a/app.js": 200,
	} {
		if rec := w.do(p, w.session("ana")); rec.Code != want {
			t.Errorf("%s: %d, want %d", p, rec.Code, want)
		}
	}
}

// A multi-page tile whose sub-page directory holds index.html has that
// directory registered as a nested component. Navigating its frame between
// its own pages — parent → sub-page, back, sub-page → sibling — with the
// current page's token (xbin.url, tokens mode's link handler) mints the
// target page's token again (review: it minted none, so the page's
// xbin.fetch 401ed and, in tokens mode, its relative loads failed). A
// fetch of the page (readable) still gets none, and nothing crosses to
// another tile tree.
func TestNestedPageNavigationMints(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens} {
		w := newAssetWS(t, mode)
		for _, d := range []string{"apps/a/settings", "apps/a/profile"} {
			if err := os.MkdirAll(filepath.Join(w.root, d), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(w.root, d, "index.html"), []byte(assetPage), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := w.s.Reg.Rescan(); err != nil {
			t.Fatal(err)
		}
		if _, err := w.st.Upsert(users.User{ID: "cy", Role: users.RoleUser, Tiles: map[string]string{"apps/a/*": users.LevelRead, "apps/b": users.LevelRead}}, "password1"); err != nil {
			t.Fatal(err)
		}
		nav := []reqOpt{hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Dest", "iframe"), hdr("Sec-Fetch-Site", "cross-site")}
		fetch := []reqOpt{hdr("Sec-Fetch-Mode", "cors"), hdr("Sec-Fetch-Dest", "empty"), hdr("Sec-Fetch-Site", "cross-site")}
		minted := func(from, path string, opts []reqOpt) string {
			tok := w.a.MintFrameToken(from, "cy", time.Minute)
			rec := w.do(path+"?frame="+url.QueryEscape(tok), opts...)
			m := injectedFrameToken.FindStringSubmatch(rec.Body.String())
			if rec.Code != 200 || m == nil {
				t.Fatalf("%s %s from %s: %d", mode, path, from, rec.Code)
			}
			if m[1] != "" && mode == TileAssetsTokens && !strings.Contains(rec.Body.String(), "data-xbin-assets") {
				t.Errorf("%s: %s minted a token but no asset <base>", mode, path)
			}
			c, _, _ := w.a.VerifyFrameToken(m[1])
			return c
		}
		for _, c := range []struct{ from, path, want string }{
			{"apps/a", "/c/apps/a/settings/", "apps/a/settings"},
			{"apps/a/settings", "/c/apps/a/", "apps/a"},
			{"apps/a/settings", "/c/apps/a/profile/", "apps/a/profile"},
			{"apps/b", "/c/apps/a/settings/", ""}, // another tree
		} {
			if got := minted(c.from, c.path, nav); got != c.want {
				t.Errorf("%s: navigating %s → %s minted %q, want %q", mode, c.from, c.path, got, c.want)
			}
		}
		if got := minted("apps/a", "/c/apps/a/settings/", fetch); got != "" {
			t.Errorf("%s: a fetch of the sub-page lifted its token (%q)", mode, got)
		}
	}
}

// Origins mode: a multi-page tile's in-frame navigation to a page that is a
// nested component (its own origin) goes through the workspace with the
// tile origin as Referer — same tree: the cookie is kept and the ticket
// minted; another tree's origin gets nothing.
func TestOriginsNavigationWithinTree(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	d := filepath.Join(w.root, "apps/a/settings")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "index.html"), []byte(assetPage), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	// ana reads apps/a (exact) — give her the sub-page too.
	u, _ := w.st.Get("ana")
	u.Tiles["apps/a/settings"] = users.LevelRead
	if _, err := w.st.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	from := func(tile string) *http.Response {
		return w.do("/c/apps/a/settings/", append(hopNav, w.session("ana"), hdr("Referer", "http://"+w.originHost(tile)+"/"))...).Result()
	}
	rec := from("apps/a")
	if loc := rec.Header.Get("Location"); rec.StatusCode != http.StatusFound || !strings.HasPrefix(loc, "http://"+w.originHost("apps/a/settings")+"/c/apps/a/settings/?"+ticketParam+"=") {
		t.Fatalf("in-tree navigation: %d %q", rec.StatusCode, loc)
	}
	if rec := from("apps/b"); strings.Contains(rec.Header.Get("Location"), ticketParam) {
		t.Fatalf("another tree's origin got a ticket: %q", rec.Header.Get("Location"))
	}
	_ = auth.CookieName
}
