package tilesbx

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

func TestKeys(t *testing.T) {
	m := New(Options{Root: "/ws"})
	k := Key{Tile: "apps/coding-sandbox"}
	ck := k.CK()
	if !strings.HasPrefix(ck, "apps~coding-sandbox-") {
		t.Fatalf("CK %q", ck)
	}
	d := &Def{Name: "sb-1", UID: "0123456789ab"}
	if dir, err := m.StateDir(k, d); err != nil || dir != filepath.Join("/ws/.xbin/sbx", ck, "sb-1.0123456789ab") {
		t.Fatalf("StateDir %q %v", dir, err)
	}
	if dir, err := m.CurDir(k, d); err != nil || dir != filepath.Join("/ws/.xbin/sbx", ck, "sb-1.0123456789ab", "cur") {
		t.Fatalf("CurDir %q %v", dir, err)
	}
	if dir, err := m.TrashDir(k); err != nil || dir != filepath.Join("/ws/.xbin/sbx", ck, ".trash") {
		t.Fatalf("TrashDir %q %v", dir, err)
	}
	// A definition without a well-formed uid (or name) has no state dir.
	for _, bad := range []*Def{{Name: "sb-1"}, {Name: "sb-1", UID: "../../etc"}, {Name: "..", UID: "0123456789ab"}} {
		if dir, err := m.StateDir(k, bad); err == nil {
			t.Fatalf("StateDir of %+v: %q", bad, dir)
		}
	}
	if got := RegistryID(k, "sb-1"); got != "tile:"+ck+":sb-1" {
		t.Fatalf("RegistryID %q", got)
	}
	if got := Leaf(k, "sb-1"); got != "sbx-"+ck+"-sb-1" {
		t.Fatalf("Leaf %q", got)
	}
	if got := ArchiveKey(k, d); got != ck+".sbx.sb-1.0123456789ab" {
		t.Fatalf("ArchiveKey %q", got)
	}
	if root, err := m.CodeRoot(k); err != nil || root != "/ws/apps/coding-sandbox" {
		t.Fatalf("CodeRoot %q %v", root, err)
	}
	// A deployment's key: its ids and leaf now, its paths once they exist.
	dk := Key{Tile: "apps/coding-sandbox", Deployment: "blue"}
	if RegistryID(dk, "sb-1") != "tile+blue:"+ck+":sb-1" || Leaf(dk, "sb-1") != "sbx-"+ck+"+blue-sb-1" || ArchiveKey(dk, d) != "" {
		t.Fatalf("deployment ids %q %q", RegistryID(dk, "sb-1"), Leaf(dk, "sb-1"))
	}
	if _, err := m.StateDir(dk, d); err == nil {
		t.Fatal("a deployment's state dir before deployments exist")
	}
	if _, err := m.TrashDir(dk); err == nil {
		t.Fatal("a deployment's trash before deployments exist")
	}
	if _, err := m.CodeRoot(dk); err == nil {
		t.Fatal("a deployment's code root before deployments exist")
	}
	if _, err := m.ResourceMount(dk, "res:x/y"); err == nil {
		t.Fatal("a deployment's resource mount before deployments exist")
	}
	// An admin naming a deployment: unsupported, never main's sandbox.
	e := newEnv(t)
	e.create(ns("sb-1"))
	e.want(e.do(admin, "DELETE", "/sandboxes/sb-1?tile=apps/mgr&deployment=blue", nil), http.StatusNotImplemented, RefUnsupported)
	e.want(e.do(mgr, "GET", "/sandboxes/sb-1", nil), http.StatusOK, "")
}

// reviewedPrincipalFields are the auth.Principal fields reviewed for the
// sandbox key: none of them names a deployment, so keyOf builds main's
// key from Component alone. A field added to auth.Principal fails this
// test until someone decides what it means for the key — above all
// dev-lifecycle's Deployment (plans/tile-sandbox-runtime.md §1.4, §14):
// replace deploymentOf's reflection with p.Deployment, build non-main
// keys, and add the field here.
var reviewedPrincipalFields = []string{
	"Access", "Component", "DeviceID", "Gen", "Impersonator", "Owner", "Role", "User", "UserID", "Via",
}

func TestPrincipalFieldsReviewed(t *testing.T) {
	var got []string
	pt := reflect.TypeOf(auth.Principal{})
	for i := range pt.NumField() {
		got = append(got, pt.Field(i).Name)
	}
	sort.Strings(got)
	want := append([]string(nil), reviewedPrincipalFields...)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("auth.Principal's fields are %v, the reviewed ones %v: a new principal field must be reviewed for the sandbox key (internal/tilesbx/keys.go keyOf)", got, want)
	}
}

