package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// orgFixture: broker + store with alice (member), bob (team member),
// carol (org admin of sales), dave (outsider), root2 (workspace admin).
func orgFixture(t *testing.T) (*Broker, *users.Store) {
	t.Helper()
	b := testBroker(t)
	st := testUsers(t, b)
	for _, id := range []string{"alice", "bob", "carol", "dave"} {
		if _, err := st.Upsert(users.User{ID: id, Role: users.RoleUser}, "password"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Upsert(users.User{ID: "root2", Role: users.RoleAdmin}, "password"); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertTeam("sales", users.Team{ID: "backend", Members: []string{"bob"},
		Tiles: map[string]string{"apps/o/sales/*": users.LevelWrite}}); err != nil {
		t.Fatal(err)
	}
	return b, st
}

// sessionPrincipal builds what FromRequest builds for a signed-in human.
func sessionPrincipal(t *testing.T, st *users.Store, id string) auth.Principal {
	t.Helper()
	u, ok := st.Get(id)
	if !ok {
		t.Fatalf("no user %s", id)
	}
	acc, _ := st.Access(id)
	return auth.Principal{UserID: id, User: u, Access: acc, Via: "session"}
}

func call(t *testing.T, h http.HandlerFunc, p auth.Principal, method, target, body string, pathVals map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(method, target, strings.NewReader(body))
	r = r.WithContext(auth.WithPrincipal(context.Background(), p))
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

func TestOrgAPIGates(t *testing.T) {
	b, st := orgFixture(t)
	carol := func() auth.Principal { return sessionPrincipal(t, st, "carol") }
	alice := func() auth.Principal { return sessionPrincipal(t, st, "alice") }
	admin := auth.Principal{Owner: true}

	// Org admin can rename their org and manage teams…
	if w := call(t, b.apiOrgUpdate, carol(), "PATCH", "/x", `{"name":"Sales Dept"}`, map[string]string{"org": "sales"}); w.Code != 200 {
		t.Fatalf("org admin rename: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiTeamUpsert, carol(), "POST", "/x", `{"id":"frontend","members":["alice"]}`, map[string]string{"org": "sales"}); w.Code != 200 {
		t.Fatalf("org admin team create: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiTeamUpsert, carol(), "PATCH", "/x", `{"members":["alice","bob"]}`, map[string]string{"org": "sales", "team": "backend"}); w.Code != 200 {
		t.Fatalf("org admin team patch: %d %s", w.Code, w.Body.String())
	}
	// …but not the workspace-security knobs (term flags, policy, create/delete).
	if w := call(t, b.apiTeamUpsert, carol(), "PATCH", "/x", `{"termNet":true}`, map[string]string{"org": "sales", "team": "backend"}); w.Code != 403 {
		t.Fatalf("org admin term flag must 403, got %d", w.Code)
	}
	if w := call(t, b.apiPolicyPut, carol(), "PUT", "/x", `{"policy":[]}`, map[string]string{"org": "sales"}); w.Code != 403 {
		t.Fatalf("org admin policy must 403, got %d", w.Code)
	}
	if w := call(t, b.apiOrgCreate, carol(), "POST", "/x", `{"id":"eng"}`, nil); w.Code != 403 {
		t.Fatalf("org admin org-create must 403, got %d", w.Code)
	}
	if w := call(t, b.apiOrgDelete, carol(), "DELETE", "/x", ``, map[string]string{"org": "sales"}); w.Code != 403 {
		t.Fatalf("org admin org-delete must 403, got %d", w.Code)
	}

	// A plain member manages nothing.
	if w := call(t, b.apiOrgUpdate, alice(), "PATCH", "/x", `{"name":"nope"}`, map[string]string{"org": "sales"}); w.Code != 403 {
		t.Fatalf("member org patch must 403, got %d", w.Code)
	}

	// A frame principal riding carol's user id gets the ELEMENT's power, not
	// carol's org-adminship (plans/auth.md: no privilege inheritance).
	frame := auth.Principal{Component: "apps/email", UserID: "carol", Via: "frame"}
	if w := call(t, b.apiOrgUpdate, frame, "PATCH", "/x", `{"name":"nope"}`, map[string]string{"org": "sales"}); w.Code != 403 {
		t.Fatalf("frame principal must not inherit org-admin, got %d", w.Code)
	}

	// Workspace admin: everything, including term flags and policy.
	if w := call(t, b.apiTeamUpsert, admin, "PATCH", "/x", `{"termNet":true}`, map[string]string{"org": "sales", "team": "backend"}); w.Code != 200 {
		t.Fatalf("ws admin term flag: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiPolicyPut, admin, "PUT", "/x", `{"policy":[{"tiles":"*","deny":["net"]}]}`, map[string]string{"org": "sales"}); w.Code != 200 {
		t.Fatalf("ws admin org policy: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiPolicyGet, admin, "GET", "/x", ``, map[string]string{"org": "sales"}); w.Code != 200 || !strings.Contains(w.Body.String(), "net") {
		t.Fatalf("policy get: %d %s", w.Code, w.Body.String())
	}

	// GET /orgs: ws admin sees all; carol sees hers; alice sees none.
	if w := call(t, b.apiOrgsList, admin, "GET", "/x", ``, nil); !strings.Contains(w.Body.String(), "\"sales\"") {
		t.Fatalf("admin orgs list: %s", w.Body.String())
	}
	if w := call(t, b.apiOrgsList, carol(), "GET", "/x", ``, nil); !strings.Contains(w.Body.String(), "\"sales\"") {
		t.Fatalf("org admin orgs list: %s", w.Body.String())
	}
	if w := call(t, b.apiOrgsList, alice(), "GET", "/x", ``, nil); strings.Contains(w.Body.String(), "\"sales\"") {
		t.Fatalf("member orgs list should be empty: %s", w.Body.String())
	}
}

func TestAccessAPI(t *testing.T) {
	b, st := orgFixture(t)
	admin := auth.Principal{Owner: true}
	carol := func() auth.Principal { return sessionPrincipal(t, st, "carol") }

	// Seed: exact user entry + team pattern + base permission.
	if err := st.SetUserTile("dave", "apps/o/sales/crm", users.LevelRead); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Admins: []string{"carol"}, Members: []string{"alice", "bob"}, BasePermission: users.LevelRead}); err != nil {
		t.Fatal(err)
	}

	w := call(t, b.apiAccessGet, admin, "GET", "/x?tile=apps/o/sales/crm", ``, nil)
	if w.Code != 200 {
		t.Fatalf("access get: %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Org     string `json:"org"`
		Entries []struct{ Kind, ID, Level, Source string }
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	find := func(kind, id string) (string, string) {
		for _, e := range got.Entries {
			if e.Kind == kind && e.ID == id {
				return e.Level, e.Source
			}
		}
		return "", ""
	}
	if got.Org != "sales" {
		t.Fatalf("org = %q", got.Org)
	}
	if l, s := find("user", "dave"); l != "read" || s != "exact" {
		t.Fatalf("dave row: %q %q", l, s)
	}
	if l, s := find("team", "sales/backend"); l != "write" || s != "pattern:apps/o/sales/*" {
		t.Fatalf("team row: %q %q", l, s)
	}
	if l, s := find("org", "sales"); l != "read" || s != "base" {
		t.Fatalf("base row: %q %q", l, s)
	}

	// PUT: org admin sets a team exact entry on an org tile.
	if w := call(t, b.apiAccessPut, carol(), "PUT", "/x", `{"tile":"apps/o/sales/crm","kind":"team","id":"sales/backend","level":"terminal"}`, nil); w.Code != 200 {
		t.Fatalf("access put: %d %s", w.Code, w.Body.String())
	}
	o, _ := st.Org("sales")
	if o.Team("backend").Tiles["apps/o/sales/crm"] != users.LevelTerminal {
		t.Fatal("team exact entry not written")
	}
	// Remove it again (level "").
	if w := call(t, b.apiAccessPut, carol(), "PUT", "/x", `{"tile":"apps/o/sales/crm","kind":"team","id":"sales/backend","level":""}`, nil); w.Code != 200 {
		t.Fatalf("access remove: %d %s", w.Code, w.Body.String())
	}
	o, _ = st.Org("sales")
	if _, ok := o.Team("backend").Tiles["apps/o/sales/crm"]; ok {
		t.Fatal("team exact entry not removed")
	}

	// Org admin cannot touch tiles outside their org.
	if w := call(t, b.apiAccessPut, carol(), "PUT", "/x", `{"tile":"apps/chat","kind":"user","id":"alice","level":"read"}`, nil); w.Code != 403 {
		t.Fatalf("out-of-org access put must 403, got %d", w.Code)
	}
	if w := call(t, b.apiAccessGet, carol(), "GET", "/x?tile=apps/chat", ``, nil); w.Code != 403 {
		t.Fatalf("out-of-org access get must 403, got %d", w.Code)
	}
	// A team can never be assigned outside its org (would be inert anyway).
	if w := call(t, b.apiAccessPut, admin, "PUT", "/x", `{"tile":"apps/chat","kind":"team","id":"sales/backend","level":"read"}`, nil); w.Code != 400 {
		t.Fatalf("cross-org team assignment must 400, got %d", w.Code)
	}
}

func TestWhoamiOrgs(t *testing.T) {
	b, st := orgFixture(t)
	w := call(t, b.apiWhoami, sessionPrincipal(t, st, "bob"), "GET", "/x", ``, nil)
	var got struct {
		Orgs []struct {
			ID    string `json:"id"`
			Admin bool   `json:"admin"`
			Teams []struct{ ID string }
		} `json:"orgs"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Orgs) != 1 || got.Orgs[0].ID != "sales" || got.Orgs[0].Admin ||
		len(got.Orgs[0].Teams) != 1 || got.Orgs[0].Teams[0].ID != "backend" {
		t.Fatalf("whoami orgs: %s", w.Body.String())
	}
	w = call(t, b.apiWhoami, sessionPrincipal(t, st, "carol"), "GET", "/x", ``, nil)
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Orgs) != 1 || !got.Orgs[0].Admin {
		t.Fatalf("whoami org admin flag: %s", w.Body.String())
	}
}

// whoami's driving-user view is scoped by tile trust: plain tiles get
// identity only, org tiles their own org's slice, xbin-capable tiles the
// full membership list — and an xbin-caps policy deny downgrades that tier.
func TestWhoamiDriverViewScoping(t *testing.T) {
	b, st := orgFixture(t)
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/email", Target: "xbin", Role: "writer"})
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "eng", Members: []string{"bob"}}); err != nil {
		t.Fatal(err)
	}
	// A sales org tile must exist as the org-tile case's component identity —
	// components need not be registered for whoami (Component is the path).
	whoami := func(comp string) map[string]any {
		w := call(t, b.apiWhoami, auth.Principal{Component: comp, UserID: "bob", Via: "frame"}, "GET", "/x", ``, nil)
		var got map[string]any
		if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		return got
	}
	orgsOf := func(g map[string]any) []any {
		u, _ := g["user"].(map[string]any)
		if u == nil {
			t.Fatalf("no user view: %v", g)
		}
		orgs, _ := u["orgs"].([]any)
		return orgs
	}

	// Plain tile: id+name, no orgs, no admin flag.
	g := whoami("apps/calendar")
	u := g["user"].(map[string]any)
	if u["id"] != "bob" || u["orgs"] != nil || u["admin"] != nil {
		t.Fatalf("plain tile must see identity only, got %v", u)
	}

	// The tile's own org: exactly that org's slice.
	orgs := orgsOf(whoami("apps/o/sales/crm"))
	if len(orgs) != 1 || orgs[0].(map[string]any)["id"] != "sales" {
		t.Fatalf("org tile must see its own org only, got %v", orgs)
	}

	// xbin-capable tile: everything (bob is in sales and eng).
	orgs = orgsOf(whoami("apps/email"))
	if len(orgs) != 2 {
		t.Fatalf("capable tile must see all memberships, got %v", orgs)
	}

	// A policy row denying xbin-caps strips the tier back down.
	if err := st.SetPolicy([]users.PolicyRow{{Tiles: "apps/email", Deny: []string{users.PolicyDenyXbinCaps}}}); err != nil {
		t.Fatal(err)
	}
	g = whoami("apps/email")
	u = g["user"].(map[string]any)
	if u["orgs"] != nil || u["admin"] != nil {
		t.Fatalf("xbin-caps-denied tile must lose the full view, got %v", u)
	}
}

// The access matrix resolves every user × tile with provenance; the
// directory is reachable by org admins but carries identity only.
func TestAccessMatrixAndDirectory(t *testing.T) {
	b, st := orgFixture(t)
	if err := st.GrantTile("dave", "apps/calendar", users.LevelRead); err != nil {
		t.Fatal(err)
	}

	w := call(t, b.apiAccessMatrix, auth.Principal{Owner: true}, "GET", "/x", ``, nil)
	if w.Code != 200 {
		t.Fatalf("matrix: %d %s", w.Code, w.Body.String())
	}
	var m struct {
		Users []struct{ ID, Role string }
		Tiles []string
		Cells map[string]map[string]struct {
			Level string
			Via   []struct{ Level, Source string }
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &m); err != nil {
		t.Fatal(err)
	}
	if len(m.Users) != 5 || len(m.Tiles) == 0 {
		t.Fatalf("matrix shape: %d users, %d tiles", len(m.Users), len(m.Tiles))
	}
	for _, tile := range m.Tiles {
		if tile == "root" || tile == "shell" {
			t.Fatal("chrome must be excluded from the matrix")
		}
	}
	dc, ok := m.Cells["dave"]["apps/calendar"]
	if !ok || dc.Level != "read" || len(dc.Via) == 0 || dc.Via[0].Source != "direct:apps/calendar" {
		t.Fatalf("dave cell: %+v", dc)
	}
	if rc, ok := m.Cells["root2"]["apps/calendar"]; !ok || rc.Level != "terminal" || rc.Via[0].Source != "admin" {
		t.Fatalf("admin cell: %+v", rc)
	}
	if _, ok := m.Cells["alice"]["apps/calendar"]; ok {
		t.Fatal("no-access cells must be absent")
	}

	// Directory: org admin carol may enumerate; plain alice may not; the
	// payload is identity-only.
	w = call(t, b.apiUsersDirectory, sessionPrincipal(t, st, "carol"), "GET", "/x", ``, nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"dave"`) || strings.Contains(w.Body.String(), "role") {
		t.Fatalf("directory for org admin: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiUsersDirectory, sessionPrincipal(t, st, "alice"), "GET", "/x", ``, nil); w.Code != 403 {
		t.Fatalf("directory for plain member must 403, got %d", w.Code)
	}
	// Frame principals never inherit the driving human's org-adminship here either.
	if w := call(t, b.apiUsersDirectory, auth.Principal{Component: "apps/email", UserID: "carol", Via: "frame"}, "GET", "/x", ``, nil); w.Code != 403 {
		t.Fatalf("frame principal directory must 403, got %d", w.Code)
	}
}
