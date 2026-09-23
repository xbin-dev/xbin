package server

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/branding"
	"github.com/xbin-dev/xbin/internal/events"
)

const testPNG = "data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg=="

// GET is for every signed-in principal, PUT for admins; a PUT persists,
// answers with the full view and tells the hub; the sign-in page carries
// the brand once set and xbin's own before and after clearing.
func TestBrandingRoutesAndLoginPage(t *testing.T) {
	h, s := termServer(t)
	s.Brand = branding.New(filepath.Join(t.TempDir(), "branding.json"))
	if s.Hub == nil {
		s.Hub = events.NewHub()
	}
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, "/api/xbin"+path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	loginPage := func() string {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("GET", "/login", nil))
		return w.Body.String()
	}
	head := func(p string) string { return p[:min(len(p), 300)] }

	if c, b := do(bob, "GET", "/branding", ""); c != 200 || !strings.Contains(b, `"hasIcon":false`) {
		t.Fatalf("unbranded GET: %d %s", c, b)
	}
	if p := loginPage(); !strings.Contains(p, "<title>xbin — sign in</title>") || !strings.Contains(p, "X/BIN") || !strings.Contains(p, `href="data:image/svg+xml;base64,`) {
		t.Fatalf("unbranded sign-in page: %s", head(p))
	}
	if c, _ := do(bob, "PUT", "/branding", `{"title":"Acme"}`); c != 403 {
		t.Fatalf("a user may not brand: %d", c)
	}
	ch, cancel := s.Hub.Subscribe(func(e events.Event) bool { return e.Type == "branding" })
	defer cancel()
	c, b := do(alice, "PUT", "/branding", `{"title":"  Acme Ops ","icon":"`+testPNG+`"}`)
	if c != 200 || !strings.Contains(b, `"title":"Acme Ops"`) || !strings.Contains(b, `"hasIcon":true`) {
		t.Fatalf("admin PUT: %d %s", c, b)
	}
	select {
	case e := <-ch:
		if e.Type != "branding" {
			t.Fatalf("event: %+v", e)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no branding event on the hub")
	}
	if c, b := do(bob, "GET", "/branding", ""); c != 200 || !strings.Contains(b, `"icon":"data:image/png;base64,`) {
		t.Fatalf("anyone signed in reads the brand: %d %s", c, b)
	}
	p := loginPage()
	if !strings.Contains(p, "<title>Acme Ops — sign in</title>") || !strings.Contains(p, `<img class="mark" src="data:image/png;base64,`) ||
		!strings.Contains(p, `href="data:image/png;base64,`) || strings.Contains(p, "X/BIN") {
		t.Fatalf("branded sign-in page: %s", head(p))
	}
	// a title is escaped; bad icons are refused and leave the brand alone
	if c, b := do(alice, "PUT", "/branding", `{"title":"<b>x</b>"}`); c != 200 || !strings.Contains(loginPage(), "&lt;b&gt;x&lt;/b&gt; — sign in") {
		t.Fatalf("escaped title: %d %s", c, b)
	}
	for _, bad := range []string{`{"icon":"https://x/i.png"}`, `{"icon":"data:image/gif;base64,` + testPNG[22:] + `"}`, `{"icon":"data:image/png;base64,PHN2Zy8+"}`} {
		if c, _ := do(alice, "PUT", "/branding", bad); c != 400 {
			t.Fatalf("bad icon %s: %d", bad, c)
		}
	}
	if !strings.Contains(loginPage(), `<img class="mark" src="data:image/png;base64,`) {
		t.Fatal("a refused icon left the old one")
	}
	if c, b := do(alice, "PUT", "/branding", `{"title":"","icon":""}`); c != 200 || !strings.Contains(b, `"hasIcon":false`) {
		t.Fatalf("clear: %d %s", c, b)
	}
	if p := loginPage(); !strings.Contains(p, "<title>xbin — sign in</title>") || !strings.Contains(p, "X/BIN") {
		t.Fatal("cleared: xbin's own again")
	}
	if !auditable("PUT", "/branding") || auditable("GET", "/branding") {
		t.Fatal("branding writes are audited, reads are not")
	}
}
