//go:build linux && integration

package isolated

// wsagent_mcp_test.go — the workspace agent and an MCP tile (D239), end to
// end on the xbind wsagent_test.go boots (startWsAgent): the builtin
// mcp-server template instantiated as apps/dice, MCP tiles on in the
// workspace's policy and in alice's Preferences.
//
//   - the free reads: alice's agent sends tools/list to apps/dice's declared
//     endpoint with its own credential and gets roll_dice, asked of nobody;
//     a tools/call there is refused (mcp-call) — a tool goes through xbind;
//   - mcp_tools: the model is told roll_dice, marked read-only by its server;
//   - mcp_call under a ticket: the call is a request (kind mcp) that alice
//     allows once; the tool runs once and the roll reaches the conversation;
//   - "MCP tools marked read-only don't ask me": roll_dice runs at once, and
//     nothing is filed.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestWorkspaceAgentMCPTile$' ./test/isolated/

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestWorkspaceAgentMCPTile(t *testing.T) {
	e := startWsAgent(t)
	d := e.d
	const dice = "apps/dice"
	d.Must(t, "POST", "/api/xbin/templates/new", map[string]any{"source": "mcp-server", "path": dice}, 200)
	d.WaitComponent(t, dice)

	e.person(wsAdmin, "admin", nil)
	e.person("alice", "user", map[string]string{dice: "write"})
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone", "mcp": map[string]any{"mode": "tiles"}}, 200, e.as(wsAdmin)...)
	// turning it on is alice's own act: her agent's page can't
	alice := e.open("alice", map[string]any{"main": true})
	t.Cleanup(func() { alice.transcript(t) })
	if r := d.Call(t, "PUT", "/api/xbin/delegate/state", map[string]any{"mcp": true}, alice.fr); r.Status != 403 {
		t.Errorf("the agent's page turns MCP on: %d %s, want 403", r.Status, r)
	}
	d.Must(t, "PUT", "/api/xbin/delegate/state", map[string]any{"mcp": true}, 200, e.as("alice")...)

	t.Run("1 the reads on the endpoint are free; a call there is not", func(t *testing.T) {
		e.in(t)
		var got []wsDelegateCall
		const list = `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`
		const call = `{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"roll_dice","arguments":{}}}`
		deadline := time.Now().Add(5 * time.Minute) // the tile answers once its first build is up
		for {
			got = e.asDelegate("alice", alice.handle, "GET /api/xbin/delegate/map", "POST /api/"+dice+"/mcp "+list, "POST /api/"+dice+"/mcp "+call)
			if got[1].Status == 200 || time.Now().After(deadline) {
				break
			}
			time.Sleep(2 * time.Second)
		}
		if got[1].Status != 200 || !strings.Contains(got[1].Body, `"name":"roll_dice"`) || !strings.Contains(got[1].Body, `"readOnlyHint":true`) {
			t.Fatalf("tools/list on %s as alice's agent: %d %s, want roll_dice, marked read-only", dice, got[1].Status, got[1].Body)
		}
		if got[2].Status != 403 || !strings.Contains(got[2].Body, `"mcp-call"`) {
			t.Errorf("tools/call on the endpoint itself: %d %s, want 403 mcp-call", got[2].Status, got[2].Body)
		}
		if reqs := e.requests("alice"); len(reqs) != 0 {
			t.Errorf("the reads filed requests: %+v", reqs)
		}
	})

	t.Run("2 mcp_tools names roll_dice, read-only", func(t *testing.T) {
		e.in(t)
		turn := alice.say(`ws: mcp_tools {"tile":"` + dice + `"}`)
		if got := turn.tool("mcp_tools", 0); !strings.Contains(got, "- roll_dice (marked read-only by its server)") || !strings.Contains(got, "untrusted") {
			t.Errorf("mcp_tools: %q, want roll_dice marked read-only, framed as the tile's data", got)
		}
	})

	rolled := regexp.MustCompile(`rolled 1d20: \d+`)
	t.Run("3 a call under a ticket", func(t *testing.T) {
		e.in(t)
		turn := alice.say(`ws: mcp_call {"tile":"` + dice + `","tool":"roll_dice","arguments":{"sides":20},"why":"roll a d20 for alice"}`)
		if got := turn.tool("mcp_call", 0); !strings.HasPrefix(got, "Pending — request ") {
			t.Fatalf("mcp_call without an approval: %q, want a pending request", got)
		}
		reqs := e.requests("alice")
		if len(reqs) != 1 || reqs[0].Kind != "mcp" || reqs[0].Target != "/api/xbin/delegate/mcp/call" || reqs[0].Preview.Class != "mcp" ||
			!strings.Contains(reqs[0].Body, `"tool":"roll_dice"`) || reqs[0].Summary != "roll a d20 for alice" {
			t.Fatalf("alice's requests at xbind: %+v, want the tool's call, kind mcp", reqs)
		}
		after := alice.last()
		d.Must(t, "POST", "/api/xbin/delegate/requests/"+reqs[0].ID+"/approve", map[string]any{"grant": "once"}, 200, e.as("alice")...)
		told := alice.woken(after, time.Minute, "the approved tool ran", "approved by your person — it ran: roll_dice on "+dice+" answered")
		if !rolled.MatchString(told) {
			t.Errorf("the update: %q, want the roll", told)
		}
		if got := e.request("alice", reqs[0].ID); got.State != "used" {
			t.Errorf("the request after the call: %s, want used", got.State)
		}
		var ran bool
		for _, r := range e.audit("alice") {
			if r.Class == "mcp" && r.Ticket == reqs[0].ID && r.Status == 200 {
				ran = true
			}
		}
		if !ran {
			t.Errorf("alice's log has no mcp row under the ticket: %+v", e.audit("alice"))
		}
	})

	t.Run("4 read-only tools don't ask, when alice says so", func(t *testing.T) {
		e.in(t)
		d.Must(t, "PUT", "/api/xbin/delegate/state", map[string]any{"mcpReadOnly": true}, 200, e.as("alice")...)
		turn := alice.say(`ws: mcp_call {"tile":"` + dice + `","tool":"roll_dice","arguments":{"sides":20},"why":"another d20"}`)
		if got := turn.tool("mcp_call", 0); !strings.HasPrefix(got, "roll_dice on "+dice+" answered") || !rolled.MatchString(got) {
			t.Errorf("mcp_call of a read-only tool: %q, want the roll at once", got)
		}
		if reqs := e.requests("alice"); len(reqs) != 0 {
			t.Errorf("a read-only tool filed a request: %+v", reqs)
		}
		var pref bool
		for _, r := range e.audit("alice") {
			if r.Class == "mcp" && r.Under == "preference" && r.Status == 200 {
				pref = true
			}
		}
		if !pref {
			t.Errorf("alice's log has no mcp row under her preference: %+v", e.audit("alice"))
		}
	})

	t.Run("5 a tile at agent level read: its reads and its read-only tools, through the real proxy", func(t *testing.T) {
		e.in(t)
		raw, err := os.ReadFile(filepath.Join(d.WS, dice, "xbin.json"))
		if err != nil || !strings.Contains(string(raw), `"agent": "full"`) {
			t.Fatalf("apps/dice's manifest: %v %s", err, raw)
		}
		d.WriteTile(t, dice, map[string]string{"xbin.json": strings.Replace(string(raw), `"agent": "full"`, `"agent": "read"`, 1)})
		var got []wsDelegateCall
		deadline := time.Now().Add(time.Minute) // the lowered level is in force once the manifest is read again
		for {
			got = e.asDelegate("alice", alice.handle, "GET /api/xbin/delegate/map", "POST /api/"+dice+`/mcp {"jsonrpc":"2.0","id":1,"method":"tools/list"}`, "POST /api/"+dice+"/other {}")
			if strings.Contains(got[0].Body, `"agent":"read"`) || time.Now().After(deadline) {
				break
			}
			time.Sleep(time.Second)
		}
		if !strings.Contains(got[0].Body, `"agent":"read"`) {
			t.Fatalf("apps/dice's agent level on the map isn't read: %s", got[0].Body)
		}
		if got[1].Status != 200 || !strings.Contains(got[1].Body, `"roll_dice"`) {
			t.Errorf("tools/list on a read-level MCP tile: %d %s", got[1].Status, got[1].Body)
		}
		if got[2].Status != 403 {
			t.Errorf("a POST elsewhere on it: %d %s", got[2].Status, got[2].Body)
		}
		turn := alice.say(`ws: mcp_call {"tile":"` + dice + `","tool":"roll_dice","arguments":{"sides":20},"why":"a d20 from a read-level tile"}`)
		if got := turn.tool("mcp_call", 0); !strings.HasPrefix(got, "roll_dice on "+dice+" answered") || !rolled.MatchString(got) {
			t.Errorf("a read-only tool on a read-level tile: %q, want the roll", got)
		}
	})
}
