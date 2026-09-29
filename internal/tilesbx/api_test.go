package tilesbx

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// Only a manager call — the backend of a tile holding cap:sandboxes —
// reaches the manager routes; an admin may list (the registry view, in
// internal/boot), stop and delete, and gets 403 everywhere else.
func TestGates(t *testing.T) {
	e := newEnv(t)
	e.create(ns("sb-1"))

	manager := []struct{ method, path string }{
		{"GET", "/sandboxes/runtime"},
		{"GET", "/sandboxes"},
		{"POST", "/sandboxes"},
		{"GET", "/sandboxes/sb-1"},
		{"PATCH", "/sandboxes/sb-1"},
		{"POST", "/sandboxes/sb-1/start"},
		{"POST", "/sandboxes/sb-1/reset"},
		{"POST", "/sandboxes/sb-1/run"},
		{"POST", "/sandboxes/sb-1/execs"},
		{"GET", "/sandboxes/sb-1/execs/abc123-1/output"},
		{"GET", "/sandboxes/sb-1/execs/abc123-1/tty"},
		{"GET", "/sandboxes/sb-1/tty"},
		{"GET", "/sandboxes/sb-1/files/stat?path=/"},
		{"PUT", "/sandboxes/sb-1/files/content?path=/x"},
		{"GET", "/sandboxes/sb-1/tar?path=/"},
		{"POST", "/sandboxes/copy"},
		{"GET", "/sandboxes/sb-1/snapshots"},
	}
	for _, p := range []auth.Principal{mgrFrame, mgrTerm, cron, user, noCap, admin} {
		for _, r := range manager {
			w := e.do(p, r.method, r.path, "{}")
			if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), `"refusal":"not-allowed"`) {
				t.Errorf("%+v %s %s: %d %s, want 403 not-allowed", p, r.method, r.path, w.Code, w.Body)
			}
		}
		if p.Owner {
			continue
		}
		for _, r := range []struct{ method, path string }{
			{"POST", "/sandboxes/sb-1/stop?tile=apps/mgr"}, {"DELETE", "/sandboxes/sb-1?tile=apps/mgr"},
			{"GET", "/sandboxes/policy"}, {"PUT", "/sandboxes/policy"},
		} {
			if w := e.do(p, r.method, r.path, "{}"); w.Code != http.StatusForbidden {
				t.Errorf("%+v %s %s: %d, want 403", p, r.method, r.path, w.Code)
			}
		}
	}
	// The manager passes every gate (routes not built yet answer 501).
	for _, r := range manager {
		if w := e.do(mgr, r.method, r.path, "{}"); w.Code == http.StatusForbidden {
			t.Errorf("manager %s %s: 403 %s", r.method, r.path, w.Body)
		}
	}
	// An admin stops (a stopped sandbox stays stopped) and deletes with ?tile=.
	e.want(e.do(admin, "POST", "/sandboxes/sb-1/stop", nil), http.StatusBadRequest, RefInvalid)
	e.want(e.do(admin, "POST", "/sandboxes/sb-1/stop?tile=apps/mgr", nil), http.StatusOK, "")
	e.want(e.do(admin, "POST", "/sandboxes/sb-9/stop?tile=apps/mgr", nil), http.StatusNotFound, RefNotFound)
	e.want(e.do(admin, "GET", "/sandboxes/policy", nil), http.StatusOK, "")
	e.want(e.do(admin, "DELETE", "/sandboxes/sb-1?tile=apps/mgr", nil), http.StatusNoContent, "")
	e.want(e.do(mgr, "GET", "/sandboxes/sb-1", nil), http.StatusNotFound, RefNotFound)
}

