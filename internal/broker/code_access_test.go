package broker

import (
	"net/http/httptest"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/registry"
)

// code:<component> grants a component read-only source access; without it a
// cross-scope element is refused, admin and self always pass.
func TestCodeReadGrant(t *testing.T) {
	b := testBroker(t) // apps/calendar, apps/email (different-ish scopes)

	call := func(p auth.Principal) int {
		r := httptest.NewRequest("GET", "/code/tree?component=apps/calendar", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiCodeTree(w, r)
		return w.Code
	}

	if got := call(auth.Principal{Owner: true}); got != 200 {
		t.Fatalf("admin: want 200, got %d", got)
	}
	if got := call(auth.Principal{Component: "apps/calendar"}); got != 200 {
		t.Fatalf("self-read: want 200, got %d", got)
	}
	if got := call(auth.Principal{Component: "apps/email"}); got != 403 {
		t.Fatalf("ungranted element: want 403, got %d", got)
	}

	// Grant apps/email code:apps/calendar → now allowed.
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "code:apps/calendar", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := call(auth.Principal{Component: "apps/email"}); got != 200 {
		t.Fatalf("granted element (code:calendar): want 200, got %d", got)
	}
}

// A bare "code" grant reads ANY component's source (tooling/scanners).
func TestCodeReadBlanket(t *testing.T) {
	b := testBroker(t)
	tree := func(comp string, p auth.Principal) int {
		r := httptest.NewRequest("GET", "/code/tree?component="+comp, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiCodeTree(w, r)
		return w.Code
	}
	stats := auth.Principal{Component: "apps/email"}
	if tree("apps/calendar", stats) != 403 {
		t.Fatal("no grant: want 403")
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "code", Role: "reader"})
	}); err != nil {
		t.Fatal(err)
	}
	// Now it reads every component, not just one.
	for _, c := range []string{"apps/calendar", "apps/email"} {
		if got := tree(c, stats); got != 200 {
			t.Fatalf("blanket code grant reading %s: want 200, got %d", c, got)
		}
	}
}
