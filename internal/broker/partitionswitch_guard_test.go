package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

const docsUserManifest = `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`

// pend fills apps/docs with data and asks for user partitions: pending.
func (f *switchFx) pend(manifest string) (nsKey string) {
	f.t.Helper()
	nsKey, _ = f.fill()
	f.write(map[string]string{"apps/docs/xbin.json": manifest})
	f.rescan()
	if st, _, _ := f.state("apps/docs"); st != registry.PartitionPending {
		f.t.Fatalf("apps/docs: %v, want pending", st)
	}
	return nsKey
}

// covers PD-44 01§2.2 01§2.6 D118 — a top-level tile at the path
// "workspace" renders the workspace-level resources' bucket prefix
// (res:workspace/) and data key: those aren't its data. It doesn't "hold
// data" through them, a switch's dry run counts none of them, and its
// owner's switch leaves every workspace-level bucket, volume and plaintext
// file in place.
func TestPartitionSwitchWorkspacePath(t *testing.T) {
	f := newSwitchFxWith(t, map[string]string{
		"xbin.json":            `{"resources":{"shared2":{"type":"kv"},"files":{"type":"filesystem"}}}`,
		"workspace/scope.json": `{}`,
		"workspace/xbin.json":  `{"runtime":"go"}`,
	})
	if _, err := f.st.Upsert(users.User{ID: "mallory", Role: users.RoleUser}, "password"); err != nil {
		t.Fatal(err)
	}
	if err := f.st.SetOwner("workspace", "user:mallory"); err != nil {
		t.Fatal(err)
	}
	f.kvPut("workspace", "shared2") // res:workspace/shared2: the workspace-level resource
	vol := f.emptyVolume("", "files")
	f.write(map[string]string{
		mustRel(t, f.root, filepath.Join(vol, "Zm9v")): "the workspace's ciphertext",
		"data/resources/workspace/files/x":             "the workspace's plaintext",
	})
	ask := registry.PartitionAsk{Tile: "workspace", Scope: "workspace", RootsScope: true}
	if held, err := holdsNamespaces(f.b, ask); held || err != nil {
		t.Fatalf("the workspace tile holds the workspace-level resources: %v %v", held, err)
	}
	// it holds data of its own (a vault key): pending
	if err := f.b.vaultWrite("workspace", map[string]string{"k": "v"}); err != nil {
		t.Fatal(err)
	}
	f.write(map[string]string{"workspace/xbin.json": `{"runtime":"go","partition":["user"]}`})
	f.rescan()
	if st, _, _ := f.state("workspace"); st != registry.PartitionPending {
		t.Fatalf("workspace: %v, want pending", st)
	}
	mallory := principalFor(t, f.st, "mallory")
	code, out := f.act(mallory, `{"tile":"workspace","act":"switch","from":null,"to":{"user":true},"dryRun":true}`)
	wiped, _ := out["wiped"].(map[string]any)
	if code != 200 || wiped["namespaces"] != 0.0 || wiped["bytes"] != 0.0 || wiped["vaultKeys"] != 1.0 {
		t.Fatalf("dry run: %d %v", code, out)
	}
	if code, out := f.act(mallory, `{"tile":"workspace","act":"switch","from":null,"to":{"user":true},"confirm":"workspace"}`); code != 200 {
		t.Fatalf("switch: %d %v", code, out)
	}
	if !f.bucketHas("res:workspace/shared2") || !f.exists("data/resources/workspace/files/x") ||
		!f.exists(mustRel(t, f.root, filepath.Join(vol, "Zm9v"))) {
		t.Error("the workspace tile's switch deleted workspace-level data")
	}
	if f.exists("data/vault/" + util.CompKey("workspace") + ".json") {
		t.Error("the workspace tile's own vault survived its switch")
	}
}