// A manager only ever names its own sandboxes.
func TestKeyedByTile(t *testing.T) {
	e := newEnv(t)
	e.create(ns("sb-1"))
	other := auth.Principal{Component: "apps/mgr2", Via: "instance"}
	e.want(e.do(other, "GET", "/sandboxes/sb-1", nil), http.StatusNotFound, RefNotFound)
	e.want(e.do(other, "DELETE", "/sandboxes/sb-1", nil), http.StatusNotFound, RefNotFound)
	w := e.do(other, "GET", "/sandboxes", nil)
	e.want(w, http.StatusOK, "")
	if !strings.Contains(w.Body.String(), `"sandboxes":[]`) {
		t.Fatalf("another manager sees %s", w.Body)
	}
	// The same name is free under another tile.
	e.want(e.do(other, "POST", "/sandboxes", ns("sb-1")), http.StatusCreated, "")
}

// Without --isolate the manager routes answer unsupported, and the runtime
// says so.
func TestNoIsolation(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.Isolated = false })
	w := e.do(mgr, "GET", "/sandboxes/runtime", nil)
	e.want(w, http.StatusOK, "")
	var rt Runtime
	if err := json.Unmarshal(w.Body.Bytes(), &rt); err != nil {
		t.Fatal(err)
	}
	if rt.Isolation || len(rt.Modes) != 0 || len(rt.Unavailable) != 2 {
		t.Fatalf("runtime without isolation: %+v", rt)
	}
	for _, r := range [][2]string{{"GET", "/sandboxes"}, {"POST", "/sandboxes"}, {"GET", "/sandboxes/sb-1"}, {"POST", "/sandboxes/sb-1/run"}} {
		e.want(e.do(mgr, r[0], r[1], ns("sb-1")), http.StatusNotImplemented, RefUnsupported)
	}
	// Not a manager: still 403 first.
	e.want(e.do(user, "POST", "/sandboxes", ns("sb-1")), http.StatusForbidden, RefNotAllowed)
}

func TestCRUDAndPersistence(t *testing.T) {
	e := newEnv(t)
	in := e.create(map[string]any{"name": "sb-7f3a", "mode": "namespace", "labels": map[string]string{"manager.v": "1"},
		"for": "apps/agent", "forUser": "alice", "defaults": map[string]any{"cwd": "/work", "env": map[string]string{"HOME": "/home/dev"}}})
	if in.State != StateStopped || in.Version != 1 || in.Created != 1790000000000 || in.MemMiB != 2048 ||
		in.Net.Egress != "none" || in.Net.Reach != "none" || !in.AutoStart || in.IdleStopMin != 30 || in.Users != "any" {
		t.Fatalf("created %+v", in)
	}
	w := e.do(mgr, "GET", "/sandboxes", nil)
	var list struct{ Sandboxes []Info }
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || len(list.Sandboxes) != 1 || list.Sandboxes[0].Name != "sb-7f3a" {
		t.Fatalf("list %s", w.Body)
	}
	w = e.do(mgr, "PATCH", "/sandboxes/sb-7f3a", map[string]any{"labels": map[string]string{"k": "v"}, "idleStopMin": 60})
	e.want(w, http.StatusOK, "")
	if in := e.info(w); in.Version != 2 || in.Labels["k"] != "v" || in.Labels["manager.v"] != "" || in.IdleStopMin != 60 || in.RestartNeeded {
		t.Fatalf("patched %+v", in)
	}

	// The file is xbind's: 0600, version 1, keyed by tile.
	path := e.m.defsPath()
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("definitions file: %v %v", st, err)
	}
	var f struct {
		Version int
		Tiles   map[string]struct{ Sandboxes map[string]Def }
	}
	b, _ := os.ReadFile(path)
	if err := json.Unmarshal(b, &f); err != nil || f.Version != 1 || f.Tiles["apps/mgr"].Sandboxes["sb-7f3a"].ForUser != "alice" {
		t.Fatalf("definitions file: %s", b)
	}

	// A restarted xbind finds them.
	e2 := &testEnv{t: t, m: New(Options{Root: e.m.root, Isolated: true, UIDRange: true, Deps: testDeps()})}
	e2.mux = routes(e2.m)
	if in := e2.info(e2.do(mgr, "GET", "/sandboxes/sb-7f3a", nil)); in.Version != 2 || in.For != "apps/agent" || in.Defaults.Env["HOME"] != "/home/dev" {
		t.Fatalf("after restart %+v", in)
	}
	e2.want(e2.do(mgr, "DELETE", "/sandboxes/sb-7f3a", nil), http.StatusNoContent, "")
	e2.want(e2.do(mgr, "DELETE", "/sandboxes/sb-7f3a", nil), http.StatusNotFound, RefNotFound)
	b, _ = os.ReadFile(path)
	if strings.Contains(string(b), "sb-7f3a") || strings.Contains(string(b), "apps/mgr") {
		t.Fatalf("deleted, still stored: %s", b)
	}
}

