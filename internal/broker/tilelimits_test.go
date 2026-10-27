package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/limits"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// limitsFx is a workspace for the tile limits (D203): orgFixture's — org
// sales owns apps/email (carol admins it, bob is a member, alice reads) —
// plus apps/notes owned by dave, the admin console (tiles/admin, xbin
// admin), and a runtime that answers memory, VM and sandbox questions.
type limitsFx struct {
	t  *testing.T
	b  *Broker
	st *users.Store

	mu      sync.Mutex
	applied []string
	usage   map[string]int64 // "tile|kind" → usage
	free    int64            // MemAvailable, MiB
}

func limitsFixture(t *testing.T) *limitsFx {
	t.Helper()
	b, st := orgFixture(t)
	fx := &limitsFx{t: t, b: b, st: st, usage: map[string]int64{}, free: 8192}
	if _, err := st.Upsert(users.User{ID: "dave", Role: users.RoleUser, Tiles: map[string]string{"apps/notes": users.LevelWrite}}, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/notes", "user:dave"); err != nil {
		t.Fatal(err)
	}
	fx.write("apps/email/xbin.json", `{"runtime":"go","limits":{"memory":"4G","disk":"10G"},
		"uses":[{"target":"apps/calendar","role":"reader"}]}`)
	fx.write("apps/notes/xbin.json", `{"runtime":"go"}`)
	fx.write("tiles/admin/xbin.json", `{}`)
	fx.write("tiles/admin/index.html", `<p>admin</p>`)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "tiles/admin", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	fx.rescan()
	b.SetLimitSources(LimitSources{
		Default: func(tile string, k limits.Kind) (int64, bool) {
			return map[limits.Kind]int64{limits.MemMiB: 2048, limits.VMMemMiB: 1024, limits.VMVCPUs: 2,
				limits.SbxMemMiB: 4096, limits.SbxVCPUs: 4, limits.SbxDiskGiB: 20}[k], true
		},
		Max: func(k limits.Kind) int64 {
			return map[limits.Kind]int64{limits.MemMiB: 16384, limits.VMMemMiB: 8192, limits.VMVCPUs: 8}[k]
		},
		Usage: func(tile string, k limits.Kind) int64 {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			return fx.usage[tile+"|"+string(k)]
		},
		Capacity: func(k limits.Kind) limits.Capacity {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			if k == limits.MemMiB {
				return limits.Capacity{Total: 16384, Free: fx.free}
			}
			return limits.Capacity{}
		},
		Apply: func(tile string, k limits.Kind, restart bool) {
			fx.mu.Lock()
			defer fx.mu.Unlock()
			fx.applied = append(fx.applied, fmt.Sprintf("%s %s %v", tile, k, restart))
		},
	})
	t.Cleanup(b.Close)
	return fx
}

func (fx *limitsFx) write(rel, body string) {
	fx.t.Helper()
	p := filepath.Join(fx.b.Reg.Root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		fx.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		fx.t.Fatal(err)
	}
}

func (fx *limitsFx) rescan() {
	fx.t.Helper()
	if err := fx.b.Reg.Rescan(); err != nil {
		fx.t.Fatal(err)
	}
}

// do calls a limits route as p; out (when non-nil) is the decoded answer.
func (fx *limitsFx) do(p auth.Principal, method, url, body string) (int, map[string]any) {
	fx.t.Helper()
	h := map[string]http.HandlerFunc{"GET": fx.b.apiLimitsGet, "POST": fx.b.apiLimitsPost, "DELETE": fx.b.apiLimitsDelete}[method]
	if strings.HasPrefix(url, "/limits/dismiss") {
		h = fx.b.apiLimitsDismiss
	}
	w := call(fx.t, h, p, method, url, body, nil)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	if out == nil && w.Body.Len() > 0 {
		out = map[string]any{"raw": w.Body.String()}
	}
	return w.Code, out
}

func (fx *limitsFx) raise(p auth.Principal, tile string, k limits.Kind, v int64, extra string) (int, map[string]any) {
	fx.t.Helper()
	return fx.do(p, "POST", "/limits", fmt.Sprintf(`{"tile":%q,"kind":%q,"value":%d,"source":"raise"%s}`, tile, k, v, extra))
}

func (fx *limitsFx) principals() map[string]auth.Principal {
	pf := func(id string) auth.Principal { return principalFor(fx.t, fx.st, id) }
	carol := pf("carol")
	return map[string]auth.Principal{
		"root":     {Owner: true, Via: "bearer"},
		"root2":    pf("root2"),
		"carol":    carol,
		"bob":      pf("bob"),
		"alice":    pf("alice"),
		"dave":     pf("dave"),
		"frame":    {Component: "apps/email", Via: "frame", UserID: "carol", Gen: "s.1", Access: carol.Access},
		"instance": {Component: "apps/email", Via: "instance"},
		"terminal": {Component: "apps/email", Via: "terminal", UserID: "carol", Access: carol.Access},
		"agent":    {Component: "apps/notes", Via: "terminal", UserID: "dave", Gen: "u.dave"},
		"consoleB": {Component: "tiles/admin", Via: "frame", UserID: "bob", Gen: "s.1"},
		"consoleA": {Component: "tiles/admin", Via: "frame", UserID: "root2", Gen: "s.1"},
		"shellC":   {Component: "shell", Via: "frame", UserID: "carol", Gen: "s.1", Access: carol.Access},
		"shellA":   {Component: "shell", Via: "frame", UserID: "root2", Gen: "s.1"},
		"viewAs":   {UserID: "carol", User: carol.User, Access: carol.Access, Impersonator: "owner"},
	}
}

