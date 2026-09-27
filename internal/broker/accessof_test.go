package broker

import (
	"encoding/json"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
)

// GET /access/{user}: a tile's backend learns a person's level on ITSELF —
// the X-XBin-User-Level resolution, none for a disabled or unknown account —
// and nothing else asks.
func TestAccessOf(t *testing.T) {
	b, st := orgFixture(t)
	if err := st.SetUserTile("alice", "apps/calendar", "write"); err != nil {
		t.Fatal(err)
	}
	if err := st.SetUserTile("bob", "apps/email", "none"); err != nil { // an exact exclusion (D31)
		t.Fatal(err)
	}
	if err := st.SetOwner("apps/mine", "user:dave"); err != nil { // a personal tile (D24/D88)
		t.Fatal(err)
	}
	backend := func(tile string) auth.Principal { return auth.Principal{Component: tile, Via: "instance"} }
	ask := func(p auth.Principal, user string) (int, map[string]any) {
		t.Helper()
		w := call(t, b.apiAccessOf, p, "GET", "/api/xbin/access/"+user, "", map[string]string{"user": user})
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	for _, c := range []struct {
		tile, user, level string
		active            bool
	}{
		{"apps/calendar", "alice", "write", true},      // her own entry
		{"apps/calendar", "dave", "none", true},        // nothing reaches it
		{"apps/email", "alice", "read", true},          // the owning org's member level
		{"apps/email", "carol", "terminal", true},      // the owning org's admin
		{"apps/email", "bob", "none", true},            // excluded outright
		{"apps/email", "dave", "none", true},           // not in the org
		{"apps/email", "root2", "terminal", true},      // a workspace admin
		{"apps/mine", "dave", "terminal", true},        // his own tile
		{"apps/mine", "alice", "none", true},           // someone else's
		{"apps/email", "zed", "none", false},           // no such account: 200, not 404
		{"apps/email", "user:Carol", "terminal", true}, // "user:<id>", any case
	} {
		code, out := ask(backend(c.tile), c.user)
		if code != 200 || out["level"] != c.level || out["active"] != c.active {
			t.Errorf("%s on %s: %d %v, want %s active=%v", c.user, c.tile, code, out, c.level, c.active)
		}
	}
	if _, out := ask(backend("apps/email"), "user:Carol"); out["user"] != "carol" {
		t.Errorf("the id as the store keeps it: %v", out)
	}

	// a disabled account: none, and inactive (its sessions and frame tokens
	// stopped working with it — D34)
	u, _ := st.Get("alice")
	nu := *u
	nu.Disabled = true
	if _, err := st.Upsert(nu, ""); err != nil {
		t.Fatal(err)
	}
	if code, out := ask(backend("apps/calendar"), "alice"); code != 200 || out["level"] != "none" || out["active"] != false {
		t.Errorf("a disabled account: %d %v", code, out)
	}

	// only a tile's backend asks, about itself
	for name, p := range map[string]auth.Principal{
		"a frame token":    {Component: "apps/email", UserID: "carol", Via: "frame"},
		"a terminal token": {Component: "apps/email", UserID: "carol", Via: "terminal"},
		"a person":         principalFor(t, st, "carol"),
		"an admin":         principalFor(t, st, "root2"),
		"the owner token":  {Owner: true},
	} {
		if code, _ := ask(p, "carol"); code != 403 {
			t.Errorf("%s asked: %d, want 403", name, code)
		}
	}
	if code, _ := ask(backend("apps/email"), ""); code != 400 {
		t.Errorf("no user: %d", code)
	}
}
