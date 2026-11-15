package server

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// A person's agent's page is framed by xbind's own chrome documents alone,
// in the asset modes where every tile's document is served from the
// workspace origin too (final review, 6): there frame-ancestors can't tell
// a tile's document from the shell — it matches by URL — so a framed load
// that a sandboxed document started (cross-site, by its opaque origin) is
// refused, and the document says frame-ancestors 'self' to every other
// site. (Origins mode: TestOriginsSealedComponent.)
func TestHolderPageIsFramedByChromeAlone(t *testing.T) {
	for _, mode := range []string{TileAssetsLegacy, TileAssetsTokens} {
		t.Run(mode, func(t *testing.T) {
			w := newAssetWS(t, mode)
			const holder, plain = "xbin/probe", "xbin/other"
			files := map[string][]byte{
				"xbin.json":  []byte(`{}`),
				"index.html": []byte("<!doctype html><html><head><title>p</title></head><body>release</body></html>"),
				"app.js":     []byte("export const v = 'release';\n"),
			}
			var codes []registry.SealedCode
			for i, p := range []string{holder, plain} {
				dir := filepath.Join(t.TempDir(), filepath.Base(p))
				if err := os.MkdirAll(dir, 0o755); err != nil {
					t.Fatal(err)
				}
				codes = append(codes, registry.SealedCode{Path: p, ID: strings.Repeat(string(rune('a'+i)), 24), Dir: dir, Files: files, Manifest: files["xbin.json"]})
			}
			reg, err := registry.OpenSealed(w.root, func() []registry.SealedCode { return codes })
			if err != nil {
				t.Fatal(err)
			}
			w.s.Reg = reg
			t.Cleanup(auth.TestSealDelegate(holder))
			for _, p := range []string{holder, plain} {
				if err := w.st.SetUserTile("ana", p, users.LevelRead); err != nil {
					t.Fatal(err)
				}
			}
			ana := w.session("ana")
			doc := func(path string, opts ...reqOpt) (int, string, string) {
				rec := w.do(path, append([]reqOpt{ana, hdr("Accept", "text/html"), hdr("Sec-Fetch-Mode", "navigate")}, opts...)...)
				return rec.Code, rec.Header().Get("Content-Security-Policy"), rec.Body.String()
			}
			framed := func(site string) []reqOpt {
				return []reqOpt{hdr("Sec-Fetch-Dest", "iframe"), hdr("Sec-Fetch-Site", site)}
			}
			// framed by xbind's chrome (the shell's dock, /xbin/agent): served,
			// sandboxed, and no other site may frame it
			code, csp, body := doc("/c/"+holder+"/", framed("same-origin")...)
			if code != 200 || !strings.Contains(body, "release") || !strings.Contains(csp, "sandbox") || !strings.Contains(csp, "frame-ancestors 'self'") {
				t.Fatalf("framed by the chrome: %d %q", code, csp)
			}
			// framed by a tile's sandboxed document, or by another site
			for _, site := range []string{"cross-site", "same-site", "none", ""} {
				for _, dest := range []string{"iframe", "frame", "embed", "object"} {
					code, csp, body := doc("/c/"+holder+"/", hdr("Sec-Fetch-Dest", dest), hdr("Sec-Fetch-Site", site))
					if code != 403 || strings.Contains(body, "release") || !strings.Contains(csp, "frame-ancestors 'none'") {
						t.Errorf("framed (%s) from %q: %d %q", dest, site, code, csp)
					}
				}
			}
			// opened as a page of its own, and a client that sends no Fetch
			// Metadata (the native app, bx): served, with the header
			for name, opts := range map[string][]reqOpt{
				"a tab":             {hdr("Sec-Fetch-Dest", "document"), hdr("Sec-Fetch-Site", "none")},
				"no Fetch Metadata": {},
			} {
				if code, csp, _ := doc("/c/"+holder+"/", opts...); code != 200 || !strings.Contains(csp, "frame-ancestors 'self'") {
					t.Errorf("%s: %d %q", name, code, csp)
				}
			}
			// a system component that holds no delegation is framed as before
			if code, csp, _ := doc("/c/"+plain+"/", framed("cross-site")...); code != 200 || strings.Contains(csp, "frame-ancestors") {
				t.Errorf("another system component, framed cross-site: %d %q", code, csp)
			}
		})
	}
}
