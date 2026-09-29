package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	bolt "go.etcd.io/bbolt"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// switchFx is a workspace for the mode acts: apps/docs roots its scope
// (kv db, filesystem files) and holds data in every store a switch wipes;
// apps/other and a nested scope apps/docs/inner hold data a switch must
// keep. bob owns apps/docs, carol may write it, dan read it, erin is an
// outsider, root2 a workspace admin.
type switchFx struct {
	*partWS
	st     *users.Store
	pushMu sync.Mutex
	pushes []string // "user kind"
}

const docsManifest = `{"runtime":"go","uses":[{"target":"res:apps/docs/db","role":"writer"}]}`

func newSwitchFx(t *testing.T) *switchFx {
	t.Helper()
	w := newPartWS(t, map[string]string{
		"apps/docs/scope.json":       `{"resources":{"db":{"type":"kv"},"files":{"type":"filesystem"},"events":{"type":"bus"}}}`,
		"apps/docs/xbin.json":        docsManifest,
		"apps/docs/inner/scope.json": `{"resources":{"db":{"type":"kv"}}}`,
		"apps/docs/inner/xbin.json":  `{"runtime":"go"}`,
		"apps/other/scope.json":      `{"resources":{"db":{"type":"kv"}}}`,
		"apps/other/xbin.json":       `{"runtime":"go"}`,
	}, nil)
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	w.b.Users = st
	for id, tiles := range map[string]map[string]string{"bob": nil, "carol": {"apps/docs": "write"},
		"dan": {"apps/docs": "read"}, "erin": nil} {
		if _, err := st.Upsert(users.User{ID: id, Role: users.RoleUser, Tiles: tiles}, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Upsert(users.User{ID: "root2", Role: users.RoleAdmin}, "password"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/docs", "user:bob"); err != nil {
		t.Fatal(err)
	}
	f := &switchFx{partWS: w, st: st}
	w.b.SetPartitionPush(func(user, kind, title, body, link, collapse string) {
		f.pushMu.Lock()
		f.pushes = append(f.pushes, user+" "+kind)
		f.pushMu.Unlock()
	})
	old := partitionIsolated
	partitionIsolated = func() bool { return true }
	t.Cleanup(func() { partitionIsolated = old })
	w.b.RemoveDeploymentFile = func(tile, dep, file string) error {
		f, _ := deploymentFiles(tile, dep)
		err := os.Remove(filepath.Join(w.root, filepath.FromSlash(f.Records), file))
		if errNotExist(err) {
			return nil
		}
		return err
	}
	return f
}

func (f *switchFx) pushed() []string {
	f.pushMu.Lock()
	defer f.pushMu.Unlock()
	return slices.Clone(f.pushes)
}

// kvPutDep writes a key into deployment dep's namespace of scope.
func (f *switchFx) kvPutDep(scope, dep, name string) {
	f.t.Helper()
	db, err := f.b.scopeKV(scope, dep, true)
	if err != nil {
		f.t.Fatal(err)
	}
	if err := db.Update(func(tx *bolt.Tx) error {
		bk, err := tx.CreateBucketIfNotExists([]byte("res:" + scope + "/" + name))
		if err != nil {
			return err
		}
		return bk.Put([]byte("k"), []byte("v"))
	}); err != nil {
		f.t.Fatal(err)
	}
}

// bucketHas reports whether main's kv.db has a key in bucket.
func (f *switchFx) bucketHas(bucket string) bool {
	has := false
	_ = f.b.kv.db.View(func(tx *bolt.Tx) error {
		if bk := tx.Bucket([]byte(bucket)); bk != nil {
			k, _ := bk.Cursor().First()
			has = k != nil
		}
		return nil
	})
	return has
}

func (f *switchFx) exists(rel string) bool {
	_, err := os.Lstat(filepath.Join(f.root, filepath.FromSlash(rel)))
	return err == nil
}

// fill gives apps/docs data in every store of today, and the tiles around
// it data a switch keeps; it answers the backup keys it made (ns:, tile:).
func (f *switchFx) fill() (nsKey, tileKey string) {
	t, b := f.t, f.b
	t.Helper()
	f.kvPut("apps/docs", "db")
	f.kvPut("apps/docs/inner", "db") // a nested scope's: kept
	f.kvPut("apps/other", "db")      // another tile's: kept
	vol := f.emptyVolume("apps/docs", "files")
	f.write(map[string]string{
		mustRel(t, f.root, filepath.Join(vol, "Zm9v")):                          "ciphertext",
		"data/resources/apps~docs/files/note.txt":                               "plaintext",
		"data/resources/apps~other/db/x":                                        "kept",
		".xbin/term/" + util.CompKey("apps/docs") + "/x":                        "the tile's own terminal layer: kept",
		"homes/bob/notes":                                                       "a home: kept",
		"data/agent-history/bob/x.json":                                         "a person's agent history: kept",
		"data/deployments/" + util.TileKey("apps/docs") + "/dev/" + depCronFile: `{"schema":1,"rows":[]}`,
	})
	if err := b.vaultWrite("apps/docs", map[string]string{"token": "x", "other": "y"}); err != nil {
		t.Fatal(err)
	}
	if err := b.vaultWriteIn("apps/docs", "dev", map[string]string{"dev": "z"}); err != nil {
		t.Fatal(err)
	}
	if err := b.vaultWrite("apps/other", map[string]string{"keep": "me"}); err != nil {
		t.Fatal(err)
	}
	f.kvPutDep("apps/docs", "dev", "db")
	if err := b.updateNS(nsOf("apps/docs", "dev"), true, func(m *nsMeta) { m.State = nsSeeded }); err != nil {
		t.Fatal(err)
	}
	if err := b.cron.add(cronJob{Name: "tick", Schedule: "@every 1h", Component: "apps/docs", Path: "/tick"}); err != nil {
		t.Fatal(err)
	}
	if err := b.cron.add(cronJob{Name: "tick", Schedule: "@every 1h", Component: "apps/other", Path: "/tick"}); err != nil {
		t.Fatal(err)
	}
	if err := b.bus.put(busSub{Name: "s", Resource: "res:apps/docs/events", Component: "apps/docs", Path: "/bus", Role: "reader"}); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.IfaceInstances = map[string]map[string]string{"apps/docs": {"a": "b"}}
	}); err != nil {
		t.Fatal(err)
	}
	s := b.backupKeys()
	for i, k := range []struct{ subject, tile string }{{nsSubject("apps/docs", ""), "apps/docs"},
		{tileSubject("apps/docs"), "apps/docs"}, {nsSubject("apps/other", ""), "apps/other"}} {
		id, _ := newSubkeyID()
		if err := s.write(backupSubkey{Schema: backupKeySchema, ID: id, Subject: k.subject, Tile: k.tile, Gen: 1,
			Created: "2026-09-29T00:00:00Z", Wrapped: []byte("w")}); err != nil {
			t.Fatal(err)
		}
		switch i {
		case 0:
			nsKey = id
		case 1:
			tileKey = id
		}
	}
	return nsKey, tileKey
}

