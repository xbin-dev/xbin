package broker

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
)

// pbindWS is the bind-types fixture (plans/partitions/05 §3): apps/agent,
// partitioned with global, requests a multi http slot mcp, a single one
// llm and a multi docs; apps/plain is an unpartitioned requester of the
// same; apps/pu (user only) and apps/pg (user + global) are partitioned
// docs providers; users/alice/mcp and users/bob/mcp are personal mcp
// providers, users/alice/part a partitioned one, users/alice/pagent a
// partitioned personal requester; apps/oagent is a partitioned requester of
// two multi mcp slots (mcp, tools) and apps/omcp, apps/dmcp two more mcp
// providers, for the org cases (the test gives their owners). alice reads
// apps/* and users/*, bob is an admin (and owns users/bob/mcp), carol reads
// apps/* only, erin reads users/erin/* only (and owns users/erin/mcp).
// restarts records each partition restart.
func pbindWS(t *testing.T) (*partWS, *users.Store, *[]string) {
	t.Helper()
	mcp := `{"kind":"http","service":"mcp"}`
	files := map[string]string{
		"xbin.json":                    `{"schema":1}`,
		"apps/agent/xbin.json":         `{"runtime":"go","partition":["user","global"],"interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true},"llm":` + mcp + `,"docs":{"kind":"http","service":"docs","multi":true}}}`,
		"apps/plain/xbin.json":         `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true},"docs":{"kind":"http","service":"docs","multi":true}}}`,
		"apps/pu/xbin.json":            `{"runtime":"go","partition":["user"],"provides":{"docs":{"kind":"http","service":"docs"}}}`,
		"apps/pg/xbin.json":            `{"runtime":"go","partition":["user","global"],"provides":{"docs":{"kind":"http","service":"docs"}}}`,
		"users/alice/mcp/xbin.json":    `{"runtime":"go","provides":{"mcp":` + mcp + `}}`,
		"users/alice/part/xbin.json":   `{"runtime":"go","partition":["user"],"provides":{"mcp":` + mcp + `}}`,
		"users/alice/pagent/xbin.json": `{"runtime":"go","partition":["user"],"interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true}}}`,
		"users/bob/mcp/xbin.json":      `{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"writer"}}}`,
		"users/erin/mcp/xbin.json":     `{"runtime":"go","provides":{"mcp":` + mcp + `}}`,
		"apps/oagent/xbin.json":        `{"runtime":"go","partition":["user"],"interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true},"tools":{"kind":"http","service":"mcp","multi":true}}}`,
		"apps/omcp/xbin.json":          `{"runtime":"go","provides":{"mcp":` + mcp + `}}`,
		"apps/dmcp/xbin.json":          `{"runtime":"go","provides":{"mcp":` + mcp + `}}`,
	}
	for rel := range files {
		if dir := filepath.Dir(rel); dir != "." {
			files[dir+"/backend/main.go"] = "package main\n"
		}
	}
	w := newPartWS(t, files, nil)
	w.rescan()
	st, err := users.Open(filepath.Join(w.root, "data"))
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{
		{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead, "users/*": users.LevelRead}},
		{ID: "bob", Role: users.RoleAdmin},
		{ID: "carol", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead}},
		{ID: "erin", Role: users.RoleUser, Tiles: map[string]string{"users/erin/*": users.LevelRead}},
	} {
		if _, err := st.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	for tile, owner := range map[string]string{"users/alice/mcp": "user:alice", "users/alice/part": "user:alice",
		"users/alice/pagent": "user:alice", "users/bob/mcp": "user:bob", "users/erin/mcp": "user:erin"} {
		if err := st.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	w.b.Users = st
	w.b.PrimaryOf = func(string) string { return util.MainDeployment }
	w.b.DeploymentExists = func(_, name string) bool { return name == util.MainDeployment }
	w.b.AddressedDeployment = func(auth.Principal, string) (string, error) { return util.MainDeployment, nil }
	restarts := &[]string{}
	w.b.SetPartitionRestart(func(tile, dep, part string) { *restarts = append(*restarts, tile+"@"+dep+"/"+part) })
	for _, tile := range []string{"apps/agent", "apps/pu", "apps/pg", "users/alice/part", "users/alice/pagent", "apps/oagent"} {
		if st, _, _ := w.state(tile); st != registry.PartitionPartitioned {
			t.Fatalf("%s: %v, want partitioned", tile, st)
		}
	}
	return w, st, restarts
}

func pbindCall(t *testing.T, w *partWS, p auth.Principal, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	h := w.b.apiPersonalBindAdd
	switch method {
	case "GET":
		h = w.b.apiPersonalBindsList
	case "DELETE":
		h = w.b.apiPersonalBindDelete
	}
	return call(t, h, p, method, "/partitions/binds", body, nil)
}

func pbindBody(requester, slot, provider string) string {
	b, _ := json.Marshal(map[string]string{"requester": requester, "slot": slot, "provider": provider})
	return string(b)
}

// covers PD-16 PD-54 — who may create a personal bind (05 §3): the owner
// of a user-owned, unpartitioned provider, for their own partition of a
// partitioned requester they can read, on a multi http slot whose service
// the provider provides; never an admin (for someone else, or with a tile
// of their own: an admin's bind is always global), anyone else, tile code
// or the root token; an unknown path is told only to who may know it; and
// only that person's partition restarts.
func TestPersonalBindCreate(t *testing.T) {
	w, st, restarts := pbindWS(t)
	alice, bob, carol, erin := principalFor(t, st, "alice"), principalFor(t, st, "bob"), principalFor(t, st, "carol"), principalFor(t, st, "erin")
	if err := st.SetOwner("users/alice/gone", "user:alice"); err != nil { // an owner entry a removed tile left
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		p    auth.Principal
		body string
		code int
		msg  string
	}{
		{"an admin, for alice's tile", bob, pbindBody("apps/agent", "mcp", "users/alice/mcp"), 403, "always a global bind"},
		{"an admin, their own tile", bob, pbindBody("apps/agent", "mcp", "users/bob/mcp"), 403, "always a global bind"},
		{"carol, alice's tile", carol, pbindBody("apps/agent", "mcp", "users/alice/mcp"), 403, "isn't yours"},
		{"alice, bob's tile", alice, pbindBody("apps/agent", "mcp", "users/bob/mcp"), 403, "isn't yours"},
		{"erin, who can't read the requester", erin, pbindBody("apps/agent", "mcp", "users/erin/mcp"), 403, "can't read apps/agent"},
		{"a single slot", alice, pbindBody("apps/agent", "llm", "users/alice/mcp"), 409, "no multi http slot"},
		{"an unpartitioned requester", alice, pbindBody("apps/plain", "mcp", "users/alice/mcp"), 409, "doesn't keep each person's data apart"},
		{"a partitioned provider", alice, pbindBody("apps/agent", "mcp", "users/alice/part"), 409, "keeps each person's data apart"},
		{"a service the provider lacks", alice, pbindBody("apps/agent", "docs", "users/alice/mcp"), 409, "doesn't provide"},
		// existence is told only past the authority checks: no oracle
		{"an unknown provider", alice, pbindBody("apps/agent", "mcp", "users/alice/none"), 403, "isn't yours"},
		{"a gone provider alice still owns", alice, pbindBody("apps/agent", "mcp", "users/alice/gone"), 404, "no such component: users/alice/gone"},
		{"an unknown requester erin can't read", erin, pbindBody("apps/none", "mcp", "users/erin/mcp"), 403, "can't read apps/none"},
		{"an unknown requester alice may read", alice, pbindBody("apps/none", "mcp", "users/alice/mcp"), 404, "no such component: apps/none"},
		{"alice's frame of the requester", frameOf("apps/agent", "alice"), pbindBody("apps/agent", "mcp", "users/alice/mcp"), 403, "person's own act"},
		{"the root token", auth.Principal{Owner: true}, pbindBody("apps/agent", "mcp", "users/alice/mcp"), 403, "person's own act"},
		{"a body naming whose", alice, `{"requester":"apps/agent","slot":"mcp","provider":"users/alice/mcp","user":"bob"}`, 400, "need"},
	} {
		rec := pbindCall(t, w, c.p, "POST", c.body)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s: %d %s, want %d with %q", c.name, rec.Code, rec.Body.String(), c.code, c.msg)
		}
	}
	if len(*restarts) != 0 {
		t.Fatalf("a refused bind restarted %v", *restarts)
	}
	if _, err := os.Stat(w.b.personalBindsRoot()); !os.IsNotExist(err) {
		t.Fatalf("a refused bind wrote the store: %v", err)
	}
	rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp"))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"live":true`) {
		t.Fatalf("alice's own bind: %d %s", rec.Code, rec.Body.String())
	}
	if want := []string{"apps/agent@main/user:alice"}; !slices.Equal(*restarts, want) {
		t.Errorf("restarts %v, want only alice's partition %v", *restarts, want)
	}
	u, _ := st.Get("alice")
	if u.UID == "" {
		t.Fatal("alice has no uid after her first personal bind")
	}
	raw, err := os.ReadFile(filepath.Join(w.root, "data", "partitions", "binds", u.UID+".json"))
	if err != nil {
		t.Fatal(err)
	}
	var f personalBindsFile
	if err := json.Unmarshal(raw, &f); err != nil || f.Schema != 1 || f.User != "alice" || f.UID != u.UID || len(f.Binds) != 1 ||
		f.Binds[0].Requester != "apps/agent" || f.Binds[0].Slot != "mcp" || f.Binds[0].Provider != "users/alice/mcp" || f.Binds[0].ID == "" {
		t.Fatalf("the record: %v %s", err, raw)
	}
	// the same bind again answers the existing one
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 || !strings.Contains(rec.Body.String(), f.Binds[0].ID) {
		t.Errorf("again: %d %s", rec.Code, rec.Body.String())
	}
	if f2 := w.b.livePersonalBinds("alice"); f2 == nil || len(f2.Binds) != 1 {
		t.Errorf("again: the record holds %+v", f2)
	}
	// a provider bound for everyone on the slot can't be bound personally too
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/agent": {"mcp": registry.BindTo("users/erin/mcp")}}
	}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("users/erin/mcp", "user:alice"); err != nil {
		t.Fatal(err)
	}
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/erin/mcp")); rec.Code != 409 || !strings.Contains(rec.Body.String(), "for everyone") {
		t.Errorf("a globally bound provider: %d %s", rec.Code, rec.Body.String())
	}
}