// deployedPrincipal stands in for a principal type with a Deployment field
// while auth.Principal has none.
type deployedPrincipal struct {
	auth.Principal
	Deployment string
}

// withDeployment makes every principal the gates see act in deployment d:
// through auth.Principal's own Deployment field once it has one, through
// the stand-in type until then.
func withDeployment(t *testing.T, d string) func(auth.Principal) auth.Principal {
	t.Helper()
	if _, ok := reflect.TypeOf(auth.Principal{}).FieldByName("Deployment"); ok {
		return func(p auth.Principal) auth.Principal {
			reflect.ValueOf(&p).Elem().FieldByName("Deployment").SetString(d)
			return p
		}
	}
	old := principalDeployment
	principalDeployment = func(p auth.Principal) string {
		return deploymentOf(deployedPrincipal{Principal: p, Deployment: d})
	}
	t.Cleanup(func() { principalDeployment = old })
	return func(p auth.Principal) auth.Principal { return p }
}

func TestDeploymentOf(t *testing.T) {
	if got := deploymentOf(deployedPrincipal{Deployment: "blue"}); got != "blue" {
		t.Fatalf("stand-in: %q", got)
	}
	if got := deploymentOf(&deployedPrincipal{Deployment: "blue"}); got != "blue" {
		t.Fatalf("stand-in pointer: %q", got)
	}
	if got := deploymentOf(deployedPrincipal{}); got != "" {
		t.Fatalf("stand-in, main: %q", got)
	}
	if _, ok := reflect.TypeOf(auth.Principal{}).FieldByName("Deployment"); !ok {
		if got := deploymentOf(mgr); got != "" {
			t.Fatalf("auth.Principal without the field: %q", got)
		}
	}
	// A field of another type fails closed: set, it is never main.
	type named string
	type odd struct{ Deployment struct{ N int } }
	if got := deploymentOf(odd{}); got != "" {
		t.Fatalf("an unset odd field: %q", got)
	}
	if got := deploymentOf(odd{Deployment: struct{ N int }{1}}); got == "" {
		t.Fatal("a set odd field reads as main")
	}
	if got := deploymentOf(struct{ Deployment named }{"green"}); got != "green" {
		t.Fatalf("a named string field: %q", got)
	}
	if got := deploymentOf(nil); got != "" {
		t.Fatalf("nil: %q", got)
	}
	if got := deploymentOf((*deployedPrincipal)(nil)); got != "" {
		t.Fatalf("a nil pointer: %q", got)
	}
}

// A principal of a non-main deployment gets 501 on every manager route,
// never main's sandboxes: without the guard a branch deployed as a dev
// deployment of the manager would list, exec into and spend main's.
func TestNonMainDeploymentUnsupported(t *testing.T) {
	e := newEnv(t)
	e.create(ns("sb-1"))
	as := withDeployment(t, "blue")
	dmgr := as(mgr)
	for pat := range routeTable(e.m) {
		method, path, _ := strings.Cut(pat, " ")
		if strings.HasPrefix(path, "/sandboxes/policy") {
			continue // admin routes: the key isn't the caller's
		}
		path = strings.NewReplacer("{name}", "sb-1", "{id}", "0a1b2c-1", "{sid}", "s-1").Replace(path)
		w := e.do(dmgr, method, path, ns("sb-2"))
		if w.Code != http.StatusNotImplemented || !strings.Contains(w.Body.String(), "non-main deployment") {
			t.Errorf("%s %s as a deployment's manager: %d %s, want 501 (non-main deployment)", method, path, w.Code, w.Body)
		}
	}
	// Nothing of main's changed: sb-1 is there, sb-2 was never created.
	if rows := e.m.AdminList("apps/mgr"); len(rows) != 1 || rows[0].Name != "sb-1" {
		t.Fatalf("main's sandboxes after a deployment's calls: %+v", rows)
	}
	// The admin's routes don't take the caller's key.
	e.want(e.do(as(admin), "GET", "/sandboxes/policy", nil), http.StatusOK, "")
	e.want(e.do(as(admin), "DELETE", "/sandboxes/sb-1?tile=apps/mgr", nil), http.StatusNoContent, "")
}

// countingCaps counts cap lookups: a request refused for its path must
// not reach them.
type countingCaps struct {
	Caps
	n *atomic.Int64
}

func (c countingCaps) SandboxesFor(tile string) bool { c.n.Add(1); return c.Caps.SandboxesFor(tile) }