func TestNames(t *testing.T) {
	e := newEnv(t)
	for _, n := range []string{"", "Sb", "-a", "a_b", "a.b", strings.Repeat("a", 33), "runtime", "policy", "copy"} {
		e.want(e.do(mgr, "POST", "/sandboxes", ns(n)), http.StatusBadRequest, RefInvalid)
	}
	for _, n := range []string{"a", "0", "sb-7f3a", strings.Repeat("a", 32)} {
		e.create(ns(n))
	}
	e.want(e.do(mgr, "GET", "/sandboxes/nope", nil), http.StatusNotFound, RefNotFound)
	// A name no create could make is refused before any lookup.
	e.want(e.do(mgr, "GET", "/sandboxes/BAD", nil), http.StatusBadRequest, RefInvalid)
	e.want(e.do(mgr, "DELETE", "/sandboxes/policy", nil), http.StatusBadRequest, RefInvalid)
	e.want(e.do(mgr, "PATCH", "/sandboxes/nope", "{}"), http.StatusNotFound, RefNotFound)
}

func TestClientID(t *testing.T) {
	e := newEnv(t)
	req := map[string]any{"name": "sb-1", "mode": "namespace", "clientId": "c-5e1"}
	first := e.create(req)
	w := e.do(mgr, "POST", "/sandboxes", req)
	e.want(w, http.StatusOK, "")
	if again := e.info(w); again.Name != "sb-1" || again.Created != first.Created || again.ClientID != "c-5e1" {
		t.Fatalf("repeat %+v", again)
	}
	// The same clientId for another request, and an existing name, are exists.
	e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-2", "mode": "namespace", "clientId": "c-5e1"}), http.StatusConflict, RefExists)
	e.want(e.do(mgr, "POST", "/sandboxes", ns("sb-1")), http.StatusConflict, RefExists)
	e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-3", "mode": "namespace", "clientId": "has space"}), http.StatusBadRequest, RefInvalid)
}

// Bodies decode leniently and are capped.
func TestBodies(t *testing.T) {
	e := newEnv(t)
	e.create(map[string]any{"name": "sb-1", "mode": "namespace", "futureField": map[string]any{"x": 1}})
	e.want(e.do(mgr, "POST", "/sandboxes", ""), http.StatusBadRequest, RefInvalid)
	e.want(e.do(mgr, "POST", "/sandboxes", "{not json"), http.StatusBadRequest, RefInvalid)
	big := map[string]any{"name": "sb-2", "mode": "namespace", "labels": map[string]string{"x": strings.Repeat("a", defBodyMax)}}
	e.want(e.do(mgr, "POST", "/sandboxes", big), http.StatusRequestEntityTooLarge, RefTooLarge)
	e.want(e.do(admin, "PUT", "/sandboxes/policy", `{"idleStopMin": 10, "`+strings.Repeat("x", defBodyMax)+`": 1}`), http.StatusRequestEntityTooLarge, RefTooLarge)
}

