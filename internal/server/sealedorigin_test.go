package server

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// A sealed component in origins mode (--tile-assets=origins: every tile's
// frontend on an origin of its own). Its page gets an origin like a tile's,
// through the same ticket exchange; there its files are the release's, from
// memory; and the cookie of that origin — which makes /api there act as the
// component's frame principal — is a viewer's credential like its frame
// token: the component's own backend, its renewal, and nothing else.
func TestOriginsSealedComponent(t *testing.T) {
	w := newAssetWS(t, TileAssetsOrigins)
	const sealed = "xbin/probe"
	files := map[string][]byte{
		"xbin.json":  []byte(`{}`),
		"index.html": []byte("<!doctype html><html><head><title>p</title></head><body>release</body></html>"),
		"app.js":     []byte("export const v = 'release';\n"),
	}
	// the copy on disk says otherwise: it is never what is served
	dir := filepath.Join(t.TempDir(), "probe")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("from the disk"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	code := registry.SealedCode{Path: sealed, ID: "0123456789abcdef01234567", Dir: dir, Files: files, Manifest: files["xbin.json"]}
	reg, err := registry.OpenSealed(w.root, func() []registry.SealedCode { return []registry.SealedCode{code} })
	if err != nil {
		t.Fatal(err)
	}
	w.s.Reg = reg
	if err := w.st.SetUserTile("ana", sealed, users.LevelRead); err != nil {
		t.Fatal(err)
	}
	var seen []string // what reached the component's backend
	w.s.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		p := auth.PrincipalOf(r)
		seen = append(seen, r.URL.Path)
		WriteJSON(rw, 200, map[string]string{"component": p.Component, "user": p.UserID, "via": p.Via, "path": r.URL.Path, "query": r.URL.RawQuery})
	})
	w.s.RegisterAPI("GET /kv/{rest...}", func(rw http.ResponseWriter, r *http.Request) {
		WriteJSON(rw, 200, map[string]string{"value": "its data"})
	})

	// an origin of its own, not a tile's and not the workspace's
	origin := w.originHost(sealed)
	if origin == w.originHost("apps/a") || origin == "xbin.localhost:9260" || !strings.HasSuffix(origin, ".xbin.localhost:9260") {
		t.Fatalf("its origin: %s", origin)
	}
	// the exchange a tile goes through; bob, who can't read it, gets none
	c, rec := w.exchange(sealed, "ana", "/c/"+sealed+"/")
	if c == nil || rec.Code != http.StatusFound {
		t.Fatalf("the exchange for a reader: %d %v", rec.Code, rec.Header())
	}
	if u, rec := w.ticketURL("/c/"+sealed+"/", w.session("bob")); u != nil && u.Host == origin {
		if bc, _ := w.exchangeURL(u); bc != nil {
			t.Errorf("someone without access got its origin's cookie (%d)", rec.Code)
		}
	}
	o, ck := host(origin), cookie(c.Name, c.Value)
	nav := []reqOpt{o, ck, hdr("Sec-Fetch-Mode", "navigate"), hdr("Sec-Fetch-Site", "same-site")}

	// its document: the release's bytes, the code's id, and the workspace
	// origin its client talks to
	rec = w.do("/c/"+sealed+"/", nav...)
	body := rec.Body.String()
	if rec.Code != 200 || !strings.Contains(body, "<body>release</body>") || strings.Contains(body, "from the disk") ||
		!strings.Contains(body, `<meta name="xbin-code" content="0123456789abcdef01234567">`) ||
		!strings.Contains(body, `<meta name="xbin-workspace-origin" content="http://xbin.localhost:9260">`) {
		t.Fatalf("its document on its origin: %d %s", rec.Code, body)
	}
	if csp := rec.Header().Get("Content-Security-Policy"); !strings.Contains(csp, "frame-ancestors 'self' http://xbin.localhost:9260") {
		t.Errorf("its document's framing: %q", csp)
	}
	if rec := w.do("/c/"+sealed+"/app.js", o, ck); rec.Code != 200 || rec.Body.String() != "export const v = 'release';\n" {
		t.Errorf("its module on its origin: %d %q", rec.Code, rec.Body.String())
	}
	if rec := w.do("/c/"+sealed+"/planted.js", o, ck); rec.Code != 404 {
		t.Errorf("a file the release doesn't have: %d", rec.Code)
	}
	// nothing of a tile is on this origin, nor of it on a tile's
	if rec := w.do("/c/apps/a/app.js", o, ck); rec.Code == 200 {
		t.Errorf("a tile's file on the sealed component's origin: %d %s", rec.Code, rec.Body.String())
	}
	// (the other way round is a reader's read: a tile's page, for a person
	// who may read the component, loads its files as that person could)

	// /api on its origin: its frame principal, and that is a viewer
	rec = w.do("/api/"+sealed+"/hello?a=1", o, ck, sameOrig)
	var who struct{ Component, User, Via, Path, Query string }
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if rec.Code != 200 || who.Component != sealed || who.User != "ana" || who.Via != "frame" {
		t.Fatalf("its page's call to its backend, on its origin: %d %+v %s", rec.Code, who, rec.Body.String())
	}
	if rec := w.do("/api/xbin/frame-token?component="+sealed, o, ck, sameOrig); rec.Code != 200 {
		t.Errorf("its token's renewal on its origin: %d %s", rec.Code, rec.Body.String())
	}
	for _, path := range []string{"/api/xbin/kv/res:" + sealed + "/db/k", "/api/xbin/components", "/api/apps/a/x", "/api/xbin/frame-token?component=apps/a"} {
		before := len(seen)
		rec := w.do(path, o, ck, sameOrig)
		if rec.Code != http.StatusForbidden || len(seen) != before {
			t.Errorf("its origin's cookie reached %s: %d %s", path, rec.Code, rec.Body.String())
		}
	}
	// a page of another release's code on that origin is told to reload
	rec = w.do("/api/"+sealed+"/hello", o, ck, sameOrig, hdr(SealedCodeHeader, strings.Repeat("0", 24)))
	if rec.Code != http.StatusConflict || rec.Header().Get(SealedReloadHeader) != "0123456789abcdef01234567" {
		t.Errorf("a stale page on its origin: %d reload %q", rec.Code, rec.Header().Get(SealedReloadHeader))
	}
	rec = w.do("/api/"+sealed+"/hello?a=1&xbin-code=0123456789abcdef01234567", o, ck, sameOrig)
	_ = json.Unmarshal(rec.Body.Bytes(), &who)
	if rec.Code != 200 || who.Query != "a=1" {
		t.Errorf("the current page's URL on its origin: %d query %q", rec.Code, who.Query)
	}
}
