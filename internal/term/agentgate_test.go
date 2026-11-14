package term

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/auth"
)

// covers D229 — the harness gate on agent sessions people start: a harness
// the tile's orgs don't allow is refused on create with the reason, a
// session already running keeps running (and can't restart, which would
// end it), and a past session of that harness can't be resumed.
func TestAgentHarnessGate(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	owner := auth.Principal{Owner: true}
	var blocked atomic.Bool
	var asked atomic.Int32
	reason := errors.New("Finance allows these coding agents only: claude (not fake)")
	m.AgentGate = func(p auth.Principal, tile, harness string) error {
		asked.Add(1)
		if tile != "apps/x" || harness != "fake" {
			t.Errorf("the gate was asked about %s on %s", harness, tile)
		}
		if blocked.Load() {
			return reason
		}
		return nil
	}
	idle := func(id string) {
		t.Helper()
		r.until(t, func(e SessionEvent) bool {
			return e.ID == id && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
		})
	}

	info, code, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "kept", "", nil)
	if err != nil || code != 200 || asked.Load() != 1 {
		t.Fatalf("open while allowed: %d %v (asked %d)", code, err, asked.Load())
	}
	idle(info.ID)
	if _, err := m.AgentPrompt(context.Background(), info.ID, "one turn"); err != nil {
		t.Fatal(err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == agent.EvTurnEnd })

	// the org tightens its list: nothing new of that harness opens…
	blocked.Store(true)
	if _, code, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "", "", nil); code != 403 || err == nil || err.Error() != reason.Error() {
		t.Fatalf("create of a disallowed harness: %d %v", code, err)
	}
	// …the running session is untouched: it still takes a prompt…
	if _, ok := m.Info(info.ID); !ok {
		t.Fatal("the running session was ended")
	}
	if _, err := m.AgentPrompt(context.Background(), info.ID, "still here"); err != nil {
		t.Fatalf("a prompt to the running session: %v", err)
	}
	r.until(t, func(e SessionEvent) bool { return e.ID == info.ID && e.Type == agent.EvTurnEnd })
	// …and a restart, which would end it and open another, is refused first
	if _, _, code, err := m.RestartAgent(owner, info.ID, "none", "", true, nil); code != 403 || err == nil ||
		!strings.Contains(err.Error(), "keeps running") || !strings.Contains(err.Error(), reason.Error()) {
		t.Fatalf("restart of a disallowed harness: %d %v", code, err)
	}
	if _, ok := m.Info(info.ID); !ok {
		t.Fatal("the refused restart ended the session")
	}

	// its history can't be resumed while the harness is disallowed, and says why
	m.Kill(info.ID)
	waitClose(t, r.change, "close:"+info.ID)
	if hist := m.ListHistory(OwnHistory("owner"), "apps/x", nil); len(hist) != 1 || !hist[0].Loadable {
		t.Fatalf("history: %+v", hist)
	}
	if _, code, err := m.OpenAgent(owner, "apps/x", "", "", "", "", info.ID, nil); code != 403 || err == nil ||
		!strings.Contains(err.Error(), "can't be resumed") || !strings.Contains(err.Error(), reason.Error()) {
		t.Fatalf("resume of a disallowed harness: %d %v", code, err)
	}
	if _, _, err := m.ReadHistory(OwnHistory("owner"), info.ID); err != nil {
		t.Fatalf("the transcript stays readable: %v", err)
	}
	// allowed again: it resumes
	blocked.Store(false)
	again, code, err := m.OpenAgent(owner, "apps/x", "", "", "", "", info.ID, nil)
	if err != nil || code != 200 || again.Name != "kept" {
		t.Fatalf("resume once allowed: %d %v", code, err)
	}
	m.Kill(again.ID)
	waitClose(t, r.change, "close:"+again.ID)
}

// covers D229 — a session says who started it: nothing for its person,
// origin "agent" for one their workspace agent opened — on the directory
// row and on the history entry, which also keeps the component. Only
// xbind marks a session (the delegation package, at open); here the mark
// is put on a live session directly.
func TestAgentSessionOrigin(t *testing.T) {
	r := newAgentRig(t)
	m := r.m
	owner := auth.Principal{Owner: true}
	idle := func(id string) {
		t.Helper()
		r.until(t, func(e SessionEvent) bool {
			return e.ID == id && e.Type == agent.EvStatus && edata(e.Event)["status"] == agent.StatusIdle
		})
	}
	mine, _, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "", "", nil)
	if err != nil || mine.Origin != "" {
		t.Fatalf("a person's own session: %+v %v", mine, err)
	}
	idle(mine.ID)
	theirs, _, err := m.OpenAgent(owner, "apps/x", "", "fake", "", "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	idle(theirs.ID)
	m.mu.Lock()
	m.sessions[theirs.ID].origin = "xbin/agent" // as an open by the person's workspace agent leaves it
	m.mu.Unlock()
	for _, row := range m.ListFor("owner", "apps/x", nil) {
		if want := map[string]string{mine.ID: "", theirs.ID: OriginAgent}[row.ID]; row.Origin != want {
			t.Errorf("the directory row of %s: origin %q, want %q", row.ID, row.Origin, want)
		}
	}
	if info, _ := m.Info(theirs.ID); info.Origin != "agent" {
		t.Errorf("Info: %+v", info)
	}
	for _, id := range []string{mine.ID, theirs.ID} {
		if _, err := m.AgentPrompt(context.Background(), id, "work"); err != nil {
			t.Fatal(err)
		}
		r.until(t, func(e SessionEvent) bool { return e.ID == id && e.Type == agent.EvTurnEnd })
		m.Kill(id)
	}
	waitClose(t, r.change, "close:"+theirs.ID)
	m.FlushAgents()
	got := map[string]HistoryMeta{}
	for _, h := range m.ListHistory(OwnHistory("owner"), "apps/x", nil) {
		got[h.ID] = h
	}
	if h := got[theirs.ID]; h.Origin != "agent" || h.Agent != "xbin/agent" {
		t.Errorf("the agent-started session's history entry: %+v", h)
	}
	if h, ok := got[mine.ID]; !ok || h.Origin != "" || h.Agent != "" {
		t.Errorf("the person's own session's history entry: %+v (%v)", h, ok)
	}
}
