package broker

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/registry"
)

// covers PD-18 S20 — POST /partitions/limits (plans/partitions 06 §6): an
// admin sets the workspace's cap and a tile's cap and per-partition
// ceiling; a tile manager, with their own session, only lowers their own
// tile's; everyone else — the tile's own code, a manager's terminal, a
// member — is refused; the runner reads the result through PartitionCaps
// and the data plane through PartitionBytes.
func TestPartitionLimitsAPI(t *testing.T) {
	b, st := orgFixture(t) // apps/email is owned by org sales: carol is its admin
	b.SetPartitionCapDefaults(func() (int, int) { return 6, 12 })
	if err := b.Reg.MutateWorkspace(func(ws *registry.WorkspaceManifest) {
		ws.Grants = append(ws.Grants, registry.Grant{From: "apps/calendar", Target: "xbin", Role: "admin"})
	}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(b.Reg.Root, "data", "partition-limits.json")
	root := auth.Principal{Owner: true, Via: "bearer"}
	carol, bob := principalFor(t, st, "carol"), principalFor(t, st, "bob")
	post := func(p auth.Principal, body string) (int, map[string]any) {
		t.Helper()
		w := call(t, b.apiPartitionLimits, p, "POST", "/partitions/limits", body, nil)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	caps := func(want0, want1 int) {
		t.Helper()
		if a, w := b.PartitionCaps("apps/email"); a != want0 || w != want1 {
			t.Errorf("PartitionCaps = %d/%d, want %d/%d", a, w, want0, want1)
		}
	}

	caps(0, 0)
	if _, err := os.Stat(file); err == nil {
		t.Fatal("a limits file before anyone set a limit")
	}
	for _, body := range []string{``, `{}`, `{"tile":"apps/email"}`, `{"maxRunning":-1}`, `{"maxRunning":5000}`,
		`{"tile":"apps/email","partitionBytes":10}`, `{"partitionBytes":1073741824}`} {
		if code, _ := post(root, body); code != 400 {
			t.Errorf("%s: %d, want 400", body, code)
		}
	}
	if code, _ := post(root, `{"tile":"apps/nope","maxRunning":3}`); code != 404 {
		t.Errorf("an unknown tile: %d", code)
	}
	if code, _ := post(bob, `{"tile":"apps/nope","maxRunning":3}`); code != 403 {
		t.Errorf("a person asking about an unknown tile: %d, want 403 (no existence probe)", code)
	}

	code, out := post(root, `{"tile":"apps/email","maxRunning":8,"partitionBytes":1073741824}`)
	lim, _ := out["limits"].(map[string]any)
	if code != 200 || lim["maxRunning"] != 8.0 || lim["partitionBytes"] != float64(1<<30) {
		t.Fatalf("the admin's tile limits: %d %v", code, out)
	}
	caps(8, 0)
	if code, out := post(root, `{"maxRunning":20}`); code != 200 || out["workspace"].(map[string]any)["maxRunning"] != 20.0 {
		t.Errorf("the workspace cap: %d %v", code, out)
	}
	caps(8, 20)

	if code, out := post(carol, `{"tile":"apps/email","maxRunning":4,"partitionBytes":536870912}`); code != 200 ||
		out["limits"].(map[string]any)["maxRunning"] != 4.0 {
		t.Errorf("the manager lowers: %d %v", code, out)
	}
	caps(4, 20)
	if b.PartitionBytes("apps/email") != 512<<20 {
		t.Errorf("PartitionBytes = %d", b.PartitionBytes("apps/email"))
	}
	for name, body := range map[string]string{
		"raises the cap":     `{"tile":"apps/email","maxRunning":9}`,
		"raises the ceiling": `{"tile":"apps/email","partitionBytes":2147483648}`,
		"the workspace":      `{"maxRunning":2}`,
		"another tile":       `{"tile":"apps/calendar","maxRunning":1}`,
	} {
		if code, _ := post(carol, body); code != 403 {
			t.Errorf("the manager %s: %d, want 403", name, code)
		}
	}
	if code, _ := post(carol, `{"tile":"apps/email","maxRunning":0}`); code != 200 {
		t.Errorf("the manager clears her lowering: %d", code)
	}
	caps(8, 20)

	for name, p := range map[string]auth.Principal{
		"a member":                bob,
		"the manager's terminal":  {Component: "apps/notes", Via: "terminal", UserID: "carol", User: carol.User, Access: carol.Access},
		"the tile's own frame":    {Component: "apps/email", Via: "frame", UserID: "carol"},
		"the tile's own instance": {Component: "apps/email", Via: "instance"},
		"a cron delivery":         {Component: CronPrincipal, Via: "cron", Role: "writer"},
		"nobody":                  {},
	} {
		if code, _ := post(p, `{"tile":"apps/email","maxRunning":1}`); code != 403 {
			t.Errorf("%s: %d, want 403", name, code)
		}
	}
	adminTile := auth.Principal{Component: "apps/calendar", Via: "frame", UserID: "bob"}
	if code, _ := post(adminTile, `{"tile":"apps/email","maxRunning":7}`); code != 200 {
		t.Errorf("the admin tile (xbin:admin) on another tile: %d", code)
	}
	if code, _ := post(adminTile, `{"tile":"apps/calendar","maxRunning":7}`); code != 403 {
		t.Errorf("a tile's own code setting its own limits: %d", code)
	}
	var doc limitsDoc
	if bs, err := os.ReadFile(file); err != nil || json.Unmarshal(bs, &doc) != nil || doc.Schema != 1 || doc.Tiles["apps/email"].MaxRunning != 7 {
		t.Errorf("the file: %+v %v", doc, err)
	}
	if fi, err := os.Stat(file); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("the file's mode: %v", err)
	}

	t.Run("a file xbind can't read", func(t *testing.T) {
		b, _ := orgFixture(t)
		file := filepath.Join(b.Reg.Root, "data", "partition-limits.json")
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("{nope"), 0o600); err != nil {
			t.Fatal(err)
		}
		if a, w := b.PartitionCaps("apps/email"); a != 0 || w != 0 {
			t.Errorf("an unreadable file's caps: %d/%d, want the defaults", a, w)
		}
		if w := call(t, b.apiPartitionLimits, auth.Principal{Owner: true}, "POST", "/partitions/limits", `{"maxRunning":3}`, nil); w.Code != 500 {
			t.Errorf("POST over an unreadable file: %d", w.Code)
		}
		if bs, _ := os.ReadFile(file); string(bs) != "{nope" {
			t.Errorf("the unreadable file was overwritten: %q", bs)
		}
	})
}