// covers PD-54 — personal binds in the env and the document meta, per
// partition (05 §3): alice's partition lists her entry after the global
// rows, marked personal; bob's partition, the global instance and the
// unpartitioned view don't.
func TestPersonalBindEnv(t *testing.T) {
	w, st, _ := pbindWS(t)
	alice := principalFor(t, st, "alice")
	if err := w.b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/agent": {"mcp": registry.BindTo("users/bob/mcp")}}
	}); err != nil {
		t.Fatal(err)
	}
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
		t.Fatalf("alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	c, _ := w.b.Reg.Component("apps/agent")
	iface := func(env []string) []map[string]any {
		t.Helper()
		for _, e := range env {
			if v, ok := strings.CutPrefix(e, "XBIN_IFACE_MCP="); ok {
				var list []map[string]any
				if err := json.Unmarshal([]byte(v), &list); err != nil {
					t.Fatalf("XBIN_IFACE_MCP=%s: %v", v, err)
				}
				return list
			}
		}
		t.Fatalf("no XBIN_IFACE_MCP in %v", env)
		return nil
	}
	env, _ := w.b.PartitionEnv(c, util.MainDeployment, "user:alice")
	got := iface(env)
	if len(got) != 2 || got[0]["provider"] != "users/bob/mcp" || got[0]["personal"] != nil ||
		got[1]["provider"] != "users/alice/mcp" || got[1]["personal"] != true || got[1]["url"] != "http://xbin/api/users/alice/mcp" || got[1]["service"] != "mcp" {
		t.Errorf("alice's partition: %v", got)
	}
	for _, part := range []string{"user:bob", "global", ""} {
		env, _ := w.b.PartitionEnv(c, util.MainDeployment, part)
		if got := iface(env); len(got) != 1 || got[0]["provider"] != "users/bob/mcp" {
			t.Errorf("partition %q: %v", part, got)
		}
	}
	if got := partitionIfaceEnvSeam(w.b, c, "dev", "user:alice", w.b.EnvFor(c)); len(iface(got)) != 1 {
		t.Errorf("a non-primary deployment's instance: %v", iface(got))
	}
	endpoints := func(m map[string]any) []IfaceEndpoint {
		return m["mcp"].(map[string]any)["endpoints"].([]IfaceEndpoint)
	}
	if eps := endpoints(w.b.HTTPInterfacesIn("apps/agent", "user:alice")); len(eps) != 2 || !eps[1].Personal || eps[1].URL != "/api/users/alice/mcp" || eps[0].Personal {
		t.Errorf("alice's frames: %+v", eps)
	}
	for _, part := range []util.Partition{"user:bob", util.PartitionGlobal, ""} {
		if eps := endpoints(w.b.HTTPInterfacesIn("apps/agent", part)); len(eps) != 1 {
			t.Errorf("%q's frames: %+v", part, eps)
		}
	}
	if eps := endpoints(w.b.HTTPInterfaces("apps/agent")); len(eps) != 1 {
		t.Errorf("HTTPInterfaces: %+v", eps)
	}
	j, _ := json.Marshal(w.b.HTTPInterfaces("apps/agent"))
	if strings.Contains(string(j), "personal") {
		t.Errorf("the global view names personal: %s", j)
	}
}

