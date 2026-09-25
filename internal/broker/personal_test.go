package broker

import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// Self-approval of GRANTS on a personal tile (D88): the owner approves what
// their personal allowance covers or what they own themselves, revokes
// anything, never xbin; nobody else gains anything; the pending list tells
// the owner which rows are theirs to approve.
func TestPersonalGrantSelfApproval(t *testing.T) {
	b, st := orgFixture(t) // bob: sales member; apps/email org-owned by sales
	if err := st.SetOwner("apps/calendar", "user:bob"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	dave := principalFor(t, st, "dave")
	grant := func(p auth.Principal, method, from, target, role string) int {
		t.Helper()
		body := `{"from":"` + from + `","target":"` + target + `","role":"` + role + `"}`
		h := b.apiGrantsAdd
		if method == "DELETE" {
			h = b.apiGrantsRevoke
		}
		return call(t, h, p, method, "/grants", body, nil).Code
	}
	// no allowance yet: a capability is not his to approve…
	if c := grant(bob, "POST", "apps/calendar", "gpu:0", "egress"); c != 403 {
		t.Fatalf("gpu without allowance: %d", c)
	}
	// …his own property is (bob owns apps/crm too)
	if err := st.SetOwner("apps/crm", "user:bob"); err != nil {
		t.Fatal(err)
	}
	if c := grant(bob, "POST", "apps/calendar", "apps/crm", "reader"); c != 200 {
		t.Fatalf("intra-user target: %d", c)
	}
	// a permission set on bob: the allowance now covers gpu
	if err := st.UpsertPermissionSet("gpu", users.PermissionSet{Allow: []string{"gpu:*"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{Sets: &[]string{"gpu"}}); err != nil {
		t.Fatal(err)
	}
	if c := grant(bob, "POST", "apps/calendar", "gpu:0", "egress"); c != 200 {
		t.Fatalf("allowance-covered gpu: %d", c)
	}
	if c := grant(bob, "POST", "apps/calendar", "xbin", "writer"); c != 403 {
		t.Fatalf("xbin must never be self-approved: %d", c)
	}
	// revoke: always, even what an admin approved
	if c := grant(auth.Principal{Owner: true}, "POST", "apps/calendar", "apps/email", "reader"); c != 200 {
		t.Fatalf("admin approve: %d", c)
	}
	if c := grant(bob, "DELETE", "apps/calendar", "apps/email", "reader"); c != 200 {
		t.Fatalf("owner revokes on his own tile: %d", c)
	}
	// not his tile: nothing
	if c := grant(dave, "POST", "apps/calendar", "apps/crm", "reader"); c != 403 {
		t.Fatalf("a stranger approves on bob's tile: %d", c)
	}
	if c := grant(bob, "DELETE", "apps/email", "apps/calendar", "reader"); c != 403 {
		t.Fatalf("bob revokes on an org tile he doesn't admin: %d", c)
	}

	// Who-can-approve hints name the owner exactly when his allowance covers.
	hint := func(target string) string {
		return strings.Join(b.approverHint(registry.Grant{From: "apps/calendar", Target: target, Role: "egress"}), ",")
	}
	if h := hint("gpu:0"); !strings.Contains(h, "owner") {
		t.Errorf("covered: hint %q must name the owner", h)
	}
	if h := hint("cap:containers"); strings.Contains(h, "owner") || !strings.Contains(h, "workspace-admin") {
		t.Errorf("uncovered: hint %q must not name the owner", h)
	}
}

// The personal network (D88): with no personal sets a personal tile keeps
// today's behaviour; with them an unbound net slot defaults to "personal"
// (relay under the owner's rules), `personal` binds only on personal tiles,
// and it goes inert — not wider — when the sets go away.
func TestPersonalNetworkEgress(t *testing.T) {
	b, st := netSetFixture(t, "") // apps/mine: bob's, net slot, unbound
	mine, _ := b.Reg.Component("apps/mine")
	if nb := b.netBinding("apps/mine"); nb != "" {
		t.Fatalf("no personal sets: unbound stays no egress, got %q", nb)
	}
	if err := st.UpsertNetSet("web", users.NetSet{Rules: []string{"internet:*.github.com:443"}}); err != nil {
		t.Fatal(err)
	}
	var restarted []string
	b.OnGrantChange = func(comp string) { restarted = append(restarted, comp) }
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NetSets: &[]string{"web"}}); err != nil {
		t.Fatal(err)
	}
	b.netSetsChanged("", nil, "user:bob")
	if strings.Join(restarted, ",") != "apps/mine" {
		t.Fatalf("attaching a personal set restarts the owner's net tiles: %v", restarted)
	}
	if nb := b.netBinding("apps/mine"); nb != NetRefPersonal {
		t.Fatalf("unbound personal tile with sets → personal, got %q", nb)
	}
	if pol := b.EgressFor(mine); pol.Empty() || !strings.Contains(pol.Rules[0].String(), "github.com") {
		t.Fatalf("personal egress = the owner's rules: %+v", pol)
	}
	if b.NetHostShare(mine) {
		t.Fatal("no host rule → no host netns")
	}
	if l := b.NetLabel("apps/mine"); l.Source != "user:bob's personal network (web)" || l.Effective != "relay" {
		t.Fatalf("label: %+v", l)
	}
	// the pending row + the picker say so
	var mineRow *pendingBind
	for _, pb := range b.pendingBindings(true) {
		if pb.Component == "apps/mine" {
			pb := pb
			mineRow = &pb
		}
	}
	if mineRow == nil || mineRow.Default != NetRefPersonal || mineRow.Options[0].ID != NetRefPersonal {
		t.Fatalf("pending row: %+v", mineRow)
	}
	// `personal` is for personal tiles only
	if err := b.validateBinding("apps/bot", "net", registryBind(NetRefPersonal)); err == nil {
		t.Fatal("personal on an org tile must be refused")
	}
	if err := b.validateBinding("apps/mine", "net", registryBind(NetRefPersonal)); err != nil {
		t.Fatalf("personal on a personal tile: %v", err)
	}
	// a personal default reaches every owner, and host in a set means host
	if err := st.UpsertNetSet("web", users.NetSet{Rules: []string{"host"}}); err != nil {
		t.Fatal(err)
	}
	if !b.NetHostShare(mine) {
		t.Fatal("a host rule in the personal network → host netns")
	}
	// sets gone → an explicit personal binding is inert, never wider
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Bindings = map[string]map[string]registry.Binding{"apps/mine": {"net": registryBind(NetRefPersonal)}}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NetSets: &[]string{}}); err != nil {
		t.Fatal(err)
	}
	if nb := b.netBinding("apps/mine"); nb != "" || !strings.Contains(b.InertNetBindings()["apps/mine"], "personal network") {
		t.Fatalf("personal without sets must be inert: %q %v", nb, b.InertNetBindings())
	}
	// a transfer to an org kills a personal binding (preview says so)
	rep := b.transferPreview(auth.Principal{Owner: true}, st, "apps/mine", "org:sales")
	if len(rep.DeadBind) != 1 || !strings.Contains(rep.DeadBind[0].Reason, "personal egress") {
		t.Fatalf("transfer preview: %+v", rep.DeadBind)
	}
}