// A path with a dot segment, an encoded /, . or \, or an id failing its
// grammar is 400 invalid before anything is looked up — the caller
// included.
func TestPathHygiene(t *testing.T) {
	var lookups atomic.Int64
	e := newEnv(t, func(o *Options) {
		o.Deps.Caps = countingCaps{Caps: o.Deps.Caps, n: &lookups}
		isAdmin := o.Deps.Admin
		o.Deps.Admin = func(p auth.Principal) bool { lookups.Add(1); return isAdmin(p) }
	})
	e.create(ns("x"))
	base := lookups.Load()
	refused := func(w *httptest.ResponseRecorder, what string, before int64) {
		t.Helper()
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"refusal":"invalid"`) {
			t.Errorf("%s: %d %s, want 400 invalid", what, w.Code, w.Body)
		}
		if n := lookups.Load() - before; n != 0 {
			t.Errorf("%s: %d lookups before the refusal", what, n)
		}
	}
	for _, r := range []struct{ method, path string }{
		{"GET", "/sandboxes/a%2Fb"},
		{"GET", "/sandboxes/a%2fb"},
		{"DELETE", "/sandboxes/x%2F..%2Fy"},
		{"GET", "/sandboxes/x/execs/..%2Fy"},
		{"GET", "/sandboxes/x/execs/%2E%2E/output"},
		{"GET", "/sandboxes/x/execs/%2e%2e/output"},
		{"GET", "/sandboxes/x/execs/%2E/output"},
		{"POST", "/sandboxes/x/execs/0a1b2c-1%5Cx/stdin"},
		{"POST", "/sandboxes/x/snapshots/..%2F..%2Fy/restore"},
		{"GET", "/sandboxes/x/execs/abc-1"},          // a bad exec id
		{"GET", "/sandboxes/x/execs/0A1B2C-1"},       // upper case
		{"DELETE", "/sandboxes/x/execs/0a1b2c-"},     // no number
		{"POST", "/sandboxes/x/snapshots/1/restore"}, // a bad snapshot id
		{"DELETE", "/sandboxes/x/snapshots/s-1x"},
		{"GET", "/sandboxes/X"},       // a bad name
		{"DELETE", "/sandboxes/copy"}, // a reserved one
		{"GET", "/sandboxes/runtime%2E"},
		{"GET", "/sandboxes/policy%2E"},
	} {
		for _, p := range []auth.Principal{mgr, admin, user} {
			before := lookups.Load()
			refused(e.do(p, r.method, r.path, "{}"), fmt.Sprintf("%s %s as %+v", r.method, r.path, p), before)
		}
	}
	// Behind xbind's /api/xbin prefix (server.handleAPI). Its inner URL
	// keeps the encoding (WP-2b: Path and RawPath cut from the escaped
	// path), so an encoded "/" stays in its segment and fails the name's
	// check. The stale shape it had before (Path decoded, RawPath the outer
	// one, so the decoded path was routed) is refused too: the path as sent
	// still carries the encoded "/".
	for _, r := range []struct{ method, path string }{
		{"POST", "/api/xbin/sandboxes/x%2Fstop"},
		{"GET", "/api/xbin/sandboxes/x%2Fexecs%2F0a1b2c-1"},
		{"POST", "/api/xbin/sandboxes/x/execs/0a1b2c-1%2Fsignal"},
	} {
		for _, stale := range []bool{false, true} {
			before := lookups.Load()
			req := httptest.NewRequest(r.method, r.path, strings.NewReader("{}"))
			req = req.WithContext(auth.WithPrincipal(req.Context(), mgr))
			r2 := req.Clone(req.Context())
			r2.URL.Path = strings.TrimPrefix(req.URL.Path, "/api/xbin")
			if !stale {
				r2.URL.RawPath = strings.TrimPrefix(req.URL.RawPath, "/api/xbin")
			}
			w := httptest.NewRecorder()
			e.mux.ServeHTTP(w, r2)
			what := fmt.Sprintf("%s %s (behind /api/xbin, stale %v)", r.method, r.path, stale)
			if !stale && w.Code == http.StatusMethodNotAllowed { // no POST /…/execs/{id}: the mux answers
				if n := lookups.Load() - before; n != 0 {
					t.Errorf("%s: %d lookups", what, n)
				}
				continue
			}
			refused(w, what, before)
		}
	}
	if lookups.Load() != base {
		t.Fatal("lookups counted outside the refused requests")
	}
	// Well-formed ids pass the gates (to routes not built yet: 501).
	for _, r := range []struct{ method, path string }{
		{"GET", "/sandboxes/x/execs/" + e.m.bootID + "-1"},
		{"GET", "/sandboxes/x/execs/" + e.m.bootID + "-123456789012/output"},
		{"POST", "/sandboxes/x/snapshots/s-1/restore"},
		{"DELETE", "/sandboxes/x/snapshots/s-123456789012"},
	} {
		e.want(e.do(mgr, r.method, r.path, "{}"), http.StatusNotImplemented, RefUnsupported)
	}
	if lookups.Load() == base {
		t.Fatal("the counting fake saw nothing: the test proves nothing")
	}
}