// covers PD-44 11§3 01§2.5 — an erase that writes no tombstone fails the
// switch: 500, nothing recorded, the request open, the backup key intact;
// a retry erases and records.
func TestPartitionSwitchEraseFails(t *testing.T) {
	f := newSwitchFx(t)
	nsKey := f.pend(docsUserManifest)
	erased := filepath.Join(f.root, "data", "vault", backupKeysDir, backupErasedFile)
	if err := os.WriteFile(erased, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	root := auth.Principal{Owner: true}
	if code, out := f.act(root, switchUser); code != 500 || !strings.Contains(out["error"].(string), "backup keys") {
		t.Fatalf("a switch whose erase fails: %d %v", code, out)
	}
	if st, _, req := f.state("apps/docs"); st != registry.PartitionPending || req == nil || f.ops("apps/docs") != "request" {
		t.Errorf("after a failed erase: %v %+v, history %q", st, req, f.ops("apps/docs"))
	}
	if !f.exists("data/vault/.backup-keys/" + nsKey + ".json") {
		t.Error("the ns: key went although no tombstone was written")
	}
	if f.bucketHas("res:apps/docs/db") {
		t.Error("the wipe didn't run before the erase")
	}
	if err := os.WriteFile(erased, []byte("[]"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, out := f.act(root, switchUser)
	if wiped, _ := out["wiped"].(map[string]any); code != 200 || wiped["subkeys"] != 1.0 {
		t.Fatalf("the retry: %d %v", code, out)
	}
	if f.exists("data/vault/.backup-keys/"+nsKey+".json") || f.ops("apps/docs") != "request,switch" {
		t.Errorf("after the retry: key there %v, history %q", f.exists("data/vault/.backup-keys/"+nsKey+".json"), f.ops("apps/docs"))
	}
}

// covers PD-44 01§2.5 — the namespaces are held before anything stops: when
// a backend of the scope is stopped, its namespace is held already and
// neither the tile's primary nor another tile of the scope may start; both
// may again once the switch is done.
func TestPartitionSwitchHoldsBeforeStop(t *testing.T) {
	// a tile of the scope that doesn't root it (it asks alike: every tile of
	// a partitioned scope does)
	f := newSwitchFxWith(t, map[string]string{"apps/docs/sub/xbin.json": `{"runtime":"go","partition":["user"]}`})
	f.pend(docsUserManifest)
	var mu sync.Mutex
	stopped := map[string]string{} // tile → what was wrong when it stopped ("" ok)
	f.b.StopBackend = func(comp string) {
		why := ""
		switch {
		case f.b.busyAct(nsOf("apps/docs", util.MainDeployment)) != nsResetting:
			why = "main's namespace isn't held"
		case f.b.PartitionHoldReason(comp) == "":
			why = "it may start again"
		}
		mu.Lock()
		stopped[comp] = why
		mu.Unlock()
	}
	if code, out := f.act(auth.Principal{Owner: true}, switchUser); code != 200 {
		t.Fatalf("switch: %d %v", code, out)
	}
	mu.Lock()
	defer mu.Unlock()
	for _, tile := range []string{"apps/docs", "apps/docs/sub"} {
		if why, ok := stopped[tile]; !ok || why != "" {
			t.Errorf("%s: stopped %v, %s", tile, ok, why)
		}
	}
	for _, tile := range []string{"apps/docs", "apps/docs/sub"} {
		if why := f.b.PartitionHoldReason(tile); why != "" {
			t.Errorf("%s is still held after the switch: %s", tile, why)
		}
	}
}

// covers PD-49 D127m — the admin tile's frame under a person's login
// decides as that person: a tile manager's is heard, a writer's is 403 like
// the writer's own session.
func TestPartitionModeAdminFrame(t *testing.T) {
	f := newSwitchFxWith(t, map[string]string{"apps/admin/xbin.json": `{"runtime":"go"}`})
	if err := f.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/admin", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	f.pend(docsUserManifest)
	frame := func(uid string) auth.Principal {
		return auth.Principal{Component: "apps/admin", UserID: uid, Via: "frame", Gen: "s." + uid}
	}
	if code, out := f.act(frame("carol"), keepUser); code != 403 {
		t.Errorf("a writer through the admin frame: %d %v", code, out)
	}
	if code, out := f.act(frame("bob"), keepUser); code != 200 {
		t.Errorf("the owner through the admin frame: %d %v", code, out)
	}
	if rec := f.record("apps/docs"); rec.Declined == nil || rec.Declined.By != "bob" {
		t.Errorf("record %+v", rec)
	}
}

// covers PD-44 C12 — bound sandbox managers whose hello lacks "partitions"
// are named, and a switch to user partitions needs yes; one that keeps
// people apart needs nothing. An offloaded tile is refused.
func TestPartitionSwitchManagersAndOffload(t *testing.T) {
	f := newSwitchFx(t)
	f.pend(`{"runtime":"go","partition":["user"],"interfaces":{"sbx":{"kind":"http","service":"sandbox-manager"}},"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`)
	if err := f.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings["apps/docs"]["sbx"] = registry.Binding{{Ref: "apps/mgr"}}
	}); err != nil {
		t.Fatal(err)
	}
	var caps []string
	f.b.ProxyHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/mgr/sbx/hello" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"protocol": 1, "caps": caps})
	})
	root := auth.Principal{Owner: true}
	code, out := f.act(root, `{"tile":"apps/docs","act":"switch","from":null,"to":{"user":true},"dryRun":true}`)
	if m, _ := out["managers"].([]any); code != 200 || len(m) != 1 || m[0] != "apps/mgr" {
		t.Errorf("dry run: %d %v", code, out)
	}
	code, out = f.act(root, switchUser)
	if m, _ := out["managers"].([]any); code != 409 || len(m) != 1 {
		t.Errorf("without yes: %d %v", code, out)
	}
	if !f.bucketHas("res:apps/docs/db") {
		t.Fatal("a refused switch deleted data")
	}
	// offloaded: refused whatever the managers say
	if err := f.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Lifecycle = map[string]string{"apps/docs": registry.StateOffloaded}
	}); err != nil {
		t.Fatal(err)
	}
	if code, out := f.act(root, strings.Replace(switchUser, `"confirm"`, `"yes":true,"confirm"`, 1)); code != 409 || !strings.Contains(out["error"].(string), "offloaded") {
		t.Errorf("an offloaded tile: %d %v", code, out)
	}
	if err := f.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { ws.Lifecycle = nil }); err != nil {
		t.Fatal(err)
	}
	caps = []string{"exec", "partitions"}
	code, out = f.act(root, switchUser)
	if code != 200 || out["managers"] != nil {
		t.Errorf("a manager that keeps people apart: %d %v", code, out)
	}
}

