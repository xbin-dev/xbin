package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/term"
)

// covers D245 — the home rule is wired wherever sessions exist (the same
// source and the same trust as the sign-in's, and the tiles' identities),
// GET /agent/signins says which home a session on a tile would mount and
// lists the copies kept of the caller's home, DELETE …/homes/{id} forgets
// one — a person's own, never another's, never a tile's credentials — a
// trust can be given with saved sign-ins switched off (it also decides the
// home), and a deleted account's copies go with it. A workspace without
// sandboxes (this one) mounts nothing and says so; the mounts themselves
// are internal/term's TestHomeCopy*, under isolation.
func TestHomeCopyRoutes(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	for rel, body := range map[string]string{"index.html": "<html><body>plain</body></html>", "xbin.json": `{"title":"Plain"}`} {
		p := filepath.Join(ws, "apps", "plain", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := startSealed(t, ws, nil, func(c *Config) { c.NoAuth, c.Dev = false, true })
	api := func(want int, method, path string, body any, as ...string) string {
		t.Helper()
		code, out := d.do(method, "/api/xbin"+path, body, as...)
		if code != want {
			t.Fatalf("%s %s = %d, want %d: %s", method, path, code, want, out)
		}
		return out
	}
	alice := d.addUser("alice", map[string]string{"apps/plain": "terminal"})
	bob := d.addUser("bob", map[string]string{"apps/plain": "terminal"})

	tm := d.st.Term
	if tm.SigninSource == nil || tm.TileIdentity == nil || tm.Signins == nil {
		t.Fatal("the home rule is not wired: a session would mount its person's real home on every tile")
	}
	if id := tm.TileIdentity("apps/plain"); id == "" {
		t.Fatal("the tile has no identity")
	}

	type view struct {
		Home *struct {
			Mode        string
			Why         string
			Unsandboxed bool
			Docs        string
			Copy        *struct{ ID string }
		}
		Homes []struct {
			ID, Tile string
			Bytes    int64
			Sessions int
			Unused   bool
			Gone     bool
		}
		Harnesses []struct {
			Here *struct {
				Trust *struct{ Basis string }
			}
		}
	}
	get := func(as string) view {
		t.Helper()
		var v view
		if err := json.Unmarshal([]byte(api(200, "GET", "/agent/signins?tile=apps/plain", nil, as)), &v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	// no sandboxes: the real home, said plainly; no copies
	v := get(alice)
	if v.Home == nil || v.Home.Mode != term.HomeOwn || !v.Home.Unsandboxed || !strings.Contains(v.Home.Why, "without sandboxes") || v.Home.Docs != term.HomeDocs || len(v.Homes) != 0 {
		t.Fatalf("alice's home on apps/plain without sandboxes: %+v homes %+v", v.Home, v.Homes)
	}

	// a copy of alice's home for the tile, as a session under isolation
	// leaves one (made by hand here: nothing mounts without sandboxes)
	ident := tm.TileIdentity("apps/plain")
	seed := func(home, tile, ident string) string {
		t.Helper()
		dir := filepath.Join(ws, "data", "home-copies", home, "hc_"+home+"_"+strings.ReplaceAll(tile, "/", "_"))
		if err := os.MkdirAll(filepath.Join(dir, "upper", ".claude"), 0o700); err != nil {
			t.Fatal(err)
		}
		meta, _ := json.Marshal(map[string]any{"v": 1, "tile": tile, "ident": ident, "created": 1})
		if err := os.WriteFile(filepath.Join(dir, "meta.json"), meta, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "upper", ".claude", "settings.json"), []byte(strings.Repeat("x", 9000)), 0o600); err != nil {
			t.Fatal(err)
		}
		return filepath.Base(dir)
	}
	hers := seed("alice", "apps/plain", ident)
	gone := seed("alice", "apps/gone", "i-was-here")
	his := seed("bob", "apps/plain", ident)
	v = get(alice)
	if len(v.Homes) != 2 || v.Home.Copy == nil || v.Home.Copy.ID != hers {
		t.Fatalf("alice's copies: %+v here %+v", v.Homes, v.Home.Copy)
	}
	for _, h := range v.Homes {
		switch h.ID {
		case hers:
			if h.Tile != "apps/plain" || h.Bytes < 9000 || h.Gone {
				t.Errorf("her copy: %+v", h)
			}
		case gone:
			if !h.Gone || h.Unused {
				t.Errorf("the gone tile's copy: %+v", h)
			}
		default:
			t.Errorf("a copy that isn't hers: %+v", h)
		}
	}

	// forget: hers alone; never bob's, never by a tile's credentials or the root token
	api(404, "DELETE", "/agent/signins/homes/"+his, nil, alice)
	api(404, "DELETE", "/agent/signins/homes/nope", nil, alice)
	api(400, "DELETE", "/agent/signins/homes/..%2F..%2Fbob%2F"+his, nil, alice) // (an encoded dot segment never reaches a handler)
	if code, out := d.do("DELETE", "/api/xbin/agent/signins/homes/"+hers, nil); code != 403 {
		t.Fatalf("the root token forgetting a person's copy = %d: %s", code, out)
	}
	if code, out := d.ownCall("apps/plain", "DELETE", "/api/xbin/agent/signins/homes/"+hers, nil); code == 200 {
		t.Fatalf("a tile's own credentials forgot a person's copy: %s", out)
	}
	var forgot struct {
		OK    bool
		Tile  string
		Ended int
	}
	if err := json.Unmarshal([]byte(api(200, "DELETE", "/agent/signins/homes/"+hers, nil, alice)), &forgot); err != nil || !forgot.OK || forgot.Tile != "apps/plain" {
		t.Fatalf("forget: %+v %v", forgot, err)
	}
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "alice", hers)); !os.IsNotExist(err) {
		t.Fatalf("the forgotten copy is still on disk: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "bob", his)); err != nil {
		t.Fatalf("bob's copy went with alice's: %v", err)
	}
	// the sweep: a copy whose tile is gone goes, bob's for the live tile stays
	if n := tm.SweepHomeCopies(); n != 1 {
		t.Fatalf("the sweep removed %d copies, want the gone tile's", n)
	}
	if v = get(alice); len(v.Homes) != 0 {
		t.Fatalf("alice's copies after the forget and the sweep: %+v", v.Homes)
	}

	// trusting a tile is not gated by the switch on SAVED SIGN-INS: it
	// decides the home too
	// (under isolation the home's own question carries the basis whatever
	// that switch says; this workspace has no sandboxes, so the sign-in
	// card's question is read while the switch is still on)
	v = get(alice)
	if len(v.Harnesses) == 0 || v.Harnesses[0].Here == nil || v.Harnesses[0].Here.Trust == nil {
		t.Fatalf("the trust question: %+v", v.Harnesses)
	}
	api(200, "PUT", "/agent/signins/policy", map[string]any{"enabled": false})
	api(201, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": v.Harnesses[0].Here.Trust.Basis}, alice)
	if tr := d.st.Signins.Trusted("alice"); len(tr) != 1 || tr[0].Void() {
		t.Fatalf("her trust with saved sign-ins off: %+v", tr)
	}

	// the account is deleted: its copies go
	api(200, "DELETE", "/users/bob", nil)
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "bob")); !os.IsNotExist(err) {
		t.Fatalf("a deleted account's copies stay: %v", err)
	}
	_ = bob
}
