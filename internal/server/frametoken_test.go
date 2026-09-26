package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

var injectedFrameToken = regexp.MustCompile(`xbin-frame-token" content="([^"]*)"`)

// One tile's frontend must never obtain another tile's frame token by
// fetching that tile's document: the D4 injection mints only for a human or
// for the tile itself. Before this, any tile an admin opened could
// xbin.fetch('/c/tiles/admin/') and lift the admin tile's token (and with it
// the admin tile's xbin:admin grant) out of the HTML.
func TestInjectionNeverMintsAnotherTilesToken(t *testing.T) {
	root := t.TempDir()
	for rel, c := range map[string]string{
		"apps/evil/xbin.json":    `{}`,
		"apps/x/xbin.json":       `{}`,
		"apps/x/index.html":      `<!doctype html><html><head></head><body>x</body></html>`,
		"apps/x/editor/app.html": `<!doctype html><html><head></head><body>ed</body></html>`,
		"tiles/admin/xbin.json":  `{}`,
		"tiles/admin/index.html": `<!doctype html><html><head></head><body>admin</body></html>`,
	} {
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
	if _, err := st.Upsert(users.User{ID: "boss", Role: users.RoleAdmin}, "password1"); err != nil {
		t.Fatal(err)
	}
	s := &Server{Reg: reg, Auth: a}
	h := s.authedStatic(http.HandlerFunc(s.handleComponentStatic))

	tokenFor := func(url, frameComp string) string {
		r := httptest.NewRequest("GET", url, nil)
		r.Header.Set(auth.FrameTokenHeader, a.MintFrameToken(frameComp, "boss", time.Minute))
		r.Header.Set("Sec-Fetch-Site", "cross-site") // xbin.fetch out of an opaque origin
		r.Header.Set("Sec-Fetch-Mode", "cors")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s as %s: %d", url, frameComp, w.Code)
		}
		m := injectedFrameToken.FindStringSubmatch(w.Body.String())
		if m == nil {
			t.Fatalf("%s: no frame-token meta", url)
		}
		return m[1]
	}

	if tok := tokenFor("/c/tiles/admin/", "apps/evil"); tok != "" {
		c, _, _ := a.VerifyFrameToken(tok)
		t.Fatalf("apps/evil's frame principal obtained a frame token for %q", c)
	}
	// The tile itself still gets its own token (renewal by reload, direct
	// fetch of its own document)…
	if tok := tokenFor("/c/apps/x/", "apps/x"); tok == "" {
		t.Fatal("a tile's own document lost its token")
	}
	// …and so does an xbin.window sub-path frame (bx-frame mints its
	// bootstrap token for the sub-path; the document belongs to apps/x).
	if tok := tokenFor("/c/apps/x/editor/app.html", "apps/x/editor"); tok == "" {
		t.Fatal("a sub-path window document lost its token")
	}
	c, _, _ := a.VerifyFrameToken(tokenFor("/c/apps/x/editor/app.html", "apps/x/editor"))
	if c != "apps/x" {
		t.Fatalf("sub-path document minted for %q, want apps/x", c)
	}
}
