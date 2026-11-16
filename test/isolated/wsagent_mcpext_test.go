//go:build linux && integration

package isolated

// wsagent_mcpext_test.go — the workspace agent and an MCP server OUTSIDE the
// workspace (D240), end to end on the xbind wsagent_test.go boots
// (startWsAgent): a fake MCP server on this host's loopback (127.0.0.2, so
// listed as on a private network, plain http) that takes a bearer token,
// listed by the admin with that secret; MCP "listed" in the workspace's
// policy and on in alice's Preferences.
//
//   - the secret is write-only: the admin's read says hasSecret, never it;
//   - mcp_tools: the model is told the server's tools, framed as an
//     outsider's words — xbind listed them with the secret, the agent never
//     saw it;
//   - mcp_call under a ticket: a request (kind mcp) whose preview says the
//     server is outside the workspace and its host; alice allows it once;
//     the tool runs once, with the bearer token, and the answer reaches the
//     conversation; her log says the server and the tool, never the token.
//
//	set -a; eval "$(sed -n 's/^export \([A-Z_]*\) := \(.*\)$/\1=\2/p' .dev.mk | grep -v ^PATH)"; set +a
//	go test -tags=integration -count=1 -v -run '^TestWorkspaceAgentMCPOutside$' ./test/isolated/

import (
	"net"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/mcpclient/mcpfake"
)

func TestWorkspaceAgentMCPOutside(t *testing.T) {
	const secret = "tok-OUTSIDE-e2e-77"
	fake := &mcpfake.Server{Name: "tracker", Tools: mcpfake.Tools(), Header: "Authorization", Want: "Bearer " + secret}
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("no 127.0.0.2 here: %v", err)
	}
	srv := httptest.NewUnstartedServer(fake)
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	t.Cleanup(srv.Close)

	e := startWsAgent(t)
	d := e.d
	e.person(wsAdmin, "admin", nil)
	e.person("alice", "user", nil)
	server := map[string]any{"id": "tracker", "name": "Tracker", "url": srv.URL + "/mcp", "auth": "bearer", "secret": secret,
		"privateNetwork": true, "plainHttp": true, "note": "Our issue tracker."}
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone", "mcp": map[string]any{"mode": "listed", "servers": []any{server}}}, 200, e.as(wsAdmin)...)
	if r := d.Call(t, "GET", "/api/xbin/delegate/workspace", nil, e.as(wsAdmin)...); r.Status != 200 || strings.Contains(string(r.Body), secret) || !strings.Contains(string(r.Body), `"hasSecret":true`) {
		t.Fatalf("the admin's read: %d %s — want hasSecret, never the secret", r.Status, r)
	}
	alice := e.open("alice", map[string]any{"main": true})
	t.Cleanup(func() { alice.transcript(t) })
	d.Must(t, "PUT", "/api/xbin/delegate/state", map[string]any{"mcp": true}, 200, e.as("alice")...)

	t.Run("1 mcp_tools lists the outside server's tools, framed", func(t *testing.T) {
		e.in(t)
		turn := alice.say(`ws: mcp_tools {"server":"ext:tracker"}`)
		got := turn.tool("mcp_tools", 0)
		if !strings.Contains(got, "ext:tracker — Tracker, outside this workspace (127.0.0.2)") || !strings.Contains(got, "- lookup (its server says read-only") || !strings.Contains(got, "untrusted") {
			t.Errorf("mcp_tools: %q, want the server's tools framed as an outsider's words", got)
		}
		if strings.Contains(got, secret) {
			t.Error("the model was told the secret")
		}
		if reqs := e.requests("alice"); len(reqs) != 0 {
			t.Errorf("a list filed requests: %+v", reqs)
		}
	})

	t.Run("2 a call under a ticket", func(t *testing.T) {
		e.in(t)
		turn := alice.say(`ws: mcp_call {"server":"ext:tracker","tool":"post","arguments":{"id":"T-7","text":"fixed"},"why":"comment on T-7 for alice"}`)
		if got := turn.tool("mcp_call", 0); !strings.HasPrefix(got, "Pending — request ") {
			t.Fatalf("mcp_call without an approval: %q, want a pending request", got)
		}
		reqs := e.requests("alice")
		if len(reqs) != 1 || reqs[0].Kind != "mcp" || reqs[0].Preview.Class != "mcp" || !strings.Contains(reqs[0].Body, `"server":"ext:tracker"`) {
			t.Fatalf("alice's requests: %+v, want the outside tool's call", reqs)
		}
		raw := string(d.Call(t, "GET", "/api/xbin/delegate/requests/"+reqs[0].ID, nil, e.as("alice")...).Body)
		if !strings.Contains(raw, `"outside":true`) || !strings.Contains(raw, `"host":"127.0.0.2"`) || strings.Contains(raw, secret) {
			t.Errorf("the request's preview: %s, want outside and its host, never the secret", raw)
		}
		after := alice.last()
		d.Must(t, "POST", "/api/xbin/delegate/requests/"+reqs[0].ID+"/approve", map[string]any{"grant": "once"}, 200, e.as("alice")...)
		told := alice.woken(after, time.Minute, "the approved outside tool ran", "approved by your person — it ran: post on ext:tracker (outside this workspace) answered")
		if !strings.Contains(told, "called post") || strings.Contains(told, secret) {
			t.Errorf("the update: %q, want the tool's answer", told)
		}
		var calls int
		for _, s := range fake.Log() {
			if s.Credential != "Bearer "+secret {
				t.Errorf("a message without the secret xbin keeps: %+v", s)
			}
			if s.Method == "tools/call" {
				calls++
			}
		}
		if calls != 1 {
			t.Errorf("the tool ran %d times, want once", calls)
		}
		var row bool
		for _, r := range e.audit("alice") {
			if r.Class == "mcp" && r.Ticket == reqs[0].ID && r.Status == 200 && strings.HasPrefix(r.Path, "http://127.0.0.2:") && !strings.Contains(r.Path, "/mcp") {
				row = true
			}
		}
		if !row {
			t.Errorf("alice's log has no outside mcp row under the ticket: %+v", e.audit("alice"))
		}
	})
}
