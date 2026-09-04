package broker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
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
