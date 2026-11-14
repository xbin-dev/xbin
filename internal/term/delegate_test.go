package term

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// A person's workspace agent (a delegate principal, auth/delegate.go) opens
// a tile agent session as that person: the person's gates, directory and
// home, marked origin "agent" — with the mode and pickers of the ticket the
// person approved, or of their preferences, never of the agent's choosing.
func TestAgentSessionAsDelegate(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	const holder = "xbin/agent"
	alice := &users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}}
	person := auth.Principal{UserID: "alice", User: alice, Via: "session"}
	delegate := auth.Principal{UserID: "alice", User: alice, Via: "delegate", Agent: holder, AgentModel: "m1"}
	reader := &users.User{ID: "rita", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}}
	approved := &DelegateStart{Approved: true}
	prefs := &DelegateStart{Prefs: &delegation.StartPrefs{}}
	refused := func(name string, p auth.Principal, a AgentOpen, want string) {
		t.Helper()
		if info, code, err := m.OpenAgentWith(p, a); code != 403 || err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %d %v, want a 403 containing %q", name, code, err, want)
			if err == nil {
				m.Kill(info.ID)
			}
		}
	}

	// no agent policy installed: nothing opens, even approved
	refused("no policy", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: approved}, "rules aren't set up")
	var asked []string
	m.AgentPolicy = sessionPolicy(func(s delegation.Subject, provider string) error {
		asked = append(asked, s.Holder+" "+s.User+" "+s.Tile+" "+s.Model+" "+provider)
		return nil
	})
	// the person's own gate: no terminal level, no session — for their agent too
	refused("a reader's agent", auth.Principal{UserID: "rita", User: reader, Via: "delegate", Agent: holder},
		AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: approved}, "terminal access")
	// neither a ticket nor a preference: refused, whatever it asks for
	refused("no approval", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake"}, "approval")
	refused("an empty authorisation", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{}}, "approval")
	// the default deployment only, approved or not
	refused("a deployment", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Deployment: "dev", Delegate: approved}, "default deployment")
	// starting on a preference: the sandbox is never the agent's pick
	for name, a := range map[string]AgentOpen{
		"another option": {Cwd: "apps/x", Provider: "fake", Options: map[string]string{"mode": "yolo"}},
		"two options":    {Cwd: "apps/x", Provider: "fake", Options: map[string]string{"model": "fake-fast", "thinking": "high"}},
		"a VM sandbox":   {Cwd: "apps/x", Provider: "fake", VM: true},
		"a GPU":          {Cwd: "apps/x", Provider: "fake", GPU: "all"},
	} {
		a.Delegate = prefs
		refused(name, delegate, a, "needs the person's approval")
	}
	// the org's rule refuses
	m.AgentPolicy = sessionPolicy(func(delegation.Subject, string) error { return errors.New("Finance allows the claude harness only") })
	refused("the org's harness list", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: approved}, "Finance allows")
	m.AgentPolicy = delegation.OpenPolicy{}

	// on a preference: a mode the agent sends is ignored — the preference's,
	// here the provider's asking default
	info, code, err := m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "yolo", Net: "internet", Name: "fix the build",
		Options: map[string]string{"model": "fake-fast"}, Delegate: prefs})
	if err != nil || code != 200 {
		t.Fatalf("the delegate's open: %d %v", code, err)
	}
	if info.Origin != OriginAgent || info.Mode != "ask" || info.Kind != KindAgent || info.Name != "fix the build" || info.API || info.Net == "internet" {
		t.Fatalf("info (the preference's mode, network and API access): %+v", info)
	}
	id := info.ID
	if len(asked) == 0 || asked[0] != holder+" alice apps/x m1 fake" {
		t.Errorf("the policy was asked %v", asked)
	}
	if m.AgentOrigin(id) != holder || m.AgentOrigin("nope") != "" {
		t.Fatalf("AgentOrigin: %q", m.AgentOrigin(id))
	}
	// it is the person's session: in their directory, driven by them
	found := false
	for _, row := range m.ListFor(HomeKey(person), "apps/x", nil) {
		found = found || row.ID == id && row.Origin == OriginAgent
	}
	if !found {
		t.Fatal("the session isn't in alice's directory")
	}
	if err := m.MayDrive(id, person); err != nil {
		t.Fatalf("the person can't drive the session their agent opened: %v", err)
	}
	e := r.until(t, func(e SessionEvent) bool {
		return e.ID == id && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if e.User != "alice" {
		t.Fatalf("the session's events belong to %q, want alice", e.User)
	}
	// the sandbox is the person's: their home
	if _, err := m.AgentPrompt(context.Background(), id, "env"); err != nil {
		t.Fatal(err)
	}
	e = r.until(t, func(e SessionEvent) bool {
		return e.ID == id && e.Type == agent.EvMessageDelta && edata(e.Event)["role"] == "agent"
	})
	if text, _ := edata(e.Event)["text"].(string); !strings.Contains(text, "HOME="+HomeDir(r.root, "alice")) {
		t.Fatalf("the agent's home: %q", text)
	}
	m.Kill(id)

	// with a ticket: what the person approved, an explicit mode included
	info, code, err = m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "yolo", Delegate: approved})
	if err != nil || code != 200 || info.Mode != "yolo" || info.Origin != OriginAgent {
		t.Fatalf("approved, in yolo: %d %v %+v", code, err, info)
	}
	m.Kill(info.ID)
	// a preference may name a mode
	info, code, err = m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{Prefs: &delegation.StartPrefs{Mode: "yolo", API: true}}})
	if err != nil || code != 200 || info.Mode != "yolo" {
		t.Fatalf("a preference's mode: %d %v %+v", code, err, info)
	}
	m.Kill(info.ID)
	// the person's own sessions carry no origin
	info, code, err = m.OpenAgentWith(person, AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "yolo"})
	if err != nil || code != 200 || info.Origin != "" || m.AgentOrigin(info.ID) != "" {
		t.Fatalf("the person, in yolo: %d %v origin %q", code, err, info.Origin)
	}
	m.Kill(info.ID)
}

type sessionPolicy func(delegation.Subject, string) error

func (f sessionPolicy) TileAllows(s delegation.Subject) error                 { return f(s, "") }
func (f sessionPolicy) SessionAllows(s delegation.Subject, prov string) error { return f(s, prov) }
func (sessionPolicy) StartPrefs(string, string, string) (delegation.StartPrefs, bool) {
	return delegation.StartPrefs{}, false
}
