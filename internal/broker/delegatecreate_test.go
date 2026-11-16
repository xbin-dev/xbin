package broker

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/xbin-dev/xbin/internal/builtins"
	"github.com/xbin-dev/xbin/internal/delegation"
)

// planTemplates installs two builtin templates: "assistant" (an http slot
// "mcp" wanting the mcp service, an "llm" slot, partitioned) and "tool"
// (provides the mcp service).
func planTemplates(t *testing.T, w *csWorld) {
	t.Helper()
	set, err := builtins.LoadTemplates(fstest.MapFS{
		"assistant/xbin.json": {Data: []byte(`{"runtime":"go","template": {"title": "AI Agent", "defaultName": "assistant"},
			"interfaces": {"mcp": {"kind": "http", "service": "mcp", "multi": true}, "llm": {"kind": "http", "service": "openai", "multi": true, "required": true}, "net": {"kind": "net"}}}`)},
		"assistant/backend/main.go": {Data: []byte("package main\n")},
		"tool/xbin.json": {Data: []byte(`{"runtime":"go","template": {"title": "MCP server", "defaultName": "tool"},
			"provides": {"mcp": {"kind": "http", "service": "mcp", "role": "reader"}}}`)},
		"tool/backend/main.go": {Data: []byte("package main\n")},
		// an empty tile: what a tool's tile is before its coding agent wrote it
		"blank/xbin.json":  {Data: []byte(`{"template": {"title": "Starter", "defaultName": "blank"}}`)},
		"blank/index.html": {Data: []byte("<!doctype html>\n")},
	})
	if err != nil {
		t.Fatal(err)
	}
	w.b.SetBuiltinTemplates(set)
}

func dryRun(r *http.Request) { r.Header.Set("X-XBin-Dry-Run", "1") }

func both(fs ...func(*http.Request)) func(*http.Request) {
	return func(r *http.Request) {
		for _, f := range fs {
			f(r)
		}
	}
}

// G1 (plans/wsagent/journey.md): an admin's agent creates a tile like
// anyone's — the tile is its person's, as the sheet says — and what the
// dry run and the request's preview say is what the approved call does:
// all three run the handler's own check (templateNewCheck).
func TestDelegateCreatesForAnAdmin(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	_, alice := w.session("alice")

	for _, c := range []struct {
		user string
		cred func(*http.Request)
		path string
	}{{"bob", bob, "apps/dice"}, {"alice", alice, "apps/dice2"}} {
		call := `{"source":"tool","path":"` + c.path + `"}`
		code, body := w.do("POST", "/templates/new", call, both(agentOf(c.user), dryRun))
		var dry struct {
			Allowed       bool
			NeedsApproval bool
			Refusal       string
			Create        map[string]any
		}
		if json.Unmarshal([]byte(body), &dry); code != 200 || !dry.Allowed || !dry.NeedsApproval || dry.Create["owner"] != "user:"+c.user || dry.Create["path"] != c.path {
			t.Fatalf("%s's agent's dry run of a creation: %d %s", c.user, code, body)
		}
		ask, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/templates/new", "body": call})
		code, body = w.do("POST", "/delegate/requests", string(ask), agentOf(c.user))
		var req delegation.Request
		if json.Unmarshal([]byte(body), &req); code != 200 || req.Kind != delegation.KindTemplate {
			t.Fatalf("%s's agent's request: %d %s", c.user, code, body)
		}
		if create, _ := req.Preview["create"].(map[string]any); create["owner"] != "user:"+c.user || create["template"] != "tool" {
			t.Fatalf("the request's preview says %v", req.Preview)
		}
		if code, body := w.do("POST", "/delegate/requests/"+req.ID+"/approve", `{}`, c.cred); code != 200 {
			t.Fatalf("approve: %d %s", code, body)
		}
		code, body = w.do("POST", "/templates/new", call, both(agentOf(c.user), func(r *http.Request) { r.Header.Set("X-XBin-Ticket", req.ID) }))
		if code != 200 {
			t.Fatalf("%s's agent's approved creation is refused: %d %s", c.user, code, body)
		}
		if got := w.b.Users.Owner(c.path); got != "user:"+c.user {
			t.Fatalf("%s is owned by %q, want user:%s", c.path, got, c.user)
		}
	}

	// what the real call refuses, the dry run refuses with the same words —
	// and no request is put in front of the person
	for _, bad := range []string{
		`{"source":"tool","path":"tiles/zz"}`,                    // reserved for built-in tiles
		`{"source":"tool","path":"apps/dice"}`,                   // exists now
		`{"source":"tool","path":"apps/n","owner":"user:carol"}`, // another person's
		`{"source":"nope","path":"apps/n"}`,
	} {
		code, body := w.do("POST", "/templates/new", bad, both(agentOf("bob"), dryRun))
		var dry struct {
			Allowed bool
			Refusal string
		}
		if json.Unmarshal([]byte(body), &dry); code != 200 || dry.Allowed || dry.Refusal == "" {
			t.Errorf("the dry run of %s: %d %s", bad, code, body)
		}
		ask, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/templates/new", "body": bad})
		code, body = w.do("POST", "/delegate/requests", string(ask), agentOf("bob"))
		if code != 403 || !strings.Contains(body, dry.Refusal) {
			t.Errorf("a request for %s: %d %s (the dry run said %q)", bad, code, body, dry.Refusal)
		}
	}

	// an admin's own hands still make a workspace-owned tile by default
	if code, body := w.do("POST", "/templates/new", `{"source":"tool","path":"apps/byhand"}`, bob); code != 200 {
		t.Fatalf("bob, by hand: %d %s", code, body)
	}
	if got := w.b.Users.Owner("apps/byhand"); got != "" {
		t.Fatalf("an admin's own creation is owned by %q, want the workspace", got)
	}
}
