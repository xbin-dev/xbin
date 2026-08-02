package broker

import (
	"encoding/json"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// D37 org screens: create/edit gates follow the org and the edit knob;
// D38: self-service password change + org-admin reset-by-link.
func TestScreensAndAccountFlows(t *testing.T) {
	b, st := orgFixture(t) // sales: carol admin(terminal), bob member(terminal,create), alice read
	carol := principalFor(t, st, "carol")
	alice := principalFor(t, st, "alice")
	bob := principalFor(t, st, "bob")
	dave := principalFor(t, st, "dave")
	root := auth.Principal{Owner: true}

	// ws-admin sets the workspace default screen; non-admin can't.
	w := call(t, b.apiScreensDefaultPut, root, "PUT", "/screens/default", `{"tiles":[{"path":"apps/welcome"}]}`, nil)
	if w.Code != 200 {
		t.Fatalf("default put: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiScreensDefaultPut, carol, "PUT", "/screens/default", `{"tiles":[]}`, nil); w.Code != 403 {
		t.Fatalf("non-ws-admin default put: %d", w.Code)
	}

	// carol (org admin) creates an org screen editable at write level.
	w = call(t, b.apiScreensOrgPut, carol, "PUT", "/screens/org",
		`{"org":"sales","name":"Sales HQ","edit":"write","tiles":[{"path":"apps/email"}]}`, nil)
	if w.Code != 200 {
		t.Fatalf("org screen create: %d %s", w.Code, w.Body.String())
	}
	// alice (read member) can't create, sees it read-only; bob (terminal) can edit tiles.
	if w := call(t, b.apiScreensOrgPut, alice, "PUT", "/screens/org",
		`{"org":"sales","name":"x","tiles":[]}`, nil); w.Code != 403 {
		t.Fatalf("member create: %d", w.Code)
	}
	var view struct {
		Org []struct {
			ID      string `json:"id"`
			CanEdit bool   `json:"canEdit"`
		} `json:"org"`
	}
	get := func(p auth.Principal) {
		t.Helper()
		w := call(t, b.apiScreensGet, p, "GET", "/screens", "", nil)
		if w.Code != 200 {
			t.Fatalf("screens get: %d %s", w.Code, w.Body.String())
		}
		view.Org = nil
		if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
			t.Fatal(err)
		}
	}
	get(alice)
	if len(view.Org) != 1 || view.Org[0].CanEdit {
		t.Fatalf("read member must see the screen read-only: %+v", view.Org)
	}
	get(bob)
	if len(view.Org) != 1 || !view.Org[0].CanEdit {
		t.Fatalf("write-level member must be able to edit: %+v", view.Org)
	}
	get(dave)
	if len(view.Org) != 0 {
		t.Fatalf("non-member must see no org screens: %+v", view.Org)
	}
	id := view.Org
	_ = id
	get(bob)
	sid := view.Org[0].ID
	// bob edits tiles (200) but can't rename/change the knob.
	if w := call(t, b.apiScreensOrgPut, bob, "PUT", "/screens/org",
		`{"id":"`+sid+`","org":"sales","tiles":[{"path":"apps/email"},{"path":"apps/welcome"}]}`, nil); w.Code != 200 {
		t.Fatalf("member tile edit: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiScreensOrgPut, bob, "PUT", "/screens/org",
		`{"id":"`+sid+`","org":"sales","edit":"members","tiles":[]}`, nil); w.Code != 403 {
		t.Fatalf("member meta change must refuse: %d", w.Code)
	}
	// alice (read) can't edit tiles under edit:write.
	if w := call(t, b.apiScreensOrgPut, alice, "PUT", "/screens/org",
		`{"id":"`+sid+`","org":"sales","tiles":[]}`, nil); w.Code != 403 {
		t.Fatalf("read member tile edit must refuse: %d", w.Code)
	}
	// delete: alice no, carol yes.
	if w := call(t, b.apiScreensOrgDelete, alice, "DELETE", "/screens/org", `{"id":"`+sid+`","org":"sales"}`, nil); w.Code != 403 {
		t.Fatalf("member delete: %d", w.Code)
	}
	if w := call(t, b.apiScreensOrgDelete, carol, "DELETE", "/screens/org", `{"id":"`+sid+`","org":"sales"}`, nil); w.Code != 200 {
		t.Fatalf("admin delete: %d %s", w.Code, w.Body.String())
	}

	// D38 self-service password change: wrong current refused, right one works.
	if w := call(t, b.apiAccountPassword, bob, "POST", "/account/password",
		`{"current":"wrong","new":"new-password-9"}`, nil); w.Code != 400 {
		t.Fatalf("wrong current: %d", w.Code)
	}
	if w := call(t, b.apiAccountPassword, bob, "POST", "/account/password",
		`{"current":"password","new":"new-password-9"}`, nil); w.Code != 200 {
		t.Fatalf("change: %d %s", w.Code, w.Body.String())
	}
	if _, ok := st.Verify("bob", "new-password-9"); !ok {
		t.Fatal("new password must verify")
	}

	// D38 delegated reset-by-link: carol re-mints for member alice (200), but
	// not for dave (not her member) nor for an ADMIN user.
	if w := call(t, b.apiUsersInvite, carol, "POST", "/users/alice/invite", "", map[string]string{"id": "alice"}); w.Code != 200 {
		t.Fatalf("org-admin reset-by-link for member: %d %s", w.Code, w.Body.String())
	}
	if w := call(t, b.apiUsersInvite, carol, "POST", "/users/dave/invite", "", map[string]string{"id": "dave"}); w.Code != 403 {
		t.Fatalf("non-member reset must refuse: %d", w.Code)
	}
	if _, err := st.Upsert(users.User{ID: "root2", Role: users.RoleAdmin,
		Tiles: map[string]string{}}, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := st.UpsertOrg(users.Org{ID: "sales", Members: []users.Member{
		{ID: "carol", Level: users.LevelTerminal, Admin: true},
		{ID: "root2", Level: users.LevelRead},
	}}); err != nil {
		t.Fatal(err)
	}
	carol = principalFor(t, st, "carol")
	if w := call(t, b.apiUsersInvite, carol, "POST", "/users/root2/invite", "", map[string]string{"id": "root2"}); w.Code != 403 {
		t.Fatalf("resetting an ADMIN user must stay ws-admin-only: %d", w.Code)
	}
}
