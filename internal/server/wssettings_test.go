package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/wssettings"
)

// GET /workspace-settings is for every signed-in principal, PUT for
// admins (D175, D180): base auto-update defaults to on, the partitioned
// tiles' switches to off; a PUT persists and keeps the file's other keys, a
// bad body is 400, and /ws/term/env says what the terminals apply.
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

	if c, b := do(bob, "GET", api, ""); c != 200 || b != `{"baseAutoUpdate":true,"credentialResetConfirm":false,"partitionConsent":false}` {
		t.Fatalf("a fresh workspace: %d %s", c, b)
	}
	if c, b := do(alice, "GET", "/ws/term/env?cwd=apps/x", ""); c != 200 || !strings.Contains(b, `"baseAutoUpdate":true`) {
		t.Fatalf("/ws/term/env: %d %s", c, b)
	}
	for _, body := range []string{`{"baseAutoUpdate":false}`, `{"partitionConsent":true}`} {
		if c, _ := do(bob, "PUT", api, body); c != 403 {
			t.Fatalf("a non-admin PUT %s: %d", body, c)
		}
	}
	if err := os.WriteFile(file, []byte(`{"newerKey":{"x":1}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{`{}`, `{"baseAutoUpdate":"no"}`, `{"newerKey":1}`, `{"schema":1}`, `nope`} {
		if c, b := do(alice, "PUT", api, bad); c != 400 {
			t.Fatalf("PUT %s: %d %s", bad, c, b)
		}
	}
	ch, cancel := s.Hub.Subscribe(func(e events.Event) bool { return e.Type == "workspace-settings" || e.Type == "policies" })
	defer cancel()
	if c, b := do(alice, "PUT", api, `{"baseAutoUpdate":false}`); c != 200 || b != `{"baseAutoUpdate":false,"credentialResetConfirm":false,"partitionConsent":false}` {
		t.Fatalf("PUT off: %d %s", c, b)
	}
	// published before the answer: open terminal windows re-read /ws/term/env
	if got := drain(ch); len(got) != 1 || got[0].Type != "workspace-settings" || mustJSONString(got[0].Data) != `{"baseAutoUpdate":false,"changed":["baseAutoUpdate"]}` {
		t.Fatalf("the events of a base auto-update write: %+v", got)
	}
	if raw, _ := os.ReadFile(file); !strings.Contains(string(raw), `"newerKey"`) || !strings.Contains(string(raw), `"baseAutoUpdate": false`) {
		t.Fatalf("the file after the PUT: %s", raw)
	}
	if c, b := do(bob, "GET", api, ""); c != 200 || !strings.Contains(b, `"baseAutoUpdate":false`) {
		t.Fatalf("GET after off: %d %s", c, b)
	}
	if c, b := do(alice, "GET", "/ws/term/env?cwd=apps/x", ""); c != 200 || !strings.Contains(b, `"baseAutoUpdate":false`) {
		t.Fatalf("/ws/term/env after off: %d %s", c, b)
	}
	// a partitioned tiles' switch: the same route, and the `policies` event
	// clients older than D180 follow; the event carries no switch's value
	if c, b := do(alice, "PUT", api, `{"partitionConsent":true}`); c != 200 || !strings.Contains(b, `"partitionConsent":true`) {
		t.Fatalf("PUT partitionConsent: %d %s", c, b)
	}
	if got := drain(ch); len(got) != 2 || got[0].Type != "workspace-settings" || got[1].Type != "policies" ||
		mustJSONString(got[0].Data) != `{"baseAutoUpdate":false,"changed":["partitionConsent"]}` || got[1].Data != nil {
		t.Fatalf("the events of a partitioned tiles' write: %+v", got)
	}
	if c, b := do(bob, "GET", "/api/xbin/workspace-policies", ""); c != 200 || b != `{"credentialResetConfirm":false,"partitionConsent":true,"schema":1}` {
		t.Fatalf("the alias after the unified PUT: %d %s", c, b)
	}
	// An unreadable file: base auto-update off with the reason, each switch
	// on (fail closed after a value was read: its last value); a PUT doesn't
	// overwrite it.
	if err := os.WriteFile(file, []byte(`{broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	c, b := do(bob, "GET", api, "")
	var view map[string]any
	_ = json.Unmarshal([]byte(b), &view)
	errs, _ := view["errors"].(map[string]any)
	if c != 200 || view["baseAutoUpdate"] != false || view["partitionConsent"] != true || view["credentialResetConfirm"] != false ||
		!strings.Contains(view["error"].(string), "data/workspace-settings.json") || len(errs) != 3 ||
		!strings.Contains(errs["partitionConsent"].(string), "an admin must fix") {
		t.Fatalf("GET of a broken file as a person: %d %s", c, b)
	}
	if c, b := do(alice, "GET", api, ""); c != 200 || !strings.Contains(b, `"partitionConsent":"data/workspace-settings.json isn't a JSON object`) {
		t.Fatalf("GET of a broken file as an admin: %d %s", c, b)
	}
	for _, path := range []string{api, "/api/xbin/workspace-policies"} {
		if c, _ := do(alice, "PUT", path, `{"partitionConsent":true}`); c != 500 {
			t.Fatalf("PUT %s over a broken file: %d", path, c)
		}
	}
	if raw, _ := os.ReadFile(file); string(raw) != `{broken` {
		t.Fatalf("the broken file was rewritten: %s", raw)
	}
}

// adminTilePolicy: the admin tile (apps/admin) holds xbin:admin, as the
// broker's IsAdmin answers for a grant.
type adminTilePolicy struct{ NoopPolicy }

func (adminTilePolicy) IsAdmin(p auth.Principal) bool { return p.Component == "apps/admin" }

func callAs(h http.HandlerFunc, p auth.Principal, method, url, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, url, strings.NewReader(body))
	r = r.WithContext(auth.WithPrincipal(r.Context(), p))
	w := httptest.NewRecorder()
	h(w, r)
	return w
}

// covers PD-55, D180 — who reads and writes what: the terminals' setting
// is every signed-in principal's to read; the partitioned tiles' switches
// admins' and people's (their session or device, or a terminal or agent
// session they drive) — tile code sees no key of them, and the alias
// refuses it; every write is an admin's, on either route.
func TestWorkspaceSettingsReaders(t *testing.T) {
	s := &Server{Pol: adminTilePolicy{}, Hub: events.NewHub(),
		Settings: wssettings.New(filepath.Join(t.TempDir(), "workspace-settings.json"))}
	bob := auth.Principal{UserID: "bob", Via: "session"}
	bobTerm := auth.Principal{Component: "apps/email", UserID: "bob", Via: "terminal"}
	bobAgent := bobTerm
	bobAgent.Deployment = "dev"
	for _, tc := range []struct {
		name          string
		p             auth.Principal
		partitions    bool // the unified GET shows the partitioned tiles' switches
		aliasGet, put int
	}{
		{"the root token", auth.Principal{Owner: true, Via: "bearer"}, true, 200, 200},
		{"the admin tile (xbin:admin)", auth.Principal{Component: "apps/admin", Via: "frame", UserID: "bob"}, true, 200, 200},
		{"a person", bob, true, 200, 403},
		{"a person's terminal", bobTerm, true, 200, 403},
		{"a person's agent session on a deployment", bobAgent, true, 200, 403},
		{"a tile frame", auth.Principal{Component: "apps/email", Via: "frame", UserID: "bob"}, false, 403, 403},
		{"a tile instance", auth.Principal{Component: "apps/email", Via: "instance"}, false, 403, 403},
		{"a cron delivery", auth.Principal{Component: "xbin/cron", Via: "cron", Role: "writer"}, false, 403, 403},
	} {
		w := callAs(s.apiWorkspaceSettingsGet, tc.p, "GET", "/workspace-settings", "")
		var view map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &view)
		_, sees := view["partitionConsent"]
		_, sees2 := view["credentialResetConfirm"]
		if w.Code != 200 || view["baseAutoUpdate"] != true || sees != tc.partitions || sees2 != tc.partitions {
			t.Errorf("%s: GET /workspace-settings = %d %s", tc.name, w.Code, w.Body)
		}
		if w := callAs(s.apiWorkspacePoliciesGet, tc.p, "GET", "/workspace-policies", ""); w.Code != tc.aliasGet {
			t.Errorf("%s: GET /workspace-policies = %d, want %d: %s", tc.name, w.Code, tc.aliasGet, w.Body)
		}
		if w := callAs(s.apiWorkspaceSettingsPut, tc.p, "PUT", "/workspace-settings", `{"credentialResetConfirm":false}`); w.Code != tc.put {
			t.Errorf("%s: PUT /workspace-settings = %d, want %d: %s", tc.name, w.Code, tc.put, w.Body)
		}
		if w := callAs(s.apiWorkspaceSettingsPut, tc.p, "PUT", "/workspace-settings", `{"baseAutoUpdate":true}`); w.Code != tc.put {
			t.Errorf("%s: PUT baseAutoUpdate = %d, want %d: %s", tc.name, w.Code, tc.put, w.Body)
		}
		if w := callAs(s.apiWorkspacePoliciesPut, tc.p, "PUT", "/workspace-policies", `{"credentialResetConfirm":false}`); w.Code != tc.put {
			t.Errorf("%s: PUT /workspace-policies = %d, want %d: %s", tc.name, w.Code, tc.put, w.Body)
		}
	}
}

