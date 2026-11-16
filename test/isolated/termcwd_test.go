//go:build linux && integration

package isolated

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// A terminal opens on a tile: its directory is a registered component, or
// inside one. It was any workspace path the caller had terminal level on —
// and an admin has that everywhere — and it is bound read-write over the
// masks that hide xbind's own trees. Under --isolate an admin's shell with
// cwd=.xbin could not read the owner token or the frame-token secret, but
// could replace both; with cwd=data it could rewrite data/users.json and
// move the vault away; with cwd=homes write into anyone's home.
func TestTerminalCwdIsARegisteredTile(t *testing.T) {
	a := xbindtest.Require(t)
	d := xbindtest.Start(t, a, xbindtest.Options{Auth: true})
	d.AddUser(t, "carol", "pw-carol-5c2e81", "admin", nil)
	carol := xbindtest.H("Authorization", "Bearer "+d.Login(t, "carol", "pw-carol-5c2e81"))
	read := func(rel string) string {
		b, _ := os.ReadFile(filepath.Join(d.WS, filepath.FromSlash(rel)))
		return string(b)
	}
	token, users := read(".xbin/token"), read("data/users.json")
	if token == "" || users == "" {
		t.Fatal("the workspace has no owner token or users file to protect")
	}
	for _, cwd := range []string{".xbin", "data", "homes", "homes/admin", ".xbin/run", "data/resources-enc", "apps/../.xbin", "/.xbin/", "./data", "vendor", "apps/nosuch", "apps"} {
		for _, who := range [][]xbindtest.Header{{carol}, nil} { // an admin, and the root token
			conn, r, err := d.Dial(t, "/ws/term?cwd="+url.QueryEscape(cwd), who...)
			if err == nil {
				conn.Close()
				t.Errorf("a terminal opened with cwd=%s", cwd)
				continue
			}
			if r.Status != 403 {
				t.Errorf("cwd=%s: %d %s (%v), want 403", cwd, r.Status, r, err)
			}
			if r := d.Call(t, "POST", "/api/xbin/term/sessions", map[string]any{"cwd": cwd, "kind": "agent", "provider": "claude"}, who...); r.Status != 403 {
				t.Errorf("an agent session with cwd=%s: %d %s, want 403", cwd, r.Status, r)
			}
		}
	}
	// a tile's terminal is as before — for the admin too — with the masks
	// intact: nothing of xbind's own is read or written from it
	s := w2OpenTerm(t, d, "apps/welcome", carol)
	out, _ := s.run(t, "cat "+d.WS+"/.xbin/token 2>&1 | head -c 200; "+
		"(echo planted > "+d.WS+"/.xbin/token) 2>&1; (echo x >> "+d.WS+"/data/users.json) 2>&1; "+
		"(echo x > "+d.WS+"/homes/admin/planted) 2>&1; touch "+d.WS+"/apps/welcome/made-here && echo wrote-own-tile", time.Minute)
	if strings.Contains(out, strings.TrimSpace(token)) {
		t.Errorf("a tile terminal reads the owner token: %s", out)
	}
	if !strings.Contains(out, "wrote-own-tile") {
		t.Errorf("a tile terminal can't write its own tile: %s", out)
	}
	if read(".xbin/token") != token || read("data/users.json") != users {
		t.Errorf("a tile terminal changed xbind's own files: %s", out)
	}
	if _, err := os.Stat(filepath.Join(d.WS, "homes", "admin", "planted")); err == nil {
		t.Error("a tile terminal wrote into another person's home")
	}
}
