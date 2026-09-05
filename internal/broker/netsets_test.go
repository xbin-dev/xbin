package broker

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// netSetFixture: a real registry root with an org-owned tile declaring a
// net interface (bound to `initial`, or unbound when ""), a user-owned tile,
// a same-org provider tile, org sales + admin carol + member bob.
func netSetFixture(t *testing.T, initial string) (*Broker, *users.Store) {
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
	ws := `{"schema":1}`
	if initial != "" {
		ws = `{"schema":1,"bindings":{"apps/bot":{"net":"` + initial + `"}}}`
	}
	write("xbin.json", ws)
	write("apps/bot/xbin.json", `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`)
	write("apps/mine/xbin.json", `{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`)
	write("apps/vpn/xbin.json", `{"runtime":"go","provides":{"egress":{"kind":"net"}}}`)
	write("apps/othervpn/xbin.json", `{"runtime":"go","provides":{"egress":{"kind":"net"}}}`)
	reg, err := registry.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	b, err := New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	st := testUsers(t, b)
	for _, u := range []users.User{{ID: "root2", Role: users.RoleAdmin}, {ID: "carol"}, {ID: "bob"}} {
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
	for tile, owner := range map[string]string{"apps/bot": "org:sales", "apps/vpn": "org:sales", "apps/mine": "user:bob"} {
		if err := st.SetOwner(tile, owner); err != nil {
			t.Fatal(err)
		}
	}
	return b, st
}

func TestNetRuleTargets(t *testing.T) {
	targets, host := netRuleTargets([]string{"internet", "internet:*.github.com:443", "lan:10.0.0.0/8", "provider:apps/*", "host"})
	if !host || len(targets) != 3 || targets[0] != "net:internet" || targets[1] != "net:*.github.com:443" || targets[2] != "net:10.0.0.0/8" {
		t.Fatalf("targets=%v host=%v", targets, host)
	}
	if targets, host := netRuleTargets([]string{"provider:apps/vpn"}); len(targets) != 0 || host {
		t.Fatalf("provider-only: %v %v", targets, host)
	}
}

// The org's network sets are the ceiling, the default and the allowance for
// its tiles: unbound → org; uncovered → refused at validate / inert at
// resolution; widening the set brings a binding back; none is explicit
// deny-all; org is refused on non-org tiles; deny net beats everything.
func TestNetSetCeiling(t *testing.T) {
	b, st := netSetFixture(t, "internet")
	if nb := b.netBinding("apps/bot"); nb != "internet" {
		t.Fatalf("baseline: %q", nb)
	}
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"lan:10.0.0.0/8", "internet:*.github.com:443"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	// The stored `internet` binding is now outside the set → inert + reason.
	if nb := b.netBinding("apps/bot"); nb != "" {
		t.Fatalf("uncovered binding must be inert, got %q", nb)
	}
	if reason := b.InertNetBindings()["apps/bot"]; !strings.Contains(reason, "devs-net") {
		t.Fatalf("inert reason: %q", reason)
	}
	err := b.validateBinding("apps/bot", "net", registry.BindTo("internet"))
	if err == nil || !strings.Contains(err.Error(), "not covered") || !strings.Contains(err.Error(), "devs-net") {
		t.Fatalf("validate uncovered: %v", err)
	}
	if err := b.validateBinding("apps/bot", "net", registry.BindTo("lan:10.42.0.0/16")); err != nil {
		t.Fatalf("covered lan must validate: %v", err)
	}
	if err := b.validateBinding("apps/bot", "net", registry.BindTo("internet:api.github.com:443")); err != nil {
		t.Fatalf("covered filtered internet must validate: %v", err)
	}
	if err := b.validateBinding("apps/bot", "net", registry.BindTo("host")); err == nil {
		t.Fatal("host must be refused without a host rule")
	}
	// Same-org provider needs no rule; another provider does.
	if err := b.validateBinding("apps/bot", "net", registry.BindTo("apps/vpn")); err != nil {
		t.Fatalf("same-org provider: %v", err)
	}
	if err := b.validateBinding("apps/bot", "net", registry.BindTo("apps/othervpn")); err == nil {
		t.Fatal("workspace provider must need a provider: rule")
	}
	// org / none.
	if err := b.validateBinding("apps/bot", "net", registry.BindTo(NetRefOrg)); err != nil {
		t.Fatalf("org on an org tile: %v", err)
	}
	if err := b.validateBinding("apps/bot", "net", registry.BindTo(NetRefNone)); err != nil {
		t.Fatalf("none: %v", err)
	}
	if err := b.validateBinding("apps/mine", "net", registry.BindTo(NetRefOrg)); err == nil || !strings.Contains(err.Error(), "org-owned") {
		t.Fatalf("org on a user tile: %v", err)
	}
	if err := b.validateBinding("apps/mine", "net", registry.BindTo("internet")); err != nil {
		t.Fatalf("user tiles are unaffected by sets: %v", err)
	}
	// Widen the set → the stored binding resolves again, inert cleared.
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"internet", "lan:10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if nb := b.netBinding("apps/bot"); nb != "internet" {
		t.Fatalf("widened set must re-enable the binding, got %q", nb)
	}
	if _, inert := b.InertNetBindings()["apps/bot"]; inert {
		t.Fatal("inert must clear")
	}
	// Unbound → org by default; the resolved policy is the set union.
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { delete(ws.Bindings, "apps/bot") })
	if nb := b.netBinding("apps/bot"); nb != NetRefOrg {
		t.Fatalf("unbound org tile must default to org, got %q", nb)
	}
	c, _ := b.Reg.Component("apps/bot")
	pol := b.EgressFor(c)
	if len(pol.Rules) != 2 || b.NetHostShare(c) {
		t.Fatalf("org policy: %+v host=%v", pol, b.NetHostShare(c))
	}
	pending := b.pendingBindings()
	var found bool
	for _, pb := range pending {
		if pb.Component == "apps/bot" && pb.Slot == "net" {
			found = true
			if pb.Default != NetRefOrg {
				t.Fatalf("pending default: %+v", pb)
			}
			var hasOrg, hasNone bool
			for _, o := range pb.Options {
				hasOrg = hasOrg || o.ID == NetRefOrg
				hasNone = hasNone || o.ID == NetRefNone
				if o.ID == "host" && !strings.Contains(o.Label, "not covered") {
					t.Fatalf("host option must be marked uncovered: %q", o.Label)
				}
			}
			if !hasOrg || !hasNone {
				t.Fatalf("options: %+v", pb.Options)
			}
		}
	}
	if !found {
		t.Fatal("unbound net slot must still be listed (as satisfied by default)")
	}
	// A user tile's options carry no org.
	mine, _ := b.Reg.Component("apps/mine")
	for _, o := range b.bindOptions("apps/mine", mine.Manifest.Interfaces["net"]) {
		if o.ID == NetRefOrg {
			t.Fatal("org option on a user tile")
		}
	}
	// Explicit none → no egress, not inert.
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/bot": {"net": registry.BindTo(NetRefNone)}}
	})
	if nb := b.netBinding("apps/bot"); nb != "" {
		t.Fatalf("none: %q", nb)
	}
	if _, inert := b.InertNetBindings()["apps/bot"]; inert {
		t.Fatal("none is not inert")
	}
	// A host rule: org resolves to the host netns.
	if err := st.UpsertNetSet("infra-net", users.NetSet{Rules: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net", "infra-net"}); err != nil {
		t.Fatal(err)
	}
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) { delete(ws.Bindings, "apps/bot") })
	if !b.NetHostShare(c) || !b.EgressFor(c).Empty() {
		t.Fatal("org with a host rule must share the host netns")
	}
	if err := b.validateBinding("apps/bot", "net", registry.BindTo("host")); err != nil {
		t.Fatalf("explicit host under a host rule: %v", err)
	}
	// deny net beats the default binding.
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyNet}}}); err != nil {
		t.Fatal(err)
	}
	if nb := b.netBinding("apps/bot"); nb != "" {
		t.Fatalf("deny net must beat the org default, got %q", nb)
	}
	// org bound on an org WITHOUT sets: inert with a reason.
	if err := st.SetOrgPolicy("sales", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", nil); err != nil {
		t.Fatal(err)
	}
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/bot": {"net": registry.BindTo(NetRefOrg)}}
	})
	if nb := b.netBinding("apps/bot"); nb != "" || !strings.Contains(b.InertNetBindings()["apps/bot"], "no network sets") {
		t.Fatalf("org without sets: %q %v", nb, b.InertNetBindings())
	}
}