func (f *switchFx) act(p auth.Principal, body string) (int, map[string]any) {
	f.t.Helper()
	w := call(f.t, f.b.apiPartitionMode, p, "POST", "/partitions/mode", body, nil)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// holds is "holds data" for apps/docs as the mode store asks it.
func (f *switchFx) holds() (bool, string) {
	held, store, _ := f.b.tileHoldsData(registry.PartitionAsk{Tile: "apps/docs", Scope: "apps/docs", RootsScope: true})
	return held, store
}

const (
	keepUser   = `{"tile":"apps/docs","act":"keep","from":null,"to":{"user":true}}`
	switchUser = `{"tile":"apps/docs","act":"switch","from":null,"to":{"user":true},"confirm":"apps/docs"}`
)

// covers PD-44 PD-49 01§2.5 — who decides a mode switch: a tile manager
// acting as a person (the owner, a workspace admin, the root token); a
// writer, a reader, an outsider, the tile's own instance and a terminal of
// its owner are refused. A stale from/to answers 409 with the current
// request; keep deletes nothing, runs R at once and records the decline; a
// second keep is stale.
func TestPartitionModeKeep(t *testing.T) {
	f := newSwitchFx(t)
	f.fill()
	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan()
	if st, _, _ := f.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("apps/docs: %v, want pending", st)
	}
	// the request pushed to the managers: the root token's devices and bob
	waitFor(t, func() bool {
		p := f.pushed()
		return slices.Contains(p, "owner tile.partition-switch") && slices.Contains(p, "bob tile.partition-switch") &&
			slices.Contains(p, "root2 tile.partition-switch")
	}, "the managers' pushes")
	for _, u := range []string{"carol", "dan", "erin"} {
		if slices.Contains(f.pushed(), u+" tile.partition-switch") {
			t.Errorf("%s isn't a manager but was pushed", u)
		}
	}

	bob := principalFor(t, f.st, "bob")
	for name, p := range map[string]auth.Principal{
		"a writer":             principalFor(t, f.st, "carol"),
		"a reader":             principalFor(t, f.st, "dan"),
		"an outsider":          principalFor(t, f.st, "erin"),
		"the tile's instance":  {Component: "apps/docs", Via: "instance"},
		"its owner's terminal": {Component: "apps/docs", Via: "terminal", UserID: "bob", User: bob.User, Access: bob.Access},
		"its frame":            {Component: "apps/docs", Via: "frame", UserID: "bob", User: bob.User, Access: bob.Access, Gen: "s.x"},
	} {
		if code, out := f.act(p, keepUser); code != 403 {
			t.Errorf("%s decided: %d %v", name, code, out)
		}
	}
	// stale: the request is → user, not → user + global; from isn't R
	if code, out := f.act(bob, `{"tile":"apps/docs","act":"keep","from":null,"to":{"user":true,"global":true}}`); code != 409 ||
		out["partition"] == nil {
		t.Errorf("a stale to: %d %v", code, out)
	}
	if code, _ := f.act(bob, `{"tile":"apps/docs","act":"keep","from":{"user":true},"to":{"user":true}}`); code != 409 {
		t.Errorf("a stale from: %d", code)
	}
	if code, _ := f.act(bob, `{"tile":"apps/docs","act":"remove","from":null,"to":{"user":true}}`); code != 400 {
		t.Errorf("an unknown act: %d", code)
	}
	if code, out := f.act(bob, keepUser); code != 200 || out["ok"] != true {
		t.Fatalf("the owner keeps: %d %v", code, out)
	}
	st, r, req := f.state("apps/docs")
	if st != registry.PartitionUnpartitioned || !r.IsZero() || req == nil || !req.Declined {
		t.Errorf("after keep: %v %v %+v, want unpartitioned running, declined", st, r, req)
	}
	if why := f.b.PartitionHoldReason("apps/docs"); why != "" {
		t.Errorf("a kept tile is held: %q", why)
	}
	if held, store := f.holds(); !held || !f.bucketHas("res:apps/docs/db") {
		t.Errorf("keep deleted data: holds %v (%s)", held, store)
	}
	if rec := f.record("apps/docs"); rec.Declined == nil || rec.Declined.By != "bob" || f.ops("apps/docs") != "request,keep" {
		t.Errorf("record %+v, history %q", rec, f.ops("apps/docs"))
	}
	if code, _ := f.act(bob, keepUser); code != 409 {
		t.Errorf("keeping twice: %d", code)
	}
	// a workspace admin and the root token are managers too: a new request,
	// kept by the root token
	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user","global"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan()
	if code, out := f.act(auth.Principal{Owner: true}, `{"tile":"apps/docs","act":"keep","from":null,"to":{"user":true,"global":true}}`); code != 200 {
		t.Errorf("the root token keeps: %d %v", code, out)
	}
	if rec := f.record("apps/docs"); rec.Declined == nil || rec.Declined.By != "owner" {
		t.Errorf("record %+v", rec)
	}
}

