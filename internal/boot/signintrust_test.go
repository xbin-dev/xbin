package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/signins"
)

// covers D230 (review finding 1) — "I trust this tile's source" is for the
// tile as it stood, and is VOID from the moment its basis is seen to
// change: at the event, with no session running and nobody reading a
// list. This enumerates the ways who owns a tile or who can change it
// moves, each through the route (or the disk) an admin would use, on a
// daemon wired as xbind wires itself — every one must reach the store.
func TestSigninTrustIsVoidedByEveryChangeOfItsBasis(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	tile := func(dir string) {
		t.Helper()
		for rel, body := range map[string]string{"index.html": "<html><body>plain</body></html>", "xbin.json": `{"title":"Plain"}`} {
			p := filepath.Join(ws, dir, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	tile("apps/plain")
	d := startSealed(t, ws, nil, func(c *Config) { c.NoAuth, c.Dev = false, true }) // people, and saved sign-ins without sandboxes (a development workspace)
	api := func(want int, method, path string, body any, as ...string) string {
		t.Helper()
		code, out := d.do(method, "/api/xbin"+path, body, as...)
		if code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, out)
		}
		return out
	}
	alice := d.addUser("alice", map[string]string{"apps/plain": "terminal"})
	for _, id := range []string{"carol", "mallory", "pat", "orgm", "orgm2"} {
		d.addUser(id, nil)
	}
	api(200, "POST", "/users", map[string]any{"id": "dana", "name": "dana", "role": "admin", "password": "pw-dana-pw", "tiles": map[string]string{"apps/plain": "write"}})
	api(200, "POST", "/orgs", map[string]any{"id": "crew", "name": "Crew"})
	api(200, "PUT", "/orgs/crew/members/orgm", map[string]any{"level": "write"})
	store := d.st.Signins
	// alice answers the card as it stands now
	trust := func() {
		t.Helper()
		var v struct {
			Harnesses []struct {
				ID   string
				Here *struct {
					Trust *struct{ Basis string }
				}
			}
		}
		if err := json.Unmarshal([]byte(api(200, "GET", "/agent/signins?tile=apps/plain", nil, alice)), &v); err != nil || len(v.Harnesses) == 0 || v.Harnesses[0].Here == nil || v.Harnesses[0].Here.Trust == nil {
			t.Fatalf("the question for apps/plain: %+v %v", v, err)
		}
		api(201, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": v.Harnesses[0].Here.Trust.Basis}, alice)
		if tr := store.Trusted("alice"); len(tr) != 1 || tr[0].Void() {
			t.Fatalf("after her answer: %+v", tr)
		}
	}
	// read from the store itself: her list would recheck, and hide an event that never came
	voided := func(what string, eventually bool) signins.Trust {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for {
			tr := store.Trusted("alice")
			if len(tr) == 1 && tr[0].Void() {
				return tr[0]
			}
			if !eventually || time.Now().After(deadline) {
				t.Fatalf("%s: her trust was not voided when it happened: %+v", what, tr)
			}
			time.Sleep(50 * time.Millisecond)
		}
	}
	for _, c := range []struct {
		what   string
		change func()
		why    string
		undo   func()
	}{
		{"an access entry of someone's own",
			func() {
				api(200, "PUT", "/access", map[string]any{"tile": "apps/plain", "kind": "user", "id": "mallory", "level": "write"})
			}, signins.VoidEditor,
			func() {
				api(200, "PUT", "/access", map[string]any{"tile": "apps/plain", "kind": "user", "id": "mallory", "level": ""})
			}},
		{"a pattern on someone's account",
			func() { api(200, "PATCH", "/users/pat", map[string]any{"tiles": map[string]string{"apps/*": "write"}}) }, signins.VoidEditor,
			func() { api(200, "PATCH", "/users/pat", map[string]any{"tiles": map[string]string{}}) }},
		{"the tile shared with an org",
			func() {
				api(200, "PUT", "/access", map[string]any{"tile": "apps/plain", "kind": "org", "id": "crew", "level": "write"})
			}, signins.VoidEditor, nil},
		{"a new member of an org it is shared with",
			func() { api(200, "PUT", "/orgs/crew/members/orgm2", map[string]any{"level": "write"}) }, signins.VoidEditor, nil},
		{"an admin who could already change it becomes a user",
			func() { api(200, "PATCH", "/users/dana", map[string]any{"role": "user"}) }, signins.VoidEditor, nil},
		{"another owner",
			func() { api(200, "POST", "/owner", map[string]any{"tile": "apps/plain", "to": "user:carol"}) }, signins.VoidOwner, nil},
	} {
		trust()
		c.change()
		if got := voided(c.what, false); got.VoidWhy != c.why {
			t.Errorf("%s: voided as %q, want %q", c.what, got.VoidWhy, c.why)
		}
		if c.undo != nil {
			// …and back as it was: gained and lost again — the answer stays void
			c.undo()
			if tr := store.Trusted("alice"); len(tr) != 1 || !tr[0].Void() {
				t.Errorf("%s, and undone: her old answer is live again: %+v", c.what, tr)
			}
		}
	}
	// the tile itself, on disk: made again at its path, then moved away — the watcher's rescan
	trust()
	if err := os.RemoveAll(filepath.Join(ws, "apps", "plain")); err != nil {
		t.Fatal(err)
	}
	tile("apps/plain")
	voided("the tile made again at its path", true)
	// (wait for the registry to hold the new tile before she is asked about it)
	deadline := time.Now().Add(30 * time.Second)
	for {
		code, _ := d.do("GET", "/api/xbin/agent/signins?tile=apps/plain", nil, alice)
		src, ok := d.st.Broker.SigninSource("alice", "apps/plain")
		if code == 200 && ok && src.ID != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the re-made tile never registered")
		}
		time.Sleep(50 * time.Millisecond)
	}
	trust()
	if err := os.Rename(filepath.Join(ws, "apps", "plain"), filepath.Join(ws, "apps", "moved")); err != nil {
		t.Fatal(err)
	}
	voided("the tile moved to another path", true)
}