// covers PD-55, D180 — GET/PUT /workspace-policies, v0.3.66's routes, are
// an alias over the settings: the same values both ways, v0.3.66's shape
// and body rules (baseAutoUpdate is 400 there), the `policies` event and
// the audit line with each setting's old→new.
func TestWorkspacePoliciesAlias(t *testing.T) {
	s := &Server{Pol: adminTilePolicy{}, Hub: events.NewHub(),
		Settings: wssettings.New(filepath.Join(t.TempDir(), "workspace-settings.json"))}
	owner := auth.Principal{Owner: true, Via: "bearer"}
	bob := auth.Principal{UserID: "bob", Via: "session"}
	if w := callAs(s.apiWorkspacePoliciesGet, bob, "GET", "/workspace-policies", ""); strings.TrimSpace(w.Body.String()) != `{"credentialResetConfirm":false,"partitionConsent":false,"schema":1}` {
		t.Fatalf("the defaults: %d %s", w.Code, w.Body)
	}
	ch, cancel := s.Hub.Subscribe(func(e events.Event) bool { return e.Type == "workspace-settings" || e.Type == "policies" })
	defer cancel()
	logs := captureSlog(t)
	w := callAs(s.apiWorkspacePoliciesPut, owner, "PUT", "/workspace-policies", `{"partitionConsent":true}`)
	if w.Code != 200 || strings.TrimSpace(w.Body.String()) != `{"credentialResetConfirm":false,"partitionConsent":true,"schema":1}` {
		t.Fatalf("PUT partitionConsent = %d %s", w.Code, w.Body)
	}
	// the audit line says which switch changed, and to what
	if l := logs.String(); !strings.Contains(l, "msg=audit who=owner method=PUT path=/workspace-policies") || !strings.Contains(l, "partitionConsent=false→true") ||
		!strings.Contains(l, "credentialResetConfirm=false→false") || !strings.Contains(l, "baseAutoUpdate=true→true") {
		t.Errorf("the PUT's audit line: %s", l)
	}
	if got := drain(ch); len(got) != 2 || got[0].Type != "workspace-settings" || got[1].Type != "policies" {
		t.Fatalf("the alias PUT's events: %+v", got)
	}
	// partial: an absent key is left alone; the unified GET reads it
	callAs(s.apiWorkspacePoliciesPut, owner, "PUT", "/workspace-policies", `{"credentialResetConfirm":true}`)
	if w := callAs(s.apiWorkspaceSettingsGet, bob, "GET", "/workspace-settings", ""); strings.TrimSpace(w.Body.String()) != `{"baseAutoUpdate":true,"credentialResetConfirm":true,"partitionConsent":true}` {
		t.Fatalf("the unified GET after two alias PUTs: %s", w.Body)
	}
	for _, body := range []string{`{}`, `{"partitionConsent":"yes"}`, `{"partitionConsent":true,"other":1}`, `{"schema":1}`, `{"baseAutoUpdate":false}`, `nope`} {
		if w := callAs(s.apiWorkspacePoliciesPut, owner, "PUT", "/workspace-policies", body); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s = %d, want 400", body, w.Code)
		}
	}
	if st, _ := s.Settings.Load(); st != (wssettings.Settings{BaseAutoUpdate: true, PartitionConsent: true, CredentialResetConfirm: true}) {
		t.Fatalf("a refused PUT changed the settings: %+v", st)
	}
	// a switch that can't be read: the alias answers 500 — the reason for
	// admins, a generic line for people, never the host path
	file := s.Settings.Path()
	if err := os.WriteFile(file, []byte(`{"partitionConsent":"no","credentialResetConfirm":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := callAs(s.apiWorkspacePoliciesGet, owner, "GET", "/workspace-policies", ""); w.Code != 500 ||
		!strings.Contains(w.Body.String(), `partitionConsent is \"no\"`) || strings.Contains(w.Body.String(), filepath.Dir(file)) {
		t.Errorf("an admin's GET = %d %s", w.Code, w.Body)
	}
	if w := callAs(s.apiWorkspacePoliciesGet, bob, "GET", "/workspace-policies", ""); w.Code != 500 || !strings.Contains(w.Body.String(), "an admin must fix") {
		t.Errorf("a person's GET = %d %s", w.Code, w.Body)
	}
	// base auto-update alone can't be read: the alias still answers
	if err := os.WriteFile(file, []byte(`{"baseAutoUpdate":"no","partitionConsent":false,"credentialResetConfirm":true}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if w := callAs(s.apiWorkspacePoliciesGet, bob, "GET", "/workspace-policies", ""); w.Code != 200 || !strings.Contains(w.Body.String(), `"credentialResetConfirm":true`) {
		t.Errorf("the alias with base auto-update unreadable = %d %s", w.Code, w.Body)
	}
}

func drain(ch <-chan events.Event) (out []events.Event) {
	for {
		select {
		case e := <-ch:
			out = append(out, e)
		default:
			return out
		}
	}
}

func mustJSONString(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

type syncBuf struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureSlog sends slog's default logger to a buffer for the test's life.
func captureSlog(t *testing.T) *syncBuf {
	t.Helper()
	buf, old := &syncBuf{}, slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	return buf
}