// covers PD-44 01§6 — an invalid partition request is an /alerts row
// (partition-invalid) for admins and the tile's readers, and nobody else.
func TestPartitionInvalidAlert(t *testing.T) {
	f := newSwitchFx(t)
	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["bogus"]}`})
	f.rescan()
	if st, _, _ := f.state("apps/docs"); st != registry.PartitionInvalid {
		t.Fatalf("apps/docs: %v, want invalid", st)
	}
	kinds := func(p auth.Principal) (out []string) {
		for _, a := range f.b.partitionAlerts(p, p.IsAdmin()) {
			out = append(out, a.Kind+" "+a.Tile)
		}
		return out
	}
	for _, id := range []string{"dan", "root2"} {
		if k := kinds(principalFor(t, f.st, id)); !slices.Equal(k, []string{"partition-invalid apps/docs"}) {
			t.Errorf("%s: %v", id, k)
		}
	}
	if k := kinds(principalFor(t, f.st, "erin")); len(k) != 0 {
		t.Errorf("an outsider: %v", k)
	}
}

// covers PD-44 01§2.4 — a request pushes to the tile's managers at most
// once per partitionPushQuiet: a writer toggling the manifest can't burn
// their push budget.
func TestPartitionRequestPushQuiet(t *testing.T) {
	f := newSwitchFx(t)
	now := time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC)
	old := partitionNow
	partitionNow = func() time.Time { return now }
	t.Cleanup(func() { partitionNow = old })
	req := modeHistory{Op: modeOpRequest, To: &userSpec}
	bobs := func() int {
		n := 0
		for _, p := range f.pushed() {
			if p == "bob tile.partition-switch" {
				n++
			}
		}
		return n
	}
	f.b.partitionModeChanged("apps/docs", req)
	f.b.partitionModeChanged("apps/docs", modeHistory{Op: modeOpWithdrawn})
	f.b.partitionModeChanged("apps/docs", req)
	if n := bobs(); n != 1 {
		t.Errorf("toggled within the quiet time: %d pushes, want 1", n)
	}
	now = now.Add(partitionPushQuiet + time.Minute)
	f.b.partitionModeChanged("apps/docs", req)
	if n := bobs(); n != 2 {
		t.Errorf("after the quiet time: %d pushes, want 2", n)
	}
}

// covers PD-44 01§2.2 01§2.6 — every store "holds data" asks has a wipe
// hook of the same name, so what makes a tile hold data is what a switch
// deletes: a plane that registers a store (registerPartitionStore) must
// register its wipe (registerWipeHook) too, or a switch would record the
// new mode and leave that data behind. The lifecycle store is the
// exception: a switch refuses an offloaded tile.
func TestWipeHooksCoverHoldsData(t *testing.T) {
	exempt := map[string]string{"lifecycle": "an offloaded tile's switch is refused (restore it first)"}
	var hooks []string
	for _, h := range wipeHooks {
		hooks = append(hooks, h.name)
	}
	for _, s := range partitionStores {
		if _, ok := exempt[s.name]; !ok && !slices.Contains(hooks, s.name) {
			t.Errorf("the %q store answers \"holds data\", but no wipe hook %q deletes it on a switch (registerWipeHook)", s.name, s.name)
		}
	}
}
