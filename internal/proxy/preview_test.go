package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// In a preview every identity header a backend can authorize on says
// read-only (D234): the role is the read clamp and the person's level is
// "read", on plain GETs and on upgrades alike — a WebSocket is a GET whose
// messages may write, and a GET's handler is the backend's to judge.
func TestPreviewCallsAreReadOnly(t *testing.T) {
	px := &Proxy{UserLevel: func(uid, tile string) string {
		return map[string]string{"bob": "terminal", "eve": "none"}[uid]
	}}
	comp := &registry.Component{Path: "apps/x"}
	preview := auth.Principal{Component: "apps/x", UserID: "bob", Via: "frame", Impersonator: auth.PreviewViewer}
	plain := httptest.NewRequest("GET", "/api/apps/x/data", nil)
	socket := httptest.NewRequest("GET", "/api/apps/x/ws", nil)
	socket.Header.Set("Upgrade", "websocket")

	d := px.previewReadOnly(plain, preview, comp, Decision{Role: "admin", Attribute: &auth.Attribution{UserID: "bob", Role: "writer", Level: "write"}})
	if d.Role != "reader" || d.Attribute.Role != "reader" || d.Attribute.Level != "read" || !d.Clamped || d.Deny != nil {
		t.Errorf("a preview's call: %+v (attribute %+v)", d, d.Attribute)
	}
	for name, p := range map[string]auth.Principal{
		"the person":       {Component: "apps/x", UserID: "bob", Via: "frame"},
		"an admin viewing": {Component: "apps/x", UserID: "bob", Via: "frame", Impersonator: "alice"},
	} {
		if d := px.previewReadOnly(plain, p, comp, Decision{Role: "admin"}); d.Role != "admin" || d.Clamped {
			t.Errorf("%s's call was narrowed: %+v", name, d)
		}
	}
	// a role the read clamp can't narrow has no read form: a plain GET
	// keeps it, a socket is refused
	if d := px.previewReadOnly(plain, preview, comp, Decision{Role: "consumer"}); d.Deny != nil || d.Role != "consumer" {
		t.Errorf("a custom role's GET in a preview: %+v", d)
	}
	if d := px.previewReadOnly(socket, preview, comp, Decision{Role: "consumer"}); d.Deny == nil {
		t.Errorf("a custom role's socket in a preview: %+v", d)
	}
	if d := px.previewReadOnly(socket, preview, comp, Decision{Role: "admin"}); d.Role != "reader" {
		t.Errorf("a socket in a preview: %+v", d)
	}
	px.ReaderClamp = func(target, role string) (string, bool) { return "subscriber", role == "publisher" }
	if d := px.previewReadOnly(plain, preview, comp, Decision{Role: "publisher"}); d.Role != "subscriber" {
		t.Errorf("the broker's clamp: %+v", d)
	}

	// the headers a backend reads
	px.ReaderClamp = nil
	for _, upgrade := range []bool{false, true} {
		for who, want := range map[string][3]string{
			"preview":  {"read", "1", ""},
			"person":   {"terminal", "", ""},
			"view-as":  {"terminal", "", "alice"},
			"no-level": {"none", "1", ""},
		} {
			p := auth.Principal{Component: "apps/x", UserID: "bob", Via: "frame"}
			switch who {
			case "preview":
				p.Impersonator = auth.PreviewViewer
			case "view-as":
				p.Impersonator = "alice"
			case "no-level":
				p.UserID, p.Impersonator = "eve", auth.PreviewViewer
			}
			r := httptest.NewRequest("GET", "/api/apps/x/data", nil)
			r.Header.Set("X-XBin-Preview", "0") // nothing a caller sends survives
			r.Header.Set("X-XBin-User-Level", "terminal")
			if upgrade {
				r.Header.Set("Upgrade", "websocket")
			}
			px.identify(r, p, px.previewReadOnly(r, p, comp, Decision{Role: "admin"}).Role, "apps/x")
			if got := [3]string{r.Header.Get(HeaderUserLevel), r.Header.Get(HeaderPreview), r.Header.Get(HeaderViewedBy)}; got != want {
				t.Errorf("%s (upgrade %v): level, preview, viewed-by = %q, want %q", who, upgrade, got, want)
			}
			if role := r.Header.Get(HeaderRole); (who == "preview" || who == "no-level") != (role == "reader") {
				t.Errorf("%s (upgrade %v): role %q", who, upgrade, role)
			}
		}
	}
}
