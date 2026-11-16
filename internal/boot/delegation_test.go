package boot

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/broker"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/events"
	"github.com/xbin-dev/xbin/internal/llm"
	"github.com/xbin-dev/xbin/internal/registry"
	"github.com/xbin-dev/xbin/internal/server"
)

// The LLM connector's side of an agent session: a delegation holder's
// model call runs only under a live session of that person and holder,
// and only on that session's model — so the model the orgs' rules are
// asked with is the model the conversation really goes to. Everyone else
// calls as before.
func TestHolderModelCallsAreTheSessions(t *testing.T) {
	const holder = "xbin/wsagent"
	t.Cleanup(auth.TestSealDelegate(holder))
	reg := &registry.Registry{Root: t.TempDir()}
	if err := reg.Rescan(); err != nil {
		t.Fatal(err)
	}
	brk, err := broker.New(reg, events.NewHub(), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(brk.Close)
	store := delegation.OpenStore(t.TempDir())
	srv := &server.Server{Delegation: store}
	policy := holderCallPolicy(srv, brk)
	h, err := store.NewAgentSession("bob", holder, nil, "local/llama")
	if err != nil {
		t.Fatal(err)
	}
	local, hosted := llm.UpstreamInfo{Name: "local", Private: true}, llm.UpstreamInfo{Name: "hosted"}
	call := func(c llm.Caller, u llm.UpstreamInfo, model string) string {
		if err := policy(c, u, model); err != nil {
			return err.Error()
		}
		return ""
	}
	bob := llm.Caller{Component: holder, Via: "component", User: "bob", AgentSession: h.ID}
	if why := call(bob, local, "llama"); why != "" {
		t.Fatalf("the session's own model: %s", why)
	}
	for name, c := range map[string]struct {
		c     llm.Caller
		u     llm.UpstreamInfo
		model string
		want  string
	}{
		"another model of the same upstream": {bob, local, "mistral", "is on local/llama"},
		"a hosted model":                     {bob, hosted, "big", "is on local/llama"},
		"the same model name elsewhere":      {bob, hosted, "llama", "is on local/llama"},
		"no session":                         {llm.Caller{Component: holder, Via: "component", User: "bob"}, local, "llama", "run under an agent session"},
		"another person's session":           {llm.Caller{Component: holder, Via: "component", User: "alice", AgentSession: h.ID}, local, "llama", "no such agent session"},
		"a session that isn't one":           {llm.Caller{Component: holder, Via: "component", User: "bob", AgentSession: "nope"}, local, "llama", "no such agent session"},
		"the holder's global instance":       {llm.Caller{Component: holder, Via: "component", AgentSession: h.ID}, local, "llama", "run under an agent session"},
	} {
		if why := call(c.c, c.u, c.model); !strings.Contains(why, c.want) {
			t.Errorf("%s: %q, want a refusal with %q", name, why, c.want)
		}
	}
	// a session opened on a role's name is on the model the connector
	// resolves that role to: its calls on that model pass, on another not
	role, err := store.NewAgentSession("bob", holder, nil, "xbin/agent")
	if err != nil {
		t.Fatal(err)
	}
	brk.AgentModels = func(id string) (agentpolicy.ModelRef, bool) {
		if id == "xbin/agent" {
			return agentpolicy.ModelRef{Upstream: "hosted", Model: "big"}, true
		}
		return agentpolicy.ModelRef{}, false
	}
	onRole := llm.Caller{Component: holder, Via: "component", User: "bob", AgentSession: role.ID}
	if why := call(onRole, hosted, "big"); why != "" {
		t.Errorf("a session on the agent role's name, calling the model that role names: %s", why)
	}
	if why := call(onRole, local, "llama"); !strings.Contains(why, "is on xbin/agent") {
		t.Errorf("…and calling another model: %q", why)
	}
	brk.AgentModels = nil
	// the LIST of models is no call: a holder is shown every model, with a
	// session or without one, so its person can pick — a call on any of
	// them but the session's own is still refused (above)
	for name, c := range map[string]llm.Caller{"with a session": bob, "without one": {Component: holder, Via: "component", User: "bob"}} {
		c.Listing = true
		for _, m := range [][2]any{{local, "llama"}, {local, "mistral"}, {hosted, "big"}} {
			if why := call(c, m[0].(llm.UpstreamInfo), m[1].(string)); why != "" {
				t.Errorf("listing %s %s: %s", name, m[1], why)
			}
		}
	}
	// the session changes model: the connector follows it
	if _, err := store.UpdateAgentSession("bob", holder, h.ID, func(a *delegation.AgentSession) error { a.Model = "hosted/big"; return nil }); err != nil {
		t.Fatal(err)
	}
	if why := call(bob, hosted, "big"); why != "" {
		t.Errorf("after the session moved to hosted/big: %s", why)
	}
	if why := call(bob, local, "llama"); why == "" {
		t.Error("the model the session left still passes")
	}
	// the off switches stop model turns too: paused, or the workspace's switch off
	srv.SetDelegationGate(server.DelegationGate(store, true, false))
	if why := call(bob, hosted, "big"); !strings.Contains(why, "is off") || !strings.Contains(why, "an admin hasn't turned") {
		t.Errorf("with workspace agents off: %q", why)
	}
	if _, err := store.SetWorkspace(true, "test"); err != nil {
		t.Fatal(err)
	}
	if why := call(bob, hosted, "big"); why != "" {
		t.Errorf("with workspace agents on: %s", why)
	}
	if _, err := store.SetState("bob", func(st *delegation.State) { st.Paused = true }); err != nil {
		t.Fatal(err)
	}
	if why := call(bob, hosted, "big"); !strings.Contains(why, "paused") {
		t.Errorf("a paused agent's model call: %q", why)
	}
	if _, err := store.SetState("bob", func(st *delegation.State) { st.Paused = false }); err != nil {
		t.Fatal(err)
	}
	// anyone who isn't a holder is not this policy's business
	for _, c := range []llm.Caller{{Component: "apps/chat", Via: "component", User: "bob"}, {User: "bob", Via: "person"}, {User: "bob", Via: "key"}} {
		if why := call(c, hosted, "big"); why != "" {
			t.Errorf("%+v: %s", c, why)
		}
	}
}

// covers D239 — the host's web and MCP policy for workspace agents
// (XBIN_WSAGENT_CONFIG): a file that is invalid stops the boot, naming the
// variable — a host never runs without the limits it wrote.
func TestWsAgentHostFileStopsBoot(t *testing.T) {
	if testing.Short() {
		t.Skip("boots a workspace")
	}
	quiet(t)
	bad := filepath.Join(t.TempDir(), "wsagent.jsonc")
	if err := os.WriteFile(bad, []byte(`{"web": {"mode": "full"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	ws := filepath.Join(t.TempDir(), "ws")
	if err := InitWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	cfg := testConfig(ws, ln)
	cfg.WsAgentConfig = bad
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := Run(ctx, cfg); err == nil || !strings.Contains(err.Error(), "XBIN_WSAGENT_CONFIG") {
		t.Fatalf("Run with an invalid XBIN_WSAGENT_CONFIG: %v", err)
	}
}
