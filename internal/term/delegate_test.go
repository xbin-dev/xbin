package term

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
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
	approved := &DelegateStart{Approved: true, Provider: "fake", Mode: "ask"}
	prefs := &DelegateStart{Provider: "fake", Prefs: &delegation.StartPrefs{}}
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
	info, code, err = m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "yolo", Delegate: &DelegateStart{Approved: true, Provider: "fake", Mode: "yolo"}})
	if err != nil || code != 200 || info.Mode != "yolo" || info.Origin != OriginAgent {
		t.Fatalf("approved, in yolo: %d %v %+v", code, err, info)
	}
	if got := m.AgentOriginMode(info.ID); got != "yolo" {
		t.Fatalf("the mode the person approved it in: %q", got)
	}
	m.Kill(info.ID)
	// a preference may name a mode — one known to ask, checked here again
	// whatever the store handed over (the second review's N5), and only
	// for the harness the preference was asked about
	info, code, err = m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{Provider: "fake", Prefs: &delegation.StartPrefs{Mode: "ask", API: true}}})
	if err != nil || code != 200 || info.Mode != "ask" {
		t.Fatalf("a preference's mode: %d %v %+v", code, err, info)
	}
	// the mode it was started in stays the session's record, whatever its
	// person switches it to — across their restart of it too (the server
	// refuses the agent's messages to a session that has left it)
	r.until(t, func(e SessionEvent) bool {
		return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
	})
	if err := m.AgentSetOption(context.Background(), info.ID, "mode", "yolo"); err != nil {
		t.Fatalf("the person switches the mode: %v", err)
	}
	r.until(t, func(e SessionEvent) bool {
		return e.ID == info.ID && e.Type == agent.EvStatus && edata(e.Event)["currentMode"] == "yolo"
	})
	if si, _ := m.Info(info.ID); si.Mode != "yolo" || m.AgentOriginMode(info.ID) != "ask" {
		t.Fatalf("after the switch: mode %q, started in %q", si.Mode, m.AgentOriginMode(info.ID))
	}
	again, _, code, err := m.RestartAgent(person, info.ID, "", "", true, nil)
	if err != nil || code != 200 {
		t.Fatalf("the person's restart: %d %v", code, err)
	}
	if again.Mode != "yolo" || m.AgentOrigin(again.ID) != holder || m.AgentOriginMode(again.ID) != "ask" {
		t.Fatalf("after the restart: mode %q, origin %q, started in %q", again.Mode, m.AgentOrigin(again.ID), m.AgentOriginMode(again.ID))
	}
	if m.AgentOriginMode("nope") != "" {
		t.Fatal("an unknown session has a start mode")
	}
	m.Kill(again.ID)
	refused("a preference naming a mode that asks nothing", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{Provider: "fake", Prefs: &delegation.StartPrefs{Mode: "yolo", API: true}}}, "known to ask")
	// "keeps asking" is the harness's ask or plan mode and no other: a mode
	// that edits or runs on its own (auto here; Claude's acceptEdits and
	// auto, Codex's agent, Gemini's autoEdit) is not started unasked
	refused("a preference naming a mode that acts without asking each time", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{Provider: "fake", Prefs: &delegation.StartPrefs{Mode: "auto", API: true}}}, "known to ask")
	refused("a preference asked about another harness", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{Provider: "claude", Prefs: &delegation.StartPrefs{}}}, "preference is for claude")
	refused("a preference asked about no harness", delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Delegate: &DelegateStart{Prefs: &delegation.StartPrefs{}}}, "preference is for")
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

