package boot

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/confine"
	"github.com/xbin-dev/xbin/internal/users"
)

// covers P21 T9 — protection is managed from the admin tile and with the
// root token (P21, extended by the owner 2026-09-28), end to end in the
// running daemon with auth on. The root token's bx protects, unprotects and
// reassigns the primary: a human credential. The admin tile's frame, its
// token minted by the frame-token route under a person's own login, stands
// in for that person: the tile's owner protects, switches deliveries,
// unprotects and reassigns through it; a person who doesn't manage the tile
// is refused through it; the admin tile's terminal and agent tokens are
// refused, and so is every frame token they mint for the admin tile, the
// owner-driven one's included; an admin's view of the owner is read-only.
// The frame reads the state as the full view of what its person manages.
func TestAdminTileManagesProtection(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git on this host")
	}
	if confine.Isolated() {
		t.Skip("confinement is on: the store's tools run directly here")
	}
	ws := zsWorkspace(t)
	for rel, body := range map[string]string{
		"xbin.json":              `{"schema":1,"grants":[{"from":"tiles/admin","target":"xbin","role":"admin"}]}`,
		"tiles/admin/xbin.json":  `{"uses":[{"target":"xbin","role":"admin"}]}`,
		"tiles/admin/index.html": "<!doctype html><html><head></head><body>admin</body></html>\n",
	} {
		p := filepath.Join(ws, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := zsBoot(t, ws)
	for _, u := range []users.User{
		{ID: "wsad", Role: users.RoleAdmin},
		{ID: "carol", Role: users.RoleUser, Tiles: map[string]string{"tiles/admin": users.LevelRead, "apps/zs": users.LevelRead}},
		{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"tiles/admin": users.LevelRead, "apps/zs": users.LevelTerminal}},
	} {
		if _, err := d.st.Users.Upsert(u, "password1"); err != nil {
			t.Fatal(err)
		}
	}
	if err := d.st.Users.SetOwner("apps/zs", "user:carol"); err != nil {
		t.Fatal(err)
	}
	const tile = "apps/zs"
	owner := "Bearer " + d.owner

	// send is one request with a credential: "Bearer …", "Cookie: …", or
	// "Frame: <token>" (the frame token alone, as a sandboxed frame sends it).
	send := func(method, path, body, cred string) (int, []byte) {
		t.Helper()
		r, err := http.NewRequest(method, d.url+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		switch {
		case strings.HasPrefix(cred, "Cookie: "):
			r.Header.Set("Cookie", strings.TrimPrefix(cred, "Cookie: "))
		case strings.HasPrefix(cred, "Frame: "):
			r.Header.Set(auth.FrameTokenHeader, strings.TrimPrefix(cred, "Frame: "))
		default:
			r.Header.Set("Authorization", cred)
		}
		if body != "" {
			r.Header.Set("Content-Type", "application/json")
		}
		resp, err := (&http.Client{Timeout: 60 * time.Second}).Do(r)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, b
	}
	// frame is the admin tile's frame token as the frame-token route mints
	// it for cred.
	frame := func(cred string) string {
		t.Helper()
		code, b := send("GET", "/api/xbin/frame-token?component=tiles/admin", "", cred)
		var m struct{ Token string }
		if code != http.StatusOK || json.Unmarshal(b, &m) != nil || m.Token == "" {
			t.Fatalf("the admin tile's frame token for %.20s…: %d %s", cred, code, b)
		}
		return "Frame: " + m.Token
	}
	type state struct {
		Primary          string `json:"primary"`
		ProtectedPrimary bool   `json:"protectedPrimary"`
		View             string `json:"view"`
		Seq              int64  `json:"seq"`
		Caller           struct {
			Manager bool `json:"manager"`
		} `json:"caller"`
		Deployments []struct {
			Name       string `json:"name"`
			Deliveries bool   `json:"deliveries"`
		} `json:"deployments"`
	}
	read := func(cred string) state {
		t.Helper()
		code, b := send("GET", "/api/xbin/deployments?tile="+tile, "", cred)
		var s state
		if code != http.StatusOK || json.Unmarshal(b, &s) != nil {
			t.Fatalf("the state: %d %s", code, b)
		}
		return s
	}
	must := func(what, route, body, cred string) {
		t.Helper()
		if code, b := send("POST", "/api/xbin/deployments/"+route, body, cred); code != http.StatusOK {
			t.Fatalf("%s: %d %s", what, code, b)
		}
	}
	refused := func(what, route, body, cred, want string) {
		t.Helper()
		code, b := send("POST", "/api/xbin/deployments/"+route, body, cred)
		if code != http.StatusForbidden || !strings.Contains(string(b), want) {
			t.Errorf("%s: %d %s, want 403 naming %q", what, code, b, want)
		}
	}

	// The root token (bx on the host): a record, a second deployment, then
	// protect, unprotect and reassign — the manager gate passes it.
	must("the root token pauses live reload", "live-reload/pause", `{"tile":"`+tile+`"}`, owner)
	must("the root token adds dev", "add", `{"tile":"`+tile+`","deployment":"dev"}`, owner)
	must("the root token protects", "protect", `{"tile":"`+tile+`","on":true}`, owner)
	if s := read(owner); !s.ProtectedPrimary {
		t.Fatal("the root token's protect didn't protect")
	}
	must("the root token unprotects", "protect", `{"tile":"`+tile+`","on":false}`, owner)
	must("the root token reassigns to dev", "primary", `{"tile":"`+tile+`","deployment":"dev","confirm":"data-stays"}`, owner)
	if s := read(owner); s.Primary != "dev" {
		t.Fatalf("after the root token's reassignment the primary is %q", s.Primary)
	}
	must("the root token reassigns back", "primary", `{"tile":"`+tile+`","deployment":"main","confirm":"data-stays"}`, owner)

	carol := "Cookie: xbin_session=" + d.st.Auth.NewSession("carol", "127.0.0.1")
	bob := "Cookie: xbin_session=" + d.st.Auth.NewSession("bob", "127.0.0.1")
	carolFrame, bobFrame := frame(carol), frame(bob)

	// The admin tile's frame for the tile's owner: the full view, a manager.
	if s := read(carolFrame); s.View != "full" || !s.Caller.Manager || len(s.Deployments) != 2 {
		t.Errorf("the owner's admin frame reads %+v, want the full view as a manager", s)
	}
	if s := read(bobFrame); s.View != "reader" || s.Caller.Manager {
		t.Errorf("a non-manager's admin frame reads %+v, want the reader view", s)
	}

	// Refused: a person who doesn't manage the tile, through the frame.
	refused("a non-manager's admin frame protects", "protect", `{"tile":"`+tile+`","on":true}`, bobFrame,
		"is a tile manager's act: the tile's owner, its org's admins, or a workspace admin")
	// Refused: the admin tile's terminal and agent tokens, and the frames
	// they mint for the admin tile — the owner-driven one's included.
	session := "done in a person's own session: terminal, agent and tile credentials can't do it"
	for name, term := range map[string]string{
		"the owner's terminal on the admin tile":  "Bearer " + d.st.Auth.MintTerminal("tiles/admin", "carol"),
		"an owner-driven terminal or agent token": "Bearer " + d.st.Auth.MintTerminal("tiles/admin", ""),
	} {
		refused(name, "protect", `{"tile":"`+tile+`","on":true}`, term, session)
		refused("a frame minted by "+name, "protect", `{"tile":"`+tile+`","on":true}`, frame(term), session)
	}
	// Refused: an admin's view of the owner, through the admin tile.
	wsad := d.st.Auth.NewSession("wsad", "127.0.0.1")
	ar := httptest.NewRequest("GET", "/", nil)
	ar.AddCookie(&http.Cookie{Name: auth.CookieName, Value: wsad})
	admin, ok := d.st.Auth.FromRequest(ar)
	if !ok {
		t.Fatal("the admin's session")
	}
	tk, err := d.st.Auth.NewImpersonationTicket(admin, "carol")
	if err != nil {
		t.Fatal(err)
	}
	view, err := d.st.Auth.RedeemImpersonation(tk, admin, wsad, "127.0.0.1")
	if err != nil {
		t.Fatal(err)
	}
	if code, b := send("POST", "/api/xbin/deployments/protect", `{"tile":"`+tile+`","on":true}`, frame("Cookie: xbin_session="+view)); code != http.StatusForbidden || !strings.Contains(string(b), "read-only") {
		t.Errorf("an admin's view of the owner, through the admin tile: %d %s, want the read-only 403", code, b)
	}
	if s := read(owner); s.ProtectedPrimary {
		t.Fatal("a refused request protected the primary")
	}

	// The owner through the admin tile: protect, deliveries, unprotect,
	// reassign — the acts its deployments tab offers.
	must("the owner's admin frame protects", "protect", `{"tile":"`+tile+`","on":true}`, carolFrame)
	must("the owner's admin frame turns on dev's deliveries", "deliveries", `{"tile":"`+tile+`","deployment":"dev","on":true}`, carolFrame)
	s := read(carolFrame)
	if !s.ProtectedPrimary || len(s.Deployments) != 2 || !s.Deployments[1].Deliveries {
		t.Fatalf("after the admin frame's acts: %+v", s)
	}
	must("the owner's admin frame unprotects", "protect", `{"tile":"`+tile+`","on":false}`, carolFrame)
	must("the owner's admin frame reassigns to dev", "primary", `{"tile":"`+tile+`","deployment":"dev","confirm":"data-stays"}`, carolFrame)
	if s := read(owner); s.Primary != "dev" || s.ProtectedPrimary {
		t.Errorf("after the admin frame's reassignment: primary %q, protected %v", s.Primary, s.ProtectedPrimary)
	}
	// Still refused through the frame: a manager act the admin tile doesn't do.
	refused("the owner's admin frame sets an edge policy", "edge", `{"tile":"`+tile+`","edge":"slot:net","policy":"block"}`, carolFrame, session)
}
