package server

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers D240 — an admin lists outside servers with PUT /delegate/workspace:
// the secret is write-only (no read, not the PUT's own answer, says it),
// the orgs must exist, the workspace's own address is refused, and without
// a vault to keep a secret in the write is refused, nothing changed
func TestMCPServersPolicyWrite(t *testing.T) {
	w := newWebRig(t)
	put := `{"mcp":{"mode":"listed","servers":[{"id":"gh","name":"GitHub","url":"https://mcp.example.com/mcp","auth":"bearer","secret":"ghp-SECRET-VALUE"}]}}`
	if rec := w.policy(put); rec.Code != 409 || refusalOf(rec) != "vault" {
		t.Fatalf("no vault: %d %s", rec.Code, rec.Body.String())
	}
	if len(w.store.MCPServers()) != 0 {
		t.Fatal("a refused write changed the list")
	}
	w.store.SetSecrets(filepath.Join(t.TempDir(), "mcp-secrets.json"), nil, true)
	rec := w.policy(put)
	if rec.Code != 200 || strings.Contains(rec.Body.String(), "SECRET") || !strings.Contains(rec.Body.String(), `"hasSecret":true`) {
		t.Fatalf("set: %d %s", rec.Code, rec.Body.String())
	}
	get := w.do(w.full, "GET", "/api/xbin/delegate/workspace", "", w.cookie("alice"))
	if strings.Contains(get.Body.String(), "SECRET") || !strings.Contains(get.Body.String(), `"server":"ext:gh"`) || !strings.Contains(get.Body.String(), `"mode":"listed"`) {
		t.Fatalf("read: %s", get.Body.String())
	}
	// a card sends back what it read: the read-only members are taken, the secret kept
	var view struct {
		MCP struct {
			Servers []map[string]any `json:"servers"`
		} `json:"mcp"`
	}
	_ = json.Unmarshal(get.Body.Bytes(), &view)
	back, _ := json.Marshal(map[string]any{"mcp": map[string]any{"servers": view.MCP.Servers}})
	if rec := w.policy(string(back)); rec.Code != 200 {
		t.Fatalf("a read sent back: %d %s", rec.Code, rec.Body.String())
	}
	if m, _ := w.store.MCPServerNamed("ext:gh"); !m.HasSecret {
		t.Error("the secret went with a round trip")
	}
	for what, body := range map[string]string{
		"an unknown org":      `{"mcp":{"servers":[{"id":"a","name":"A","url":"https://a.example.com/mcp","orgs":["nope"]}]}}`,
		"the workspace's own": `{"mcp":{"servers":[{"id":"a","name":"A","url":"https://ws.example.com/api/apps/dice/mcp"}]}}`,
		"http, unmarked":      `{"mcp":{"servers":[{"id":"a","name":"A","url":"http://a.example.com/mcp"}]}}`,
		"a host's secretFile": `{"mcp":{"servers":[{"id":"a","name":"A","url":"https://a.example.com/mcp","secretFile":"/etc/shadow"}]}}`,
		"a person, not admin": "",
	} {
		var rec = w.policy(body)
		if body == "" {
			rec = w.do(w.full, "PUT", "/api/xbin/delegate/workspace", `{"mcp":{"servers":[]}}`, w.cookie("bob"))
			if rec.Code != 403 {
				t.Errorf("%s: %d", what, rec.Code)
			}
			continue
		}
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", what, rec.Code, rec.Body.String())
		}
	}
}

// covers D240 — a person's Preferences list the outside servers their agent
// may use; a server listed for orgs is seen by their members only
func TestMCPServersForPerson(t *testing.T) {
	w := newWebRig(t)
	w.store.SetSecrets(filepath.Join(t.TempDir(), "mcp-secrets.json"), nil, true)
	if _, err := w.s.Auth.Users.UpsertOrg(users.Org{ID: "finance", Name: "Finance", Members: []users.Member{{ID: "alice", Level: users.LevelRead}}}); err != nil {
		t.Fatal(err)
	}
	if rec := w.policy(`{"mcp":{"mode":"listed","servers":[{"id":"open","name":"Open data","url":"https://open.example.com/mcp"},
		{"id":"ledger","name":"Ledger","url":"https://ledger.example.com/mcp","orgs":["finance"]}]}}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	state := func(user string) string {
		return w.do(w.full, "GET", "/api/xbin/delegate/state", "", w.cookie(user)).Body.String()
	}
	if s := state("bob"); !strings.Contains(s, `"server":"ext:open"`) || strings.Contains(s, "ledger") {
		t.Errorf("bob: %s", s)
	}
	if s := state("alice"); !strings.Contains(s, `"server":"ext:ledger"`) || !strings.Contains(s, `"host":"ledger.example.com"`) {
		t.Errorf("alice: %s", s)
	}
	// tiles only: no outside server is anyone's
	if rec := w.policy(`{"mcp":{"mode":"tiles"}}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if s := state("alice"); strings.Contains(s, "ext:") {
		t.Errorf("tiles mode: %s", s)
	}
}
