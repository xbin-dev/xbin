package proxy

import (
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// A preview's sockets reach a backend at reader (D234): a WebSocket is a GET
// whose messages may write, and the method gate reads nothing after the
// upgrade. Plain GETs, and everyone else's sockets, keep their role.
func TestPreviewSocketIsReader(t *testing.T) {
	px := &Proxy{}
	comp := &registry.Component{Path: "apps/x"}
	preview := auth.Principal{Component: "apps/x", UserID: "bob", Via: "frame", Impersonator: auth.PreviewViewer}
	upgrade := httptest.NewRequest("GET", "/api/apps/x/ws", nil)
	upgrade.Header.Set("Upgrade", "websocket")
	plain := httptest.NewRequest("GET", "/api/apps/x/data", nil)

	d := px.previewSocket(upgrade, preview, comp, Decision{Role: "admin", Attribute: &auth.Attribution{UserID: "bob", Role: "writer"}})
	if d.Role != "reader" || d.Attribute.Role != "reader" || !d.Clamped || d.Deny != nil {
		t.Errorf("a preview's socket: %+v (attribute %+v)", d, d.Attribute)
	}
	if d := px.previewSocket(plain, preview, comp, Decision{Role: "admin"}); d.Role != "admin" || d.Clamped {
		t.Errorf("a preview's plain GET: %+v", d)
	}
	for name, p := range map[string]auth.Principal{
		"the person":       {Component: "apps/x", UserID: "bob", Via: "frame"},
		"an admin viewing": {Component: "apps/x", UserID: "bob", Via: "frame", Impersonator: "alice"},
	} {
		if d := px.previewSocket(upgrade, p, comp, Decision{Role: "admin"}); d.Role != "admin" {
			t.Errorf("%s's socket was narrowed: %+v", name, d)
		}
	}
	// a role the read clamp can't narrow is refused, not passed
	if d := px.previewSocket(upgrade, preview, comp, Decision{Role: "auditor"}); d.Deny == nil {
		t.Errorf("a custom role's socket in a preview: %+v", d)
	}
	px.ReaderClamp = func(target, role string) (string, bool) { return "subscriber", role == "publisher" }
	if d := px.previewSocket(upgrade, preview, comp, Decision{Role: "publisher"}); d.Role != "subscriber" {
		t.Errorf("the broker's clamp: %+v", d)
	}
	if d := px.previewSocket(upgrade, preview, comp, Decision{Role: "auditor"}); d.Deny == nil {
		t.Errorf("the broker's clamp refusing: %+v", d)
	}
}
