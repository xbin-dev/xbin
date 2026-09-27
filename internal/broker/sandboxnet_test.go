package broker

import (
	"encoding/json"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/sandbox"
	"github.com/xbin-dev/xbin/internal/users"
)

// sandboxNetFixture: sandbox managers declaring sandbox-net classes —
// apps/mgr (org:sales, with its own net slot too), apps/pmgr (bob's,
// personal), apps/wmgr (workspace-owned) — beside a net provider tile
// (apps/vpn, org:sales), org sales with admin carol and member bob.
func sandboxNetFixture(t *testing.T) (*Broker, *users.Store) {
	t.Helper()
	root := t.TempDir()
	write := func(rel, content string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("xbin.json", `{"schema":1}`)
	write("apps/mgr/xbin.json", `{"runtime":"go","interfaces":{"net":{"kind":"net"},"internet":{"kind":"sandbox-net"},"lab":{"kind":"sandbox-net"}}}`)
	write("apps/pmgr/xbin.json", `{"runtime":"go","interfaces":{"internet":{"kind":"sandbox-net"}}}`)
	write("apps/wmgr/xbin.json", `{"runtime":"go","interfaces":{"internet":{"kind":"sandbox-net"}}}`)
	write("apps/vpn/xbin.json", `{"runtime":"go","provides":{"egress":{"kind":"net"}}}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	st := testUsers(t, b)
	for _, u := range []users.User{{ID: "carol"}, {ID: "bob"}} {
		if _, err := st.Upsert(u, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{
		{ID: "carol", Level: users.LevelTerminal, Create: true, Admin: true},
		{ID: "bob", Level: users.LevelWrite},
	}}); err != nil {
		t.Fatal(err)
	}
	for tile, owner := range map[string]string{"apps/mgr": "org:sales", "apps/vpn": "org:sales", "apps/pmgr": "user:bob"} {
		if err := st.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	return b, st
}

// storeBind writes a binding straight into the workspace manifest — a
// binding made earlier, or by hand.
func storeBind(t *testing.T, b *Broker, comp, slot, ref string) {
	t.Helper()
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		if ws.Bindings == nil {
			ws.Bindings = map[string]map[string]registry.Binding{}
		}
		if ws.Bindings[comp] == nil {
			ws.Bindings[comp] = map[string]registry.Binding{}
		}
		ws.Bindings[comp][slot] = registry.BindTo(ref)
	}); err != nil {
		t.Fatal(err)
	}
}

func classOf(t *testing.T, b *Broker, tile, class string) SandboxNet {
	t.Helper()
	sn, err := b.SandboxEgress(tile, class)
	if err != nil {
		t.Fatal(err)
	}
	return sn
}

// What a sandbox-net slot binds: the shared network builtins, never host, a
// provider tile, a set that says host, an #instance or two refs. The tile's
// own net slot keeps its wider vocabulary.
func TestSandboxNetRefusals(t *testing.T) {
	b, st := sandboxNetFixture(t)
	for name, rules := range map[string][]string{"lab-net": {"lan:10.0.0.0/8"}, "hosty": {"lan:10.0.0.0/8", "host"}, "vpn-only": {"provider:apps/*"}} {
		if err := st.UpsertNetSet(name, users.NetSet{Rules: rules}); err != nil {
			t.Fatal(err)
		}
	}
	refuse := map[string]string{
		"host":                 "never shares the host",
		"apps/vpn":             "not a sandbox network",
		"set:hosty":            "says host",
		"set:vpn-only":         "no relay reach",
		"set:gone":             "no such network set",
		"internet#x":           "no #instance",
		"lan:db.internal":      "address or CIDR",
		"internet:10.0.0.1":    "not a public address",
		NetRefOrg:              "org-owned tiles",
		NetRefPersonal:         "personal (user-owned) tiles",
		"apps/wmgr":            "own provider",
		"internet:*.github.io": "concrete hosts",
	}
	for ref, want := range refuse {
		err := b.validateBinding("apps/wmgr", "internet", registry.BindTo(ref))
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("sandbox-net %s: want an error with %q, got %v", ref, want, err)
		}
	}
	if err := b.validateBinding("apps/wmgr", "internet", registry.BindTo("internet", "lan:10.0.0.0/8")); err == nil || !strings.Contains(err.Error(), "single binding") {
		t.Errorf("two refs: %v", err)
	}
	for _, ref := range []string{"internet", "internet:api.github.com:443,203.0.113.0/24", "lan:10.0.0.0/8", "lan:192.168.1.5:5432", NetRefNone, "set:lab-net"} {
		if err := b.validateBinding("apps/wmgr", "internet", registry.BindTo(ref)); err != nil {
			t.Errorf("sandbox-net %s: %v", ref, err)
		}
	}
	if err := b.validateBinding("apps/mgr", "lab", registry.BindTo(NetRefOrg)); err != nil {
		t.Errorf("org on an org-owned manager: %v", err)
	}
	if err := b.validateBinding("apps/pmgr", "internet", registry.BindTo(NetRefPersonal)); err != nil {
		t.Errorf("personal on a personal manager: %v", err)
	}
	// the same refs on a net slot keep today's meaning
	for _, ref := range []string{"host", "apps/vpn", "set:hosty", "lan:db.internal"} {
		if err := b.validateBinding("apps/mgr", "net", registry.BindTo(ref)); err != nil {
			t.Errorf("net slot %s: %v", ref, err)
		}
	}
	if err := b.validateBinding("apps/mgr", "net", registry.BindTo("internet#x")); err == nil || err.Error() != "net bindings take no #instance" {
		t.Errorf("net #instance message changed: %v", err)
	}
}

// An unbound class is none — no org (D54) or personal (D88) auto-default —
// and a bound one resolves to the relay policy the runtime starts with.
func TestSandboxNetResolution(t *testing.T) {
	b, st := sandboxNetFixture(t)
	if err := st.UpsertNetSet("web", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"web"}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NetSets: &[]string{"web"}}); err != nil {
		t.Fatal(err)
	}
	if nb := b.netBinding("apps/mgr"); nb != NetRefOrg {
		t.Fatalf("control: the manager's own net slot defaults to org, got %q", nb)
	}
	classes := b.SandboxNetClasses("apps/mgr")
	var got []string
	for _, c := range classes {
		got = append(got, c.Class+"="+c.Reach)
		if !c.Policy.Empty() || c.Ref != "" || c.inert {
			t.Errorf("unbound class %s must be none, got %+v", c.Class, c)
		}
	}
	if strings.Join(got, ",") != "none=none,class:internet=none,class:lab=none" {
		t.Fatalf("classes: %v", got)
	}
	if c := classOf(t, b, "apps/pmgr", "class:internet"); c.Reach != sandbox.ReachNone {
		t.Fatalf("a personal manager's unbound class is none too: %+v", c)
	}
	for _, pb := range b.pendingBindings(true) {
		if pb.Component == "apps/mgr" && pb.Slot != "net" && (pb.Kind != registry.KindSandboxNet || pb.Default != "") {
			t.Errorf("pending class row: %+v", pb)
		}
	}

	if err := st.SetOrgNetSets("sales", nil); err != nil { // no D54 ceiling below
		t.Fatal(err)
	}
	storeBind(t, b, "apps/mgr", "internet", "internet")
	storeBind(t, b, "apps/mgr", "lab", "lan:10.42.0.0/16")
	c := classOf(t, b, "apps/mgr", "class:internet")
	if c.Reach != sandbox.ReachInternet || !slices.Equal(c.Rules, []string{"net:internet"}) || c.Ref != "internet" || c.Slot != "internet" || c.Note != "" {
		t.Fatalf("internet class: %+v", c)
	}
	if !c.Policy.Allow(mustAddr("1.1.1.1"), 443) || c.Policy.Allow(mustAddr("10.0.0.1"), 80) {
		t.Fatalf("internet class policy: %+v", c.Policy)
	}
	if c := classOf(t, b, "apps/mgr", "class:lab"); c.Reach != sandbox.ReachOpen || !slices.Equal(c.Rules, []string{"net:10.42.0.0/16"}) {
		t.Fatalf("lab class: %+v", c)
	}
	storeBind(t, b, "apps/wmgr", "internet", "internet:api.github.com:443")
	if c := classOf(t, b, "apps/wmgr", "class:internet"); c.Reach != sandbox.ReachInternet || !c.Policy.HasHostRules() {
		t.Fatalf("filtered class: %+v", c)
	}
	for _, sel := range []string{"", SandboxClassNone} {
		if c := classOf(t, b, "apps/mgr", sel); c.Class != SandboxClassNone || c.Reach != sandbox.ReachNone || !c.Policy.Empty() {
			t.Fatalf("selector %q: %+v", sel, c)
		}
	}
	for _, sel := range []string{"class:net", "class:nope", "internet", "class:"} {
		if _, err := b.SandboxEgress("apps/mgr", sel); err == nil {
			t.Errorf("selector %q must name no class", sel)
		}
	}
	if _, err := b.SandboxEgress("apps/gone", "class:internet"); err == nil {
		t.Error("a vanished tile has no classes")
	}
}

// D54 on org-owned managers: explicit class refs must sit inside the org's
// sets (refused at bind, inert at resolution), org is the union, and a set
// that says host loses host for sandboxes; D20's deny row empties every
// class.
func TestSandboxNetOrgCeiling(t *testing.T) {
	b, st := sandboxNetFixture(t)
	storeBind(t, b, "apps/mgr", "internet", "internet")
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"lan:10.42.0.0/16", "internet:*.github.com:443"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	c := classOf(t, b, "apps/mgr", "class:internet")
	if !c.inert || c.Reach != sandbox.ReachNone || !strings.Contains(c.Note, "devs-net") {
		t.Fatalf("uncovered class must be inert with the reason: %+v", c)
	}
	if err := b.validateBinding("apps/mgr", "internet", registry.BindTo("internet")); err == nil || !strings.Contains(err.Error(), "not covered") {
		t.Fatalf("validate uncovered: %v", err)
	}
	if err := b.validateBinding("apps/mgr", "internet", registry.BindTo("lan:10.42.7.0/24")); err != nil {
		t.Fatalf("covered lan: %v", err)
	}
	opts := b.sandboxNetBuiltinOptions("apps/mgr", true)
	ids := map[string]bindOption{}
	for _, o := range opts {
		ids[o.ID] = o
	}
	if _, ok := ids["host"]; ok {
		t.Fatalf("host offered to a sandbox class: %+v", opts)
	}
	if !ids["internet"].Blocked || ids[NetRefOrg].ID == "" || ids[NetRefNone].Blocked {
		t.Fatalf("options: %+v", opts)
	}
	storeBind(t, b, "apps/mgr", "internet", NetRefOrg)
	if c := classOf(t, b, "apps/mgr", "class:internet"); c.inert || c.Reach != sandbox.ReachOpen || len(c.Rules) != 2 {
		t.Fatalf("org class = the union: %+v", c)
	}
	// the union gains host: the class keeps the relay rules, never host
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"lan:10.42.0.0/16", "host"}}); err != nil {
		t.Fatal(err)
	}
	c = classOf(t, b, "apps/mgr", "class:internet")
	if c.inert || !slices.Equal(c.Rules, []string{"net:10.42.0.0/16"}) || !strings.Contains(c.Note, "host") {
		t.Fatalf("org class with host: %+v", c)
	}
	for _, o := range b.sandboxNetBuiltinOptions("apps/mgr", true) {
		if o.ID == NetRefOrg && !strings.Contains(o.Label, "without host") {
			t.Fatalf("the org option must say host is left out: %q", o.Label)
		}
	}
	// only host: nothing left
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if c := classOf(t, b, "apps/mgr", "class:internet"); !c.inert || !c.Policy.Empty() {
		t.Fatalf("a host-only network gives a sandbox nothing: %+v", c)
	}
	// D20: a deny-net row empties every class, and refuses binding one
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if c := classOf(t, b, "apps/mgr", "class:internet"); !c.inert || !strings.Contains(c.Note, "denies net") {
		t.Fatalf("deny row: %+v", c)
	}
	if err := b.validateBinding("apps/mgr", "lab", registry.BindTo(NetRefNone)); err == nil || !strings.Contains(err.Error(), "denies net") {
		t.Fatalf("deny row at bind: %v", err)
	}
}

// Who wires a class, through the real POST /bindings: D26 org admins within
// their allowance, D88 owners within theirs, D65 sets for workspace admins
// only — the same gates as a net slot.
func TestSandboxNetApprovers(t *testing.T) {
	b, st := sandboxNetFixture(t)
	if err := st.SetOrgAllow("sales", []string{"net:internet"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNetSet("lab-net", users.NetSet{Rules: []string{"lan:10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	admin := auth.Principal{Owner: true}
	bind := func(p auth.Principal, comp, slot, ref string) int {
		t.Helper()
		w := call(t, b.apiBindingSet, p, "POST", "/bindings", `{"component":"`+comp+`","slot":"`+slot+`","provider":"`+ref+`"}`, nil)
		if w.Code == 200 {
			call(t, b.apiBindingSet, admin, "DELETE", "/bindings", `{"component":"`+comp+`","slot":"`+slot+`"}`, nil)
		}
		return w.Code
	}
	carol, bob := principalFor(t, st, "carol"), principalFor(t, st, "bob")
	for ref, want := range map[string]int{"internet": 200, NetRefNone: 200, NetRefOrg: 200, "lan:10.0.0.0/8": 403, "host": 403, "set:lab-net": 403} {
		if got := bind(carol, "apps/mgr", "lab", ref); got != want {
			t.Errorf("carol binds %s on a class of an org manager: want %d, got %d", ref, want, got)
		}
	}
	if got := bind(carol, "apps/wmgr", "internet", "internet"); got != 403 {
		t.Errorf("carol on a workspace manager: %d", got)
	}
	for ref, want := range map[string]int{NetRefNone: 200, "internet": 403, "host": 403} {
		if got := bind(bob, "apps/pmgr", "internet", ref); got != want {
			t.Errorf("bob (no personal allowance) binds %s: want %d, got %d", ref, want, got)
		}
	}
	if err := st.UpsertNetSet("inet", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NetSets: &[]string{"inet"}}); err != nil {
		t.Fatal(err)
	}
	bob = principalFor(t, st, "bob")
	for ref, want := range map[string]int{"internet": 200, NetRefPersonal: 200, "lan:10.0.0.0/8": 403} {
		if got := bind(bob, "apps/pmgr", "internet", ref); got != want {
			t.Errorf("bob (internet allowance) binds %s: want %d, got %d", ref, want, got)
		}
	}
	for ref, want := range map[string]int{"set:lab-net": 200, "host": 400, "apps/vpn": 400} {
		if got := bind(admin, "apps/wmgr", "internet", ref); got != want {
			t.Errorf("admin binds %s on a class: want %d, got %d", ref, want, got)
		}
	}
}

// A class change reaches the runtime (OnSandboxNetChange) on a bind, an
// unbind, a set edit, an org attachment, a personal holder and a transfer —
// and never restarts the manager's backend on its own account.
func TestSandboxNetChangeHook(t *testing.T) {
	b, st := sandboxNetFixture(t)
	var changed, restarted []string
	b.OnSandboxNetChange = func(tile string) { changed = append(changed, tile) }
	b.OnGrantChange = func(comp string) { restarted = append(restarted, comp) }
	reset := func() { changed, restarted = nil, nil }
	admin := auth.Principal{Owner: true}
	expect := func(what string, wantChanged, wantRestarted []string) {
		t.Helper()
		if !slices.Equal(changed, wantChanged) || !slices.Equal(restarted, wantRestarted) {
			t.Errorf("%s: sandbox-net changes %v (want %v), restarts %v (want %v)", what, changed, wantChanged, restarted, wantRestarted)
		}
		reset()
	}
	w := call(t, b.apiBindingSet, admin, "POST", "/bindings", `{"component":"apps/mgr","slot":"internet","provider":"internet"}`, nil)
	if w.Code != 200 {
		t.Fatalf("bind: %d %s", w.Code, w.Body)
	}
	expect("bind", []string{"apps/mgr"}, nil)
	call(t, b.apiBindingSet, admin, "DELETE", "/bindings", `{"component":"apps/mgr","slot":"internet"}`, nil)
	expect("unbind", []string{"apps/mgr"}, nil)
	if got := b.Reg.Workspace().Bindings["apps/mgr"]["internet"]; len(got) != 0 {
		t.Fatalf("unbind left %v", got)
	}
	call(t, b.apiBindingSet, admin, "POST", "/bindings", `{"component":"apps/mgr","slot":"net","provider":"internet"}`, nil)
	expect("the net slot (control)", nil, []string{"apps/mgr"})

	// a set bound to a class, attached nowhere: the edit re-resolves, restarts nothing
	if err := st.UpsertNetSet("lab-net", users.NetSet{Rules: []string{"lan:10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	storeBind(t, b, "apps/wmgr", "internet", "set:lab-net")
	b.netSetsChanged("lab-net", st.NetSetAttachedTo("lab-net"))
	expect("set edit", []string{"apps/wmgr"}, nil)
	if bound := b.netSetBoundTiles("lab-net"); !slices.Equal(bound, []string{"apps/wmgr"}) {
		t.Fatalf("boundBy: %v", bound)
	}
	w = call(t, b.apiNetSetDelete, admin, "DELETE", "/net-sets/lab-net", "", map[string]string{"name": "lab-net"})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "apps/wmgr") {
		t.Fatalf("deleting a set a class is bound to: %d %s", w.Code, w.Body)
	}
	// an org attachment: the org's manager re-resolves its classes AND
	// restarts for its own net slot (which follows the union)
	b.netSetsChanged("", []string{"sales"})
	expect("org attachment", []string{"apps/mgr"}, []string{"apps/mgr"})
	// a personal holder: bob's manager has classes only
	b.netSetsChanged("", nil, "user:bob")
	expect("personal holder", []string{"apps/pmgr"}, nil)
	// a transfer
	rep := b.transferPreview(admin, st, "apps/pmgr", "org:sales")
	b.executeTransferEffects("apps/pmgr", rep)
	if !slices.Contains(changed, "apps/pmgr") {
		t.Fatalf("transfer: %v", changed)
	}
}

// The bindings answer: sandboxNetOptions for every visible manager (no
// host, no provider tiles), inert notes per class, owner self-blocking.
func TestSandboxNetBindingsView(t *testing.T) {
	b, st := sandboxNetFixture(t)
	if err := st.UpsertNetSet("gone-soon", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	storeBind(t, b, "apps/wmgr", "internet", "set:gone-soon")
	if err := st.DeleteNetSet("gone-soon"); err != nil {
		t.Fatal(err)
	}
	var out struct {
		SandboxNetOptions map[string][]bindOption      `json:"sandboxNetOptions"`
		Inert             map[string]map[string]string `json:"inert"`
		Pending           []pendingBind                `json:"pending"`
	}
	w := call(t, b.apiBindingsList, auth.Principal{Owner: true}, "GET", "/bindings", "", nil)
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, tile := range []string{"apps/mgr", "apps/pmgr", "apps/wmgr"} {
		opts := out.SandboxNetOptions[tile]
		if len(opts) == 0 {
			t.Fatalf("no sandboxNetOptions for %s: %v", tile, out.SandboxNetOptions)
		}
		for _, o := range opts {
			if o.ID == "host" || o.ID == "apps/vpn" {
				t.Errorf("%s offers %s to a sandbox class", tile, o.ID)
			}
		}
	}
	if _, ok := out.SandboxNetOptions["apps/vpn"]; ok {
		t.Error("a tile without classes gets no sandboxNetOptions")
	}
	if why := out.Inert["apps/wmgr"]["internet"]; !strings.Contains(why, "no longer exists") {
		t.Fatalf("inert class: %v", out.Inert)
	}
	pending := 0
	for _, pb := range out.Pending {
		if pb.Kind == registry.KindSandboxNet {
			pending++
			for _, o := range pb.Options {
				if o.ID == "host" {
					t.Errorf("pending class row offers host: %+v", pb)
				}
			}
		}
	}
	if pending != 3 { // apps/mgr internet + lab, apps/pmgr internet (apps/wmgr's is bound)
		t.Fatalf("pending class rows: %d", pending)
	}
	// bob, the personal manager's owner: approvable, outside-allowance greyed
	w = call(t, b.apiBindingsList, principalFor(t, st, "bob"), "GET", "/bindings", "", nil)
	out.SandboxNetOptions = nil
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	for _, o := range out.SandboxNetOptions["apps/pmgr"] {
		if o.ID == "internet" && (!o.Blocked || !strings.Contains(o.Label, "outside your network allowance")) {
			t.Fatalf("bob's internet option: %+v", o)
		}
	}
	if _, ok := out.SandboxNetOptions["apps/wmgr"]; ok {
		t.Error("bob sees a workspace manager's classes")
	}
}

// A transfer kills class bindings the new owner can't hold, like net slots.
func TestSandboxNetTransfer(t *testing.T) {
	b, st := sandboxNetFixture(t)
	if err := st.UpsertNetSet("web", users.NetSet{Rules: []string{"internet:*.github.com:443"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NetSets: &[]string{"web"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"web"}); err != nil {
		t.Fatal(err)
	}
	storeBind(t, b, "apps/pmgr", "internet", NetRefPersonal)
	if c := classOf(t, b, "apps/pmgr", "class:internet"); c.Reach != sandbox.ReachInternet {
		t.Fatalf("personal class: %+v", c)
	}
	rep := b.transferPreview(auth.Principal{Owner: true}, st, "apps/pmgr", "org:sales")
	if len(rep.DeadBind) != 1 || rep.DeadBind[0].Slot != "internet" || !strings.Contains(rep.DeadBind[0].Reason, "personal") {
		t.Fatalf("personal class under an org: %+v", rep.DeadBind)
	}
	storeBind(t, b, "apps/pmgr", "internet", "internet")
	rep = b.transferPreview(auth.Principal{Owner: true}, st, "apps/pmgr", "org:sales")
	if len(rep.DeadBind) != 1 || !strings.Contains(rep.DeadBind[0].Reason, "not covered") {
		t.Fatalf("an uncovered class under the new org: %+v", rep.DeadBind)
	}
}

// Two net slots used to resolve in map order; the name-sorted first wins now.
func TestNetSlotDeterministic(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "apps/two"), 0o755); err != nil {
		t.Fatal(err)
	}
	for rel, content := range map[string]string{
		"xbin.json":          `{"schema":1,"bindings":{"apps/two":{"b":"none","c":"lan:10.0.0.0/8","a":"internet"}}}`,
		"apps/two/xbin.json": `{"runtime":"go","interfaces":{"c":{"kind":"net"},"b":{"kind":"net"},"a":{"kind":"net"}}}`,
	} {
		if err := os.WriteFile(filepath.Join(root, rel), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(b.Close)
	c, _ := reg.Component("apps/two")
	for i := 0; i < 50; i++ {
		if slot, _ := netIfaceSlot(c); slot != "a" {
			t.Fatalf("netIfaceSlot = %q", slot)
		}
		if nb := b.netBinding("apps/two"); nb != "internet" {
			t.Fatalf("run %d: netBinding = %q", i, nb)
		}
	}
}

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
