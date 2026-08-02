package broker

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// D41: org-terminator ingress consent — host/zone routed through a
// terminator tile owned by an org the caller administers are consented
// (org property through org property); host ports and the builtin runtime
// listener stay allowance/ws-admin.
func TestTerminatorIngressConsent(t *testing.T) {
	b, st := orgFixture(t) // sales: carol admin, alice read member; owns apps/email
	write := func(rel, content string) {
		t.Helper()
		p := filepath.Join(b.Reg.Root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A publishing tile with an exposed http endpoint, and two terminators.
	write("apps/site/xbin.json", `{"exposes":{"web":{"kind":"http","paths":["/"]}}}`)
	write("apps/site/index.html", `<html></html>`)
	write("apps/edge/xbin.json", `{"provides":{"ingress":{"kind":"http"}}}`)
	write("apps/edge/index.html", `<html></html>`)
	write("apps/other-edge/xbin.json", `{"provides":{"ingress":{"kind":"http"}}}`)
	write("apps/other-edge/index.html", `<html></html>`)
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "dana", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "data", Members: []users.Member{
		{ID: "dana", Level: users.LevelTerminal, Admin: true},
	}}); err != nil {
		t.Fatal(err)
	}
	for tile, owner := range map[string]string{
		"apps/site": "org:sales", "apps/edge": "org:sales", "apps/other-edge": "org:data",
	} {
		if err := st.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	carol := principalFor(t, st, "carol")
	alice := principalFor(t, st, "alice")
	dana := principalFor(t, st, "dana")

	bind := func(ref registry.BindRef) registry.Binding { return registry.Binding{ref} }

	// Org owns tile + terminator → host/zone approvable with ZERO allowance.
	if !b.orgAdminMayBind(carol, "apps/site", "web", bind(registry.BindRef{Ref: "apps/edge", Host: "x.example.com"}), false) {
		t.Error("same-org terminator host must be consented (D41)")
	}
	if !b.orgAdminMayBind(carol, "apps/site", "web", bind(registry.BindRef{Ref: "apps/edge", Zone: "*.z.example.com"}), false) {
		t.Error("same-org terminator zone must be consented (D41)")
	}
	// Host PORTS stay workspace infrastructure — even through the org's own
	// terminator, and via the builtin runtime listener.
	if b.orgAdminMayBind(carol, "apps/site", "web", bind(registry.BindRef{Ref: "apps/edge", Listen: ":9000"}), false) {
		t.Error("listen must not be consented by terminator ownership")
	}
	if b.orgAdminMayBind(carol, "apps/site", "web", bind(registry.BindRef{Ref: "runtime", Host: "x.example.com"}), false) {
		t.Error("the builtin runtime listener is not org property")
	}
	// Provider side: dana's org owns the terminator, the SITE isn't hers —
	// the terminator-owning org consents to publishing through it.
	if !b.orgAdminMayBind(dana, "apps/site", "web", bind(registry.BindRef{Ref: "apps/other-edge", Host: "y.example.com"}), false) {
		t.Error("terminator-owning org admin must be able to consent (provider side)")
	}
	if b.orgAdminMayBind(dana, "apps/site", "web", bind(registry.BindRef{Ref: "apps/other-edge", Listen: ":9001"}), false) {
		t.Error("provider-side consent must never cover listen")
	}
	// Non-admin members and unrelated terminators: refused.
	if b.orgAdminMayBind(alice, "apps/site", "web", bind(registry.BindRef{Ref: "apps/edge", Host: "x.example.com"}), false) {
		t.Error("non-admin must not approve")
	}
	if b.orgAdminMayBind(carol, "apps/site", "web", bind(registry.BindRef{Ref: "apps/other-edge", Host: "x.example.com"}), false) {
		t.Error("another org's terminator is not carol's to consent (caller side; dana's provider side covers it)")
	}
}
