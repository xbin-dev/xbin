package boot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/tilesbx"
)

type capsFor map[string]bool

func (c capsFor) SandboxesFor(tile string) bool { return c[tile] }

// Every tile-sandbox route mounts beside the registry view's GET /sandboxes
// without a pattern conflict (the mux panics on one).
func TestTileSandboxRoutes(t *testing.T) {
	st := &State{TileSbx: tilesbx.New(tilesbx.Options{Root: t.TempDir()})}
	srv := &server.Server{}
	st.registerSandboxAPI(srv)
	st.registerTileSandboxAPI(srv)
	if n := len(srv.APIRoutes()); n != 38 { // 37, and the ports proxy (D135)
		t.Fatalf("%d sandbox routes, want 38", n)
	}
}

// GET /sandboxes: a manager tile's backend gets its own tile sandboxes; an
// admin the registry view; the manager tile's page and people, nothing.
func TestSandboxScopeOf(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "xbin.json"), []byte(`{"schema":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	brk, err := broker.New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	st := &State{Broker: brk, TileSbx: tilesbx.New(tilesbx.Options{Root: root, Isolated: true,
		Deps: tilesbx.Deps{Caps: capsFor{"apps/mgr": true}}})}
	if sc, ok := st.sandboxScopeOf(auth.Principal{Component: "apps/mgr", Via: "instance"}, nil); !ok || !sc.manager || sc.all {
		t.Fatalf("manager: %+v %v", sc, ok)
	}
	if sc, ok := st.sandboxScopeOf(auth.Principal{Owner: true}, map[string][]string{"tile": {"apps/x/"}}); !ok || sc.manager || !sc.all || sc.tile != "apps/x" {
		t.Fatalf("admin: %+v %v", sc, ok)
	}
	for _, p := range []auth.Principal{{Component: "apps/mgr", Via: "frame"}, {UserID: "bob", Via: "session"}} {
		if _, ok := st.sandboxScopeOf(p, nil); ok {
			t.Fatalf("%+v sees sandboxes", p)
		}
	}
}

// OnCapChange stops a tile's sandboxes only when it lost cap:sandboxes —
// never on an approve (it fires then too), never for another cap — and
// never on the broker's goroutine: the hook returns while the stop runs.
func TestTileSandboxCapHook(t *testing.T) {
	stops := make(chan string, 8)
	block := make(chan struct{})
	defer close(block)
	hook := tileSandboxCapHook(func(tile, why string) { stops <- tile + ": " + why; <-block })
	hook("apps/a", broker.SandboxesCap, true) // an approve
	hook("apps/a", "cap:containers", false)   // another cap
	hook("apps/b", broker.SandboxesCap, false)
	select {
	case s := <-stops:
		if s != "apps/b: cap:sandboxes was revoked: stopped, state kept" {
			t.Fatalf("stopped %q", s)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a revoke stopped nothing")
	}
	select {
	case s := <-stops:
		t.Fatalf("stopped %q too", s)
	case <-time.After(50 * time.Millisecond):
	}
}

// The broker's predicates see a running sandbox's resource mounts (the
// seal, a cap:containers flip), not its source mount.
func TestTileSandboxOf(t *testing.T) {
	s := tileSandboxOf(tilesbx.Key{Tile: "apps/mgr"}, &tilesbx.Def{Name: "a", Mounts: []tilesbx.Mount{
		{Res: "res:apps/mgr/work", At: "/w"}, {Source: true, At: "/src"}, {Res: "res:apps/mgr/ro", At: "/r"}}})
	if s.Tile != "apps/mgr" || s.Name != "a" || strings.Join(s.Res, " ") != "res:apps/mgr/work res:apps/mgr/ro" {
		t.Fatalf("%+v", s)
	}
}
