package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/magik6k/xbin/internal/auth"
	"github.com/magik6k/xbin/internal/users"
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
