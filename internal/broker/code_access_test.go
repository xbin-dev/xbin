package broker

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
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

// A person reads the source of a tile they may read — the tile window's
// Files and Changes (issue #50): read, write and terminal on the tile pass,
// access to another tile only does not, and the person path is never an
// element's (an element needs its code grant whoever drives it).
func TestCodeReadPerson(t *testing.T) {
	b := testBroker(t)
	call := func(path string, p auth.Principal) int {
		r := httptest.NewRequest("GET", path, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		switch {
		case strings.HasPrefix(path, "/git/log"):
			b.apiGitLog(w, r)
		case strings.HasPrefix(path, "/git/diff"):
			b.apiGitDiff(w, r)
		default:
			b.apiCodeTree(w, r)
		}
		return w.Code
	}
	person := func(id string, tiles map[string]string) auth.Principal {
		return auth.Principal{UserID: id, User: &users.User{ID: id, Role: users.RoleUser, Tiles: tiles}, Via: "session"}
	}
	for _, lvl := range []string{users.LevelRead, users.LevelWrite, users.LevelTerminal} {
		p := person("bob", map[string]string{"apps/calendar": lvl})
		for _, path := range []string{"/code/tree?component=apps/calendar", "/git/log?component=apps/calendar", "/git/diff?component=apps/calendar"} {
			if got := call(path, p); got == 403 {
				t.Fatalf("%s on the tile, %s: want it read, got 403", lvl, path)
			}
		}
	}
	if got := call("/code/tree?component=apps/calendar", person("carol", map[string]string{"apps/email": users.LevelTerminal})); got != 403 {
		t.Fatalf("a person with another tile only: want 403, got %d", got)
	}
	if got := call("/code/tree?component=apps/calendar", person("dave", nil)); got != 403 {
		t.Fatalf("a person with no tiles: want 403, got %d", got)
	}
	drv := person("bob", map[string]string{"apps/calendar": users.LevelTerminal})
	drv.Component, drv.Via = "apps/email", "frame"
	if got := call("/code/tree?component=apps/calendar", drv); got != 403 {
		t.Fatalf("an element driven by a reader: want 403 (its own grant decides), got %d", got)
	}
}

// /git/activity is read-gated exactly like the other code endpoints: admin and
// self pass, an ungranted cross-scope element is refused. (A non-repo component
// still answers 200 with repo:false — the gate runs before the repo check.)
func TestGitActivityGate(t *testing.T) {
	b := testBroker(t)
	call := func(p auth.Principal) int {
		r := httptest.NewRequest("GET", "/git/activity?component=apps/calendar", nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), p))
		w := httptest.NewRecorder()
		b.apiGitActivity(w, r)
		return w.Code
	}
	if got := call(auth.Principal{Owner: true}); got != 200 {
		t.Fatalf("admin: want 200, got %d", got)
	}
	if got := call(auth.Principal{Component: "apps/calendar"}); got != 200 {
		t.Fatalf("self: want 200, got %d", got)
	}
	if got := call(auth.Principal{Component: "apps/email"}); got != 403 {
		t.Fatalf("ungranted element: want 403, got %d", got)
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
