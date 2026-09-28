package proxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/ingress"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/runner"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// zsForge sets everything a caller could send to claim an identity or
// smuggle a credential: every X-XBin-* header xbind sets or might set
// (X-XBin-Deployment included), in two spellings, xbind's cookies beside
// the tile's own, and a bearer.
func zsForge(r *http.Request) {
	for _, h := range []string{"X-XBin-From", "X-XBin-Role", "X-XBin-User", "X-XBin-User-Level", "X-XBin-Viewed-By",
		"X-XBin-Ingress-Host", "X-XBin-Deployment", "X-XBin-Lifecycle"} {
		r.Header.Set(h, "forged")
	}
	r.Header.Set("x-xbin-lowercase", "forged") // the server canonicalizes what a client sends lowercase
	for _, c := range []string{auth.CookieName, auth.SessionCookieHostName, auth.TileCookieName, auth.HostTileCookieName, "tile_pref"} {
		r.AddCookie(&http.Cookie{Name: c, Value: "v-" + c})
	}
	r.Header.Set("Authorization", "Bearer forged-token")
	r.Header.Set("Accept", "application/json")
}

// zsHeaders renders a header set sorted by name, one "Key: value" per
// line (a key's values in their order).
func zsHeaders(h http.Header) string {
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var lines []string
	for _, k := range keys {
		for _, v := range h[k] {
			lines = append(lines, k+": "+v)
		}
	}
	return strings.Join(lines, "\n")
}

