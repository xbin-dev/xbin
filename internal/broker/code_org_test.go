package broker

import (
	"net/http/httptest"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// A code (bare) grant must read all tile code even when the tile is
// org-owned / user-owned (org-refactor regression watch).
func TestCodeGrantOrgOwned(t *testing.T) {
	b := testBroker(t)

	// Tile owned by an org.
	if _, err := b.Users.UpsertOrg(users.Org{ID: "acme", Name: "Acme"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetOwner("apps/email", "org:acme"); err != nil {
		t.Fatal(err)
	}
	// Explicit bare code grant.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "code", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	tree := func(comp string, p auth.Principal) int {
		r := httptest.NewRequest("GET", "/code/tree?component="+comp, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiCodeTree(w, r)
		return w.Code
	}
	if got := tree("apps/calendar", auth.Principal{Component: "apps/email", Via: "instance"}); got != 200 {
		t.Fatalf("org-owned tile with bare code grant: want 200, got %d", got)
	}
	// code:<comp> scoped grant variant, user-owned tile.
	if _, err := b.Users.Upsert(users.User{ID: "bob", Role: users.RoleUser}, "pw"); err != nil {
		t.Fatal(err)
	}
	if err := b.Users.SetOwner("apps/email", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "code:apps/calendar", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := tree("apps/calendar", auth.Principal{Component: "apps/email", Via: "instance"}); got != 200 {
		t.Fatalf("user-owned tile with code:<comp> grant: want 200, got %d", got)
	}
}