// covers PD-44 PD-19 01§2.5 01§2.6 11§3 — a switch: refused without the
// typed path and without isolation; the dry run counts and deletes
// nothing; the switch deletes every item of 01 §2.6 today's stores hold
// (main's namespace — kv, volumes, plaintext —, a deployment's namespace,
// every vault file, cron, bus, interface instances), erases the ns: backup
// keys and keeps the tile: key, and keeps the keep list (code, the tile's
// own terminal layer, homes, a person's agent history, other tiles' and a
// nested scope's data, grants); then the mode is recorded with the wiped
// summary, and the tile holds no data. A declined request can still be
// switched.
func TestPartitionModeSwitch(t *testing.T) {
	f := newSwitchFx(t)
	nsKey, tileKey := f.fill()
	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan()
	root := auth.Principal{Owner: true}
	if code, _ := f.act(root, keepUser); code != 200 { // declined first: a switch still works
		t.Fatalf("keep: %d", code)
	}
	if code, out := f.act(root, strings.Replace(switchUser, `"confirm":"apps/docs"`, `"confirm":"apps/doc"`, 1)); code != 400 {
		t.Errorf("a switch without the typed path: %d %v", code, out)
	}
	partitionIsolated = func() bool { return false }
	if code, out := f.act(root, switchUser); code != 409 || !strings.Contains(out["error"].(string), "--isolate") {
		t.Errorf("a switch to user partitions without isolation: %d %v", code, out)
	}
	partitionIsolated = func() bool { return true }

	code, out := f.act(root, `{"tile":"apps/docs","act":"switch","from":null,"to":{"user":true},"dryRun":true}`)
	wiped, _ := out["wiped"].(map[string]any)
	if code != 200 || out["dryRun"] != true || wiped == nil {
		t.Fatalf("dry run: %d %v", code, out)
	}
	for k, min := range map[string]float64{"namespaces": 2, "vaultKeys": 3, "registrations": 3, "subkeys": 1, "bytes": 1} {
		if n, _ := wiped[k].(float64); n < min {
			t.Errorf("dry run %s = %v, want at least %v (%v)", k, wiped[k], min, wiped)
		}
	}
	if held, _ := f.holds(); !held || !f.bucketHas("res:apps/docs/db") || !f.exists("data/resources/apps~docs/files/note.txt") {
		t.Fatal("the dry run deleted data")
	}

	code, out = f.act(root, switchUser)
	if code != 200 || out["ok"] != true {
		t.Fatalf("switch: %d %v", code, out)
	}
	// deleted
	checks := map[string]bool{
		"main's kv":             !f.bucketHas("res:apps/docs/db"),
		"main's volume":         !f.exists("data/resources-enc/apps~docs"),
		"main's plaintext":      !f.exists("data/resources/apps~docs"),
		"main's vault":          !f.exists("data/vault/" + util.CompKey("apps/docs") + ".json"),
		"dev's vault":           !f.exists("data/vault/.deployments/" + util.TileKey("apps/docs") + "/dev.json"),
		"dev's kv":              !f.exists("data/resources-enc/.deployments/" + escS("apps/docs") + "/dev/kv.db"),
		"dev's cron file":       !f.exists("data/deployments/" + util.TileKey("apps/docs") + "/dev/" + depCronFile),
		"the cron job":          len(f.b.cronJobsFor("apps/docs")) == 0,
		"the bus subscription":  len(f.b.bus.forComponent("apps/docs")) == 0,
		"interface instances":   len(f.b.Reg.Workspace().IfaceInstances["apps/docs"]) == 0,
		"the ns: backup key":    !f.exists("data/vault/.backup-keys/" + nsKey + ".json"),
		"kept: the tile: key":   f.exists("data/vault/.backup-keys/" + tileKey + ".json"),
		"kept: the code":        f.exists("apps/docs/xbin.json") && f.exists("apps/docs/scope.json"),
		"kept: terminal layer":  f.exists(".xbin/term/" + util.CompKey("apps/docs") + "/x"),
		"kept: a home":          f.exists("homes/bob/notes"),
		"kept: agent history":   f.exists("data/agent-history/bob/x.json"),
		"kept: nested scope kv": f.bucketHas("res:apps/docs/inner/db"),
		"kept: other tile's kv": f.bucketHas("res:apps/other/db"),
		"kept: other's files":   f.exists("data/resources/apps~other/db/x"),
		"kept: other's vault":   f.exists("data/vault/" + util.CompKey("apps/other") + ".json"),
		"kept: other's cron":    len(f.b.cronJobsFor("apps/other")) == 1,
	}
	for what, ok := range checks {
		if !ok {
			t.Errorf("after the switch: %s", what)
		}
	}
	if m, ok, _ := f.b.readNS(nsOf("apps/docs", "dev")); !ok || m.State != nsEmpty || !m.Reset {
		t.Errorf("dev's ns.json after the switch: %+v %v", m, ok)
	}
	if held, store := f.holds(); held {
		t.Errorf("apps/docs still holds data in %s", store)
	}
	st, r, req := f.state("apps/docs")
	if st != registry.PartitionPartitioned || r != userSpec || req != nil {
		t.Errorf("after the switch: %v %v %+v", st, r, req)
	}
	rec := f.record("apps/docs")
	h := rec.History[len(rec.History)-1]
	if h.Op != modeOpSwitch || h.By != "owner" || h.Wiped["namespaces"] < 2 || h.Wiped["subkeys"] != 1 || f.ops("apps/docs") != "request,keep,switch" {
		t.Errorf("history %q, last %+v", f.ops("apps/docs"), h)
	}
	if tombs, _ := f.b.backupKeys().tombstones(); len(tombs) != 1 || !strings.HasPrefix(tombs[0].Reason, "partition mode switch") {
		t.Errorf("tombstones %+v", tombs)
	}
	if code, _ := f.act(root, switchUser); code != 409 {
		t.Errorf("switching again: %d", code)
	}
}

