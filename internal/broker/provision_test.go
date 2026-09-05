package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

func adminJSON(t *testing.T, h http.HandlerFunc, method, path, body string) (int, map[string]any) {
	t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	h(w, r)
	var out map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &out)
	return w.Code, out
}

// POST /users {sso:true, email}: an SSO-only pre-provisioned account —
// credential-less, NO invite minted, seeded with the new-account defaults.
func TestUsersCreateSSO(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	if _, err := st.UpsertOrg(users.Org{ID: "corp"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetNewUserDefaults(users.NewUserDefaults{
		Tiles: map[string]string{"apps/shared/*": users.LevelRead},
		Orgs:  []users.OrgDefault{{Org: "corp", Level: users.LevelWrite, Create: true}},
	}); err != nil {
		t.Fatal(err)
	}

	code, out := adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"jane","sso":true}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "email") {
		t.Fatalf("sso without email: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"jane","sso":true,"email":"jane@corp.com","password":"password123"}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "no password") {
		t.Fatalf("sso with password: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"jane","name":"Jane","sso":true,"email":"Jane@Corp.com","canCreate":["apps/jane/*"]}`)
	if code != http.StatusOK {
		t.Fatalf("sso create: %d %v", code, out)
	}
	if _, minted := out["invite"]; minted {
		t.Fatal("an SSO account must not mint an invite")
	}
	u, ok := st.Get("jane")
	if !ok || u.Email != "jane@corp.com" || u.PassHash != "" || u.InvitePending() {
		t.Fatalf("stored: %+v %v", u, ok)
	}
	if u.Tiles["apps/shared/*"] != users.LevelRead || len(u.CanCreate) != 1 {
		t.Fatalf("seed + request union: %+v", u)
	}
	if acc, _ := st.Access("jane"); !acc.CanCreateAs("corp") {
		t.Fatal("default org membership missing")
	}
	// The email is what the IdP sign-in resolves against.
	if bound, ok := st.FindByEmail("jane@corp.com"); !ok || bound.ID != "jane" {
		t.Fatalf("binding: %+v %v", bound, ok)
	}
	// Plain invite path still mints one and seeds the same defaults.
	code, out = adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"bob"}`)
	if code != http.StatusOK || out["invite"] == nil {
		t.Fatalf("invite create: %d %v", code, out)
	}
	if bu, _ := st.Get("bob"); bu.Tiles["apps/shared/*"] != users.LevelRead {
		t.Fatalf("invite seed: %+v", bu)
	}
}

// GET/PUT /defaults carries defaultTiles, newUsers and tileCreation; each
// key replaces only itself.
func TestDefaultsAPI(t *testing.T) {
	b := testBroker(t)
	if _, err := b.Users.UpsertOrg(users.Org{ID: "corp"}); err != nil {
		t.Fatal(err)
	}
	code, out := adminJSON(t, b.apiDefaultsPut, "PUT", "/api/xbin/defaults", `{}`)
	if code != http.StatusBadRequest {
		t.Fatalf("empty put: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiDefaultsPut, "PUT", "/api/xbin/defaults",
		`{"newUsers":{"orgs":[{"org":"corp","level":"write","create":true}],"termNet":true},"tileCreation":"org-only"}`)
	if code != http.StatusOK || out["tileCreation"] != "org-only" {
		t.Fatalf("put: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiDefaultsPut, "PUT", "/api/xbin/defaults", `{"tileCreation":"everyone"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("bad policy: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiDefaultsPut, "PUT", "/api/xbin/defaults", `{"newUsers":{"orgs":[{"org":"ghost"}]}}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "no such org") {
		t.Fatalf("unknown default org: %d %v", code, out)
	}
	// A defaultTiles-only save (the admin tile's card) leaves the rest alone.
	code, out = adminJSON(t, b.apiDefaultsPut, "PUT", "/api/xbin/defaults", `{"defaultTiles":{"apps/welcome":"read"}}`)
	if code != http.StatusOK {
		t.Fatalf("tiles put: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiDefaultsGet, "GET", "/api/xbin/defaults", "")
	if code != http.StatusOK || out["tileCreation"] != "org-only" {
		t.Fatalf("get: %d %v", code, out)
	}
	nu := out["newUsers"].(map[string]any)
	if nu["termNet"] != true || len(nu["orgs"].([]any)) != 1 {
		t.Fatalf("newUsers survived a tiles-only save? %v", nu)
	}
	if b.Users.TileCreation() != users.TileCreationOrgOnly {
		t.Fatal("policy not persisted")
	}
}

// POST /users {orgs}: joins at creation with the given knobs; an unknown org
// refuses BEFORE any account exists; a seed-joined default org is overlaid.
func TestUsersCreateWithOrgs(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	for _, o := range []string{"sales", "infra"} {
		if _, err := st.UpsertOrg(users.Org{ID: o}); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.SetNewUserDefaults(users.NewUserDefaults{Orgs: []users.OrgDefault{{Org: "infra"}}}); err != nil {
		t.Fatal(err)
	}
	code, out := adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"jane","sso":true,"email":"jane@corp.com","orgs":[{"org":"nope"}]}`)
	if code != http.StatusBadRequest || !strings.Contains(out["error"].(string), "no such org") {
		t.Fatalf("unknown org: %d %v", code, out)
	}
	if _, exists := st.Get("jane"); exists {
		t.Fatal("no half-created account")
	}
	code, out = adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users",
		`{"id":"jane","sso":true,"email":"jane@corp.com","orgs":[{"org":"sales","level":"terminal","create":true},{"org":"infra","level":"write"}]}`)
	if code != http.StatusOK || out["orgs"] == nil {
		t.Fatalf("create with orgs: %d %v", code, out)
	}
	orgs := st.UserOrgs("jane")
	if len(orgs) != 2 || orgs[0].ID != "infra" || orgs[0].Level != users.LevelWrite || orgs[1].Level != users.LevelTerminal || !orgs[1].Create {
		t.Fatalf("memberships: %+v", orgs)
	}
}

// DELETE /users/{id}/sessions drops every session of that user; the
// SSO-only guards refuse password paths for non-admins.
func TestUsersSignoutAndSSOOnly(t *testing.T) {
	b := testBroker(t)
	st := b.Users
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	a.SetUsers(st)
	srv := &server.Server{Auth: a}
	for _, u := range []users.User{{ID: "ann"}, {ID: "boss", Role: users.RoleAdmin}} {
		if _, err := st.Upsert(u, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	a.NewSession("ann", "")
	a.NewSession("ann", "")
	a.NewSession("boss", "")
	r := httptest.NewRequest("DELETE", "/api/xbin/users/ann/sessions", nil)
	r.SetPathValue("id", "ann")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w := httptest.NewRecorder()
	b.apiUsersSignout(srv, w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"dropped":2`) {
		t.Fatalf("signout: %d %s", w.Code, w.Body.String())
	}
	if len(a.Sessions()) != 1 {
		t.Fatalf("boss's session must survive: %d", len(a.Sessions()))
	}
	r = httptest.NewRequest("DELETE", "/api/xbin/users/ghost/sessions", nil)
	r.SetPathValue("id", "ghost")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w = httptest.NewRecorder()
	b.apiUsersSignout(srv, w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown user: %d", w.Code)
	}

	// SSO-only mode needs a ready provider (external URL too).
	code, out := adminJSON(t, b.apiAuthSettingsUpdate, "PATCH", "/api/xbin/auth-settings", `{"passwordLoginDisabled":true}`)
	if code != http.StatusConflict {
		t.Fatalf("without SSO: %d %v", code, out)
	}
	if err := st.SetSSO(&users.SSOConfig{Kind: "github", ClientID: "c", AdminGroups: []string{"admins"}}); err != nil {
		t.Fatal(err)
	}
	b.ExternalURL = "https://xbin.example"
	code, out = adminJSON(t, b.apiAuthSettingsUpdate, "PATCH", "/api/xbin/auth-settings", `{"passwordLoginDisabled":true}`)
	if code != http.StatusOK || out["passwordLoginDisabled"] != true {
		t.Fatalf("enable: %d %v", code, out)
	}
	code, out = adminJSON(t, b.apiAuthSettingsGet, "GET", "/api/xbin/auth-settings", "")
	if code != http.StatusOK || out["passwordLoginDisabled"] != true || out["canDisablePassword"] != true {
		t.Fatalf("get: %d %v", code, out)
	}
	sso := out["sso"].(map[string]any)
	sync := sso["groupSync"].(map[string]any)
	if sync["rulesActive"] != true || len(sso["adminGroups"].([]any)) != 1 {
		t.Fatalf("group sync view: %v", sso)
	}
	// Non-admin without a password path → 409; invite for a non-admin → 409; admin fine.
	code, out = adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"bob"}`)
	if code != http.StatusConflict {
		t.Fatalf("passwordless non-admin under SSO-only: %d %v", code, out)
	}
	code, _ = adminJSON(t, b.apiUsersCreate, "POST", "/api/xbin/users", `{"id":"bob","sso":true,"email":"bob@corp.com"}`)
	if code != http.StatusOK {
		t.Fatalf("sso account under SSO-only: %d", code)
	}
	r = httptest.NewRequest("POST", "/api/xbin/users/bob/invite", nil)
	r.SetPathValue("id", "bob")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w = httptest.NewRecorder()
	b.apiUsersInvite(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("invite under SSO-only: %d %s", w.Code, w.Body.String())
	}
	// The probe route answers with a report even when nothing works.
	r = httptest.NewRequest("POST", "/api/xbin/auth-settings/sso/test", strings.NewReader(`{"sso":{"kind":"oidc","issuer":"http://127.0.0.1:1","clientId":"x"}}`))
	r.Header.Set("Content-Type", "application/json")
	r = r.WithContext(auth.WithPrincipal(r.Context(), auth.Principal{Owner: true}))
	w = httptest.NewRecorder()
	b.apiSSOTest(srv, w, r)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"ok":false`) {
		t.Fatalf("sso test route: %d %s", w.Code, w.Body.String())
	}
}
