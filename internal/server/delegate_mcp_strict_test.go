package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/users"
)

// covers D239 — a body xbind judges for an MCP server is read one way only:
// a tile that reads keys case-sensitively and keeps the first of two equal
// keys never runs what the gate didn't judge (a tools/call dressed as a
// read), and what reaches it is the request xbind built from what it read
func TestMCPBodiesAreReadOneWay(t *testing.T) {
	w := newMCPRig(t)
	w.mcpOn(false)
	for _, bad := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wipe","arguments":{}},"Method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"METHOD":"tools/list","method":"tools/call","params":{"name":"wipe"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","method":"tools/list","params":{"name":"wipe"}}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{"cursor":"a","cursor":"b"}}`,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/list"}`,
		`{"Jsonrpc":"2.0","id":1,"method":"tools/list"}`,
		"{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\",\"params\":{\"x\":\"\xff\"}}",
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"} {"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"wipe"}}`,
		`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[]}`,
	} {
		if rec := w.rpc(bad); rec.Code != 403 || refusalOf(rec) != "mcp-call" {
			t.Errorf("%q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	w.mu.Lock()
	ran, sent := strings.Join(w.calls, ","), len(w.raw)
	w.mu.Unlock()
	if ran != "" || sent != 0 {
		t.Fatalf("the tile was sent %d of them and ran tools %q", sent, ran)
	}
	// a read reaches the tile as xbind wrote it, not as the agent did
	if rec := w.rpc(` { "method" : "tools/list", "params": {"cursor": "c1"}, "id": 7, "jsonrpc": "2.0" } `); rec.Code != 200 {
		t.Fatalf("a read: %d %s", rec.Code, rec.Body.String())
	}
	w.mu.Lock()
	got := w.raw
	w.mu.Unlock()
	if len(got) != 1 || got[0] != `{"id":7,"jsonrpc":"2.0","method":"tools/list","params":{"cursor":"c1"}}` {
		t.Errorf("what the tile was sent: %q", got)
	}
	// a tool's call: the same reading
	for _, bad := range []string{
		`{"server":"apps/dice","tool":"roll_dice","Tool":"wipe","arguments":{}}`,
		`{"server":"apps/dice","tool":"wipe","tool":"roll_dice","arguments":{}}`,
		`{"server":"apps/dice","tool":"roll_dice","arguments":{"sides":6,"sides":20}}`,
		`{"server":"apps/dice","tool":"roll_dice","arguments":{}}{"server":"apps/dice","tool":"wipe"}`,
		`{"Server":"apps/dice","tool":"roll_dice"}`,
	} {
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/mcp/call", bad, w.bob); rec.Code != 400 || refusalOf(rec) != "mcp-call" {
			t.Errorf("a tool's call %q: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
}

// covers D239 — the endpoint however the path spells it: a trailing slash,
// doubled slashes, "." and "..", percent-encoding or a query reach the same
// /mcp of the tile's backend (the proxy resolves them), so the gate and the
// requests decide on the resolved endpoint — a tools/call is never a tile
// write there, never filed as a call, and a call's standing approval made
// before the tile declared MCP never covers it
func TestMCPEndpointHoweverSpelled(t *testing.T) {
	w := newMCPRig(t)
	if _, err := w.s.Auth.Users.Upsert(users.User{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead, "apps/mail": users.LevelRead, "apps/dice": users.LevelWrite}}, ""); err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wipe","arguments":{}}}`
	spellings := []string{"/api/apps/dice/mcp/", "/api/apps/dice//mcp", "/api/apps/dice/./mcp", "/api/apps/dice/x/../mcp", "/api/apps/dice/%6Dcp", "/api/apps/dice/mcp/?a=1", "/api/apps/dice+dev/mcp"}
	// with MCP off for the workspace, and then on: refused at the gate, before any approval
	for _, on := range []bool{false, true} {
		if on {
			w.mcpOn(false)
		}
		for _, p := range spellings {
			rec := w.do(w.full, "POST", p, call, w.bob)
			if rec.Code == 307 && rec.Header().Get("Location") == "/api/apps/dice/mcp" {
				continue // the mux sends an unclean path to the clean one: the endpoint, judged as itself
			}
			if rec.Code != 403 || refusalOf(rec) == "needs-approval" {
				t.Errorf("mcp on=%v: POST %s: %d %s", on, p, rec.Code, rec.Body.String())
			}
		}
	}
	if rec := w.do(w.full, "GET", "/api/apps/dice/mcp", "", w.bob); rec.Code != 403 || refusalOf(rec) != "mcp-call" {
		t.Errorf("GET on the endpoint: %d %s", rec.Code, rec.Body.String())
	}
	// never filed as a tile call
	for _, p := range append(spellings, "/api/apps/dice/mcp") {
		in, _ := json.Marshal(map[string]string{"method": "POST", "path": p, "body": call})
		if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), w.bob); rec.Code != 400 && rec.Code != 403 {
			t.Errorf("filing POST %s as a call: %d %s", p, rec.Code, rec.Body.String())
		}
	}
	in, _ := json.Marshal(map[string]string{"method": "DELETE", "path": "/api/apps/dice/mcp/"})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), w.bob); rec.Code != 400 {
		t.Errorf("filing DELETE on the endpoint: %d %s", rec.Code, rec.Body.String())
	}
	if len(w.calls) != 0 {
		t.Fatalf("a tool ran: %v", w.calls)
	}
}

// …and an "always" of a call made before the tile declared its MCP server
// covers nothing there once it does
func TestMCPEndpointOlderCallApproval(t *testing.T) {
	w := newMCPRig(t)
	if _, err := w.s.Auth.Users.Upsert(users.User{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead, "apps/mail": users.LevelRead, "apps/dice": users.LevelWrite}}, ""); err != nil {
		t.Fatal(err)
	}
	manifest := filepath.Join(w.s.Reg.Root, "apps", "dice", "xbin.json")
	if err := os.WriteFile(manifest, []byte(`{"title":"Dice"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	call := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wipe","arguments":{}}}`
	req := w.ask(w.bob, "POST", "/api/apps/dice/mcp/", call)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"always","scope":"call"}`, w.cookie("bob")); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if err := os.WriteFile(manifest, []byte(`{"title":"Dice","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	w.mcpOn(false)
	if rec := w.do(w.full, "POST", "/api/apps/dice/mcp/", call, w.bob); rec.Code != 403 || refusalOf(rec) != "mcp-call" {
		t.Errorf("the endpoint under the older approval: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call("wipe"); rec.Code != 403 || refusalOf(rec) != "needs-approval" {
		t.Errorf("the tool under the older approval: %d %s", rec.Code, rec.Body.String())
	}
	if len(w.calls) != 0 {
		t.Fatalf("a tool ran: %v", w.calls)
	}
}