// covers PO-4 Z5 SC-ZERO — the headers a primary's backend receives: for
// every principal kind (human, owner, element frame, instance, terminal,
// view-as, cron) identify strips every inbound X-XBin-* — a forged
// X-XBin-Deployment included — and xbind's credentials, and sets exactly
// today's set; the proxy's own path (policy → identify) and the ingress
// path (ForwardIngress) leave the same headers on the request they
// forward. Hand-maintained goldens: changing one is a compat change
// (12-compat.md).
func TestIdentifyZeroState(t *testing.T) {
	ana := &users.User{ID: "ana", Role: users.RoleUser}
	px := &Proxy{UserLevel: func(uid, tile string) string {
		if uid == "ana" {
			return "write"
		}
		return ""
	}}
	const rest = "Accept: application/json\nCookie: tile_pref=v-tile_pref"
	cases := []struct {
		name, role, tile string
		p                auth.Principal
		want             string
	}{
		{"a human (session)", "reader", "apps/t",
			auth.Principal{UserID: "ana", User: ana, Via: "session"},
			rest + "\nX-Xbin-From: user:ana\nX-Xbin-Role: reader\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"a human known only by snapshot", "reader", "apps/t",
			auth.Principal{User: ana, Via: "device"},
			rest + "\nX-Xbin-From: \nX-Xbin-Role: reader\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"the owner (bearer)", "admin", "apps/t",
			auth.Principal{Owner: true, Via: "bearer"},
			rest + "\nX-Xbin-From: owner\nX-Xbin-Role: admin"},
		{"an element frame driven by a user", "writer", "apps/t",
			auth.Principal{Component: "apps/caller", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: writer\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"an owner-driven element frame", "writer", "apps/t",
			auth.Principal{Component: "apps/caller", Via: "frame"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: writer"},
		{"the tile's own frame", "admin", "apps/t",
			auth.Principal{Component: "apps/t", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: apps/t\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"a backend (instance token)", "reader", "apps/t",
			auth.Principal{Component: "apps/caller", Via: "instance"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: reader"},
		{"a tile terminal", "admin", "apps/t",
			auth.Principal{Component: "apps/t", UserID: "ana", Via: "terminal"},
			rest + "\nX-Xbin-From: apps/t\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"an admin viewing as ana", "reader", "apps/t",
			auth.Principal{UserID: "ana", User: ana, Via: "session", Impersonator: "root"},
			rest + "\nX-Xbin-From: user:ana\nX-Xbin-Role: reader\nX-Xbin-User: ana\nX-Xbin-User-Level: write\nX-Xbin-Viewed-By: root"},
		{"view-as through a tile frame", "writer", "apps/t",
			auth.Principal{Component: "apps/caller", UserID: "ana", Via: "frame", Impersonator: "owner"},
			rest + "\nX-Xbin-From: apps/caller\nX-Xbin-Role: writer\nX-Xbin-User: ana\nX-Xbin-User-Level: write\nX-Xbin-Viewed-By: owner"},
		{"a cron tick", "admin", "apps/t",
			auth.Principal{Component: "_cron", Via: "cron", Role: "admin"},
			rest + "\nX-Xbin-From: _cron\nX-Xbin-Role: admin"},
		{"a user without a level on the target", "reader", "apps/t",
			auth.Principal{UserID: "bo", Via: "session"},
			rest + "\nX-Xbin-From: user:bo\nX-Xbin-Role: reader\nX-Xbin-User: bo"},
		{"a tile whose own path holds +", "admin", "notes+ideas",
			auth.Principal{Component: "notes+ideas", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: notes+ideas\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
	}
	for _, c := range cases {
		r := httptest.NewRequest("GET", "/api/"+c.tile+"/x", nil)
		zsForge(r)
		px.identify(r, c.p, c.role, c.tile)
		if got := zsHeaders(r.Header); got != c.want {
			t.Errorf("%s: the backend receives\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	// No UserLevel resolver installed: no level header, the rest as above.
	r := httptest.NewRequest("GET", "/api/apps/t/x", nil)
	zsForge(r)
	(&Proxy{}).identify(r, auth.Principal{UserID: "ana", User: ana, Via: "session"}, "reader", "apps/t")
	if got, want := zsHeaders(r.Header), rest+"\nX-Xbin-From: user:ana\nX-Xbin-Role: reader\nX-Xbin-User: ana"; got != want {
		t.Errorf("without a level resolver:\n%s\nwant\n%s", got, want)
	}

	// The proxy's own path and the ingress path, up to the backend: a
	// runner that may not start anything stops both right after the
	// headers are set, on the very request they would forward.
	root := t.TempDir()
	for rel, body := range map[string]string{
		"xbin.json":                     `{}`,
		"apps/t/xbin.json":              `{"runtime":"node"}`,
		"apps/t/backend/server.js":      `// never runs`,
		"notes+ideas/xbin.json":         `{"runtime":"node"}`,
		"notes+ideas/backend/server.js": `// never runs`,
	} {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	run := runner.New(root, nil, events.NewHub(), reg)
	run.ShouldRun = func(string) bool { return false }
	full := &Proxy{Reg: reg, Runner: run, UserLevel: px.UserLevel}
	for _, c := range []struct {
		name, path string
		p          auth.Principal
		want       string
	}{
		{"the owner", "/api/apps/t/x?frame=tok&q=1", auth.Principal{Owner: true, Via: "cookie"},
			rest + "\nX-Xbin-From: owner\nX-Xbin-Role: admin"},
		{"the tile's own frame", "/api/apps/t/x", auth.Principal{Component: "apps/t", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: apps/t\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
		{"a + tile's own frame", "/api/notes+ideas/x", auth.Principal{Component: "notes+ideas", UserID: "ana", Via: "frame"},
			rest + "\nX-Xbin-From: notes+ideas\nX-Xbin-Role: admin\nX-Xbin-User: ana\nX-Xbin-User-Level: write"},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		zsForge(r)
		r = r.WithContext(auth.WithPrincipal(r.Context(), c.p))
		rec := httptest.NewRecorder()
		full.ServeHTTP(rec, r)
		if rec.Code != http.StatusBadGateway {
			t.Fatalf("%s: %d %s (the runner was to refuse the start)", c.name, rec.Code, rec.Body.String())
		}
		if got := zsHeaders(r.Header); got != c.want {
			t.Errorf("proxy path, %s:\n%s\nwant\n%s", c.name, got, c.want)
		}
	}
	// A caller the default policy refuses gets 403 before any header is set.
	r = httptest.NewRequest("GET", "/api/apps/t/x", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Component: "apps/other", Via: "frame"}))
	rec := httptest.NewRecorder()
	full.ServeHTTP(rec, r)
	if rec.Code != http.StatusForbidden || r.Header.Get(HeaderFrom) != "" {
		t.Errorf("refused caller: %d, X-XBin-From %q", rec.Code, r.Header.Get(HeaderFrom))
	}

	// Ingress: the anonymous ingress principal and the public host; the
	// tile's own cookies and the Authorization header pass, the workspace
	// session cookies never do.
	r = httptest.NewRequest("GET", "http://shop.example.com/cart?frame=x", nil)
	zsForge(r)
	rec = httptest.NewRecorder()
	full.ForwardIngress(rec, r, ingress.Route{Component: "apps/t", Slot: "web", Paths: []string{"/*"}, Source: "runtime", Host: "shop.example.com"}, false)
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("ingress: %d %s", rec.Code, rec.Body.String())
	}
	wantIngress := "Accept: application/json\nAuthorization: Bearer forged-token\n" +
		"Cookie: xbin_tile=v-xbin_tile; __Host-xbin_tile=v-__Host-xbin_tile; tile_pref=v-tile_pref\n" +
		"X-Xbin-From: ingress\nX-Xbin-Ingress-Host: shop.example.com"
	if got := zsHeaders(r.Header); got != wantIngress {
		t.Errorf("ingress path:\n%s\nwant\n%s", got, wantIngress)
	}
}

// ---- tile deployments: routing (WP-37) ----

// dlFake is the deployments plane in miniature for the routing tests: the
// tiles that have a record, their deployments and primary, answered to
// ResolveRef (registry.DeploymentLookup), to the broker (its
// DeploymentAnswers) and to the runner (its DeploymentHooks). The runner's
// CodeFor counts every start it is asked for and refuses it naming the
// deployment, so no backend ever starts and a 502's text says which
// deployment a call reached.
type dlFake struct {
	mu      sync.Mutex
	primary map[string]string   // tile → its primary, for a tile with a record
	deps    map[string][]string // tile → its deployments
	asked   map[string]int      // "<tile> <deployment>" → starts asked for
}

// record gives tile a deployment record holding deps, its primary first.
func (f *dlFake) record(tile string, deps ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.primary[tile], f.deps[tile] = deps[0], deps
}

func (f *dlFake) setPrimary(tile, dep string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.primary[tile] = dep
}

func (f *dlFake) HasRecord(tile string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := f.primary[tile]
	return ok
}

func (f *dlFake) HasDeployment(tile, name string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	deps, ok := f.deps[tile]
	if !ok {
		return name == util.MainDeployment
	}
	return slices.Contains(deps, name)
}

func (f *dlFake) Primary(tile string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if p, ok := f.primary[tile]; ok {
		return p
	}
	return util.MainDeployment
}

// deployments answers the broker's DeploymentsOf.
func (f *dlFake) deployments(tile string) (string, []string) {
	primary := f.Primary(tile)
	f.mu.Lock()
	defer f.mu.Unlock()
	if deps, ok := f.deps[tile]; ok {
		return primary, slices.Clone(deps)
	}
	return primary, []string{util.MainDeployment}
}

// addressed answers the broker's AddressedDeployment as the plane's
// Addressed does, with no primary protected.
func (f *dlFake) addressed(p auth.Principal, tile string) (string, error) {
	primary := f.Primary(tile)
	if p.Component != tile {
		return primary, nil
	}
	switch dep := p.Deployment; {
	case dep == "" && p.Via != "terminal":
		return util.MainDeployment, nil
	case dep == "":
		return primary, nil
	case !f.HasDeployment(tile, dep):
		return "", util.NoDeployment(tile, dep)
	default:
		return dep, nil
	}
}

// codeFor is the runner's CodeFor: it counts the start and refuses it.
func (f *dlFake) codeFor(tile, dep string) (runner.Code, error) {
	if !f.HasDeployment(tile, dep) {
		return runner.Code{}, util.NoDeployment(tile, dep)
	}
	f.mu.Lock()
	f.asked[tile+" "+dep]++
	f.mu.Unlock()
	return runner.Code{}, fmt.Errorf("reached %s deployment %s", tile, dep)
}

// starts is how many starts of deployment dep of tile the runner was asked for.
func (f *dlFake) starts(tile, dep string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.asked[tile+" "+dep]
}

// dlWorld is a workspace routed as boot routes it: the real registry,
// broker and runner, dlFake answering their deployment questions, and the
// proxy with the broker's Route installed by the lines stepProxy gains.
//   - apps/t: a record, main (the primary) and dev; apps/t/sub is a tile
//     nested in it;
//   - apps/s: a record, main (the primary, static: no backend) and dev;
//   - apps/caller: a record, main (the primary) and dev; granted writer on
//     apps/t;
//   - apps/z: no record; apps/p+dev: no record, its own path holds a '+';
//     apps/t+old: a tile of its own, while apps/t has no deployment "old".
type dlWorld struct {
	f  *dlFake
	px *Proxy
	st *users.Store
}

func newDLWorld(t *testing.T) *dlWorld {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"xbin.json":         `{"grants":[{"from":"apps/caller","target":"apps/t","role":"writer"}]}`,
		"apps/s/xbin.json":  `{}`,
		"apps/s/index.html": `<html></html>`,
	}
	for _, tile := range []string{"apps/t", "apps/t/sub", "apps/caller", "apps/z", "apps/p+dev", "apps/t+old"} {
		files[tile+"/xbin.json"] = `{"runtime":"node"}`
		files[tile+"/backend/server.js"] = `// never runs`
	}
	for rel, body := range files {
		p := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	hub := events.NewHub()
	brk, err := broker.New(reg, hub, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(brk.Close)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	brk.Users = st
	f := &dlFake{primary: map[string]string{}, deps: map[string][]string{}, asked: map[string]int{}}
	f.record("apps/t", "main", "dev")
	f.record("apps/s", "main", "dev")
	f.record("apps/caller", "main", "dev")
	brk.PrimaryOf, brk.DeploymentsOf, brk.AddressedDeployment = f.Primary, f.deployments, f.addressed
	brk.DeploymentExists = f.HasDeployment
	run := runner.New(root, nil, hub, reg)
	run.DeploymentHooks = runner.DeploymentHooks{CodeFor: f.codeFor, Primary: f.Primary}
	px := &Proxy{Reg: reg, Runner: run, Hub: hub, Policy: brk.Policy}
	// The integrator's lines in stepProxy: broker.Decision converts to
	// Decision, field for field (this doesn't compile when they drift).
	px.Route = func(p auth.Principal, c *registry.Component, q string) Decision { return Decision(brk.Route(p, c, q)) }
	px.Deployments = f
	return &dlWorld{f: f, px: px, st: st}
}

// person is a signed-in user holding level on tile.
func (w *dlWorld) person(t *testing.T, id, tile, level string) auth.Principal {
	t.Helper()
	if _, err := w.st.Upsert(users.User{ID: id, Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if err := w.st.GrantTile(id, tile, level); err != nil {
		t.Fatal(err)
	}
	u, _ := w.st.Get(id)
	a, _ := w.st.Access(id)
	return auth.Principal{UserID: id, User: u, Access: a, Via: "session"}
}

// call serves GET path as p, the request carrying every forgery zsForge
// sends; it returns the answer and the request as the backend would get it.
func (w *dlWorld) call(p auth.Principal, path string) (*httptest.ResponseRecorder, *http.Request) {
	r := httptest.NewRequest("GET", path, nil)
	zsForge(r)
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	rec := httptest.NewRecorder()
	w.px.ServeHTTP(rec, r)
	return rec, r
}

// dlAnswer checks an answer's status, its error text and the docs page the
// proxy's errors name (auth.md for a 403).
func dlAnswer(t *testing.T, label string, rec *httptest.ResponseRecorder, code int, msg string) {
	t.Helper()
	var body struct{ Error, Docs string }
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	docs := "/docs/protocol.md"
	if code == http.StatusForbidden {
		docs = "/docs/auth.md"
	}
	if rec.Code != code || body.Error != msg || body.Docs != docs {
		t.Errorf("%s: %d %s\nwant %d {error: %q, docs: %q}", label, rec.Code, rec.Body.String(), code, msg, docs)
	}
}

// reached is the answer of a call the runner was handed for deployment dep
// of tile (dlFake refuses the start, naming it).
func reached(tile, dep string) string { return "reached " + tile + " deployment " + dep }

// covers D127j D127d D127g — /api/<tile>+<name>/… reaches EnsureDeployment for that
// deployment (11-contract §2.2, §2.3; 09-fabric §4.1): the proxy resolves
// the path with ResolveRef, asks the broker's Route (installed as boot
// installs it) and hands the runner exactly the deployment Route returned.
// <tile>+<primary> is the alias of the primary; an unknown name is a 404 to
// whoever may know the tile's deployments and a 403 to everyone else; a
// qualified URL never enters a nested tile; another tile never names a
// deployment, the primary included; a call routed to a non-primary
// deployment is judged by that deployment's own code, not by the primary's
// no-backend gate. A zero-state tile, a tile whose own path holds '+' and a
// tile at <tile>+<name> resolve as today. A reassignment moves the bare URL
// and the alias.
func TestProxyQualifiedTarget(t *testing.T) {
	w := newDLWorld(t)
	owner := auth.Principal{Owner: true, Via: "bearer"}
	devInst := auth.Principal{Component: "apps/t", Via: "instance", Deployment: "dev"}
	mainInst := auth.Principal{Component: "apps/t", Via: "instance"}
	caller := auth.Principal{Component: "apps/caller", Via: "instance"}
	stranger := auth.Principal{Component: "apps/z", Via: "instance"}
	writer := w.person(t, "wes", "apps/t", users.LevelWrite)
	reader := w.person(t, "rae", "apps/t", users.LevelRead)
	bareOnly := func(from string) string {
		return from + " calls apps/t by its bare URL, /api/apps/t/, which reaches its primary: " +
			"deployment URLs are for the tile itself and the people who work on it"
	}
	notGranted := func(from string) string {
		return from + ` is not granted access to apps/t — declare it in "uses" and approve the grant (bx grant, or the grants panel)`
	}
	check := func(cases []struct {
		name string
		p    auth.Principal
		path string
		code int
		msg  string
	}) {
		t.Helper()
		for _, c := range cases {
			rec, _ := w.call(c.p, c.path)
			dlAnswer(t, c.name, rec, c.code, c.msg)
		}
	}
	check([]struct {
		name string
		p    auth.Principal
		path string
		code int
		msg  string
	}{
		{"an admin names dev", owner, "/api/apps/t+dev/x", 502, reached("apps/t", "dev")},
		{"an admin, the bare URL", owner, "/api/apps/t/x", 502, reached("apps/t", "main")},
		{"an admin, the alias of the primary", owner, "/api/apps/t+main/x", 502, reached("apps/t", "main")},
		{"an admin names no deployment of the tile", owner, "/api/apps/t+nope/x", 404, `apps/t has no deployment "nope"`},
		{"dev's backend, its bare self-call", devInst, "/api/apps/t/x", 502, reached("apps/t", "dev")},
		{"dev's backend names itself", devInst, "/api/apps/t+dev/x", 502, reached("apps/t", "dev")},
		{"main's backend, its bare self-call", mainInst, "/api/apps/t/x", 502, reached("apps/t", "main")},
		{"main's backend, the alias of its own", mainInst, "/api/apps/t+main/x", 502, reached("apps/t", "main")},
		{"a granted tile, the bare URL", caller, "/api/apps/t/x", 502, reached("apps/t", "main")},
		{"a granted tile names dev", caller, "/api/apps/t+dev/x", 403, bareOnly("apps/caller")},
		{"a granted tile names the primary", caller, "/api/apps/t+main/x", 403, bareOnly("apps/caller")},
		{"a tile without a grant names nothing", stranger, "/api/apps/t+nope/x", 403, bareOnly("apps/z")},
		{"a tile without a grant, the bare URL", stranger, "/api/apps/t/x", 403, notGranted("apps/z")},
		{"a writer names dev: a person holds no role on /api, as today", writer, "/api/apps/t+dev/x", 403, notGranted("user:wes")},
		{"a writer names no deployment", writer, "/api/apps/t+nope/x", 404, `apps/t has no deployment "nope"`},
		{"a reader names dev", reader, "/api/apps/t+dev/x", 403, "deployment URLs need write access on apps/t"},
		{"a reader names no deployment: the same 403", reader, "/api/apps/t+nope/x", 403, "deployment URLs need write access on apps/t"},
		{"a qualified URL into a nested tile", owner, "/api/apps/t+dev/sub/x", 404, "apps/t/sub is a tile of its own; its deployments are /c/apps/t/sub+<name>/"},
		{"… from a tile that may not know apps/t's deployments", caller, "/api/apps/t+dev/sub/x", 403, bareOnly("apps/caller")},
		{"the nested tile's bare URL", owner, "/api/apps/t/sub/x", 502, reached("apps/t/sub", "main")},
		{"a static primary, the bare URL", owner, "/api/apps/s/x", 404, `component apps/s has no backend (runtime "")`},
		{"a static primary, dev named: dev's own code decides", owner, "/api/apps/s+dev/x", 502, reached("apps/s", "dev")},
		{"a zero-state tile: '+' means nothing", owner, "/api/apps/z+dev/x", 404, "no such component"},
		{"a zero-state tile whose own path holds '+'", owner, "/api/apps/p+dev/x", 502, reached("apps/p+dev", "main")},
		{"a tile at apps/t+old wins over the qualifier", owner, "/api/apps/t+old/x", 502, reached("apps/t+old", "main")},
	})
	if n := w.f.starts("apps/t", "nope"); n != 0 {
		t.Errorf("the runner was asked to start apps/t's unknown deployment %d times", n)
	}

	// A tile manager makes dev the primary: the bare URL and the alias
	// follow at once; main is reached by its name, and by its own backend.
	w.f.setPrimary("apps/t", "dev")
	check([]struct {
		name string
		p    auth.Principal
		path string
		code int
		msg  string
	}{
		{"reassigned: an admin, the bare URL", owner, "/api/apps/t/x", 502, reached("apps/t", "dev")},
		{"reassigned: an admin names main", owner, "/api/apps/t+main/x", 502, reached("apps/t", "main")},
		{"reassigned: a granted tile, the bare URL", caller, "/api/apps/t/x", 502, reached("apps/t", "dev")},
		{"reassigned: a granted tile names the new primary", caller, "/api/apps/t+dev/x", 403, bareOnly("apps/caller")},
		{"reassigned: main's backend, its bare self-call", mainInst, "/api/apps/t/x", 502, reached("apps/t", "main")},
		{"reassigned: dev's backend, its bare self-call", devInst, "/api/apps/t/x", 502, reached("apps/t", "dev")},
	})
}

// covers D127j T3 T4 SC-CLAMP — TestIdentifyDeploymentHeader (=
// TestDeploymentHeaderOnlyFromXbind, 11-contract §4, NP-11-12): a request
// from one of a tile's own principals bound to a non-primary deployment
// carries X-XBin-Deployment naming it, on its self-calls and on its calls
// to other tiles, whichever deployment is primary; a primary's
// principals, a terminal that follows the primary, humans, the owner, cron
// and bus deliveries and zero-state tiles never do; an inbound one is
// stripped every time, so a backend that receives it knows xbind set it,
// and X-XBin-From stays the tile path. Through the proxy, a non-primary
// caller's call to another tile carries its deployment and the role the
// edge verdict clamped, and a blocked edge is refused before any header is
// set. A response from a non-primary deployment names it, set over the
// backend's value; a primary's response passes as it always has.
func TestIdentifyDeploymentHeader(t *testing.T) {
	f := &dlFake{primary: map[string]string{}, deps: map[string][]string{}, asked: map[string]int{}}
	f.record("apps/t", "main", "dev") // main is the primary
	f.record("apps/r", "dev", "main") // reassigned: dev is the primary
	px := &Proxy{Deployments: f}
	own := func(tile, via, dep string) auth.Principal {
		return auth.Principal{Component: tile, Via: via, Deployment: dep}
	}
	for _, c := range []struct {
		name, target string
		p            auth.Principal
		want         string
	}{
		{"dev's backend, a self-call", "apps/t", own("apps/t", "instance", "dev"), "dev"},
		{"dev's backend calls another tile", "apps/calendar", own("apps/t", "instance", "dev"), "dev"},
		{"dev's frame, driven by a user", "apps/t", auth.Principal{Component: "apps/t", UserID: "ana", Via: "frame", Deployment: "dev"}, "dev"},
		{"a session targeting dev calls another tile", "apps/calendar", own("apps/t", "terminal", "dev"), "dev"},
		{"a session following the primary", "apps/t", own("apps/t", "terminal", ""), ""},
		{"a session targeting main, the primary", "apps/t", own("apps/t", "terminal", "main"), ""},
		{"main's backend", "apps/t", own("apps/t", "instance", ""), ""},
		{"main's frame calls another tile", "apps/calendar", own("apps/t", "frame", ""), ""},
		{"reassigned: main's frame, a self-call", "apps/r", own("apps/r", "frame", ""), "main"},
		{"reassigned: main's backend calls another tile", "apps/calendar", own("apps/r", "instance", ""), "main"},
		{"reassigned: a session targeting main", "apps/r", own("apps/r", "terminal", "main"), "main"},
		{"reassigned: dev's backend, now the primary's", "apps/r", own("apps/r", "instance", "dev"), ""},
		{"reassigned: a session following the primary", "apps/r", own("apps/r", "terminal", ""), ""},
		{"a tile without a record", "apps/z", own("apps/z", "instance", ""), ""},
		{"the owner", "apps/t", auth.Principal{Owner: true, Via: "bearer"}, ""},
		{"a person", "apps/t", auth.Principal{UserID: "ana", Via: "session"}, ""},
		{"an admin viewing as a person", "apps/t", auth.Principal{UserID: "ana", Via: "session", Impersonator: "root"}, ""},
		{"a cron tick of dev", "apps/t", auth.Principal{Component: "xbin/cron", Via: "cron", Role: "admin", Deployment: "dev"}, ""},
		{"a bus delivery of dev", "apps/t", auth.Principal{Component: "xbin/bus", Via: "bus", Role: "reader", Deployment: "dev"}, ""},
	} {
		r := httptest.NewRequest("GET", "/api/"+c.target+"/x", nil)
		zsForge(r) // X-XBin-Deployment: forged, among the rest
		px.identify(r, c.p, "admin", c.target)
		var want []string
		if c.want != "" {
			want = []string{c.want}
		}
		if got := r.Header.Values(HeaderDeployment); !slices.Equal(got, want) {
			t.Errorf("%s: X-XBin-Deployment %q, want %q", c.name, got, want)
		}
		if got := r.Header.Get(HeaderFrom); got != c.p.From() {
			t.Errorf("%s: X-XBin-From %q, want the bare %q", c.name, got, c.p.From())
		}
		for k, vs := range r.Header {
			if strings.HasPrefix(k, "X-Xbin-") && slices.Contains(vs, "forged") {
				t.Errorf("%s: the forged %s reached the backend", c.name, k)
			}
		}
	}

	// Through the proxy: the edge verdict (WP-47's edge policy, faked here)
	// clamps dev's call to apps/t, or blocks it.
	w := newDLWorld(t)
	brokerRoute := w.px.Route
	verdict := Decision{Deployment: "main", Role: "reader", Clamped: true, Edges: []string{"grant:apps/t"}}
	w.px.Route = func(p auth.Principal, c *registry.Component, q string) Decision {
		if p.Component == "apps/caller" && p.Deployment == "dev" && c.Path == "apps/t" {
			return verdict
		}
		return brokerRoute(p, c, q)
	}
	devCaller := auth.Principal{Component: "apps/caller", Via: "instance", Deployment: "dev"}
	rec, r := w.call(devCaller, "/api/apps/t/x")
	dlAnswer(t, "dev's call to apps/t, clamped", rec, 502, reached("apps/t", "main"))
	for h, want := range map[string]string{HeaderFrom: "apps/caller", HeaderRole: "reader", HeaderDeployment: "dev"} {
		if got := r.Header.Values(h); !slices.Equal(got, []string{want}) {
			t.Errorf("dev's call to apps/t: %s %q, want %q", h, got, want)
		}
	}
	rec, r = w.call(auth.Principal{Component: "apps/caller", Via: "instance"}, "/api/apps/t/x")
	dlAnswer(t, "main's call to apps/t", rec, 502, reached("apps/t", "main"))
	if got := r.Header.Values(HeaderDeployment); got != nil || r.Header.Get(HeaderRole) != "writer" {
		t.Errorf("main's call to apps/t: X-XBin-Deployment %q, X-XBin-Role %q; want none, writer", got, r.Header.Get(HeaderRole))
	}
	blocked := `apps/caller's non-primary deployment "dev" may not use edge grant:apps/t: the tile's edge policy for it is "block". A tile manager can change it in the Deployments panel.`
	verdict = Decision{Deny: errors.New(blocked)}
	rec, r = w.call(devCaller, "/api/apps/t/x")
	dlAnswer(t, "dev's call to apps/t, blocked", rec, 403, blocked)
	if got := r.Header.Get(HeaderFrom); got != "forged" {
		t.Errorf("a blocked call was identified: X-XBin-From %q", got)
	}

	// The response side, on a backend that sets the header itself.
	sock := filepath.Join(t.TempDir(), "g1.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		rw.Header().Set(HeaderDeployment, "backend-set")
		fmt.Fprintf(rw, "%s?%s %s", r.URL.Path, r.URL.RawQuery, r.Header.Get(HeaderDeployment))
	})}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })
	for _, c := range []struct{ answering, want string }{{"dev", "dev"}, {"", "backend-set"}} {
		r := httptest.NewRequest("GET", "/api/apps/t+dev/x/y?frame=tok&q=1", nil)
		r.Header.Set(HeaderDeployment, "dev") // as identify set it
		rec := httptest.NewRecorder()
		(&Proxy{}).forward(rec, r, sock, "x/y", c.answering)
		if got := rec.Header().Values(HeaderDeployment); !slices.Equal(got, []string{c.want}) {
			t.Errorf("answering %q: the response's X-XBin-Deployment %q, want %q", c.answering, got, c.want)
		}
		if got := rec.Body.String(); got != "/x/y?q=1 dev" {
			t.Errorf("answering %q: the backend saw %q, want the endpoint without the qualifier or ?frame=", c.answering, got)
		}
	}
}

// covers D127d T3 — TestIngressForwardPrimaryOnly (=
// TestIngressNeverReachesNonPrimary; 09-fabric §1, §2.5): public ingress
// reaches only the routed tile's primary. With main primary the runner is
// asked for main and never for dev; a route naming "apps/t+dev" is an
// exact tile path, which no tile holds, so nothing is reached; once dev is
// the primary, ingress reaches dev and main no more. The forwarded request
// never names a deployment, a forged X-XBin-Deployment included.
func TestIngressForwardPrimaryOnly(t *testing.T) {
	w := newDLWorld(t)
	forward := func(label, component string, code int) {
		t.Helper()
		r := httptest.NewRequest("GET", "http://shop.example.com/cart", nil)
		zsForge(r)
		rec := httptest.NewRecorder()
		w.px.ForwardIngress(rec, r, ingress.Route{Component: component, Slot: "web", Paths: []string{"/*"}, Source: "runtime", Host: "shop.example.com"}, false)
		if rec.Code != code {
			t.Errorf("%s: %d %s, want %d", label, rec.Code, rec.Body.String(), code)
		}
		if got := r.Header.Values(HeaderDeployment); code == http.StatusBadGateway && got != nil {
			t.Errorf("%s: the forwarded request carries X-XBin-Deployment %q", label, got)
		}
		if got := rec.Header().Values(HeaderDeployment); got != nil {
			t.Errorf("%s: the answer carries X-XBin-Deployment %q", label, got)
		}
	}
	forward("main primary", "apps/t", http.StatusBadGateway)
	if w.f.starts("apps/t", "main") == 0 || w.f.starts("apps/t", "dev") != 0 {
		t.Errorf("main primary: starts asked main %d, dev %d; want main only", w.f.starts("apps/t", "main"), w.f.starts("apps/t", "dev"))
	}
	forward("a route naming apps/t+dev", "apps/t+dev", http.StatusServiceUnavailable)
	if n := w.f.starts("apps/t", "dev"); n != 0 {
		t.Errorf("a route naming apps/t+dev asked for dev's start %d times", n)
	}

	w.f.setPrimary("apps/t", "dev")
	mainStarts := w.f.starts("apps/t", "main")
	forward("dev primary", "apps/t", http.StatusBadGateway)
	if w.f.starts("apps/t", "dev") == 0 || w.f.starts("apps/t", "main") != mainStarts {
		t.Errorf("dev primary: starts asked dev %d, main %d more; want dev only", w.f.starts("apps/t", "dev"), w.f.starts("apps/t", "main")-mainStarts)
	}
}

// covers D127g T3 — TestCrossDeploymentRefused (09-fabric §4.2, §4.3), the
// rows the /api/ proxy holds, in both directions: every kind of tile
// credential bound to dev (backend, frame, a session targeting dev) that
// names main, or a name the tile doesn't have, is refused with §4.3's 403
// before anything is identified or started, and so is every credential
// bound to main (backend, frame, a session targeting main, a session
// following the primary) that names dev; each reaches its own deployment
// by the bare URL. A user-attributed frame of dev needs its user's current
// write on the tile. Bindings are fixed and roles are read per request
// (F2): when dev becomes the primary, main's frame still reaches main, now
// named on the request, and a session following the primary moves to dev
// and is refused on main. Without a routing function a credential of a
// non-primary deployment reaches nothing (F6). The other rows of §4.2
// (vault, frame-token mint and renewal, registrations, status, notify,
// logs, tile status, prefs, whoami) belong to the planes that hold them.
func TestCrossDeploymentRefused(t *testing.T) {
	w := newDLWorld(t)
	refusal := func(bound, named string) string {
		return fmt.Sprintf("apps/t's deployment %q can only call itself: this credential belongs to %q, not %q. "+
			"A tile's code never reaches another deployment of its own tile; a person switches deployments by URL, "+
			"a terminal by changing its target.", bound, bound, named)
	}
	own := func(via, dep string) auth.Principal {
		return auth.Principal{Component: "apps/t", Via: via, Deployment: dep}
	}
	refused := func(label string, p auth.Principal, path, msg string) {
		t.Helper()
		rec, r := w.call(p, path)
		dlAnswer(t, label, rec, http.StatusForbidden, msg)
		if got := r.Header.Get(HeaderFrom); got != "forged" {
			t.Errorf("%s: a refused call was identified (X-XBin-From %q)", label, got)
		}
	}

	// Without the routing function (a broker-less proxy): no URL names a
	// deployment, and a non-primary credential is refused.
	bare := &Proxy{Reg: w.px.Reg, Runner: w.px.Runner}
	for _, c := range []struct {
		p    auth.Principal
		path string
		code int
	}{
		{own("instance", "dev"), "/api/apps/t/x", http.StatusForbidden},
		{own("instance", ""), "/api/apps/t+dev/x", http.StatusNotFound},
		{own("instance", ""), "/api/apps/t/x", http.StatusBadGateway},
	} {
		r := httptest.NewRequest("GET", c.path, nil)
		r = r.WithContext(auth.WithPrincipal(r.Context(), c.p))
		rec := httptest.NewRecorder()
		bare.ServeHTTP(rec, r)
		if rec.Code != c.code {
			t.Errorf("no routing function, %+v %s: %d %s, want %d", c.p, c.path, rec.Code, rec.Body.String(), c.code)
		}
	}
	if n := w.f.starts("apps/t", "dev"); n != 0 {
		t.Errorf("no routing function: dev's start was asked for %d times", n)
	}

	for _, p := range []auth.Principal{own("instance", "dev"), own("frame", "dev"), own("terminal", "dev")} {
		for _, named := range []string{"main", "nope"} {
			refused(p.Via+" of dev names "+named, p, "/api/apps/t+"+named+"/x", refusal("dev", named))
		}
		rec, _ := w.call(p, "/api/apps/t/x")
		dlAnswer(t, p.Via+" of dev, the bare URL", rec, 502, reached("apps/t", "dev"))
	}
	for _, p := range []auth.Principal{own("instance", ""), own("frame", ""), own("terminal", "main"), own("terminal", "")} {
		label := p.Via + " of main"
		if p.Via == "terminal" && p.Deployment == "" {
			label = "a session following the primary"
		}
		for _, named := range []string{"dev", "nope"} {
			refused(label+" names "+named, p, "/api/apps/t+"+named+"/x", refusal("main", named))
		}
		for _, path := range []string{"/api/apps/t/x", "/api/apps/t+main/x"} {
			rec, _ := w.call(p, path)
			dlAnswer(t, label+", "+path, rec, 502, reached("apps/t", "main"))
		}
	}

	// A frame of dev driven by a user: the user's current write decides.
	for id, level := range map[string]string{"wes": users.LevelWrite, "rae": users.LevelRead} {
		pp := w.person(t, id, "apps/t", level)
		frame := auth.Principal{Component: "apps/t", UserID: id, Access: pp.Access, Via: "frame", Deployment: "dev"}
		rec, _ := w.call(frame, "/api/apps/t/x")
		if level == users.LevelWrite {
			dlAnswer(t, "dev's frame driven by a writer", rec, 502, reached("apps/t", "dev"))
		} else {
			dlAnswer(t, "dev's frame driven by a reader", rec, 403, "deployments of apps/t need write access")
		}
	}

	// dev becomes the primary.
	w.f.setPrimary("apps/t", "dev")
	rec, r := w.call(own("frame", ""), "/api/apps/t/x")
	dlAnswer(t, "reassigned: main's frame, the bare URL", rec, 502, reached("apps/t", "main"))
	if got := r.Header.Values(HeaderDeployment); !slices.Equal(got, []string{"main"}) {
		t.Errorf("reassigned: main's frame's self-call carries X-XBin-Deployment %q, want main", got)
	}
	rec, r = w.call(own("terminal", ""), "/api/apps/t/x")
	dlAnswer(t, "reassigned: a session following the primary, the bare URL", rec, 502, reached("apps/t", "dev"))
	if got := r.Header.Values(HeaderDeployment); got != nil {
		t.Errorf("reassigned: a session following the primary carries X-XBin-Deployment %q", got)
	}
	refused("reassigned: a session following the primary names main", own("terminal", ""), "/api/apps/t+main/x", refusal("dev", "main"))
	refused("reassigned: dev's backend names main", own("instance", "dev"), "/api/apps/t+main/x", refusal("dev", "main"))
	refused("reassigned: main's backend names dev, the primary", own("instance", ""), "/api/apps/t+dev/x", refusal("main", "dev"))
}