// covers PD-44 H1 — adding "global" to a partitioned tile deletes nothing;
// removing it deletes global's data (main's namespace, vault and
// registrations at today's keys) and keeps the deployments' data and every
// wipe hook's partitions (a stand-in hook records it was asked for global
// only). A switch tells each person whose partition went.
func TestPartitionModeGlobalSwitches(t *testing.T) {
	f := newSwitchFx(t)
	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan() // no data yet: auto
	if st, r, _ := f.state("apps/docs"); st != registry.PartitionPartitioned || r != userSpec {
		t.Fatalf("auto: %v %v", st, r)
	}
	f.fill()
	var kinds []wipeKind
	old := wipeHooks
	t.Cleanup(func() { wipeHooks = old })
	registerWipeHook(wipeHook{name: "people", wipe: func(b *Broker, t wipeTarget, sum *wipeSummary) error {
		kinds = append(kinds, t.Kind)
		if t.Kind == wipeEverything && !t.DryRun {
			sum.Partitions++
			sum.addPerson("dan")
		}
		return nil
	}})
	root := auth.Principal{Owner: true}

	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user","global"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan()
	if code, out := f.act(root, `{"tile":"apps/docs","act":"switch","from":{"user":true},"to":{"user":true,"global":true},"confirm":"apps/docs"}`); code != 200 {
		t.Fatalf("adding global: %d %v", code, out)
	}
	if !f.bucketHas("res:apps/docs/db") || !f.exists("data/vault/"+util.CompKey("apps/docs")+".json") || len(f.b.cronJobsFor("apps/docs")) != 1 {
		t.Error("adding global deleted data")
	}

	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan()
	if st, _, _ := f.state("apps/docs"); st != registry.PartitionPending {
		t.Fatalf("removing global on a tile with data: %v, want pending", st)
	}
	if code, out := f.act(root, `{"tile":"apps/docs","act":"switch","from":{"user":true,"global":true},"to":{"user":true},"confirm":"apps/docs"}`); code != 200 {
		t.Fatalf("removing global: %d %v", code, out)
	}
	if f.bucketHas("res:apps/docs/db") || f.exists("data/vault/"+util.CompKey("apps/docs")+".json") || len(f.b.cronJobsFor("apps/docs")) != 0 {
		t.Error("removing global kept global's data")
	}
	if !f.exists("data/resources-enc/.deployments/"+escS("apps/docs")+"/dev/kv.db") || !f.exists("data/vault/.deployments/"+util.TileKey("apps/docs")+"/dev.json") {
		t.Error("removing global deleted a deployment's data")
	}
	if !slices.Equal(kinds, []wipeKind{wipeNone, wipeGlobal}) {
		t.Errorf("the hooks were asked %v", kinds)
	}
	if slices.Contains(f.pushed(), "dan tile.partition-deleted") {
		t.Error("a person was told their partition went, but it stayed")
	}

	// back to unpartitioned: everything goes, and the person is told
	f.write(map[string]string{"apps/docs/xbin.json": docsManifest})
	f.rescan()
	if code, out := f.act(root, `{"tile":"apps/docs","act":"switch","from":{"user":true},"to":null,"confirm":"apps/docs"}`); code != 200 || out["people"] != 1.0 {
		t.Fatalf("user → unpartitioned: %d %v", code, out)
	}
	if !slices.Contains(f.pushed(), "dan tile.partition-deleted") {
		t.Errorf("dan wasn't told: %v", f.pushed())
	}
}

