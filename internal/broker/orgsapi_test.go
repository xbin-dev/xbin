package broker

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/users"
)

// orgFixture: broker + store with carol (org admin of sales), bob (developer:
// terminal+create), alice (viewer), dave (outsider), root2 (ws admin). The
// fixture's apps/email tile is owned by sales; apps/calendar stays workspace.
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
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{
		{ID: "carol", Level: users.LevelTerminal, Create: true, Admin: true},
		{ID: "bob", Level: users.LevelTerminal, Create: true},
		{ID: "alice", Level: users.LevelRead},
	}}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/email", "org:sales"); err != nil {
		t.Fatal(err)
	}
	return b, st
}

func principalFor(t *testing.T, st *users.Store, id string) auth.Principal {
	t.Helper()
	u, ok := st.Get(id)
	if !ok {
		t.Fatalf("no user %q", id)
	}
	a, _ := st.Access(id)
	return auth.Principal{UserID: id, User: u, Access: a}
}

func call(t *testing.T, h http.HandlerFunc, p auth.Principal, method, url, body string, pathVals map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	var rd *strings.Reader
	if body != "" {
		rd = strings.NewReader(body)
	} else {
		rd = strings.NewReader("")
	}
	r := httptest.NewRequest(method, url, rd)
	for k, v := range pathVals {
		r.SetPathValue(k, v)
	}
	r = r.WithContext(auth.WithPrincipal(context.Background(), p))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// --- org management gates (D25/D26/D28) --------------------------------------

func TestOrgAPIGates(t *testing.T) {
	b, st := orgFixture(t)
	carol := principalFor(t, st, "carol") // org admin
	bob := principalFor(t, st, "bob")     // developer
	root := auth.Principal{Owner: true}

	// Org admin edits members.
	w := call(t, b.apiOrgUpdate, carol, "PATCH", "/orgs/sales",
		`{"members":[{"id":"carol","level":"terminal","create":true,"admin":true},{"id":"dave","level":"read"}]}`,
		map[string]string{"org": "sales"})
	if w.Code != 200 {
		t.Fatalf("org admin member edit: %d %s", w.Code, w.Body.String())
	}
	// A non-admin member cannot.
	w = call(t, b.apiOrgUpdate, bob, "PATCH", "/orgs/sales", `{"name":"x"}`, map[string]string{"org": "sales"})
	if w.Code != 403 {
		t.Fatalf("developer must not manage the org: %d", w.Code)
	}
	// Sets/allow are ws-admin only — even for the org admin.
	w = call(t, b.apiOrgUpdate, carol, "PATCH", "/orgs/sales", `{"allow":["net:internet"]}`, map[string]string{"org": "sales"})
	if w.Code != 403 || !strings.Contains(w.Body.String(), "workspace-admin only") {
		t.Fatalf("org admin must not self-serve allowances: %d %s", w.Code, w.Body.String())
	}
	w = call(t, b.apiOrgUpdate, root, "PATCH", "/orgs/sales", `{"allow":["net:internet"]}`, map[string]string{"org": "sales"})
	if w.Code != 200 {
		t.Fatalf("ws admin sets allowance: %d %s", w.Code, w.Body.String())
	}
	// xbin can never enter an allowance.
	w = call(t, b.apiOrgUpdate, root, "PATCH", "/orgs/sales", `{"allow":["xbin:users"]}`, map[string]string{"org": "sales"})
	if w.Code != 400 || !strings.Contains(w.Body.String(), "never delegable") {
		t.Fatalf("xbin allowance must be rejected at write: %d %s", w.Code, w.Body.String())
	}
	// Org delete refused while owning tiles.
	w = call(t, b.apiOrgDelete, root, "DELETE", "/orgs/sales", "", map[string]string{"org": "sales"})
	if w.Code != 409 {
		t.Fatalf("delete while owning tiles: %d %s", w.Code, w.Body.String())
	}

	// Permission sets: ws-admin only, delete-refusal while attached.
	w = call(t, b.apiPermSetPut, carol, "PUT", "/permission-sets/dev", `{"allow":["cap:containers"]}`, map[string]string{"name": "dev"})
	if w.Code != 403 {
		t.Fatalf("org admin must not edit sets: %d", w.Code)
	}
	w = call(t, b.apiPermSetPut, root, "PUT", "/permission-sets/dev", `{"allow":["cap:containers"]}`, map[string]string{"name": "dev"})
	if w.Code != 200 {
		t.Fatalf("ws admin set put: %d %s", w.Code, w.Body.String())
	}
	if err := st.SetOrgSets("sales", []string{"dev"}); err != nil {
		t.Fatal(err)
	}
	w = call(t, b.apiPermSetDelete, root, "DELETE", "/permission-sets/dev", "", map[string]string{"name": "dev"})
	if w.Code != 409 || !strings.Contains(w.Body.String(), "attached") {
		t.Fatalf("attached set delete must refuse: %d %s", w.Code, w.Body.String())
	}
}

// --- ownership transfer authz (D24) ------------------------------------------

func TestOwnerTransferAuthz(t *testing.T) {
	b, st := orgFixture(t)
	if err := st.SetOwner("apps/calendar", "user:bob"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	alice := principalFor(t, st, "alice")
	carol := principalFor(t, st, "carol")

	// A user-owner may transfer to an org they belong to.
	w := call(t, b.apiOwnerTransfer, bob, "POST", "/owner", `{"tile":"apps/calendar","to":"org:sales"}`, nil)
	if w.Code != 200 {
		t.Fatalf("owner → own org transfer: %d %s", w.Code, w.Body.String())
	}
	// Now org-owned: a plain member may NOT transfer it back.
	w = call(t, b.apiOwnerTransfer, alice, "POST", "/owner", `{"tile":"apps/calendar","to":"user:alice"}`, nil)
	if w.Code != 403 {
		t.Fatalf("member transfer must refuse: %d", w.Code)
	}
	// The org admin may hand it to a member.
	w = call(t, b.apiOwnerTransfer, carol, "POST", "/owner", `{"tile":"apps/calendar","to":"user:bob"}`, nil)
	if w.Code != 200 {
		t.Fatalf("org admin → member transfer: %d %s", w.Code, w.Body.String())
	}
	// bob (owner again) may NOT hand it to another user directly.
	w = call(t, b.apiOwnerTransfer, bob, "POST", "/owner", `{"tile":"apps/calendar","to":"user:alice"}`, nil)
	if w.Code != 403 {
		t.Fatalf("user → user transfer is ws-admin only: %d", w.Code)
	}
	// ws-admin can do anything.
	w = call(t, b.apiOwnerTransfer, auth.Principal{Owner: true}, "POST", "/owner", `{"tile":"apps/calendar","to":"user:alice"}`, nil)
	if w.Code != 200 {
		t.Fatalf("ws-admin transfer: %d %s", w.Code, w.Body.String())
	}
}

// --- per-tile ACL gates (D24: sharing is an ownership right) ------------------

func TestAccessAPIGates(t *testing.T) {
	b, st := orgFixture(t)
	if err := st.SetOwner("apps/calendar", "user:bob"); err != nil {
		t.Fatal(err)
	}
	bob := principalFor(t, st, "bob")
	alice := principalFor(t, st, "alice")
	carol := principalFor(t, st, "carol")

	// The user-owner shares their own tile (user + org entries).
	for _, body := range []string{
		`{"tile":"apps/calendar","kind":"user","id":"alice","level":"write"}`,
		`{"tile":"apps/calendar","kind":"org","id":"sales","level":"read"}`,
	} {
		if w := call(t, b.apiAccessPut, bob, "PUT", "/access", body, nil); w.Code != 200 {
			t.Fatalf("owner share: %d %s", w.Code, w.Body.String())
		}
	}
	// The share took effect.
	if a, _ := st.Access("alice"); !a.CanWriteTile("apps/calendar") {
		t.Fatal("user share must confer write")
	}
	if a, _ := st.Access("dave"); a.CanReadTile("apps/calendar") {
		t.Fatal("dave is in no org — org share must not reach him")
	}
	// A non-owner cannot edit the ACL...
	w := call(t, b.apiAccessPut, alice, "PUT", "/access", `{"tile":"apps/calendar","kind":"user","id":"alice","level":"terminal"}`, nil)
	if w.Code != 403 {
		t.Fatalf("non-owner ACL edit must refuse: %d", w.Code)
	}
	// ...and neither can an org admin of an org that does NOT own the tile.
	w = call(t, b.apiAccessPut, carol, "PUT", "/access", `{"tile":"apps/calendar","kind":"user","id":"carol","level":"terminal"}`, nil)
	if w.Code != 403 {
		t.Fatalf("unrelated org admin must refuse: %d", w.Code)
	}
	// Org admin CAN manage an org-owned tile's ACL (apps/email is sales').
	w = call(t, b.apiAccessPut, carol, "PUT", "/access", `{"tile":"apps/email","kind":"user","id":"dave","level":"read"}`, nil)
	if w.Code != 200 {
		t.Fatalf("org admin on org-owned tile: %d %s", w.Code, w.Body.String())
	}
	// GET /access shows owner + entries to the owner.
	w = call(t, b.apiAccessGet, bob, "GET", "/access?tile=apps/calendar", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"owner":"user:bob"`) {
		t.Fatalf("access get: %d %s", w.Code, w.Body.String())
	}
}

// --- D26: delegated grant/binding approval -----------------------------------

func TestDelegatedGrantApproval(t *testing.T) {
	b, st := orgFixture(t)
	carol := principalFor(t, st, "carol")
	bob := principalFor(t, st, "bob")

	grant := func(p auth.Principal, body string) *httptest.ResponseRecorder {
		return call(t, b.apiGrantsAdd, p, "POST", "/grants", body, nil)
	}

	// Not allowed yet: no allowance covers gpu.
	w := grant(carol, `{"from":"apps/email","target":"gpu:0","role":"egress"}`)
	if w.Code != 403 {
		t.Fatalf("uncovered target must refuse: %d %s", w.Code, w.Body.String())
	}
	// Attach an allowance (ws-admin path) → approvable by the org admin.
	if err := st.SetOrgAllow("sales", []string{"gpu:*", "cap:containers"}); err != nil {
		t.Fatal(err)
	}
	w = grant(carol, `{"from":"apps/email","target":"gpu:0","role":"egress"}`)
	if w.Code != 200 {
		t.Fatalf("allowance-covered approval by org admin: %d %s", w.Code, w.Body.String())
	}
	// cap:containers approvable only because the allowance names it.
	w = grant(carol, `{"from":"apps/email","target":"cap:containers","role":"writer"}`)
	if w.Code != 200 {
		t.Fatalf("cap allowance approval: %d %s", w.Code, w.Body.String())
	}
	// A DEVELOPER (non-admin member) may not approve.
	w = grant(bob, `{"from":"apps/email","target":"gpu:1","role":"egress"}`)
	if w.Code != 403 {
		t.Fatalf("non-admin member must not approve: %d", w.Code)
	}
	// Not for tiles the org doesn't own.
	w = grant(carol, `{"from":"apps/calendar","target":"gpu:0","role":"egress"}`)
	if w.Code != 403 {
		t.Fatalf("non-org tile must refuse: %d", w.Code)
	}
	// xbin: never — even if hand-edited into the allowance (eval floor).
	w = grant(carol, `{"from":"apps/email","target":"xbin","role":"admin"}`)
	if w.Code != 403 {
		t.Fatalf("xbin must never be org-approvable: %d", w.Code)
	}
	// Intra-org: both endpoints owned by sales → approvable with NO allowance.
	if err := st.SetOrgAllow("sales", nil); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/calendar", "org:sales"); err != nil {
		t.Fatal(err)
	}
	w = grant(carol, `{"from":"apps/email","target":"apps/calendar","role":"reader"}`)
	if w.Code != 200 {
		t.Fatalf("intra-org grant: %d %s", w.Code, w.Body.String())
	}
	// Ceiling still beats allowance: deny gpu, re-allow, then refuse.
	if err := st.SetOrgAllow("sales", []string{"gpu:*"}); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOrgPolicy("sales", []users.PolicyRow{{Tiles: "*", Deny: []string{users.PolicyDenyGPU}}}); err != nil {
		t.Fatal(err)
	}
	w = grant(carol, `{"from":"apps/email","target":"gpu:2","role":"egress"}`)
	if w.Code == 200 {
		t.Fatalf("ceiling deny must beat the allowance: %d %s", w.Code, w.Body.String())
	}
	// The org-filtered pending/grants view exists for org admins.
	w = call(t, b.apiGrantsList, carol, "GET", "/grants", "", nil)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"scope":"org"`) {
		t.Fatalf("org-filtered grants view: %d %s", w.Code, w.Body.String())
	}
	// Outsiders still get 403.
	dave := principalFor(t, st, "dave")
	if w := call(t, b.apiGrantsList, dave, "GET", "/grants", "", nil); w.Code != 403 {
		t.Fatalf("outsider grants list: %d", w.Code)
	}
}

func TestDelegatedBindingApproval(t *testing.T) {
	b, st := orgFixture(t)
	carol := principalFor(t, st, "carol")

	// A sales-owned tile that requests a net interface (the fixture tiles
	// don't declare one; an unknown slot correctly falls to ws-admin-only).
	dir := filepath.Join(b.Reg.Root, "apps", "bot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"),
		[]byte(`{"runtime":"go","interfaces":{"net":{"kind":"net"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := b.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/bot", "org:sales"); err != nil {
		t.Fatal(err)
	}

	if b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo("internet"), false) {
		t.Fatal("net:internet must need an allowance")
	}
	if err := st.SetOrgAllow("sales", []string{"net:internet"}); err != nil {
		t.Fatal(err)
	}
	if !b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo("internet"), false) {
		t.Fatal("allowance-covered net bind must pass authz")
	}
	if b.orgAdminMayBind(carol, "apps/bot", "net", registry.BindTo("host"), false) {
		t.Fatal("net:host not covered — must refuse")
	}
	// Unbind is always fine for the owning org's admin.
	if !b.orgAdminMayBind(carol, "apps/bot", "net", nil, true) {
		t.Fatal("unbind must pass")
	}
	// A slot the manifest doesn't declare: never org-approvable.
	if b.orgAdminMayBind(carol, "apps/bot", "mystery", registry.BindTo("internet"), false) {
		t.Fatal("unknown slot must refuse")
	}
	// Non-org tile: never.
	if b.orgAdminMayBind(carol, "apps/calendar", "net", registry.BindTo("internet"), false) {
		t.Fatal("non-org tile must refuse")
	}
}

// --- directory + matrix gates -------------------------------------------------

func TestDirectoryAndMatrixGates(t *testing.T) {
	b, st := orgFixture(t)
	carol := principalFor(t, st, "carol")
	dave := principalFor(t, st, "dave")

	if w := call(t, b.apiUsersDirectory, carol, "GET", "/users-directory", "", nil); w.Code != 200 {
		t.Fatalf("org admin directory: %d", w.Code)
	}
	if w := call(t, b.apiUsersDirectory, dave, "GET", "/users-directory", "", nil); w.Code != 403 {
		t.Fatalf("outsider directory must refuse: %d", w.Code)
	}
	// The directory never leaks role/tiles/hashes.
	w := call(t, b.apiUsersDirectory, carol, "GET", "/users-directory", "", nil)
	if strings.Contains(w.Body.String(), "passHash") || strings.Contains(w.Body.String(), `"role"`) {
		t.Fatal("directory must be id+name only")
	}
	// Matrix is ws-admin only; carries owners for provenance.
	if w := call(t, b.apiAccessMatrix, carol, "GET", "/access-matrix", "", nil); w.Code != 403 {
		t.Fatalf("matrix must be ws-admin only: %d", w.Code)
	}
	w = call(t, b.apiAccessMatrix, auth.Principal{Owner: true}, "GET", "/access-matrix", "", nil)
	if w.Code != 200 {
		t.Fatalf("matrix: %d", w.Code)
	}
	var mx struct {
		Owners map[string]string `json:"owners"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &mx); err != nil || mx.Owners["apps/email"] != "org:sales" {
		t.Fatalf("matrix owners: %v %v", err, mx.Owners)
	}
}
