package server

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

const hostHolder = "xbin/wsagent"

// hostServer: two admins and a person, the delegation store, the agent's
// holder, and the host's rule as *rule says.
func hostServer(t *testing.T, rule *HostAgentRule) (http.Handler, *Server, *users.Store, *int) {
	t.Helper()
	a, err := auth.Load(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	st, err := users.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range []users.User{{ID: "alice", Role: users.RoleAdmin}, {ID: "bob", Role: users.RoleUser}, {ID: "dave", Role: users.RoleAdmin}} {
		if _, err := st.Upsert(u, "pw-long-enough"); err != nil {
			t.Fatal(err)
		}
	}
	a.SetUsers(st)
	changed := 0
	s := &Server{Auth: a, Delegation: delegation.OpenStore(t.TempDir()),
		AgentHolder: func() string { return hostHolder }, HostAgent: func() HostAgentRule { return *rule },
		PeopleChanged: func() { changed++ }}
	return s.Handler(), s, st, &changed
}

func putWorkspace(h http.Handler, sid, body string) (int, string) {
	w := serve(h, frReq("PUT", "/api/xbin/delegate/workspace", body, sid))
	return w.Code, w.Body.String()
}

// The host's "false": agents locked off — the switch is written as the
// host's, reads say it is locked, and the API can't turn it on.
func TestHostAgentLockedOff(t *testing.T) {
	rule := HostAgentRule{Set: true, Enabled: false}
	h, s, _, _ := hostServer(t, &rule)
	s.ApplyHostAgent()
	ws := s.Delegation.Workspace()
	if ws.Enabled || ws.By != "host" || ws.At.IsZero() {
		t.Fatalf("the host's switch in force: %+v", ws)
	}
	alice := s.Auth.NewSession("alice", "10.0.0.1")
	if code, out := putWorkspace(h, alice, `{"enabled":true}`); code != http.StatusConflict || s.Delegation.Workspace().Enabled {
		t.Fatalf("an admin can't turn on what the host turned off: %d %s", code, out)
	}
	w := serve(h, frReq("GET", "/api/xbin/delegate/workspace", "", alice))
	var view struct {
		Locks map[string]struct {
			Locked bool
			Reason string
		}
	}
	_ = json.Unmarshal(w.Body.Bytes(), &view)
	if l := view.Locks["enabled"]; !l.Locked || l.Reason != hostLockReason || view.Locks["who"].Locked {
		t.Fatalf("GET /delegate/workspace locks: %s", w.Body.String())
	}
	w = serve(h, frReq("GET", "/api/xbin/delegate/state", "", alice))
	if !strings.Contains(w.Body.String(), `"locks":{"enabled":{"locked":true`) {
		t.Fatalf("GET /delegate/state carries the locks: %s", w.Body.String())
	}
}

// "admins": on, the defaults' entry gone, every admin account given an
// entry of its own — those there when the key appears and those made
// after — and nobody else; who is the host's.
func TestHostAgentAdmins(t *testing.T) {
	rule := HostAgentRule{Set: true, Enabled: true, Who: "admins"}
	h, s, st, changed := hostServer(t, &rule)
	if err := st.SetSealedEveryone(hostHolder, true); err != nil {
		t.Fatal(err)
	}
	s.ApplyHostAgent()
	if !s.Delegation.Workspace().Enabled {
		t.Fatal("on")
	}
	if all, _ := st.SealedGrants(hostHolder); all {
		t.Fatal("admins: the defaults' everyone entry goes")
	}
	for id, want := range map[string]bool{"alice": true, "dave": true, "bob": false} {
		if got := st.Given(id, hostHolder); got != want {
			t.Fatalf("%s given an agent: %v, want %v", id, got, want)
		}
	}
	if *changed == 0 {
		t.Fatal("access moved: people changed")
	}
	n := *changed
	s.ApplyHostAgent() // in force already: nothing is written, nobody is told
	if *changed != n {
		t.Fatal("a second apply changes nothing")
	}
	if _, err := st.Upsert(users.User{ID: "erin", Role: users.RoleAdmin}, "pw-long-enough"); err != nil {
		t.Fatal(err)
	}
	s.ApplyHostAgent() // (boot runs it on every users event)
	if !st.Given("erin", hostHolder) {
		t.Fatal("an admin made later gets one")
	}
	alice := s.Auth.NewSession("alice", "10.0.0.1")
	for _, body := range []string{`{"who":"everyone"}`, `{"who":"me"}`, `{"enabled":false}`} {
		if code, out := putWorkspace(h, alice, body); code != http.StatusConflict {
			t.Fatalf("%s: the host's — %d %s", body, code, out)
		}
	}
	if !s.Delegation.Workspace().Enabled {
		t.Fatal("still on")
	}
}

// "everyone": the defaults name the agent; an admin removing that is put
// back at the next apply.
func TestHostAgentEveryone(t *testing.T) {
	rule := HostAgentRule{Set: true, Enabled: true, Who: "everyone"}
	_, s, st, _ := hostServer(t, &rule)
	s.ApplyHostAgent()
	if all, _ := st.SealedGrants(hostHolder); !all || !st.Given("bob", hostHolder) {
		t.Fatal("everyone has one")
	}
	if err := st.SetSealedEveryone(hostHolder, false); err != nil {
		t.Fatal(err)
	}
	s.ApplyHostAgent()
	if all, _ := st.SealedGrants(hostHolder); !all {
		t.Fatal("the host's rule is put back")
	}
}

// true with no who: the switch is the host's, who is the admins' to choose;
// no rule at all: nothing is the host's.
func TestHostAgentSwitchOnly(t *testing.T) {
	rule := HostAgentRule{Set: true, Enabled: true}
	h, s, st, _ := hostServer(t, &rule)
	s.ApplyHostAgent()
	alice := s.Auth.NewSession("alice", "10.0.0.1")
	if code, out := putWorkspace(h, alice, `{"who":"me"}`); code != http.StatusOK || !st.Given("alice", hostHolder) {
		t.Fatalf("who stays the admins': %d %s", code, out)
	}
	if code, _ := putWorkspace(h, alice, `{"enabled":false}`); code != http.StatusConflict {
		t.Fatalf("the switch is the host's: %d", code)
	}

	rule = HostAgentRule{}
	if code, out := putWorkspace(h, alice, `{"enabled":false}`); code != http.StatusOK || s.Delegation.Workspace().Enabled {
		t.Fatalf("no rule: the admin's switch again: %d %s", code, out)
	}
	if l := s.agentLocks(); l != nil {
		t.Fatalf("no rule, no locks: %v", l)
	}
	s.ApplyHostAgent()
	if s.Delegation.Workspace().Enabled {
		t.Fatal("no rule: apply changes nothing")
	}
}