func registryBind(refs ...string) registry.Binding { return registry.BindTo(refs...) }

// Terminals on a personal tile (D88): the owner's personal network is added
// (the default), plain internet stays with termNet or when a set holds full
// internet, each personal set is a narrowing scope, and another user's
// terminal there rides the tile owner's network (D54: it's the tile's).
func TestPersonalTerminalScopes(t *testing.T) {
	b, st := netSetFixture(t, "")
	if err := st.UpsertNetSet("lab", users.NetSet{Rules: []string{"lan:10.1.0.0/16"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertNetSet("web", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	bob := func() auth.Principal { return principalFor(t, st, "bob") }
	ids := func(g term.TermNet, p auth.Principal) (string, string) {
		scopes, def := term.ScopesFor(p, g)
		var out []string
		for _, s := range scopes {
			out = append(out, s.ID)
		}
		return strings.Join(out, ","), def
	}
	// no personal network: today's rules (no termNet → offline only)
	if got, def := ids(b.TermNetFor(bob(), "apps/mine"), bob()); got != "none" || def != "none" {
		t.Fatalf("no sets, no termNet: %s default %s", got, def)
	}
	// a lab-only personal network: personal (default), its set, offline — no internet
	if _, err := st.SetUserPersonal("bob", users.PersonalPatch{NetSets: &[]string{"lab"}}); err != nil {
		t.Fatal(err)
	}
	g := b.TermNetFor(bob(), "apps/mine")
	if got, def := ids(g, bob()); got != "personal,set:lab,none" || def != "personal" {
		t.Fatalf("lab-only: %s default %s", got, def)
	}
	if g.OwnerScope != term.NetPersonal || !strings.Contains(g.OrgLabel, "personal network (lab)") {
		t.Fatalf("owner scope: %+v", g)
	}
	// + termNet: internet joins (a union, D88 — unlike an org tile)
	if _, err := st.Upsert(users.User{ID: "bob", Role: users.RoleUser, TermNet: true}, ""); err != nil {
		t.Fatal(err)
	}
	if got, _ := ids(b.TermNetFor(bob(), "apps/mine"), bob()); got != "personal,set:lab,internet,none" {
		t.Fatalf("lab + termNet: %s", got)
	}
	// a personal set holding full internet offers internet without termNet
	if _, err := st.Upsert(users.User{ID: "bob", Role: users.RoleUser}, ""); err != nil {
		t.Fatal(err)
	}
	if err := st.SetPersonalDefaults(users.PersonalDefaults{NetSets: []string{"web"}}); err != nil {
		t.Fatal(err)
	}
	if got, def := ids(b.TermNetFor(bob(), "apps/mine"), bob()); got != "personal,set:lab,set:web,internet,none" || def != "personal" {
		t.Fatalf("lab + web default: %s default %s", got, def)
	}
	// carol with a terminal share on bob's tile rides bob's network
	if err := st.SetUserTile("carol", "apps/mine", users.LevelTerminal); err != nil {
		t.Fatal(err)
	}
	carol := principalFor(t, st, "carol")
	if g := b.TermNetFor(carol, "apps/mine"); !strings.Contains(g.OrgDesc, "user:bob") || g.OwnerScope != term.NetPersonal {
		t.Fatalf("a guest terminal uses the owner's network: %+v", g)
	}
}

// The users/defaults/whoami API carries the personal plane (D88): POST sets
// it (unknown sets refused before any account exists; the seed's switches
// can't be lifted by the request), PATCH overlays by presence and no other
// PATCH clears it, GET /defaults round-trips personalDefaults, and whoami
// folds the switch into personalTiles.
func TestPersonalPlaneAPI(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	if err := st.UpsertNetSet("web", users.NetSet{Rules: []string{"internet"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertPermissionSet("gpu", users.PermissionSet{Allow: []string{"gpu:*"}}); err != nil {
		t.Fatal(err)
	}
	// unknown set: 400, and no half-created account
	code, out := adminJSON(t, b.apiUsersCreate, "POST", "/users", `{"id":"ann","password":"password1","netSets":["nope"]}`)
	if code != 400 || !strings.Contains(out["error"].(string), "no such network set") {
		t.Fatalf("unknown set: %d %v", code, out)
	}
	if _, exists := st.Get("ann"); exists {
		t.Fatal("a refused create must not leave an account")
	}
	// the seed restricts; the request can add, never lift
	if err := st.SetNewUserDefaults(users.NewUserDefaults{NoTerminal: true}); err != nil {
		t.Fatal(err)
	}
	code, out = adminJSON(t, b.apiUsersCreate, "POST", "/users", `{"id":"ann","password":"password1","noTerminal":false,"noPersonalTiles":true,"netSets":["web"]}`)
	if code != 200 {
		t.Fatalf("create: %d %v", code, out)
	}
	u, _ := st.Get("ann")
	if !u.NoTerminal || !u.NoPersonalTiles || strings.Join(u.NetSets, ",") != "web" {
		t.Fatalf("created row: %+v", u)
	}
	// PATCH by presence; an unrelated PATCH (the admin UI's term-net toggle) keeps it
	patch := func(body string) (int, map[string]any) {
		t.Helper()
		r := httptest.NewRequest("PATCH", "/users/ann", strings.NewReader(body))
		r.SetPathValue("id", "ann")
		r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
		w := httptest.NewRecorder()
		b.apiUsersUpdate(nil, w, r)
		var o map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &o)
		return w.Code, o
	}
	if code, out := patch(`{"noTerminal":false,"sets":["gpu"]}`); code != 200 {
		t.Fatalf("patch: %d %v", code, out)
	}
	if code, out := patch(`{"termNet":true}`); code != 200 {
		t.Fatalf("unrelated patch: %d %v", code, out)
	}
	u, _ = st.Get("ann")
	if u.NoTerminal || !u.NoPersonalTiles || strings.Join(u.Sets, ",") != "gpu" || strings.Join(u.NetSets, ",") != "web" || !u.TermNet {
		t.Fatalf("after patches: %+v", u)
	}
	if code, _ := patch(`{"sets":["nope"]}`); code != 400 {
		t.Fatalf("patch with an unknown set: %d", code)
	}
	// the users list carries the resolved plane
	code, out = adminJSON(t, b.apiUsersList, "GET", "/users", "")
	var ann map[string]any
	for _, row := range out["users"].([]any) {
		if m := row.(map[string]any); m["id"] == "ann" {
			ann = m
		}
	}
	if ann == nil || ann["personal"] == nil || !strings.Contains(fmt.Sprint(ann["personal"]), "gpu:*") {
		t.Fatalf("users list personal: %v", ann)
	}
	// defaults: personalDefaults round-trips and replaces only itself
	code, out = adminJSON(t, b.apiDefaultsPut, "PUT", "/defaults", `{"personalDefaults":{"netSets":["web"]}}`)
	if code != 200 || !strings.Contains(fmt.Sprint(out["personalDefaults"]), "web") || !strings.Contains(fmt.Sprint(out["newUsers"]), "noTerminal:true") {
		t.Fatalf("defaults put: %d %v", code, out)
	}
	if code, _ := adminJSON(t, b.apiDefaultsPut, "PUT", "/defaults", `{"personalDefaults":{"sets":["nope"]}}`); code != 400 {
		t.Fatalf("unknown set in personal defaults: %d", code)
	}
	// whoami: personalTiles folds the switch; the personal block is there
	a, _ := st.Access("ann")
	u, _ = st.Get("ann")
	r := httptest.NewRequest("GET", "/whoami", nil)
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{UserID: "ann", User: u, Access: a}))
	w := httptest.NewRecorder()
	b.apiWhoami(w, r)
	var who map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &who)
	if who["personalTiles"] != false || who["personal"] == nil {
		t.Fatalf("whoami: %v", who)
	}
}
