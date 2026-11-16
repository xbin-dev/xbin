package server

import (
	"strings"
	"testing"
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