// Every per-sandbox route finds the sandbox first: one that isn't there is
// 404. The snapshot routes are served (snapshot_linux_test.go has them).
func TestRoutesFindTheSandbox(t *testing.T) {
	e := newEnv(t)
	e.create(ns("sb-1"))
	for _, r := range [][2]string{
		{"GET", "/sandboxes/sb-1/snapshots"}, {"POST", "/sandboxes/sb-1/snapshots"},
		{"POST", "/sandboxes/sb-1/snapshots/s-1/restore"}, {"DELETE", "/sandboxes/sb-1/snapshots/s-1"},
	} {
		e.want(e.do(mgr, r[0], strings.Replace(r[1], "sb-1", "sb-9", 1), nil), http.StatusNotFound, RefNotFound)
	}
	e.want(e.do(mgr, "GET", "/sandboxes/sb-1/snapshots", nil), http.StatusOK, "")
	e.want(e.do(mgr, "POST", "/sandboxes/sb-1/snapshots", nil), http.StatusBadRequest, RefInvalid)                     // a body is required
	e.want(e.do(mgr, "POST", "/sandboxes/sb-1/snapshots", map[string]any{"name": "x"}), http.StatusConflict, RefState) // it never ran
	e.want(e.do(mgr, "POST", "/sandboxes/sb-1/snapshots/s-1/restore", nil), http.StatusNotFound, RefNotFound)
	e.want(e.do(mgr, "DELETE", "/sandboxes/sb-1/snapshots/s-1", nil), http.StatusNotFound, RefNotFound)
	// the file routes are served: a sandbox that isn't there is 404 before
	// anything else is looked at (files_linux_test.go has the rest)
	for _, r := range [][2]string{
		{"GET", "/sandboxes/sb-9/files/stat"}, {"GET", "/sandboxes/sb-9/files/content"}, {"PUT", "/sandboxes/sb-9/files/content"},
		{"GET", "/sandboxes/sb-9/files/list"}, {"POST", "/sandboxes/sb-9/files/mkdir"}, {"POST", "/sandboxes/sb-9/files/remove"},
		{"POST", "/sandboxes/sb-9/files/move"}, {"GET", "/sandboxes/sb-9/tar"}, {"PUT", "/sandboxes/sb-9/tar"},
	} {
		e.want(e.do(mgr, r[0], r[1], nil), http.StatusNotFound, RefNotFound)
	}
	// The command routes are built: they find the sandbox first.
	for _, r := range [][2]string{
		{"POST", "/sandboxes/sb-9/run"}, {"GET", "/sandboxes/sb-9/execs"},
		{"POST", "/sandboxes/sb-9/execs"}, {"GET", "/sandboxes/sb-9/execs/abc123-1"}, {"DELETE", "/sandboxes/sb-9/execs/abc123-1"},
		{"GET", "/sandboxes/sb-9/execs/abc123-1/output"}, {"POST", "/sandboxes/sb-9/execs/abc123-1/stdin"},
		{"POST", "/sandboxes/sb-9/execs/abc123-1/signal"}, {"POST", "/sandboxes/sb-9/execs/abc123-1/resize"},
		{"GET", "/sandboxes/sb-9/execs/abc123-1/tty"}, {"GET", "/sandboxes/sb-9/tty"},
	} {
		e.want(e.do(mgr, r[0], r[1], nil), http.StatusNotFound, RefNotFound)
	}
	// A clone of a sandbox that never ran starts from nothing, as it would;
	// a start on create that fails leaves it stopped, and says why (this
	// runtime has no base rootfs).
	if in := e.create(map[string]any{"name": "sb-2", "mode": "namespace", "from": map[string]any{"sandbox": "sb-1"}}); in.State != StateStopped {
		t.Fatalf("a clone of nothing: %+v", in)
	}
	e.want(e.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-4", "mode": "namespace", "from": map[string]any{"sandbox": "sb-9"}}),
		http.StatusNotFound, RefNotFound)
	in := e.create(map[string]any{"name": "sb-3", "mode": "namespace", "start": true})
	if in.State != StateStopped || !contains(in.StateDetail, "base rootfs") {
		t.Fatalf("start on create: %+v", in)
	}
	e.want(e.do(mgr, "POST", "/sandboxes/sb-9/start", nil), http.StatusNotFound, RefNotFound)
	e.want(e.do(mgr, "POST", "/sandboxes/sb-9/stop", nil), http.StatusNotFound, RefNotFound)
}

