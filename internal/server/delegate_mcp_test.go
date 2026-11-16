package server

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// mcpRig is the delegate world with an MCP tile, apps/dice, that bob
// reads: its server (the component proxy's stand-in) has two tools,
// roll_dice marked read-only and wipe, which isn't.
type mcpRig struct {
	*webRig
	mu    sync.Mutex
	seen  []mcpSeen
	calls []string // tools/call names, in order
}

type mcpSeen struct {
	p       auth.Principal
	method  string
	session string
}

func newMCPRig(t *testing.T) *mcpRig {
	w := &mcpRig{webRig: newWebRig(t)}
	dir := filepath.Join(w.s.Reg.Root, "apps", "dice")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "xbin.json"), []byte(`{"title":"Dice","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := w.s.Reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	if _, err := w.s.Auth.Users.Upsert(users.User{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead, "apps/mail": users.LevelRead, "apps/dice": users.LevelRead}}, ""); err != nil {
		t.Fatal(err)
	}
	w.s.ComponentAPI = http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/dice/mcp" {
			rw.WriteHeader(404)
			return
		}
		var rq struct {
			ID     any
			Method string
			Params struct {
				Name      string
				Arguments map[string]any
			}
		}
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &rq)
		w.mu.Lock()
		w.seen = append(w.seen, mcpSeen{p: auth.PrincipalOf(r), method: rq.Method, session: r.Header.Get("Mcp-Session-Id")})
		if rq.Method == "tools/call" {
			w.calls = append(w.calls, rq.Params.Name)
		}
		w.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		var result any
		switch rq.Method {
		case "initialize":
			rw.Header().Set("Mcp-Session-Id", "sess-1")
			result = map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "dice"}}
		case "tools/list":
			result = map[string]any{"tools": []map[string]any{
				{"name": "roll_dice", "description": "Roll dice.", "inputSchema": map[string]any{"type": "object"}, "annotations": map[string]any{"readOnlyHint": true}},
				{"name": "wipe", "description": "Wipe the table.", "inputSchema": map[string]any{"type": "object"}}}}
		case "tools/call":
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": "rolled 4 (" + rq.Params.Name + ")"}}}
		default:
			result = map[string]any{}
		}
		_ = json.NewEncoder(rw).Encode(map[string]any{"jsonrpc": "2.0", "id": rq.ID, "result": result})
	})
	return w
}

func (w *mcpRig) mcpOn(readOnly bool) {
	w.t.Helper()
	if rec := w.policy(`{"mcp":{"mode":"tiles"}}`); rec.Code != 200 {
		w.t.Fatal(rec.Body.String())
	}
	body := `{"mcp":true}`
	if readOnly {
		body = `{"mcp":true,"mcpReadOnly":true}`
	}
	if rec := w.do(w.full, "PUT", "/api/xbin/delegate/state", body, w.cookie("bob")); rec.Code != 200 {
		w.t.Fatal(rec.Body.String())
	}
}

func (w *mcpRig) rpc(body string, extra ...cred) *httptest.ResponseRecorder {
	return w.do(w.full, "POST", "/api/apps/dice/mcp", body, append([]cred{w.bob}, extra...)...)
}

func (w *mcpRig) call(tool string, extra ...cred) *httptest.ResponseRecorder {
	b, _ := json.Marshal(map[string]any{"server": "apps/dice", "tool": tool, "arguments": map[string]any{"sides": 6}})
	return w.do(w.full, "POST", "/api/xbin/delegate/mcp/call", string(b), append([]cred{w.bob}, extra...)...)
}

// covers D236 / journey G4 — the agent reads an MCP tile's server itself:
// exactly initialize, ping and tools/list on the endpoint the tile
// declares, as the person, nobody asked; anything else POSTed there is
// refused, and nothing passes while MCP is off
func TestMCPReadsOnTheEndpoint(t *testing.T) {
	w := newMCPRig(t)
	list := `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
	if rec := w.rpc(list); rec.Code != 403 || refusalOf(rec) != "mcp-off" {
		t.Fatalf("while the policy is off: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.policy(`{"mcp":{"mode":"tiles"}}`); rec.Code != 200 {
		t.Fatal(rec.Body.String())
	}
	if rec := w.rpc(list); rec.Code != 403 || refusalOf(rec) != "mcp-person" {
		t.Fatalf("while the person's setting is off: %d %s", rec.Code, rec.Body.String())
	}
	w.mcpOn(false)
	for _, m := range []string{"initialize", "ping", "tools/list"} {
		if rec := w.rpc(`{"jsonrpc":"2.0","id":7,"method":"`+m+`","params":{}}`, header("Mcp-Session-Id", "sess-1"), header("X-Other", "x")); rec.Code != 200 {
			t.Fatalf("%s: %d %s", m, rec.Code, rec.Body.String())
		}
	}
	w.mu.Lock()
	seen := append([]mcpSeen(nil), w.seen...)
	w.mu.Unlock()
	if len(seen) != 3 || seen[2].method != "tools/list" || seen[2].session != "sess-1" || seen[2].p.Agent != dHolder || seen[2].p.UserID != "bob" {
		t.Fatalf("what the server saw: %+v", seen)
	}
	// what the server answered is the tile's data in the conversation
	if h, _ := w.store.AgentSession("bob", dHolder, w.handle); len(h.Tainted) == 0 {
		t.Error("an MCP read didn't taint the conversation")
	}
	for _, bad := range []string{
		`{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wipe"}}`,
		`[{"jsonrpc":"2.0","id":1,"method":"tools/list"}]`,
		`{"jsonrpc":"2.0","method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","x-call":"wipe"}`,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list"}{"jsonrpc":"2.0","id":2,"method":"tools/call"}`,
		`{"jsonrpc":"1.0","id":1,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":1,"method":"resources/read"}`,
		`not json`,
	} {
		if rec := w.rpc(bad); rec.Code != 403 || refusalOf(rec) != "mcp-call" {
			t.Errorf("%s: %d %s", bad, rec.Code, rec.Body.String())
		}
	}
	if len(w.calls) != 0 {
		t.Fatalf("a tool ran through the endpoint: %v", w.calls)
	}
	// a raw call to the endpoint can't be put to the person as a tile call either
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/apps/dice/mcp", "body": `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"wipe"}}`})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), w.bob); rec.Code != 400 {
		t.Errorf("a raw tools/call filed as a tile call: %d %s", rec.Code, rec.Body.String())
	}
	// a tile that declares no MCP server: its /mcp is an ordinary tile write
	if rec := w.do(w.full, "POST", "/api/apps/x/mcp", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`, w.bob); rec.Code != 403 || refusalOf(rec) != "needs-approval" {
		t.Errorf("a POST to /mcp of a tile that serves none: %d %s", rec.Code, rec.Body.String())
	}
}

// covers D236 — a tool's call: xbind is the client, the tool asks the
// first time ("always for this tool" is a standing approval of exactly that
// server and tool), a tool the server marks read-only runs unasked only
// under the person's own setting, a read-level tile takes read-only tools
// only, and an org whose data the conversation holds can keep it from
// servers outside it
func TestMCPToolCalls(t *testing.T) {
	w := newMCPRig(t)
	w.mcpOn(false)
	if rec := w.call("roll_dice"); rec.Code != 403 || refusalOf(rec) != "needs-approval" {
		t.Fatalf("a read-only tool without the setting: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call("roll_dice", header(HeaderDryRun, "1")); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"needsApproval":true`) {
		t.Errorf("the call's dry run: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call("nope"); rec.Code != 404 || refusalOf(rec) != "mcp-tool" {
		t.Errorf("a tool the server doesn't list: %d %s", rec.Code, rec.Body.String())
	}
	body, _ := json.Marshal(map[string]any{"server": "apps/dice", "tool": "wipe", "arguments": map[string]any{"sides": 6}})
	req := w.ask(w.bob, "POST", "/api/xbin/delegate/mcp/call", string(body))
	if req.Kind != delegation.KindMCP || req.Tile != "apps/dice" || req.Preview["tool"] != "wipe" || req.Preview["readOnly"] != false || req.Preview["serverName"] != "Dice" {
		t.Fatalf("the request: %+v", req)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"grant":"always","scope":"tile"}`, w.cookie("bob")); rec.Code != 400 {
		t.Errorf("a tool's request approved for every write of the tile: %d %s", rec.Code, rec.Body.String())
	}
	w.approve(w.cookie("bob"), req.ID, "always")
	aps, _ := w.store.Approvals("bob")
	if len(aps) != 1 || aps[0].Scope != delegation.ScopeTool || aps[0].Path != "wipe" || aps[0].Tile != "apps/dice" || aps[0].Method != delegation.MCPMethod {
		t.Fatalf("the standing approval: %+v", aps)
	}
	for i := 0; i < 2; i++ { // the ticket, then the standing approval
		if rec := w.call("wipe", header(HeaderTicket, req.ID)); rec.Code != 200 || !strings.Contains(rec.Body.String(), `rolled 4 (wipe)`) {
			t.Fatalf("call %d under the approval: %d %s", i, rec.Code, rec.Body.String())
		}
	}
	// the approval is of that tool: roll_dice still asks, and no tile call is covered
	if rec := w.call("roll_dice"); refusalOf(rec) != "needs-approval" {
		t.Errorf("another tool under wipe's approval: %d %s", rec.Code, rec.Body.String())
	}
	if _, ok := w.store.Covers("bob", "apps/dice", "POST", "/api/apps/dice/mcp"); ok {
		t.Error("a tool's approval covers a tile call")
	}
	// the person's "read-only tools don't ask": the server's readOnlyHint
	w.mcpOn(true)
	if rec := w.call("roll_dice"); rec.Code != 200 {
		t.Errorf("a read-only tool under the setting: %d %s", rec.Code, rec.Body.String())
	}
	// a tile at agent level read: read-only tools only, whatever is approved
	w.dWorld.policy.set(func(d *dPolicy) { d.readLevel = map[string]bool{"apps/dice": true} })
	if rec := w.call("wipe"); rec.Code != 403 || refusalOf(rec) != "tile" || !strings.Contains(rec.Body.String(), "read-only") {
		t.Errorf("a changing tool on a read-level tile: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.call("roll_dice"); rec.Code != 200 {
		t.Errorf("a read-only tool on a read-level tile: %d %s", rec.Code, rec.Body.String())
	}
	w.dWorld.policy.set(func(d *dPolicy) { d.readLevel = nil })
	// an org that keeps its data from servers outside it
	w.s.AgentWorld = netWorld{mcp: map[string]string{"fin": agentpolicy.MCPOrg}}
	_ = w.do(w.full, "GET", "/api/apps/mail/inbox", "", w.bob) // Finance's data
	if rec := w.call("roll_dice"); rec.Code != 403 || refusalOf(rec) != "mcp-org" {
		t.Errorf("after reading Finance's data: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.rpc(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); rec.Code != 403 || refusalOf(rec) != "mcp-org" {
		t.Errorf("a read after reading Finance's data: %d %s", rec.Code, rec.Body.String())
	}
	if got := strings.Join(w.calls, ","); got != "wipe,wipe,roll_dice,roll_dice" {
		t.Errorf("the tools that ran: %s", got)
	}
}