// covers PD-16 PD-54 S13 — the call filter (05 §3): alice's partition —
// instance and frame — reaches her tile through her bind; bob's partition,
// the global instance, an unpartitioned tile and the root's frame get
// today's refusal; after a transfer, a delete and recreate, or a removal,
// nothing passes.
func TestPersonalBindCalls(t *testing.T) {
	w, st, restarts := pbindWS(t)
	b := w.b
	alice := principalFor(t, st, "alice")
	target, _ := b.Reg.Component("users/alice/mcp")
	route := func(p auth.Principal) Decision { return b.Route(p, target, "") }
	allowed := func(name string, p auth.Principal) {
		t.Helper()
		d := route(p)
		if d.Deny != nil || d.Role != "reader" || d.CallerPartition != "user:alice" || d.CallerPartitionID == "" || d.Partition != "" {
			t.Errorf("%s: %+v", name, d)
		}
	}
	refused := func(name string, p auth.Principal) {
		t.Helper()
		var ng *NotGrantedError
		if d := route(p); d.Deny == nil || !strings.Contains(d.Deny.Error(), "not granted access to users/alice/mcp") && !asNotGranted(d.Deny, &ng) {
			t.Errorf("%s: %+v, want today's refusal", name, d)
		}
	}
	refused("alice's partition, no bind yet", instanceOf("apps/agent", "user:alice"))
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
		t.Fatalf("alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	allowed("alice's partition's instance", instanceOf("apps/agent", "user:alice"))
	allowed("alice's frame", frameOf("apps/agent", "alice"))
	refused("bob's partition", instanceOf("apps/agent", "user:bob"))
	refused("bob's frame", frameOf("apps/agent", "bob"))
	refused("the global instance", instanceOf("apps/agent", ""))
	refused("an unpartitioned tile", instanceOf("apps/plain", ""))
	if d := route(frameOf("apps/agent", "")); d.Deny == nil {
		t.Errorf("the root's frame: %+v", d)
	}
	// a transfer: Owner is read live, and the transfer drops the row and
	// restarts alice's partition
	if err := st.SetOwner("users/alice/mcp", "user:bob"); err != nil {
		t.Fatal(err)
	}
	refused("after the transfer, before the drop", instanceOf("apps/agent", "user:alice"))
	*restarts = nil
	b.executeTransferEffects("users/alice/mcp", transferReport{})
	if f := b.livePersonalBinds("alice"); f != nil {
		t.Errorf("the transfer kept %+v", f)
	}
	if want := []string{"apps/agent@main/user:alice"}; !slices.Equal(*restarts, want) {
		t.Errorf("the transfer restarted %v, want %v", *restarts, want)
	}
	if err := st.SetOwner("users/alice/mcp", "user:alice"); err != nil {
		t.Fatal(err)
	}
	refused("the tile back, the bind gone", instanceOf("apps/agent", "user:alice"))

	// delete and recreate: nothing is inherited
	again := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp"))
	if again.Code != 200 {
		t.Fatalf("alice's bind again: %d %s", again.Code, again.Body.String())
	}
	var oldBind struct{ Bind personalBindRow }
	_ = json.Unmarshal(again.Body.Bytes(), &oldBind)
	allowed("alice's partition, bound again", instanceOf("apps/agent", "user:alice"))
	u, _ := st.Get("alice")
	oldUID := u.UID
	if _, err := st.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/*": users.LevelRead, "users/*": users.LevelRead}}, "password1"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("users/alice/mcp", "user:alice"); err != nil {
		t.Fatal(err)
	}
	refused("a recreated alice", instanceOf("apps/agent", "user:alice"))
	if uid, err := b.mintPartitionUID("alice"); err != nil || uid == oldUID {
		t.Fatalf("the recreated alice's uid %q %v (was %q)", uid, err, oldUID)
	}
	refused("a recreated alice with her own uid", instanceOf("apps/agent", "user:alice"))
	if rec := pbindCall(t, w, principalFor(t, st, "alice"), "GET", ""); !strings.Contains(rec.Body.String(), `"binds":[]`) {
		t.Errorf("the recreated alice lists %s", rec.Body.String())
	}
	old := filepath.Join(b.personalBindsRoot(), oldUID+".json")
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("the old record: %v", err)
	}
	// the dead record (the users store's delete hook isn't wired yet, F7b)
	// holds no data, names nobody in a switch, and no delete reaches it —
	// neither the recreated alice's by triple nor an admin's by its id
	ask := registry.PartitionAsk{Tile: "apps/agent", Scope: "apps/agent", RootsScope: true}
	if held, err := holdsPersonalBinds(b, ask); held || err != nil {
		t.Errorf("a dead record holds apps/agent's data: %v %v", held, err)
	}
	var dry wipeSummary
	if err := wipePersonalBinds(b, wipeTarget{Tile: "apps/agent", Kind: wipeEverything, DryRun: true}, &dry); err != nil || dry.Registrations != 0 || len(dry.People) != 0 {
		t.Errorf("a switch's dry run counts the dead record: %v %+v", err, dry)
	}
	*restarts = nil
	if rec := pbindCall(t, w, principalFor(t, st, "alice"), "DELETE", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 404 || strings.Contains(rec.Body.String(), oldBind.Bind.ID) {
		t.Errorf("the recreated alice deletes her predecessor's bind: %d %s", rec.Code, rec.Body.String())
	}
	if rec := pbindCall(t, w, principalFor(t, st, "bob"), "DELETE", `{"id":"`+oldBind.Bind.ID+`"}`); rec.Code != 404 {
		t.Errorf("an admin deletes a dead record's bind: %d %s", rec.Code, rec.Body.String())
	}
	if len(*restarts) != 0 {
		t.Errorf("a dead record's delete restarted %v", *restarts)
	}
	if _, err := os.Stat(old); err != nil {
		t.Fatalf("the old record after the refused deletes: %v", err)
	}
	b.PersonalBindsUserDeleted("alice", oldUID)
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the deleted person's record stays: %v", err)
	}
}

func asNotGranted(err error, ng **NotGrantedError) bool {
	if e, ok := err.(*NotGrantedError); ok {
		*ng = e
		return true
	}
	return false
}

// covers PD-54 — listing and removing (05 §3): a person sees and removes
// their own; an admin sees and removes everyone's; nobody else, and no tile
// code; a removal restarts that person's partition only; a mode switch
// between user partitions and unpartitioned removes the tile's personal
// binds, adding or removing global keeps them, and they are data the
// switch asks about.
func TestPersonalBindLifecycle(t *testing.T) {
	w, st, restarts := pbindWS(t)
	b := w.b
	alice, bob, carol := principalFor(t, st, "alice"), principalFor(t, st, "bob"), principalFor(t, st, "carol")
	ask := registry.PartitionAsk{Tile: "apps/agent", Scope: "apps/agent", RootsScope: true}
	if held, err := holdsPersonalBinds(b, ask); held || err != nil {
		t.Fatalf("no bind: holds %v %v", held, err)
	}
	rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp"))
	if rec.Code != 200 {
		t.Fatalf("alice's bind: %d %s", rec.Code, rec.Body.String())
	}
	var added struct{ Bind personalBindRow }
	_ = json.Unmarshal(rec.Body.Bytes(), &added)
	for _, c := range []struct {
		name string
		p    auth.Principal
		code int
		rows int
	}{
		{"alice", alice, 200, 1}, {"bob, an admin", bob, 200, 1}, {"carol", carol, 200, 0},
		{"alice's frame", frameOf("apps/agent", "alice"), 403, 0},
	} {
		rec := pbindCall(t, w, c.p, "GET", "")
		var out struct{ Binds []personalBindRow }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		if rec.Code != c.code || len(out.Binds) != c.rows {
			t.Errorf("%s lists: %d %s", c.name, rec.Code, rec.Body.String())
		}
	}
	if held, err := holdsPersonalBinds(b, ask); !held || err != nil {
		t.Errorf("a bind: holds %v %v", held, err)
	}
	if held, _ := holdsPersonalBinds(b, registry.PartitionAsk{Tile: "apps/plain"}); held {
		t.Error("another tile holds alice's bind")
	}
	// removal: carol can't, alice and an admin can
	*restarts = nil
	if rec := pbindCall(t, w, carol, "DELETE", `{"id":"`+added.Bind.ID+`","user":"alice"}`); rec.Code != 403 {
		t.Errorf("carol removes alice's: %d %s", rec.Code, rec.Body.String())
	}
	if rec := pbindCall(t, w, carol, "DELETE", `{"id":"`+added.Bind.ID+`"}`); rec.Code != 404 {
		t.Errorf("carol removes by alice's id: %d %s", rec.Code, rec.Body.String())
	}
	if rec := pbindCall(t, w, bob, "DELETE", `{"id":"`+added.Bind.ID+`"}`); rec.Code != 200 {
		t.Errorf("an admin removes alice's: %d %s", rec.Code, rec.Body.String())
	}
	if want := []string{"apps/agent@main/user:alice"}; !slices.Equal(*restarts, want) {
		t.Errorf("the removal restarted %v, want %v", *restarts, want)
	}
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
		t.Fatalf("alice's bind again: %d", rec.Code)
	}
	if rec := pbindCall(t, w, alice, "DELETE", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
		t.Errorf("alice removes her own: %d %s", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(b.personalBindsRoot())); err != nil {
		t.Fatal(err)
	}
	if ents, _ := os.ReadDir(b.personalBindsRoot()); len(ents) != 0 {
		t.Errorf("an empty record stays: %v", ents)
	}
	// the switch's wipe (01 §2.6, H1)
	if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
		t.Fatalf("alice's bind again: %d", rec.Code)
	}
	for _, k := range []wipeKind{wipeGlobal, wipeNone} {
		var sum wipeSummary
		if err := wipePersonalBinds(b, wipeTarget{Tile: "apps/agent", Kind: k}, &sum); err != nil || sum.Registrations != 0 || b.livePersonalBinds("alice") == nil {
			t.Errorf("a %s switch: %v %+v", k, err, sum)
		}
	}
	var dry wipeSummary
	if err := wipePersonalBinds(b, wipeTarget{Tile: "apps/agent", Kind: wipeEverything, DryRun: true}, &dry); err != nil || dry.Registrations != 1 ||
		!slices.Equal(dry.People, []string{"alice"}) || b.livePersonalBinds("alice") == nil {
		t.Errorf("the dry run: %v %+v", err, dry)
	}
	var sum wipeSummary
	if err := wipePersonalBinds(b, wipeTarget{Tile: "apps/agent", Kind: wipeEverything}, &sum); err != nil || sum.Registrations != 1 || b.livePersonalBinds("alice") != nil {
		t.Errorf("the switch: %v %+v", err, sum)
	}
	if held, err := holdsPersonalBinds(b, ask); held || err != nil {
		t.Errorf("after the switch: holds %v %v", held, err)
	}
}

// covers PD-43 PD-54 D82 — a path taken again (05 §3): a tile created at a
// personal bind's provider's or requester's path (assignOwner, whoever
// creates it — an admin skips pathLeftovers) inherits none of it; the
// person's partition of a requester elsewhere restarts. And a switch's wipe
// still removes a dead record's rows (a deleted person's), uncounted.
func TestPersonalBindPathReuse(t *testing.T) {
	w, st, restarts := pbindWS(t)
	b := w.b
	alice := principalFor(t, st, "alice")
	bind := func() {
		t.Helper()
		if rec := pbindCall(t, w, alice, "POST", pbindBody("apps/agent", "mcp", "users/alice/mcp")); rec.Code != 200 {
			t.Fatalf("alice's bind: %d %s", rec.Code, rec.Body.String())
		}
	}
	bind()
	*restarts = nil
	b.assignOwner("users/alice/mcp", "user:alice") // her tile, removed and created again
	if f := b.livePersonalBinds("alice"); f != nil {
		t.Errorf("a new tile at the provider's path inherits %+v", f)
	}
	if want := []string{"apps/agent@main/user:alice"}; !slices.Equal(*restarts, want) {
		t.Errorf("restarts %v, want %v", *restarts, want)
	}
	bind()
	*restarts = nil
	b.assignOwner("apps/agent", "")
	if f := b.livePersonalBinds("alice"); f != nil {
		t.Errorf("a new tile at the requester's path inherits %+v", f)
	}
	if len(*restarts) != 0 {
		t.Errorf("the new requester restarted %v", *restarts)
	}
	bind()
	b.assignOwner("apps/plain", "") // another path: nothing of alice's goes
	if f := b.livePersonalBinds("alice"); f == nil || len(f.Binds) != 1 {
		t.Errorf("a tile created elsewhere dropped %+v", f)
	}

	// a dead record: the wipe removes it, counting and naming nobody
	if _, err := st.Delete("alice"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Upsert(users.User{ID: "alice", Role: users.RoleUser}, "password1"); err != nil {
		t.Fatal(err)
	}
	var sum wipeSummary
	if err := wipePersonalBinds(b, wipeTarget{Tile: "apps/agent", Kind: wipeEverything}, &sum); err != nil || sum.Registrations != 0 || len(sum.People) != 0 {
		t.Errorf("the wipe of a dead record: %v %+v", err, sum)
	}
	if ents, _ := os.ReadDir(b.personalBindsRoot()); len(ents) != 0 {
		t.Errorf("the dead record stays after the wipe: %v", ents)
	}
}

// covers PD-54 (owner ruling 2026-09-29) D26 D33 — a partitioned
// requester's global binds follow today's bind authority: an admin binds, a
// personal tile's owner binds their own provider (D88) and approves an
// intra-user grant, an org admin binds their org's tile within the org
// (D26), a provider org's admin binds their org's provider into it (D33),
// someone else is refused — as on an unpartitioned requester; and the one
// bind-time refusal, 409 for an unpartitioned requester's http slot to a
// partitioned provider without global (accepted with global, and from a
// partitioned requester), which the pickers' options mark blocked.
func TestGlobalBindAuthority(t *testing.T) {
	w, st, _ := pbindWS(t)
	b := w.b
	for _, u := range []string{"olga", "dana"} {
		if _, err := st.Upsert(users.User{ID: u, Role: users.RoleUser}, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	for _, o := range []users.Org{
		{ID: "ops", Members: []users.Member{{ID: "olga", Level: users.LevelTerminal, Admin: true}}},
		{ID: "data", Members: []users.Member{{ID: "dana", Level: users.LevelTerminal, Admin: true}}},
	} {
		if _, err := st.UpsertOrg(o); err != nil {
			t.Fatal(err)
		}
	}
	for tile, owner := range map[string]string{"apps/oagent": "org:ops", "apps/omcp": "org:ops", "apps/dmcp": "org:data"} {
		if err := st.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	alice, bob, carol := principalFor(t, st, "alice"), principalFor(t, st, "bob"), principalFor(t, st, "carol")
	olga, dana := principalFor(t, st, "olga"), principalFor(t, st, "dana")
	bind := func(p auth.Principal, comp, slot, prov string) *httptest.ResponseRecorder {
		body, _ := json.Marshal(map[string]string{"component": comp, "slot": slot, "provider": prov})
		return call(t, b.apiBindingSet, p, "POST", "/bindings", string(body), nil)
	}
	for _, c := range []struct {
		name             string
		p                auth.Principal
		comp, slot, prov string
		code             int
		msg              string
	}{
		{"unpartitioned → partitioned without global", bob, "apps/plain", "docs", "apps/pu", 409, "apps/pu is partitioned and has no global instance"},
		{"unpartitioned → partitioned with global", bob, "apps/plain", "docs", "apps/pg", 200, ""},
		{"partitioned → partitioned without global", bob, "apps/agent", "docs", "apps/pu", 200, ""},
		{"an unknown provider (today's 400)", bob, "apps/plain", "mcp", "apps/none", 400, "no such provider"},
		{"the owner of both ends, partitioned requester (D88)", alice, "users/alice/pagent", "mcp", "users/alice/mcp", 200, ""},
		{"someone else, partitioned requester", carol, "users/alice/pagent", "mcp", "users/alice/mcp", 403, "not approvable by you"},
		{"an admin, partitioned requester", bob, "apps/agent", "mcp", "users/bob/mcp", 200, ""},
		{"the requester's org admin, within the org (D26)", olga, "apps/oagent", "mcp", "apps/omcp", 200, ""},
		{"the requester's org admin, another org's provider", olga, "apps/oagent", "tools", "apps/dmcp", 403, "not approvable by you"},
		{"the provider's org admin (D33)", dana, "apps/oagent", "tools", "apps/dmcp", 200, ""},
		{"the provider's org admin, someone else's provider", dana, "apps/oagent", "tools", "apps/omcp", 403, "not approvable by you"},
	} {
		rec := bind(c.p, c.comp, c.slot, c.prov)
		if rec.Code != c.code || !strings.Contains(rec.Body.String(), c.msg) {
			t.Errorf("%s: %d %s, want %d %q", c.name, rec.Code, rec.Body.String(), c.code, c.msg)
		}
	}
	// a ref bound before its provider partitioned isn't judged again: the
	// slot's other refs stay editable
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings["apps/plain"]["docs"] = registry.BindTo("apps/pu")
	}); err != nil {
		t.Fatal(err)
	}
	both := `{"component":"apps/plain","slot":"docs","providers":["apps/pu","apps/pg"]}`
	if rec := call(t, b.apiBindingSet, bob, "POST", "/bindings", both, nil); rec.Code != 200 {
		t.Errorf("adding beside an old ref: %d %s", rec.Code, rec.Body.String())
	}
	grant := `{"from":"users/alice/pagent","target":"users/alice/mcp","role":"reader"}`
	if rec := call(t, b.apiGrantsAdd, alice, "POST", "/grants", grant, nil); rec.Code != 200 {
		t.Errorf("the owner approves an intra-user grant on a partitioned tile: %d %s", rec.Code, rec.Body.String())
	}
	if rec := call(t, b.apiGrantsAdd, carol, "POST", "/grants", grant, nil); rec.Code != 403 {
		t.Errorf("someone else approves it: %d %s", rec.Code, rec.Body.String())
	}
	// the wiring view's label: partitioned rows say so; others are unchanged
	rec := call(t, b.apiBindingsList, bob, "GET", "/bindings", "", nil)
	var out struct {
		Components []map[string]any `json:"components"`
		Pending    []pendingBind    `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	// the pickers: a partitioned provider without global is blocked on an
	// unpartitioned requester's slot (POST's 409), offered on a partitioned
	// one's; every other option as before
	option := func(comp, slot, id string) (bindOption, bool) {
		for _, pb := range out.Pending {
			if pb.Component == comp && pb.Slot == slot {
				for _, o := range pb.Options {
					if o.ID == id {
						return o, true
					}
				}
			}
		}
		return bindOption{}, false
	}
	if o, ok := option("apps/plain", "mcp", "users/alice/part"); !ok || !o.Blocked || !strings.HasSuffix(o.Label, " — partitioned, no global instance") {
		t.Errorf("apps/plain's mcp option users/alice/part: %+v %v, want blocked", o, ok)
	}
	if o, ok := option("apps/plain", "mcp", "users/alice/mcp"); !ok || o.Blocked || strings.Contains(o.Label, "partitioned") {
		t.Errorf("apps/plain's mcp option users/alice/mcp: %+v %v, want as before", o, ok)
	}
	if o, ok := option("apps/agent", "llm", "users/alice/part"); !ok || o.Blocked {
		t.Errorf("apps/agent's llm option users/alice/part: %+v %v, want offered", o, ok)
	}
	for _, c := range out.Components {
		want := c["component"] == "apps/agent" || c["component"] == "users/alice/pagent" || c["component"] == "apps/pu" ||
			c["component"] == "apps/pg" || c["component"] == "users/alice/part" || c["component"] == "apps/oagent"
		if got, _ := c["partitioned"].(bool); got != want {
			t.Errorf("%v: partitioned %v, want %v", c["component"], c["partitioned"], want)
		}
		if _, has := c["partitioned"]; !want && has {
			t.Errorf("%v carries partitioned:false (omitempty)", c["component"])
		}
	}
}