// covers PD-44 01§2.4 — the /alerts banner of a pending switch reaches
// admins and the tile's readers, and nobody else; it goes when the request
// is decided.
func TestPartitionSwitchAlert(t *testing.T) {
	f := newSwitchFx(t)
	f.fill()
	f.write(map[string]string{"apps/docs/xbin.json": `{"runtime":"go","partition":["user"],"uses":[{"target":"res:apps/docs/db","role":"writer"}]}`})
	f.rescan()
	alerts := func(p auth.Principal) []Alert {
		w := call(t, f.b.apiAlerts, p, "GET", "/alerts", "", nil)
		var out struct{ Alerts []Alert }
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		var mine []Alert
		for _, a := range out.Alerts {
			if a.Kind == "partition-switch" {
				mine = append(mine, a)
			}
		}
		return mine
	}
	for name, p := range map[string]auth.Principal{"a reader": principalFor(t, f.st, "dan"), "an admin": principalFor(t, f.st, "root2")} {
		a := alerts(p)
		if len(a) != 1 || a[0].Tile != "apps/docs" || !strings.Contains(a[0].Message, "(unpartitioned → user)") || a[0].System {
			t.Errorf("%s: %+v", name, a)
		}
	}
	if a := alerts(principalFor(t, f.st, "erin")); len(a) != 0 {
		t.Errorf("an outsider sees %+v", a)
	}
	if code, _ := f.act(auth.Principal{Owner: true}, keepUser); code != 200 {
		t.Fatal(code)
	}
	if a := alerts(principalFor(t, f.st, "dan")); len(a) != 0 {
		t.Errorf("a decided request still alerts: %+v", a)
	}
}
