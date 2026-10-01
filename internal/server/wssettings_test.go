package server

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/wssettings"
)

// GET /workspace-settings is for every signed-in principal, PUT for
// admins (D174): base auto-update defaults to on, a PUT persists and keeps
// the file's other keys, a bad body is 400, and /ws/term/env says what the
// terminals apply.
func TestWorkspaceSettingsRoutes(t *testing.T) {
	h, s := termServer(t) // alice an admin, bob a user without tiles
	file := filepath.Join(t.TempDir(), "workspace-settings.json")
	s.Settings = wssettings.New(file)
	s.Term.BaseAutoUpdate = s.Settings.BaseAutoUpdate
	alice := s.Auth.NewSession("alice", "")
	bob := s.Auth.NewSession("bob", "")
	do := func(sid, method, path, body string) (int, string) {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, withCookie(method, path, body, sid))
		return w.Code, strings.TrimSpace(w.Body.String())
	}
	const api = "/api/xbin/workspace-settings"

	if c, b := do(bob, "GET", api, ""); c != 200 || b != `{"baseAutoUpdate":true}` {
		t.Fatalf("a fresh workspace: %d %s", c, b)
	}
	if c, b := do(alice, "GET", "/ws/term/env?cwd=apps/x", ""); c != 200 || !strings.Contains(b, `"baseAutoUpdate":true`) {
		t.Fatalf("/ws/term/env: %d %s", c, b)
	}
	if c, _ := do(bob, "PUT", api, `{"baseAutoUpdate":false}`); c != 403 {
		t.Fatalf("a non-admin PUT: %d", c)
	}
	if err := os.WriteFile(file, []byte(`{"newerKey":{"x":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"baseAutoUpdate":"no"}`, `{"newerKey":1}`, `nope`} {
		if c, b := do(alice, "PUT", api, bad); c != 400 {
			t.Fatalf("PUT %s: %d %s", bad, c, b)
		}
	}
	ch, cancel := s.Hub.Subscribe(func(e events.Event) bool { return e.Type == "workspace-settings" })
	defer cancel()
	if c, b := do(alice, "PUT", api, `{"baseAutoUpdate":false}`); c != 200 || b != `{"baseAutoUpdate":false}` {
		t.Fatalf("PUT off: %d %s", c, b)
	}
	select { // published before the answer: open terminal windows re-read /ws/term/env
	case e := <-ch:
		if v, _ := e.Data.(map[string]any); v["baseAutoUpdate"] != false {
			t.Fatalf("the event: %+v", e)
		}
	default:
		t.Fatal("no workspace-settings event on the hub")
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), `"newerKey"`) || !strings.Contains(string(raw), `"baseAutoUpdate": false`) {
		t.Fatalf("the file after the PUT: %s", raw)
	}
	if c, b := do(bob, "GET", api, ""); c != 200 || b != `{"baseAutoUpdate":false}` {
		t.Fatalf("GET after off: %d %s", c, b)
	}
	if c, b := do(alice, "GET", "/ws/term/env?cwd=apps/x", ""); c != 200 || !strings.Contains(b, `"baseAutoUpdate":false`) {
		t.Fatalf("/ws/term/env after off: %d %s", c, b)
	}
	// An unreadable file: off, with the reason; a PUT doesn't overwrite it.
	if err := os.WriteFile(file, []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	if c, b := do(bob, "GET", api, ""); c != 200 || !strings.Contains(b, `"baseAutoUpdate":false`) || !strings.Contains(b, `"error"`) {
		t.Fatalf("GET of a broken file: %d %s", c, b)
	}
	if c, _ := do(alice, "PUT", api, `{"baseAutoUpdate":true}`); c != 500 {
		t.Fatalf("PUT over a broken file: %d", c)
	}
	if raw, _ := os.ReadFile(file); string(raw) != `{broken` {
		t.Fatalf("the broken file was rewritten: %s", raw)
	}
}
