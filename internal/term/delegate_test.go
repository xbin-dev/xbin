package term

import (
	"context"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/users"
)

// A person's workspace agent (a delegate principal, auth/delegate.go) opens
// a tile agent session as that person: the person's gates, the person's
// directory and home, marked origin "agent" — and only in a mode that keeps
// asking the person, since a delegate never answers a permission request.
func TestAgentSessionAsDelegate(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	alice := &users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}}
	person := auth.Principal{UserID: "alice", User: alice, Via: "session"}
	delegate := auth.Principal{UserID: "alice", User: alice, Via: "delegate", Agent: "xbin/agent"}
	reader := &users.User{ID: "rita", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}}

	// the person's own gate: no terminal level, no session — for their agent too
	if _, code, err := m.OpenAgentWith(auth.Principal{UserID: "rita", User: reader, Via: "delegate", Agent: "xbin/agent"},
		AgentOpen{Cwd: "apps/x", Provider: "fake"}); code != 403 || err == nil || !strings.Contains(err.Error(), "terminal access") {
		t.Fatalf("a reader's agent: %d %v", code, err)
	}
	// modes that stop asking, settings and sandbox pickers are the person's
	for name, a := range map[string]AgentOpen{
		"an explicit mode": {Cwd: "apps/x", Provider: "fake", Mode: "yolo"},
		"another option":   {Cwd: "apps/x", Provider: "fake", Options: map[string]string{"mode": "yolo"}},
		"two options":      {Cwd: "apps/x", Provider: "fake", Options: map[string]string{"model": "fake-fast", "thinking": "high"}},
		"a VM sandbox":     {Cwd: "apps/x", Provider: "fake", VM: true},
		"a GPU":            {Cwd: "apps/x", Provider: "fake", GPU: "all"},
	} {
		if _, code, err := m.OpenAgentWith(delegate, a); code != 403 || err == nil || !strings.Contains(err.Error(), "workspace agent") {
			t.Errorf("%s: %d %v, want a 403 naming the workspace agent", name, code, err)
		}
		// …while the person picks them freely (the fake's yolo included)
	}
	if info, code, err := m.OpenAgentWith(person, AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "yolo"}); err != nil || code != 200 || info.Origin != "" {
		t.Fatalf("the person, in yolo: %d %v origin %q", code, err, info.Origin)
	} else {
		m.Kill(info.ID)
	}

	info, code, err := m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Name: "fix the build", Options: map[string]string{"model": "fake-fast"}})
	if err != nil || code != 200 {
		t.Fatalf("the delegate's open: %d %v", code, err)
	}
	if info.Origin != OriginAgent || info.Mode != "ask" || info.Kind != KindAgent || info.Name != "fix the build" {
		t.Fatalf("info: %+v", info)
	}
	id := info.ID
	// it is the person's session: in their directory, driven by them
	rows := m.ListFor(HomeKey(person), "apps/x", nil)
	found := false
	for _, row := range rows {
		found = found || row.ID == id && row.Origin == OriginAgent
	}
	if !found {
		t.Fatalf("the session isn't in alice's directory: %+v", rows)
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
	// the sandbox is the person's: their home, and the non-admin tier
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
}