// What a ticket approves is the session the start AMOUNTS TO — the harness
// and mode a resumed session takes from its history entry included — and
// nothing else opens under it; no option moves the mode; a delegate resumes
// only sessions it started; a sealed component is no tile to start on.
// (Security review: a ticket's card said "asks before acting" while the
// history entry it resumed, or a mode-category option, made it a session
// that never asks.)
func TestDelegateStartIsWhatWasApproved(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	m.AgentPolicy = delegation.OpenPolicy{}
	const holder = "xbin/wsagent"
	t.Cleanup(auth.TestSealDelegate(holder))
	alice := &users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal, holder: users.LevelTerminal}}
	delegate := auth.Principal{UserID: "alice", User: alice, Via: "delegate", Agent: holder}
	seed := func(id, mode, by string) {
		dir := filepath.Join(r.root, "data", "agent-history", "alice", util.CompKey("apps/x"))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(historyFile{Meta: HistoryMeta{ID: id, Cwd: "apps/x", Provider: "fake", Mode: mode, ACPSessionID: "c-" + id, Loadable: true, Turns: 1,
			Created: "2026-01-01T00:00:00Z", Ended: "2026-01-01T00:01:00Z", Agent: by, Origin: originOf(by)}})
		if err := os.WriteFile(filepath.Join(dir, id+".json"), b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	seed("hers", "yolo", "")     // a session alice ran herself, in the mode that asks nothing
	seed("mine", "yolo", holder) // one the agent started, approved in yolo then
	refused := func(name string, a AgentOpen, want string) {
		t.Helper()
		info, code, err := m.OpenAgentWith(delegate, a)
		if err == nil {
			m.Kill(info.ID)
		}
		if code != 403 || err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %d %v (mode %q), want a 403 containing %q", name, code, err, info.Mode, want)
		}
	}
	ask := &DelegateStart{Approved: true, Provider: "fake", Mode: "ask"}

	// the effective start: a resumed session's harness and mode are its history's
	eff, _, err := m.DelegateEffective(delegate, AgentOpen{Cwd: "apps/x", Provider: "claude", Resume: "mine"})
	if err != nil || eff.Provider.ID != "fake" || eff.Mode != "yolo" || !eff.Resumes || eff.Tile != "apps/x" || eff.Provider.Safe(eff.Mode) {
		t.Fatalf("the effective start of a resume: %+v %v", eff, err)
	}
	if eff, _, err := m.DelegateEffective(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake"}); err != nil || eff.Mode != "ask" || eff.Resumes {
		t.Fatalf("the effective start: %+v %v", eff, err)
	}
	if _, code, err := m.DelegateEffective(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Resume: "hers"}); code != 403 || err == nil {
		t.Fatalf("asking to resume the person's own session: %d %v", code, err)
	}
	if _, code, _ := m.DelegateEffective(delegate, AgentOpen{Cwd: holder, Provider: "fake"}); code != 403 {
		t.Fatalf("asking to start on a sealed component: %d", code)
	}

	// approved as "ask": a body that resumes a yolo session is another session
	refused("a resume whose history mode isn't the approved one", AgentOpen{Cwd: "apps/x", Provider: "fake", Resume: "mine", Delegate: ask}, "exactly the session its person approved")
	refused("another mode than the approved one", AgentOpen{Cwd: "apps/x", Provider: "fake", Mode: "yolo", Delegate: ask}, "exactly the session its person approved")
	// the person's own past session: never, whatever the ticket says
	for _, d := range []*DelegateStart{ask, {Approved: true, Provider: "fake", Mode: "yolo"}, {Provider: "fake", Prefs: &delegation.StartPrefs{}}} {
		refused("resuming the person's own session", AgentOpen{Cwd: "apps/x", Provider: "fake", Resume: "hers", Delegate: d}, "resumes only the sessions it started")
	}
	refused("a sealed component", AgentOpen{Cwd: holder, Provider: "fake", Delegate: ask}, "system component") // (sessionDir answers before the delegate's own rule: no session opens on one at all)

	// no option moves the mode of a session the agent opens
	var cfgs []agent.Config
	onAgentConfig = func(c agent.Config) { cfgs = append(cfgs, c) }
	t.Cleanup(func() { onAgentConfig = nil })
	info, code, err := m.OpenAgentWith(delegate, AgentOpen{Cwd: "apps/x", Provider: "fake", Options: map[string]string{"mode": "yolo", "model": "fake-fast"}, Delegate: ask})
	if err != nil || code != 200 || info.Mode != "ask" {
		t.Fatalf("an approved start with options: %d %v %+v", code, err, info)
	}
	m.Kill(info.ID)
	person := auth.Principal{UserID: "alice", User: alice, Via: "session"}
	if info, _, err = m.OpenAgentWith(person, AgentOpen{Cwd: "apps/x", Provider: "fake"}); err != nil {
		t.Fatal(err)
	}
	m.Kill(info.ID)
	if !cfgs[0].Strict || cfgs[len(cfgs)-1].Strict { // the mode in force or no session; only a model or a thinking level set
		t.Fatalf("Strict: the agent's open %v, the person's %v", cfgs[0].Strict, cfgs[len(cfgs)-1].Strict)
	}
	if len(cfgs) != 2 || !cfgs[0].SkipModeOptions || cfgs[0].Mode != "ask" || cfgs[1].SkipModeOptions {
		t.Fatalf("SkipModeOptions: the agent's open %v, the person's %v", cfgs[0].SkipModeOptions, cfgs[len(cfgs)-1].SkipModeOptions)
	}
}

// A workspace agent's start hands the session its first prompt — xbind
// does, not the agent afterwards — and what the request left out is what
// the person's card showed.
func TestDelegateStartCarriesItsFirstPrompt(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	const holder = "xbin/wsagent"
	t.Cleanup(auth.TestSealDelegate(holder))
	alice := &users.User{ID: "alice", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}}
	delegate := auth.Principal{UserID: "alice", User: alice, Via: "delegate", Agent: holder}
	m.AgentPolicy = delegation.OpenPolicy{}
	// the request named no harness, mode or network: the approved card's
	open := AgentOpen{Cwd: "apps/x", Delegate: &DelegateStart{Approved: true, Provider: "fake", Mode: "ask"}}
	open.Provider = DelegateProvider(open)
	info, res, code, err := m.OpenAgentPrompted(context.Background(), delegate, open, "Fix the failing import test.")
	if err != nil || code != 200 || info.Provider != "fake" || info.Mode != "ask" || info.Origin != OriginAgent {
		t.Fatalf("the start: %d %v %+v", code, err, info)
	}
	defer m.Kill(info.ID)
	if res.Outcome == "" || res.Turn == 0 && res.ID == "" {
		t.Fatalf("the first prompt was neither sent nor held: %+v", res)
	}
	// no prompt: none is made up
	open.Provider = "fake"
	info2, res2, _, err := m.OpenAgentPrompted(context.Background(), delegate, open, "")
	if err != nil || res2 != (QueueResult{}) {
		t.Fatalf("a start without a prompt: %v %+v", err, res2)
	}
	m.Kill(info2.ID)
}
