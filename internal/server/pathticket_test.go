package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Path tickets (D135): a tile's page mints one for a prefix of its own API;
// /api/~<ticket>/<rest> reaches exactly /api/<tile>/<prefix>/<rest> as that
// page's frame principal, only from an address that signed in, never a
// dot segment out of the prefix, never on another tile's origin; nothing
// but a tile's page mints one.
func TestPathTickets(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsOrigins} {
		t.Run(mode, func(t *testing.T) { testPathTickets(t, mode) })
	}
}

func testPathTickets(t *testing.T, mode string) {
	w := newZeroStateWS(t, mode)
	ft := w.a.MintFrameToken("apps/a", "ana", time.Hour)
	host := "xbin.localhost:9260"
	if mode == TileAssetsOrigins {
		host = w.a.TileHostID("apps/a") + ".xbin.localhost:9260"
	}
	send := func(method, target, body, from string, opts ...reqOpt) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, target, strings.NewReader(body))
		r.Host = host
		if from != "" {
			r.RemoteAddr = from + ":1234"
		}
		for _, o := range opts {
			o(r)
		}
		rec := httptest.NewRecorder()
		if w.h == nil {
			w.h = w.s.Handler()
		}
		w.h.ServeHTTP(rec, r)
		return rec
	}
	mint := func(body string, opts ...reqOpt) (int, string) {
		t.Helper()
		rec := send("POST", "/api/xbin/path-tickets", body, "", opts...)
		var out struct {
			URL string `json:"url"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out.URL
	}
	frame := hdr(auth.FrameTokenHeader, ft)

	code, u := mint(`{"path":"runs/5/live/sb-1/8000"}`, frame) // a real auth: warms 192.0.2.1
	if code != http.StatusOK || !strings.HasPrefix(u, "/api/~p1.") || !strings.HasSuffix(u, "/") {
		t.Fatalf("mint: %d %q", code, u)
	}
	get := func(target, from string) *httptest.ResponseRecorder { return send("GET", target, "", from) }
	if mode == TileAssetsOrigins { // the shell's own calls on the workspace origin warm the address
		tileHost := host
		host = "xbin.localhost:9260"
		get("/api/xbin/status", "").Result()
		send("GET", "/api/xbin/status", "", "", w.session("ana"))
		host = tileHost
	}
	rec := get(u+"a%20b/app.js?x=1&frame=evil", "")
	want := `component api: GET /api/apps/a/runs/5/live/sb-1/8000/a%20b/app.js?x=1&frame=evil from="apps/a" via="frame" user="ana"`
	if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Body.String(), want) {
		t.Fatalf("through the ticket: %d %q\nwant %q", rec.Code, rec.Body.String(), want)
	}
	if rec := send("POST", u+"form", "k=v", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "POST /api/apps/a/runs/5/live/sb-1/8000/form") {
		t.Fatalf("a POST: %d %s", rec.Code, rec.Body)
	}
	if rec := get(strings.TrimSuffix(u, "/"), ""); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != u {
		t.Fatalf("no slash: %d %v", rec.Code, rec.Header())
	}
	for _, bad := range []string{u + "%2E%2E/%2e%2e/view", u + "x%2F..%2F..%2Fview", u + "%2e/x"} {
		if rec := get(bad, ""); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body)
		}
	}
	tok := strings.TrimSuffix(strings.TrimPrefix(u, "/api/~"), "/")
	for name, bad := range map[string]string{
		"tampered":         tok[:len(tok)-2] + "xx",
		"a frame token":    ft,
		"an asset token":   w.a.MintAssetToken("apps/a", "ana"),
		"claims of others": strings.Replace(tok, tok[3:10], "AAAAAAA", 1),
	} {
		if rec := get("/api/~"+bad+"/x", ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
	if rec := get(u+"x", "198.51.100.7"); rec.Code != http.StatusUnauthorized {
		t.Errorf("from an address that never signed in: %d %s", rec.Code, rec.Body)
	}
	// minting: a tile's page only, and a clean prefix
	sess := w.session("ana")
	if code, _ := mint(`{"path":"runs/5"}`, sess); code != http.StatusForbidden && code != http.StatusUnauthorized {
		t.Errorf("a person's session mints: %d", code)
	}
	for _, p := range []string{"", "../x", "a/../b", "a/./b", "a%2Fb", "a?b", "a b", "/", strings.Repeat("a", 300)} {
		if code, _ := mint(`{"path":"`+p+`"}`, frame); code != http.StatusBadRequest {
			t.Errorf("path %q: %d", p, code)
		}
	}
	if mode == TileAssetsOrigins { // another tile's origin serves none of apps/a's tickets
		host = w.a.TileHostID("apps/b") + ".xbin.localhost:9260"
		if rec := get(u+"x", ""); rec.Code != http.StatusForbidden {
			t.Errorf("on apps/b's origin: %d %s", rec.Code, rec.Body)
		}
	}
}