func (fx *limitsFx) ceiling(org string, c map[limits.Kind]int64) {
	fx.t.Helper()
	if _, err := fx.st.SetOrgLimits(org, c); err != nil {
		fx.t.Fatal(err)
	}
}

func kindOf(out map[string]any, k limits.Kind) map[string]any {
	row, _ := out["tile"].(map[string]any)
	lim, _ := row["limits"].(map[string]any)
	m, _ := lim[string(k)].(map[string]any)
	return m
}

// covers D203 — data/tile-limits.json: no file until the first decision; a
// decision is saved (0600) and is what LimitGrant answers and the runtime's
// Apply hears; a file this xbind can't read leaves every tile at its
// default, answers writes 500, is never overwritten, and tells admins (an
// alert of kind tile-limits) — and a hand fix takes effect.
func TestTileLimitsStore(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	file := filepath.Join(fx.b.Reg.Root, "data", "tile-limits.json")
	if g := fx.b.LimitGrant("apps/email", limits.MemMiB); g != 0 {
		t.Fatalf("a grant before any decision: %d", g)
	}
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a file before any decision")
	}
	if code, out := fx.raise(ps["root"], "apps/email", limits.MemMiB, 3072, `,"restart":true`); code != 200 || kindOf(out, limits.MemMiB)["effective"] != 3072.0 {
		t.Fatalf("raise: %d %v", code, out)
	}
	if g := fx.b.LimitGrant("apps/email", limits.MemMiB); g != 3072 {
		t.Errorf("LimitGrant = %d, want 3072", g)
	}
	fx.mu.Lock()
	if len(fx.applied) != 1 || fx.applied[0] != "apps/email memMiB true" {
		t.Errorf("Apply: %v", fx.applied)
	}
	fx.mu.Unlock()
	var doc limits.File
	if bs, err := os.ReadFile(file); err != nil || json.Unmarshal(bs, &doc) != nil || doc.Schema != 1 ||
		doc.Tiles["apps/email"].Limits[limits.MemMiB].Set.Value != 3072 || doc.Tiles["apps/email"].Limits[limits.MemMiB].Set.Source != "raise" ||
		doc.Tiles["apps/email"].Limits[limits.MemMiB].Set.By != "owner" {
		t.Fatalf("the file: %+v %v", doc, err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file's mode: %v %v", fi, err)
	}

	// A fresh xbind over a file it can't read.
	fx2 := limitsFixture(t)
	ps2 := fx2.principals()
	bad := []byte(`{"schema":1,"tiles":{"apps/email":{"limits":{"memMiB":{"set":{"value":`)
	file2 := filepath.Join(fx2.b.Reg.Root, "data", "tile-limits.json")
	if err := os.WriteFile(file2, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	if g := fx2.b.LimitGrant("apps/email", limits.MemMiB); g != 0 {
		t.Errorf("an unreadable file grants %d", g)
	}
	if code, out := fx2.do(ps2["root"], "GET", "/limits?tile=apps/email", ""); code != 200 || kindOf(out, limits.MemMiB)["effective"] != 2048.0 {
		t.Errorf("GET over an unreadable file: %d %v", code, out)
	}
	if code, out := fx2.raise(ps2["root"], "apps/email", limits.MemMiB, 3072, ""); code != 500 || !strings.Contains(fmt.Sprint(out["error"]), "can't be read") {
		t.Errorf("POST over an unreadable file: %d %v", code, out)
	}
	if code, _ := fx2.do(ps2["root"], "DELETE", "/limits?tile=apps/email&kind=memMiB", ""); code != 500 {
		t.Errorf("DELETE over an unreadable file: %d", code)
	}
	fx2.b.RecordLimitHit("apps/email", limits.MemMiB, limits.Hit{Value: 2048})
	fx2.b.tlFlushHits(0)
	if got, _ := os.ReadFile(file2); !bytes.Equal(got, bad) {
		t.Errorf("the unreadable file was overwritten: %s", got)
	}
	alerts := func(p auth.Principal) []Alert {
		w := call(t, fx2.b.apiAlerts, p, "GET", "/alerts", "", nil)
		var out struct{ Alerts []Alert }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out.Alerts
	}
	has := func(as []Alert, kind string) bool {
		for _, a := range as {
			if a.Kind == kind {
				return true
			}
		}
		return false
	}
	if !has(alerts(ps2["root"]), "tile-limits") || !has(alerts(ps2["shellA"]), "tile-limits") || has(alerts(ps2["bob"]), "tile-limits") {
		t.Errorf("the tile-limits alert: admins only")
	}
	if !has(alerts(ps2["root"]), "oom") {
		t.Errorf("a hit recorded while the file can't be read still shows")
	}
	// A hand fix: the next read takes it, the hit kept in memory joins it.
	if err := os.WriteFile(file2, []byte(`{"schema":1,"tiles":{"apps/email":{"limits":{"memMiB":{"set":{"value":2560,"by":"user:root2","at":"2026-10-27T00:00:00Z","source":"admin-set"}}}}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, out := fx2.do(ps2["root"], "GET", "/limits?tile=apps/email", ""); code != 200 || kindOf(out, limits.MemMiB)["effective"] != 2560.0 || kindOf(out, limits.MemMiB)["hit"] == nil {
		t.Errorf("after a hand fix: %d %v", code, out)
	}
	if g := fx2.b.LimitGrant("apps/email", limits.MemMiB); g != 2560 {
		t.Errorf("LimitGrant after a hand fix: %d", g)
	}
	if has(alerts(ps2["root"]), "tile-limits") {
		t.Errorf("the tile-limits alert outlives the fix")
	}
}

// covers D203 — who decides a tile's limits: workspace admins (the root
// token, an admin's session, the admin console driven by an admin);
// an admin of the owning org within the org's ceiling, never above it,
// never with force; the shell's frame acting for its driver. Never the
// tile's own frame, instance, terminal or agent, a member, a user-owner (a
// manager reverts, doesn't raise), the console under a non-admin, or a
// view-as session.
func TestTileLimitsAuthz(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.ceiling("sales", map[limits.Kind]int64{limits.MemMiB: 4096})
	for _, c := range []struct {
		who  string
		tile string
		v    int64
		ext  string
		want int
	}{
		{"root", "apps/email", 2560, "", 200},
		{"root2", "apps/email", 2688, "", 200},
		{"consoleA", "apps/email", 2752, "", 200},
		{"carol", "apps/email", 4096, "", 200},
		{"shellC", "apps/email", 3072, "", 200},
		{"carol", "apps/email", 4160, "", 403},
		{"carol", "apps/email", 3000, `,"force":true`, 403},
		{"shellC", "apps/email", 8192, "", 403},
		{"bob", "apps/email", 3072, "", 403},
		{"alice", "apps/email", 3072, "", 403},
		{"frame", "apps/email", 3072, "", 403},
		{"instance", "apps/email", 3072, "", 403},
		{"terminal", "apps/email", 3072, "", 403},
		{"agent", "apps/notes", 3072, "", 403},
		{"consoleB", "apps/email", 3072, "", 403},
		{"viewAs", "apps/email", 3072, "", 403},
		{"dave", "apps/notes", 3072, "", 403},
		{"carol", "apps/notes", 3072, "", 403},
		{"root", "apps/email", 2048, "", 400},  // the default: DELETE instead
		{"root", "apps/email", 20000, "", 400}, // past the maximum
		{"root", "apps/nope", 3072, "", 404},
		{"bob", "apps/nope", 3072, "", 403}, // no existence probe
	} {
		if code, out := fx.raise(ps[c.who], c.tile, limits.MemMiB, c.v, c.ext); code != c.want {
			t.Errorf("%s raising %s to %d%s: %d %v, want %d", c.who, c.tile, c.v, c.ext, code, out, c.want)
		}
	}
	if code, out := fx.raise(ps["carol"], "apps/email", limits.MemMiB, 4160, ""); !strings.Contains(fmt.Sprint(out["error"]), "above org sales's ceiling of 4 GiB") {
		t.Errorf("above the ceiling, the answer says why: %d %v", code, out)
	}
	fx.ceiling("sales", map[limits.Kind]int64{limits.MemMiB: 0})
	if code, out := fx.raise(ps["carol"], "apps/email", limits.MemMiB, 2560, ""); code != 403 || !strings.Contains(fmt.Sprint(out["error"]), "no ceiling") {
		t.Errorf("an org without a ceiling: %d %v", code, out)
	}
	// Bodies and kinds.
	for _, body := range []string{``, `{}`, `{"tile":"apps/email","kind":"memory","value":3072}`, `{"tile":"apps/email","kind":"cpu","value":3,"source":"raise"}`,
		`{"tile":"apps/email","kind":"memMiB","value":-1,"source":"raise"}`, `{"tile":"apps/email","scope":"apps","kind":"memMiB","value":3072,"source":"raise"}`,
		`{"scope":"apps/calendar","kind":"memMiB","value":3072,"source":"raise"}`, `{"tile":"apps/email","kind":"diskGiB","value":60,"source":"raise"}`,
		`{"tile":"apps/email","kind":"vmMemMiB","value":2048,"source":"raise"}`, `{"tile":"apps/email","kind":"memMiB","value":5000,"source":"manifest-ask"}`,
		`{"tile":"apps/notes","kind":"memMiB","value":3000,"source":"manifest-ask"}`, `{"tile":"apps/email","kind":"memMiB","value":3072,"source":"raise","nope":1}`} {
		if code, out := fx.do(ps["root"], "POST", "/limits", body); code != 400 {
			t.Errorf("%s: %d %v, want 400", body, code, out)
		}
	}
	// A bx alias names the kind too.
	if code, out := fx.do(ps["root"], "POST", "/limits", `{"tile":"apps/email","kind":"memory","value":3200,"source":"admin-set"}`); code != 200 {
		t.Errorf("the kind's alias: %d %v", code, out)
	}
}

// covers D203 — DELETE puts a kind back to its default: the tile's managers
// (its user-owner, the owning org's admins) and admins, never a member or
// the tile's own credentials; a manifest ask still standing is pending
// again.
func TestTileLimitsRevert(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	if code, _ := fx.raise(ps["root"], "apps/notes", limits.MemMiB, 3072, ""); code != 200 {
		t.Fatal(code)
	}
	for _, who := range []string{"bob", "carol", "agent", "viewAs"} {
		if code, _ := fx.do(ps[who], "DELETE", "/limits?tile=apps/notes&kind=memMiB", ""); code != 403 {
			t.Errorf("%s reverting apps/notes: %d, want 403", who, code)
		}
	}
	if code, out := fx.do(ps["dave"], "DELETE", "/limits?tile=apps/notes&kind=memory", ""); code != 200 || kindOf(out, limits.MemMiB)["effective"] != 2048.0 {
		t.Errorf("the user-owner reverts: %d %v", code, out)
	}
	if g := fx.b.LimitGrant("apps/notes", limits.MemMiB); g != 0 {
		t.Errorf("LimitGrant after the revert: %d", g)
	}
	// An approved ask, reverted by the owning org's admin: pending again.
	if code, out := fx.do(ps["root"], "POST", "/limits", `{"tile":"apps/email","kind":"memMiB","value":4096,"source":"manifest-ask"}`); code != 200 ||
		kindOf(out, limits.MemMiB)["pending"] != nil || kindOf(out, limits.MemMiB)["approved"] == nil {
		t.Fatalf("approve the ask: %d %v", code, out)
	}
	if code, _ := fx.do(ps["frame"], "DELETE", "/limits?tile=apps/email&kind=memMiB", ""); code != 403 {
		t.Errorf("the tile's own frame reverting: %d", code)
	}
	if code, out := fx.do(ps["carol"], "DELETE", "/limits?tile=apps/email&kind=memMiB", ""); code != 200 || kindOf(out, limits.MemMiB)["pending"] != true {
		t.Errorf("the org admin reverts, the ask pends again: %d %v", code, out)
	}
}

// covers D203 — what waits: a manifest ask above the default is pending
// for those who may approve it or manage the tile (not a member), approvable
// per the viewer; an ask placed where it can't apply is a note, never a
// prompt; an offloaded tile asks nothing; a scope's disk asks from the tile
// that roots it.
func TestTileLimitsPending(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.b.disk.mu.Lock()
	fx.b.disk.quota, fx.b.disk.totalB, fx.b.disk.freeB = 1<<30, 100<<30, 60<<30
	fx.b.disk.mu.Unlock()
	fx.write("apps/calendar/xbin.json", `{"runtime":"go","limits":{"disk":"3G"}}`)
	fx.rescan()
	pending := func(who string) []map[string]any {
		t.Helper()
		code, out := fx.do(ps[who], "GET", "/limits?pending=1", "")
		if code != 200 {
			t.Fatalf("%s: %d %v", who, code, out)
		}
		var items []map[string]any
		for _, it := range out["pending"].([]any) {
			items = append(items, it.(map[string]any))
		}
		return items
	}
	find := func(items []map[string]any, tile string, k limits.Kind) map[string]any {
		for _, it := range items {
			if it["tile"] == tile && it["kind"] == string(k) {
				return it
			}
		}
		return nil
	}
	root := pending("root")
	if it := find(root, "apps/email", limits.MemMiB); it == nil || it["why"] != "ask" || it["ask"] != 4096.0 || it["approvable"] != true || it["effective"] != 2048.0 {
		t.Errorf("root's memory ask: %v", root)
	}
	if it := find(root, "apps/calendar", limits.DiskGiB); it == nil || it["scope"] != "apps/calendar" || it["ask"] != 3.0 || it["unit"] != "GiB" {
		t.Errorf("the scope root's disk ask: %v", root)
	}
	if find(root, "apps/email", limits.DiskGiB) != nil {
		t.Errorf("a disk ask off the scope root prompts: %v", root)
	}
	if it := find(pending("carol"), "apps/email", limits.MemMiB); it == nil || it["approvable"] != false || it["mine"] != true {
		t.Errorf("the org admin without a ceiling: shown as hers, not approvable: %v", it)
	}
	fx.ceiling("sales", map[limits.Kind]int64{limits.MemMiB: 4096})
	if it := find(pending("carol"), "apps/email", limits.MemMiB); it == nil || it["approvable"] != true {
		t.Errorf("the org admin within the ceiling: %v", it)
	}
	if len(pending("bob")) != 0 || len(pending("instance")) != 0 {
		t.Errorf("a member, the tile itself: nothing waits for them")
	}
	code, out := fx.do(ps["root"], "GET", "/limits?tile=apps/email", "")
	if disk := kindOf(out, limits.DiskGiB); code != 200 || !strings.Contains(fmt.Sprint(disk["note"]), "ignored: the quota is the workspace's") || disk["canDecide"] != nil {
		t.Errorf("the misplaced disk ask's note: %d %v", code, disk)
	}
	if mem := kindOf(out, limits.MemMiB); mem["pending"] != true || mem["ask"] != 4096.0 || len(mem["choices"].([]any)) != 3 || out["capacity"] == nil {
		t.Errorf("the memory row: %v (capacity %v)", mem, out["capacity"])
	}
	// The tile's own credentials read their row, decide nothing; a reader reads it.
	for _, who := range []string{"instance", "alice"} {
		code, out := fx.do(ps[who], "GET", "/limits?tile=apps/email", "")
		if mem := kindOf(out, limits.MemMiB); code != 200 || mem["choices"] != nil || mem["canDecide"] != nil || out["capacity"] != nil {
			t.Errorf("%s reads read only: %d %v", who, code, out)
		}
	}
	if code, _ := fx.do(ps["dave"], "GET", "/limits?tile=apps/email", ""); code != 403 {
		t.Errorf("a stranger asks by name: %d", code)
	}
	// Offloaded: nothing pends.
	if err := fx.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Lifecycle = map[string]string{"apps/email": registry.StateOffloaded}
	}); err != nil {
		t.Fatal(err)
	}
	if find(pending("root"), "apps/email", limits.MemMiB) != nil {
		t.Errorf("an offloaded tile's ask pends")
	}
	// The list: rows for what the viewer manages.
	code, out = fx.do(ps["dave"], "GET", "/limits", "")
	if rows := out["tiles"].([]any); code != 200 || len(rows) != 1 || rows[0].(map[string]any)["tile"] != "apps/notes" || out["canSetCeilings"] != false {
		t.Errorf("dave's list: %d %v", code, out)
	}
	code, out = fx.do(ps["root"], "GET", "/limits", "")
	if code != 200 || len(out["tiles"].([]any)) < 3 || out["canSetCeilings"] != true || out["defaults"].(map[string]any)["memMiB"] != 2048.0 ||
		out["orgCeilings"].(map[string]any)["sales"] == nil {
		t.Errorf("root's list: %d %v", code, out)
	}
	if code, out := fx.do(ps["carol"], "GET", "/limits", ""); code != 200 || out["orgCeilings"].(map[string]any)["sales"].(map[string]any)["memMiB"] != 4096.0 {
		t.Errorf("carol's ceilings: %d %v", code, out)
	}
}

// covers D203 — the limits event: a decision, a new hit and a new manifest
// ask each publish {type: limits, component: <tile>}.
func TestTileLimitsEvents(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.b.RefreshLimitAsks() // the baseline
	ch, stop := fx.b.Hub.Subscribe(func(e events.Event) bool { return e.Type == "limits" })
	defer stop()
	expect := func(what, tile string) {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case e := <-ch:
				if e.Component == tile {
					return
				}
			case <-deadline:
				t.Fatalf("%s: no limits event for %s", what, tile)
			}
		}
	}
	if code, _ := fx.raise(ps["root"], "apps/notes", limits.MemMiB, 3072, ""); code != 200 {
		t.Fatal(code)
	}
	expect("a decision", "apps/notes")
	fx.b.RecordLimitHit("apps/calendar", limits.MemMiB, limits.Hit{Value: 2048})
	expect("a hit", "apps/calendar")
	fx.write("apps/notes/xbin.json", `{"runtime":"go","limits":{"memory":"8G"}}`)
	fx.rescan()
	fx.b.RefreshLimitAsks()
	expect("a new ask", "apps/notes")
}

// covers D203 — hits: sticky, coalesced (one save per 10 s at most),
// shown as alerts with a raise action to the tile's readers (a non-primary
// deployment's to admins only), dismissed for everyone by an approver
// only, and cleared by a decision on the kind.
func TestTileLimitsHits(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.ceiling("sales", map[limits.Kind]int64{limits.MemMiB: 3000})
	file := filepath.Join(fx.b.Reg.Root, "data", "tile-limits.json")
	saved := func() (limits.Hit, bool) {
		var doc limits.File
		bs, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(bs, &doc) != nil || doc.Tiles["apps/email"] == nil {
			return limits.Hit{}, false
		}
		h, ok := doc.Tiles["apps/email"].Hits[limits.MemMiB]
		return h, ok
	}
	t0 := time.Date(2026, 10, 27, 14, 2, 0, 0, time.UTC)
	fx.b.RecordLimitHit("apps/email", limits.MemMiB, limits.Hit{At: t0, Value: 2048, Detail: "OOM-killed (1 process)"})
	for deadline := time.Now().Add(5 * time.Second); ; {
		if h, ok := saved(); ok && h.Count == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("a new hit isn't saved")
		}
		time.Sleep(10 * time.Millisecond)
	}
	fx.b.RecordLimitHit("apps/email", limits.MemMiB, limits.Hit{At: t0.Add(time.Minute), Value: 2048, Count: 2})
	if h, _ := saved(); h.Count != 1 {
		t.Errorf("a repeat within 10 s is saved at once: %+v", h)
	}
	fx.b.tlFlushHits(0)
	if h, _ := saved(); h.Count != 3 || !h.Since.Equal(t0) || !h.At.Equal(t0.Add(time.Minute)) {
		t.Errorf("the coalesced hit: %+v", h)
	}
	alertsOf := func(who string) []Alert {
		w := call(t, fx.b.apiAlerts, ps[who], "GET", "/alerts", "", nil)
		var out struct{ Alerts []Alert }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		var mine []Alert
		for _, a := range out.Alerts {
			if a.Kind == "oom" {
				mine = append(mine, a)
			}
		}
		return mine
	}
	a := alertsOf("root")
	if len(a) != 1 || a[0].Tile != "apps/email" || a[0].Dismiss != "/limits/dismiss?tile=apps%2Femail&kind=memMiB" || a[0].Action == nil ||
		!strings.Contains(a[0].Message, "apps/email was OOM-killed at its 2 GiB memory limit (OOM-killed (1 process)) (3× since 14:02 UTC)") ||
		!strings.Contains(a[0].Message, "bx limits raise apps/email memory") {
		t.Fatalf("root's oom alert: %+v", a)
	}
	if ta := fx.b.TileAlerts("apps/email"); len(ta) == 0 || ta[len(ta)-1].Kind != "oom" || ta[len(ta)-1].Action != nil {
		t.Errorf("the tile's own status shows its hit, deciding nothing: %+v", ta)
	}
	if a := alertsOf("alice"); len(a) != 1 || a[0].Dismiss != "" {
		t.Errorf("a reader's oom alert: no dismiss: %+v", a)
	}
	if a := alertsOf("dave"); len(a) != 0 {
		t.Errorf("a stranger sees the hit: %+v", a)
	}
	if code, _ := fx.do(ps["bob"], "POST", "/limits/dismiss?tile=apps%2Femail&kind=memMiB", ""); code != 403 {
		t.Errorf("a member dismisses for everyone: %d", code)
	}
	if code, _ := fx.do(ps["shellC"], "POST", "/limits/dismiss?tile=apps%2Femail&kind=memMiB", ""); code != 204 {
		t.Errorf("the org admin (an approver) dismisses through the shell's frame: %d", code)
	}
	if a := alertsOf("root"); len(a) != 0 {
		t.Errorf("dismissed, still shown: %+v", a)
	}
	// A decision on the kind clears its hit; a non-primary deployment's hit is admins'.
	fx.b.RecordLimitHit("apps/email", limits.MemMiB, limits.Hit{Value: 2048, Deployment: "dev"})
	if a := alertsOf("alice"); len(a) != 0 {
		t.Errorf("a deployment's hit reaches a reader: %+v", a)
	}
	if a := alertsOf("root"); len(a) != 1 || a[0].Deployment != "dev" || !strings.Contains(a[0].Message, "apps/email (deployment dev)") {
		t.Errorf("a deployment's hit for admins: %+v", a)
	}
	if code, _ := fx.raise(ps["root"], "apps/email", limits.MemMiB, 3072, ""); code != 200 {
		t.Fatal(code)
	}
	if a := alertsOf("root"); len(a) != 0 {
		t.Errorf("a raise leaves the hit: %+v", a)
	}
}

// covers D203 — an alert's raise action is decorated per viewer: choices
// from the current limit, each approvable for this viewer (an org admin up
// to the ceiling), dismiss only for approvers; the shell's frame is its
// driver; another tile's frame decides nothing.
func TestTileLimitsAlertDecoration(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.ceiling("sales", map[limits.Kind]int64{limits.MemMiB: 3000})
	fx.b.RecordLimitHit("apps/email", limits.MemMiB, limits.Hit{Value: 2048})
	alertOf := func(p auth.Principal) *Alert {
		w := call(t, fx.b.apiAlerts, p, "GET", "/alerts", "", nil)
		var out struct{ Alerts []Alert }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		for _, a := range out.Alerts {
			if a.Kind == "oom" {
				return &a
			}
		}
		return nil
	}
	approvable := func(a *Alert) string {
		if a == nil || a.Action == nil {
			return "none"
		}
		var s []string
		for _, c := range a.Action.Choices {
			s = append(s, fmt.Sprintf("%d:%v", c.Value, c.Approvable))
		}
		return strings.Join(s, " ")
	}
	for who, want := range map[string]string{
		"root":   "2560:true 3072:true 4096:true",
		"shellA": "2560:true 3072:true 4096:true",
		"carol":  "2560:true 3072:false 4096:false",
		"shellC": "2560:true 3072:false 4096:false",
		"bob":    "2560:false 3072:false 4096:false",
		"frame":  "2560:false 3072:false 4096:false",
	} {
		a := alertOf(ps[who])
		if got := approvable(a); got != want {
			t.Errorf("%s: choices %s, want %s", who, got, want)
			continue
		}
		wantDismiss := who == "root" || who == "shellA" || who == "carol" || who == "shellC"
		if (a.Dismiss != "") != wantDismiss {
			t.Errorf("%s: dismiss %q", who, a.Dismiss)
		}
		if a.Action.Current != 2048 || a.Action.Unit != "MiB" || a.Action.Approvers[0] != "workspace-admin" {
			t.Errorf("%s: action %+v", who, a.Action)
		}
	}
	if a := alertOf(ps["carol"]); a.Action.Choices[1].Why == "" || !a.Action.Choices[1].Default {
		t.Errorf("a choice above the ceiling says why; +50%% is the default: %+v", a.Action.Choices)
	}
	// A disk alert's action (the disk monitor's): decorated on a clone.
	fx.b.disk.mu.Lock()
	fx.b.disk.quota, fx.b.disk.totalB, fx.b.disk.freeB = 1<<30, 100<<30, 60<<30
	fx.b.disk.mu.Unlock()
	src := &limits.Action{Action: limits.ActionRaise, Tile: "apps/calendar", Scope: "apps/calendar", Kind: limits.DiskGiB}
	out := fx.b.decorateAlerts(ps["root"], []Alert{{Level: "crit", Kind: "quota", Tile: "apps~calendar", Action: src}})
	if act := out[0].Action; act == src || src.Choices != nil || act.Unit != "GiB" || act.Current != 1 || len(act.Choices) != 3 || !act.Choices[0].Fits || !act.Choices[0].Approvable {
		t.Errorf("the disk action: %+v (source %+v)", act, src)
	}
}

// covers D203 — fit is checked again under the store's lock: two approvals
// racing for room that holds only one of them can't both pass; force (a
// workspace admin's) takes it anyway.
func TestTileLimitsFitUnderLock(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.mu.Lock()
	fx.free = 1638 + 1500 // the reserve (10 % of 16 GiB) and room for one +1 GiB
	fx.mu.Unlock()
	for i := range 5 {
		var wg sync.WaitGroup
		codes := make([]int, 2)
		start := make(chan struct{})
		for j, tile := range []string{"apps/email", "apps/calendar"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				codes[j], _ = fx.raise(ps["root"], tile, limits.MemMiB, 3072, "")
			}()
		}
		close(start)
		wg.Wait()
		if !(codes[0] == 200 && codes[1] == 409 || codes[0] == 409 && codes[1] == 200) {
			t.Fatalf("round %d: %v, want one 200 and one 409", i, codes)
		}
		for _, tile := range []string{"apps/email", "apps/calendar"} {
			if code, _ := fx.do(ps["root"], "DELETE", "/limits?tile="+tile+"&kind=memMiB", ""); code != 200 {
				t.Fatal(code)
			}
		}
	}
	if code, _ := fx.raise(ps["root"], "apps/email", limits.MemMiB, 3072, ""); code != 200 {
		t.Fatal(code)
	}
	code, out := fx.raise(ps["root"], "apps/calendar", limits.MemMiB, 3072, "")
	if code != 409 || !strings.Contains(fmt.Sprint(out["error"]), "1 GiB granted to other tiles but unused") {
		t.Errorf("the second: %d %v", code, out)
	}
	// Usage counts: the first tile uses its raise, so it promises nothing more.
	fx.mu.Lock()
	fx.usage["apps/email|memMiB"] = 3072
	fx.free = 1638 + 1500 - 1024
	fx.mu.Unlock()
	if code, _ := fx.raise(ps["root"], "apps/calendar", limits.MemMiB, 3072, `,"force":true`); code != 200 {
		t.Errorf("force: %d", code)
	}
}

// covers D203 — LimitGrant follows the decisions: a raise, an approved ask
// up to what the manifest still asks (dormant once it doesn't, the raise
// holding), a revert, a scope's disk by its data key, the maximum's clamp.
func TestTileLimitsGrant(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	g := func() int64 { return fx.b.LimitGrant("apps/email", limits.MemMiB) }
	if code, _ := fx.do(ps["root"], "POST", "/limits", `{"tile":"apps/email","kind":"memMiB","value":4096,"source":"manifest-ask"}`); code != 200 || g() != 4096 {
		t.Fatalf("an approved ask: %d %d", code, g())
	}
	if code, _ := fx.raise(ps["root"], "apps/email", limits.MemMiB, 3072, ""); code != 200 || g() != 4096 {
		t.Errorf("a lower raise beside the ask: %d %d", code, g())
	}
	fx.write("apps/email/xbin.json", `{"runtime":"go","limits":{"memory":"3.5G"}}`)
	fx.rescan()
	if g() != 3584 {
		t.Errorf("the ask lowered: %d, want 3584", g())
	}
	fx.write("apps/email/xbin.json", `{"runtime":"go"}`)
	fx.rescan()
	_, out := fx.do(ps["root"], "GET", "/limits?tile=apps/email", "")
	if g() != 3072 || kindOf(out, limits.MemMiB)["dormant"] != true || kindOf(out, limits.MemMiB)["effective"] != 3072.0 {
		t.Errorf("the ask dropped: grant %d, row %v", g(), kindOf(out, limits.MemMiB))
	}
	fx.write("apps/email/xbin.json", `{"runtime":"go","limits":{"memory":"4G"}}`)
	fx.rescan()
	if _, out := fx.do(ps["root"], "GET", "/limits?tile=apps/email", ""); g() != 4096 || kindOf(out, limits.MemMiB)["pending"] != nil {
		t.Errorf("asked again: no new prompt: %d %v", g(), kindOf(out, limits.MemMiB))
	}
	fx.b.SetLimitSources(LimitSources{Default: fx.b.tl.src.Default, Max: func(limits.Kind) int64 { return 3500 }})
	fx.b.RefreshLimitAsks()
	if g() != 3500 {
		t.Errorf("clamped by the maximum: %d", g())
	}
	if code, _ := fx.do(ps["root"], "DELETE", "/limits?tile=apps/email&kind=memMiB", ""); code != 200 || g() != 0 {
		t.Errorf("reverted: %d %d", code, g())
	}
	// A scope's disk, by the scope or its root tile.
	fx.b.disk.mu.Lock()
	fx.b.disk.quota, fx.b.disk.totalB, fx.b.disk.freeB = 1<<30, 100<<30, 60<<30
	fx.b.disk.mu.Unlock()
	if code, out := fx.do(ps["root"], "POST", "/limits", `{"scope":"apps/calendar","kind":"disk","value":3,"source":"admin-set"}`); code != 200 ||
		fx.b.LimitGrant("apps~calendar", limits.DiskGiB) != 3 || kindOf(out, limits.DiskGiB)["effective"] != 3.0 {
		t.Errorf("a scope's disk: %d %v", code, out)
	}
	if code, _ := fx.do(ps["root"], "POST", "/limits", `{"tile":"apps/calendar","kind":"diskGiB","value":4,"source":"raise"}`); code != 200 ||
		fx.b.LimitGrant("apps~calendar", limits.DiskGiB) != 4 {
		t.Errorf("by its root tile: %d", code)
	}
	if code, out := fx.do(ps["root"], "POST", "/limits", `{"scope":"workspace","kind":"diskGiB","value":2,"source":"raise"}`); code != 200 ||
		fx.b.LimitGrant("workspace", limits.DiskGiB) != 2 {
		t.Errorf("the workspace scope: %d %v", code, out)
	}
	if code, _ := fx.do(ps["carol"], "POST", "/limits", `{"scope":"workspace","kind":"diskGiB","value":3,"source":"raise"}`); code != 403 {
		t.Errorf("the workspace scope is admins': %d", code)
	}
}

// covers D203 — PUT /orgs/{org}/limits: workspace admins set an org's
// per-tile ceilings kind by kind (null or 0 removes one); org admins can't;
// GET /orgs carries them.
func TestOrgLimitsRoute(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	put := func(p auth.Principal, org, body string) (int, string) {
		w := call(t, fx.b.apiOrgLimitsPut, p, "PUT", "/orgs/"+org+"/limits", body, map[string]string{"org": org})
		return w.Code, w.Body.String()
	}
	if code, body := put(ps["root"], "sales", `{"memMiB":8192,"diskGiB":500}`); code != 200 || !strings.Contains(body, `"memMiB":8192`) {
		t.Fatalf("set: %d %s", code, body)
	}
	if code, body := put(ps["root"], "sales", `{"diskGiB":null,"vmVCPUs":4}`); code != 200 || strings.Contains(body, "diskGiB") || !strings.Contains(body, `"vmVCPUs":4`) {
		t.Errorf("a change: %d %s", code, body)
	}
	for _, c := range []struct {
		who, org, body string
		want           int
	}{
		{"carol", "sales", `{"memMiB":99999}`, 403},
		{"consoleB", "sales", `{"memMiB":1}`, 200}, // the admin tile's own grant: user management (canManageUsers), as for every org knob
		{"root", "sales", `{"cpu":1}`, 400},
		{"root", "sales", `{"memMiB":-5}`, 400},
		{"root", "sales", `[1]`, 400},
		{"root", "nope", `{"memMiB":1}`, 404},
	} {
		if code, body := put(ps[c.who], c.org, c.body); code != c.want {
			t.Errorf("%s %s %s: %d %s, want %d", c.who, c.org, c.body, code, body, c.want)
		}
	}
	w := call(t, fx.b.apiOrgsList, ps["carol"], "GET", "/orgs", "", nil)
	if !strings.Contains(w.Body.String(), `"limits":{`) {
		t.Errorf("GET /orgs lacks the ceilings: %s", w.Body.String())
	}
}

// covers D203 — "restart now": POST at the value that already holds, with
// restart, restarts the backend (a VM's size applies at its next start)
// and records nothing new — also at the default; not for a stranger.
func TestTileLimitsRestartNow(t *testing.T) {
	fx := limitsFixture(t)
	ps := fx.principals()
	fx.write("apps/vm/xbin.json", `{"runtime":"go","vm":{"memory":"2G","vcpus":2}}`)
	fx.rescan()
	if err := fx.st.SetOwner("apps/vm", "user:dave"); err != nil {
		t.Fatal(err)
	}
	code, out := fx.do(ps["root"], "POST", "/limits", `{"tile":"apps/vm","kind":"vmMemMiB","value":2048,"source":"manifest-ask"}`)
	vm := kindOf(out, limits.VMMemMiB)
	if code != 200 || vm["effective"] != 2048.0 || vm["appliesAt"] != "next-start" || kindOf(out, limits.MemMiB) != nil {
		t.Fatalf("approve the VM's size: %d %v", code, out)
	}
	before, _ := os.ReadFile(filepath.Join(fx.b.Reg.Root, "data", "tile-limits.json"))
	fx.mu.Lock()
	fx.applied = nil
	fx.mu.Unlock()
	if code, out := fx.do(ps["root"], "POST", "/limits", `{"tile":"apps/vm","kind":"vmMemMiB","value":2048,"source":"manifest-ask","restart":true}`); code != 200 {
		t.Fatalf("restart now: %d %v", code, out)
	}
	if code, out := fx.do(ps["dave"], "POST", "/limits", `{"tile":"apps/vm","kind":"vmVCPUs","value":2,"source":"raise","restart":true}`); code != 200 {
		t.Errorf("restart now at the default, by the tile's owner: %d %v", code, out)
	}
	if code, _ := fx.do(ps["bob"], "POST", "/limits", `{"tile":"apps/vm","kind":"vmVCPUs","value":2,"source":"raise","restart":true}`); code != 403 {
		t.Errorf("a stranger restarts: %d", code)
	}
	after, _ := os.ReadFile(filepath.Join(fx.b.Reg.Root, "data", "tile-limits.json"))
	fx.mu.Lock()
	defer fx.mu.Unlock()
	if !bytes.Equal(before, after) || strings.Join(fx.applied, ",") != "apps/vm vmMemMiB true,apps/vm vmVCPUs true" {
		t.Errorf("restart now recorded something, or didn't restart: %v\n%s\n%s", fx.applied, before, after)
	}
}