func TestRuntime(t *testing.T) {
	e := newEnv(t, func(o *Options) { o.UIDRange = false })
	e.want(e.do(admin, "PUT", "/sandboxes/policy", `{"overrides":{"apps/mgr":{"perTile":{"max":32}}}}`), http.StatusOK, "")
	e.create(ns("sb-1"))
	w := e.do(mgr, "GET", "/sandboxes/runtime", nil)
	e.want(w, http.StatusOK, "")
	var rt Runtime
	if err := json.Unmarshal(w.Body.Bytes(), &rt); err != nil {
		t.Fatal(err)
	}
	if !rt.Enabled || !rt.Isolation || rt.Users != "root" || strings.Join(rt.Caps, ",") != "exec,tty,files,tar,snapshots,clone,ports" ||
		rt.Limits.Sandboxes != 32 || rt.Limits.Running != 4 || rt.Limits.PerSandbox.MaxMemMiB != 8192 ||
		rt.Limits.OutputRing != 1<<20 || rt.Limits.WaitMaxSec != 120 || rt.Used.Sandboxes != 1 {
		t.Fatalf("runtime %+v", rt)
	}
	if len(rt.Modes) != 2 || rt.Modes[1].Mode != ModeVM || rt.Modes[1].Accel != "kvm" || len(rt.Unavailable) != 0 {
		t.Fatalf("modes %+v / %+v", rt.Modes, rt.Unavailable)
	}
	if len(rt.Egress) != 2 || rt.Egress[0].Class != "none" || rt.Egress[1].Class != "class:internet" || rt.Egress[1].Reach != "internet" {
		t.Fatalf("egress %+v", rt.Egress)
	}
	// VM mode unavailable: listed with its reason, and a VM create names it.
	e2 := newEnv(t, func(o *Options) { o.Deps.Modes = fakeModes{reason: "an admin hasn't enabled VM tile sandboxes"} })
	w = e2.do(mgr, "GET", "/sandboxes/runtime", nil)
	if !strings.Contains(w.Body.String(), `"unavailable":[{"mode":"vm","reason":"an admin hasn't enabled VM tile sandboxes"}]`) {
		t.Fatalf("runtime %s", w.Body)
	}
	w = e2.do(mgr, "POST", "/sandboxes", map[string]any{"name": "sb-1", "mode": "vm"})
	e2.want(w, http.StatusBadRequest, RefInvalid)
	if !strings.Contains(w.Body.String(), "hasn't enabled") {
		t.Fatalf("vm create: %s", w.Body)
	}
}

func TestAdminList(t *testing.T) {
	e := newEnv(t)
	e.create(ns("sb-1"))
	other := auth.Principal{Component: "apps/mgr2", Via: "instance"}
	e.want(e.do(other, "POST", "/sandboxes", ns("sb-2")), http.StatusCreated, "")
	rows := e.m.AdminList("")
	if len(rows) != 2 || rows[0].Tile != "apps/mgr" || !rows[0].TileExists || rows[1].Tile != "apps/mgr2" || rows[1].TileExists ||
		rows[0].State != StateStopped || rows[0].MemMiB != 2048 {
		t.Fatalf("admin rows %+v", rows)
	}
	if rows := e.m.AdminList("apps/mgr2"); len(rows) != 1 || rows[0].Name != "sb-2" {
		t.Fatalf("narrowed %+v", rows)
	}
	// An admin deletes a removed tile's sandbox.
	e.want(e.do(admin, "DELETE", "/sandboxes/sb-2?tile=apps/mgr2", nil), http.StatusNoContent, "")
	if rows := e.m.AdminList(""); len(rows) != 1 {
		t.Fatalf("after delete %+v", rows)
	}
}