// Org admins bind inside the sets without an allowance; org/none never need
// one; uncovered refs still fail authz.
func TestDelegatedBindingWithinNetSets(t *testing.T) {
	b, st := netSetFixture(t, "")
	carol := principalFor(t, st, "carol")
	if b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo("lan:10.42.0.0/16"), false) {
		t.Fatal("no allowance, no sets → refuse")
	}
	if !b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo(NetRefNone), false) {
		t.Fatal("none needs no allowance")
	}
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"lan:10.0.0.0/8", "internet:*.github.com:443"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	for _, ref := range []string{"lan:10.42.0.0/16", "internet:api.github.com:443", NetRefOrg, NetRefNone, "apps/vpn"} {
		if !b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo(ref), false) {
			t.Fatalf("%s must pass authz inside the set", ref)
		}
	}
	for _, ref := range []string{"internet", "host", "lan:192.168.0.0/16", "internet:evil.com:443", "apps/othervpn"} {
		if b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo(ref), false) {
			t.Fatalf("%s must fail authz outside the set", ref)
		}
	}
}

// The API: ws-admin only, grammar 400, attached 409, the org view carries
// netSets/resolvedNet, and edits restart the org's net-declaring tiles and
// their stored providers.
func TestNetSetsAPI(t *testing.T) {
	b, st := netSetFixture(t, "apps/vpn")
	carol, root := principalFor(t, st, "carol"), auth.Principal{Owner: true}
	var restarted []string
	b.OnGrantChange = func(comp string) { restarted = append(restarted, comp) }

	pv := map[string]string{"name": "devs-net"}
	if w := call(t, b.apiNetSetPut, carol, "PUT", "/net-sets/devs-net", `{"rules":["internet"]}`, pv); w.Code != 403 {
		t.Fatalf("org admin writing sets: %d", w.Code)
	}
	if w := call(t, b.apiNetSetPut, root, "PUT", "/net-sets/devs-net", `{"rules":["lan:bogus"]}`, pv); w.Code != 400 {
		t.Fatalf("bad grammar: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiNetSetPut, root, "PUT", "/net-sets/devs-net", `{}`, pv); w.Code != 400 {
		t.Fatalf("missing rules: %d", w.Code)
	}
	if w := call(t, b.apiNetSetPut, root, "PUT", "/net-sets/devs-net", `{"rules":["lan:10.0.0.0/8","provider:apps/vpn"]}`, pv); w.Code != 200 {
		t.Fatalf("create: %d %s", w.Code, w.Body.String())
	}
	if len(restarted) != 0 {
		t.Fatalf("an unattached set restarts nothing: %v", restarted)
	}
	// Attach via PATCH /orgs (ws-admin only field) → the org's net tile and its provider restart.
	if w := call(t, b.apiOrgUpdate, carol, "PATCH", "/orgs/sales", `{"netSets":["devs-net"]}`, map[string]string{"org": "sales"}); w.Code != 403 {
		t.Fatalf("org admin attaching: %d", w.Code)
	}
	w := call(t, b.apiOrgUpdate, root, "PATCH", "/orgs/sales", `{"netSets":["devs-net"]}`, map[string]string{"org": "sales"})
	if w.Code != 200 {
		t.Fatalf("attach: %d %s", w.Code, w.Body.String())
	}
	var ov struct {
		NetSets     []string `json:"netSets"`
		ResolvedNet []string `json:"resolvedNet"`
		Allow       []string `json:"resolvedAllow"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &ov); err != nil {
		t.Fatal(err)
	}
	if len(ov.NetSets) != 1 || len(ov.ResolvedNet) != 2 || !contains(ov.Allow, "net:lan:10.0.0.0/8") {
		t.Fatalf("org view: %+v", ov)
	}
	if strings.Join(restarted, ",") != "apps/bot,apps/vpn" {
		t.Fatalf("attach must restart the net tile and its stored provider: %v", restarted)
	}
	// Edit the set → same fan-out.
	restarted = nil
	if w := call(t, b.apiNetSetPut, root, "PUT", "/net-sets/devs-net", `{"rules":["internet"]}`, pv); w.Code != 200 {
		t.Fatalf("edit: %d", w.Code)
	}
	if len(restarted) != 2 {
		t.Fatalf("edit fan-out: %v", restarted)
	}
	// List + attachedTo; delete refused while attached; then allowed.
	w = call(t, b.apiNetSetsList, root, "GET", "/net-sets", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"attachedTo":{"devs-net":["sales"]}`) {
		t.Fatalf("list: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiNetSetDelete, root, "DELETE", "/net-sets/devs-net", "", pv); w.Code != 409 {
		t.Fatalf("delete attached: %d", w.Code)
	}
	if w := call(t, b.apiOrgUpdate, root, "PATCH", "/orgs/sales", `{"netSets":[]}`, map[string]string{"org": "sales"}); w.Code != 200 {
		t.Fatalf("detach: %d", w.Code)
	}
	if w := call(t, b.apiNetSetDelete, root, "DELETE", "/net-sets/devs-net", "", pv); w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiNetSetDelete, root, "DELETE", "/net-sets/devs-net", "", pv); w.Code != 404 {
		t.Fatalf("delete again: %d", w.Code)
	}
	// A whole-member PATCH by the org admin keeps the attachment.
	if w := call(t, b.apiNetSetPut, root, "PUT", "/net-sets/x", `{"rules":["internet"]}`, map[string]string{"name": "x"}); w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	call(t, b.apiOrgUpdate, root, "PATCH", "/orgs/sales", `{"netSets":["x"]}`, map[string]string{"org": "sales"})
	call(t, b.apiOrgUpdate, carol, "PATCH", "/orgs/sales", `{"members":[{"id":"carol","level":"terminal","admin":true}]}`, map[string]string{"org": "sales"})
	if got := st.OrgNetSets("sales"); len(got) != 1 {
		t.Fatalf("member PATCH dropped net sets: %v", got)
	}
}

// Transfers: org egress dies away from an org with sets; an explicit ref must
// be inside the new org's sets; none survives.
func TestTransferNetSets(t *testing.T) {
	b, st := netSetFixture(t, "")
	if _, err := st.UpsertOrg(users.Org{ID: "infra"}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNetSet("lan-only", users.NetSet{Rules: []string{"lan:10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("infra", []string{"lan-only"}); err != nil {
		t.Fatal(err)
	}
	c, _ := b.Reg.Component("apps/bot")
	reason := func(ref, to string) string {
		return b.deadSlotReason(c, "net", registry.BindTo(ref), st.CeilingFor("apps/bot", to))
	}
	if r := reason(NetRefOrg, "user:bob"); !strings.Contains(r, "no meaning") {
		t.Fatalf("org → user: %q", r)
	}
	if r := reason(NetRefOrg, "org:sales"); !strings.Contains(r, "no meaning") { // sales has no sets
		t.Fatalf("org → org without sets: %q", r)
	}
	if r := reason(NetRefOrg, "org:infra"); r != "" {
		t.Fatalf("org → org with sets: %q", r)
	}
	if r := reason("internet", "org:infra"); !strings.Contains(r, "not covered") || !strings.Contains(r, "lan-only") {
		t.Fatalf("internet → lan-only org: %q", r)
	}
	if r := reason("lan:10.42.0.0/16", "org:infra"); r != "" {
		t.Fatalf("covered ref: %q", r)
	}
	if r := reason(NetRefNone, "org:infra"); r != "" {
		t.Fatalf("none survives: %q", r)
	}
	if r := reason("internet", "user:bob"); r != "" {
		t.Fatalf("user owner has no sets: %q", r)
	}
}

// NetLabel: the console/bx status view of a tile's effective network.
func TestNetLabel(t *testing.T) {
	b, st := netSetFixture(t, "")
	if l := b.NetLabel("apps/bot"); l.Ref != "" || l.Effective != "none" {
		t.Fatalf("unbound: %+v", l)
	}
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"lan:10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	l := b.NetLabel("apps/bot")
	if l.Ref != NetRefOrg || l.Effective != "relay" || len(l.Rules) != 1 || !strings.Contains(l.Source, "devs-net") {
		t.Fatalf("org default: %+v", l)
	}
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/bot": {"net": registry.BindTo("internet")}}
	})
	if l := b.NetLabel("apps/bot"); l.Ref != "internet" || l.Effective != "none" || !strings.Contains(l.Note, "not covered") {
		t.Fatalf("inert: %+v", l)
	}
	w := call(t, b.apiBindingsList, auth.Principal{Owner: true}, "GET", "/bindings", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"inert":{"apps/bot":{"net":`) {
		t.Fatalf("bindings inert map: %d %s", w.Code, w.Body.String())
	}
}

// The inert map must be live, not "whatever last resolved": a static tile
// (nothing respawns it on a set change) whose set was narrowed reports inert
// on the very next /bindings read, with no resolution in between.
func TestInertNetLive(t *testing.T) {
	b, st := netSetFixture(t, "")
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"internet", "lan:10.0.0.0/8"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgNetSets("sales", []string{"devs-net"}); err != nil {
		t.Fatal(err)
	}
	_ = b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/bot": {"net": registry.BindTo("lan:10.1.0.0/16")}}
	})
	if _, inert := b.InertNetBindings()["apps/bot"]; inert {
		t.Fatal("a covered binding is not inert")
	}
	if err := st.UpsertNetSet("devs-net", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	w := call(t, b.apiBindingsList, auth.Principal{Owner: true}, "GET", "/bindings", "", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"inert":{"apps/bot":{"net":"net:lan:10.1.0.0/16 is not covered`) {
		t.Fatalf("inert must be reported without a prior resolution: %d %s", w.Code, w.Body.String())
	}
}

func contains(list []string, v string) bool {
	for _, e := range list {
		if e == v {
			return true
		}
	}
	return false
}
