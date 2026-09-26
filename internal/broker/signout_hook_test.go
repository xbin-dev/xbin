package broker

import (
	"net/http"
	"slices"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// OnUserSignedOut fires when a user is signed out everywhere, disabled, or
// deleted — the push plane drops the user's device registrations there.
func TestUserSignedOutHook(t *testing.T) {
	b := testBroker(t)
	for _, id := range []string{"ann", "bea", "cid"} {
		if _, err := b.Users.Upsert(users.User{ID: id}, "password123"); err != nil {
			t.Fatal(err)
		}
	}
	var got []string
	b.OnUserSignedOut = func(id string, deleted bool) {
		if deleted {
			id += ":deleted"
		}
		got = append(got, id)
	}
	withID := func(id string, h func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) { r.SetPathValue("id", id); h(w, r) }
	}
	signout := func(w http.ResponseWriter, r *http.Request) { b.apiUsersSignout(nil, w, r) }
	update := func(w http.ResponseWriter, r *http.Request) { b.apiUsersUpdate(nil, w, r) }
	del := func(w http.ResponseWriter, r *http.Request) { b.apiUsersDelete(nil, w, r) }
	if code, out := adminJSON(t, withID("ann", signout), "DELETE", "/api/xbin/users/ann/sessions", ""); code != 200 {
		t.Fatalf("signout: %d %v", code, out)
	}
	if code, out := adminJSON(t, withID("bea", update), "PATCH", "/api/xbin/users/bea", `{"name":"Bea"}`); code != 200 {
		t.Fatalf("rename: %d %v", code, out)
	}
	if code, out := adminJSON(t, withID("bea", update), "PATCH", "/api/xbin/users/bea", `{"disabled":true}`); code != 200 {
		t.Fatalf("disable: %d %v", code, out)
	}
	if code, out := adminJSON(t, withID("cid", del), "DELETE", "/api/xbin/users/cid", ""); code != 200 {
		t.Fatalf("delete: %d %v", code, out)
	}
	if want := []string{"ann", "bea", "cid:deleted"}; !slices.Equal(got, want) {
		t.Fatalf("hook calls %v, want %v (a rename is not a sign-out)", got, want)
	}
}
