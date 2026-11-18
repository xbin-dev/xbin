package boot

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/signins"
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
	// (apps/plain/sub: a tile INSIDE another's directory — whoever can write apps/plain can change its code)
	for rel, body := range map[string]string{"index.html": "<html><body>plain</body></html>", "xbin.json": `{"title":"Plain"}`,
		"sub/index.html": "<html><body>sub</body></html>", "sub/xbin.json": `{"title":"Sub"}`} {
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
	// a workspace nobody shares anything in keeps no record of who could
	// change its tiles (signins/past.go): the scaffold's tiles are the
	// workspace's, and only its admin can change them
	pastFile := filepath.Join(ws, "data", "signins", "tiles.json")
	if d.st.Broker.SigninPast == nil {
		t.Fatal("the record of who could change tiles is not wired")
	}
	d.st.Broker.ObserveTileParties()
	if _, err := os.Stat(pastFile); !os.IsNotExist(err) || d.st.Broker.SigninPast.Recorded() != 0 {
		t.Fatalf("a one-person workspace recorded who could change its tiles: %v (%d)", err, d.st.Broker.SigninPast.Recorded())
	}
	// …and a workspace with ONE ACCOUNT records nothing either, whatever
	// that account owns or hands on: a tile going from its admin's
	// ownership back to the workspace is not "someone who could change it"
	api(200, "POST", "/users", map[string]any{"id": "solo", "name": "solo", "role": "admin", "password": "pw-solo-pw-123"})
	api(200, "POST", "/owner", map[string]any{"tile": "apps/plain", "to": "user:solo"})
	api(200, "POST", "/owner", map[string]any{"tile": "apps/plain", "to": ""})
	d.st.Broker.ObserveTileParties()
	if _, err := os.Stat(pastFile); !os.IsNotExist(err) || d.st.Broker.SigninPast.Recorded() != 0 {
		t.Fatalf("a one-account workspace recorded a tile its admin handed to the workspace: %v (%d)", err, d.st.Broker.SigninPast.Recorded())
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
	api(400, "DELETE", "/agent/signins/homes/..%2F..%2Fbob%2F"+his, nil, alice)               // (an encoded dot segment never reaches a handler)
	if code, out := d.do("DELETE", "/api/xbin/agent/signins/homes/"+hers, nil); code != 404 { // (its own copies only: under the key of its home)
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
	// xbind removes NO copy on its own: the one whose tile is gone stays,
	// listed as such, hers to forget
	if seen, gone := tm.FollowHomeCopies(); seen != 2 || gone != 1 {
		t.Fatalf("the copies as xbind finds them: %d, %d gone", seen, gone)
	}
	if v = get(alice); len(v.Homes) != 1 || v.Homes[0].ID != gone || !v.Homes[0].Gone {
		t.Fatalf("alice's copies after her forget: %+v", v.Homes)
	}
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "alice", gone, "meta.json")); err != nil {
		t.Fatalf("the copy of a tile that is gone was removed: %v", err)
	}

	// a tile INSIDE another: bob, who can write apps/plain, can change
	// apps/plain/sub's code — alice's question for her own tile there names
	// him, and says through which tile
	api(200, "POST", "/owner", map[string]any{"tile": "apps/plain/sub", "to": "user:alice"})
	api(200, "PATCH", "/users/alice", map[string]any{"tiles": map[string]string{"apps/plain": "terminal", "apps/plain/sub": "terminal"}})
	var nested struct {
		Harnesses []struct {
			Here *struct {
				Trust *struct {
					OwnerKind string
					Editors   []string
					Through   map[string]string
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(api(200, "GET", "/agent/signins?tile=apps/plain/sub", nil, alice)), &nested); err != nil {
		t.Fatal(err)
	}
	if len(nested.Harnesses) == 0 || nested.Harnesses[0].Here == nil || nested.Harnesses[0].Here.Trust == nil {
		t.Fatalf("her tile inside a tile bob can write asks nothing: %+v", nested)
	}
	if q := nested.Harnesses[0].Here.Trust; q.OwnerKind != "you" || len(q.Editors) != 1 || q.Editors[0] != "bob" || q.Through["bob"] != "apps/plain" {
		t.Fatalf("the question for a tile inside one bob can write: %+v", q)
	}

	// the sticky condition, through the routes an admin uses: the tile
	// becomes alice's, and bob — who could change it — loses his access. It
	// is "only hers" now, and still asks for her trust: the card names bob
	api(200, "POST", "/owner", map[string]any{"tile": "apps/plain", "to": "user:alice"})
	api(200, "PATCH", "/users/bob", map[string]any{"tiles": map[string]string{}})
	var sticky struct {
		Harnesses []struct {
			Here *struct {
				Why   string
				Trust *struct {
					Basis     string
					OwnerKind string
					Editors   []string
					Past      []string
					PastUntil int64
					Means     int
				}
			}
		}
	}
	if err := json.Unmarshal([]byte(api(200, "GET", "/agent/signins?tile=apps/plain", nil, alice)), &sticky); err != nil {
		t.Fatal(err)
	}
	if len(sticky.Harnesses) == 0 || sticky.Harnesses[0].Here == nil || sticky.Harnesses[0].Here.Trust == nil {
		t.Fatalf("a tile bob could change before asks nothing of alice: %+v", sticky)
	}
	if q := sticky.Harnesses[0].Here; q.Trust.OwnerKind != "you" || len(q.Trust.Editors) != 0 || len(q.Trust.Past) != 1 || q.Trust.Past[0] != "bob" || q.Trust.PastUntil == 0 || q.Trust.Means != 2 ||
		!strings.Contains(q.Why, "apps/plain wasn't always only yours — bob could change it until ") {
		t.Fatalf("the question for a tile bob could change before: %+v why %q", q.Trust, q.Why)
	}
	if _, err := os.Stat(pastFile); err != nil {
		t.Fatalf("the record of who could change the tile: %v", err)
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
	api(201, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": v.Harnesses[0].Here.Trust.Basis, "shown": signins.TrustShown}, alice)
	if tr := d.st.Signins.Trusted("alice"); len(tr) != 1 || tr[0].Void() {
		t.Fatalf("her trust with saved sign-ins off: %+v", tr)
	}

	// what a trust means is said by the page that asks: an answer that
	// doesn't say what it showed — a page loaded before trusting meant the
	// home too — is refused, and told to reload; nothing is recorded
	for _, tr := range d.st.Signins.Trusted("alice") {
		if _, err := d.st.Signins.Untrust("alice", tr.ID); err != nil {
			t.Fatal(err)
		}
	}
	src, _ := tm.SigninSource("alice", "apps/plain")
	old := api(409, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": src.Basis()}, alice)
	if !strings.Contains(old, `"reload":true`) || !strings.Contains(old, "Reload this page") || !strings.Contains(old, `"changed":true`) || len(d.st.Signins.Trusted("alice")) != 0 {
		t.Fatalf("an answer that doesn't say what was shown: %s (trusted %+v)", old, d.st.Signins.Trusted("alice"))
	}
	api(409, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": src.Basis(), "shown": "signin"}, alice)
	api(201, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": src.Basis(), "shown": signins.TrustShown}, alice)

	// THE ROOT TOKEN keeps trust answers of its own (the seam): asked,
	// answered, listed and taken back through the same routes — and never
	// a saved sign-in
	rootSrc, ok := tm.SigninSource(signins.RootTokenUser, "apps/plain")
	if !ok || !rootSrc.NeedsTrust() {
		t.Fatalf("alice's tile, for the root token: %+v", rootSrc)
	}
	api(201, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": rootSrc.Basis(), "shown": signins.TrustShown})
	if !d.st.Signins.Trusts(signins.RootTokenUser, rootSrc) || len(d.st.Signins.Trusted("alice")) != 1 {
		t.Fatal("the root token's answer is not kept as its own")
	}
	var rootView struct {
		Trusted []struct {
			ID, Tile string
			Holds    bool
		}
	}
	if err := json.Unmarshal([]byte(api(200, "GET", "/agent/signins?tile=apps/plain", nil)), &rootView); err != nil || len(rootView.Trusted) != 1 || !rootView.Trusted[0].Holds || rootView.Trusted[0].Tile != "apps/plain" {
		t.Fatalf("the root token's list: %+v %v", rootView, err)
	}
	api(403, "POST", "/agent/signins", map[string]string{"harness": "claude", "secret": "sk-ant-oat01-" + strings.Repeat("x", 40)})
	api(200, "DELETE", "/agent/signins/trust/"+rootView.Trusted[0].ID, nil)
	if d.st.Signins.Trusts(signins.RootTokenUser, rootSrc) {
		t.Fatal("the root token's answer was not taken back")
	}
	if code, out := d.ownCall("apps/plain", "POST", "/api/xbin/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": rootSrc.Basis(), "shown": signins.TrustShown}); code == 201 {
		t.Fatalf("a tile's own credentials answered as the root token: %s", out)
	}

	// a WORKSPACE ADMIN who stops being one could change every tile: every
	// person's tiles ask once after it, naming them
	api(200, "POST", "/users", map[string]any{"id": "zed", "name": "zed", "role": "admin", "password": "pw-zed-pw-123"})
	api(200, "PATCH", "/users/zed", map[string]any{"role": "user"})
	after, _ := tm.SigninSource("alice", "apps/plain")
	if !slices.Contains(after.PastAdmins, "zed") || !slices.Contains(after.Past, "zed") || !after.NeedsTrust() {
		t.Fatalf("after zed stopped being an admin, alice's tile: %+v", after)
	}
	if d.st.Signins.Trusts("alice", after) { // her answer was given before: it never named zed
		t.Fatal("an answer given before zed stopped being an admin covers him")
	}
	api(201, "POST", "/agent/signins/trust", map[string]string{"tile": "apps/plain", "basis": after.Basis(), "shown": signins.TrustShown}, alice)
	if !d.st.Signins.Trusts("alice", after) {
		t.Fatal("answered again, for the question that names zed: it does not hold")
	}

	// the account is deleted: its copies are KEPT — xbind removes no home on
	// its own — and listed to admins, theirs to forget
	api(200, "DELETE", "/users/bob", nil)
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "bob", his, "meta.json")); err != nil {
		t.Fatalf("a deleted account's copy was removed: %v", err)
	}
	var kept struct {
		Kept []struct {
			User, ID, Tile string
			Bytes          int64
		}
	}
	if err := json.Unmarshal([]byte(api(200, "GET", "/agent/signins", nil)), &kept); err != nil || len(kept.Kept) != 1 || kept.Kept[0].User != "bob" || kept.Kept[0].ID != his || kept.Kept[0].Bytes < 9000 {
		t.Fatalf("what an admin sees kept from deleted accounts: %+v %v", kept, err)
	}
	if out := api(200, "GET", "/agent/signins", nil, alice); strings.Contains(out, `"kept"`) {
		t.Fatalf("a member sees what is kept from deleted accounts: %s", out)
	}
	api(404, "DELETE", "/agent/signins/homes/"+his+"?of=bob", nil, alice) // not an admin
	api(404, "DELETE", "/agent/signins/homes/"+gone+"?of=alice", nil)     // a living account's: never
	api(404, "DELETE", "/agent/signins/homes/"+his+"?of=owner", nil)      // nor the root token's by this door
	api(404, "DELETE", "/agent/signins/homes/"+his+"?of=..%2Falice", nil) // nor a path
	api(200, "DELETE", "/agent/signins/homes/"+his+"?of=bob", nil)        // the admin's forget
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "bob", his)); !os.IsNotExist(err) {
		t.Fatalf("the copy an admin forgot is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(ws, "data", "home-copies", "alice", gone, "meta.json")); err != nil {
		t.Fatalf("alice's copy went with it: %v", err)
	}
	_ = bob
}
