package server

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentpolicy"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// What a conversation tells a tile agent goes to THAT session's model. A
// conversation that holds an org's data (its handle's taint) starts a tile
// agent only where the session's model source satisfies that org's model
// rule — and a dry run of a start tells the truth: whether this start
// would run, and whether it needs the person (final review, 3 and 6).
func TestTaintBindsAStart(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	alice := delegateCred("d-alice")
	start := func(body string, c ...cred) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, append([]cred{alice}, c...)...)
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	dry := func(body string) map[string]any {
		t.Helper()
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderDryRun, "1"))
		if rec.Code != 200 {
			t.Fatalf("dry run: %d %s", rec.Code, rec.Body.String())
		}
		return decode[map[string]any](t, rec)
	}
	ask := func(body string) (int, string) {
		in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": body})
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice)
		return rec.Code, rec.Body.String()
	}
	const onX = `{"cwd":"apps/x","kind":"agent","provider":"claude","prompt":"fix the bug"}`
	const onMail = `{"cwd":"apps/mail","kind":"agent","provider":"claude","prompt":"fix the bug"}`

	// a dry run of a start says what THIS start needs: the person, until
	// their preference lets it start unasked — and still the person when the
	// body picks a mode of its own
	if got := dry(onX); got["needsApproval"] != true || got["allowed"] != true || got["tile"] != "apps/x" {
		t.Fatalf("dry run with no preference: %v", got)
	}
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{} })
	if got := dry(onX); got["needsApproval"] != false || got["under"] != "preference" {
		t.Fatalf("dry run on a preference must say the start runs unasked: %v", got)
	}
	if got := dry(`{"cwd":"apps/x","kind":"agent","provider":"claude","mode":"bypassPermissions"}`); got["needsApproval"] != true {
		t.Fatalf("dry run of a start that names its own mode: %v", got)
	}

	// untainted: every gate passes (503: no bx in this fixture)
	if c, b := start(onX); c != 503 {
		t.Fatalf("an untainted start: %d %s", c, b)
	}
	// the conversation reads a Finance tile: it now holds Finance's data
	if rec := w.do(w.full, "GET", "/api/xbin/components/apps/mail", "", alice); rec.Code != 200 {
		t.Fatalf("read apps/mail: %d %s", rec.Code, rec.Body.String())
	}
	id, _ := testHandles.Load("d-alice")
	if h, _ := w.store.AgentSession("alice", dHolder, id.(string)); len(h.Tainted) != 1 || h.Tainted[0] != "fin" {
		t.Fatalf("the handle's taint: %v", h.Tainted)
	}
	// …so it can't brief a tile agent on a tile Finance's rule doesn't bind
	if c, b := start(onX); c != 403 || !strings.Contains(b, `"refusal":"taint"`) || !strings.Contains(b, "Finance allows private models only") {
		t.Fatalf("a Finance-tainted conversation started a tile agent outside Finance: %d %s", c, b)
	}
	if got := dry(onX); got["allowed"] != false || !strings.Contains(got["refusal"].(string), "Finance") {
		t.Fatalf("the dry run of that start: %v", got)
	}
	if c, b := ask(onX); c != 403 || !strings.Contains(b, "Finance") {
		t.Fatalf("asking the person to approve it (no approval makes it startable): %d %s", c, b)
	}
	// a ticket approved before the taint doesn't carry the brief past it either
	// (the check runs before the ticket is spent)
	// …while Finance's own tile holds its sessions to Finance's rule
	if c, b := start(onMail); c != 503 {
		t.Fatalf("a start on Finance's own tile: %d %s", c, b)
	}

	// where the agents policy holds coding agents to the LLM connector, the
	// start is judged by the models it may run on: the one it names (and
	// the connector's default, when there is one)
	w.s.Term.Credentials = func(user, tile, harness, choice string) term.CredentialRule {
		return term.CredentialRule{Use: "connector", Forced: true}
	}
	if c, b := start(`{"cwd":"apps/x","kind":"agent","provider":"claude","model":"local/llama","prompt":"fix the bug"}`); c != 503 {
		t.Fatalf("held to the connector, on a model Finance allows: %d %s", c, b)
	}
	if c, b := start(`{"cwd":"apps/x","kind":"agent","provider":"claude","model":"hosted/big","prompt":"fix the bug"}`); c != 403 || !strings.Contains(b, `"refusal":"taint"`) {
		t.Fatalf("held to the connector, on a hosted model: %d %s", c, b)
	}
	if c, b := start(onX); c != 403 || !strings.Contains(b, `"refusal":"taint"`) {
		t.Fatalf("held to the connector, and nothing says on which model: %d %s", c, b)
	}
	w.s.Term.Credentials = nil

	// a first prompt that is a harness command is nobody's to approve
	const slash = `{"cwd":"apps/mail","kind":"agent","provider":"claude","prompt":"  /model opus"}`
	if c, b := start(slash); c != 403 || !strings.Contains(b, `"refusal":"command"`) {
		t.Fatalf("a start whose first prompt is a harness command: %d %s", c, b)
	}
	if c, b := ask(slash); c != 403 || !strings.Contains(b, "its own commands") {
		t.Fatalf("asking for it: %d %s", c, b)
	}
	if got := dry(slash); got["allowed"] != false {
		t.Fatalf("its dry run: %v", got)
	}
}

// The rules for what a person's agent sends a session it started, one by
// one (delegateSends): the text, the session's current mode, its model
// source against the conversation's taint.
func TestDelegateSendRules(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	for text, refused := range map[string]bool{"/model opus": true, "  \n/compact": true, "/": true, "fix /etc/hosts": false, "a/b": false, "": false} {
		if got := harnessCommand(text) != ""; got != refused {
			t.Errorf("harnessCommand(%q): refused %v, want %v", text, got, refused)
		}
	}

	// a message to a session takes no approval only where its mode asks before every edit and command; in any
	// other — whatever mode its person approved it in — each text is theirs to approve (D251)
	claude := func(mode string) term.SessionInfo { return term.SessionInfo{Provider: "claude", Mode: mode} }
	for name, c := range map[string]struct {
		info    term.SessionInfo
		refused bool
	}{
		"asking":                       {claude("default"), false},
		"in plan":                      {claude("plan"), false},
		"in bypass":                    {claude("bypassPermissions"), true},
		"in accept edits":              {claude("acceptEdits"), true},
		"in auto (the default, D251)":  {claude("auto"), true},
		"a harness xbind doesn't know": {term.SessionInfo{Provider: "nope", Mode: "x"}, true},
	} {
		why := modeRefusal(c.info, "alice")
		if (why != "") != c.refused {
			t.Errorf("%s: %q, want refused %v", name, why, c.refused)
		}
		if c.refused && !strings.Contains(why, "goes to it only with their approval of that text") {
			t.Errorf("%s: the reason: %q", name, why)
		}
	}

	if got := sendTargetOf(term.SessionInfo{Cwd: "apps/x", LLM: "connector", LLMOffered: true, Model: "local/a"}); got != (sendTarget{tile: "apps/x", served: agentpolicy.ServedOffered}) {
		t.Errorf("an offered connector is not a held one, and its model isn't vouched for: %+v", got)
	}
	if got := sendTargetOf(term.SessionInfo{Cwd: "apps/x", LLM: "own", Model: "opus"}); got != (sendTarget{tile: "apps/x", served: agentpolicy.ServedOwn}) {
		t.Errorf("a session on the person's own sign-in: %+v", got)
	}

	// a running session nothing is known about is not one that is still to
	// start: on Finance's own tile it may have begun before Finance's rule
	if got := sendTargetOf(term.SessionInfo{Cwd: "apps/mail"}); got.served != agentpolicy.ServedUnsettled {
		t.Errorf("a running session with no word on who serves it: %+v", got)
	}
	h, err := w.store.NewAgentSession("alice", dHolder, nil, "local/a")
	if err != nil {
		t.Fatal(err)
	}
	p := auth.Principal{UserID: "alice", Via: "delegate", Agent: dHolder, AgentSession: h.ID}
	own, held := sendTarget{tile: "apps/x", served: agentpolicy.ServedOwn}, sendTarget{tile: "apps/x", served: agentpolicy.ServedConnector, model: "local/a"}
	if why := w.s.sendRefusal(p, own); why != "" {
		t.Fatalf("an untainted conversation: %q", why)
	}
	if err := w.store.TaintAgentSession("alice", dHolder, h.ID, []string{"fin", agentpolicy.WorkspaceID}); err != nil {
		t.Fatal(err)
	}
	if why := w.s.sendRefusal(p, own); !strings.Contains(why, "holds data of fin") || !strings.Contains(why, "Finance allows private models only") {
		t.Errorf("a Finance-tainted conversation, a session on the person's own sign-in: %q", why)
	}
	if why := w.s.sendRefusal(p, sendTarget{tile: "apps/x", served: agentpolicy.ServedConnector, model: "hosted/big"}); why == "" {
		t.Error("…a session on the connector's hosted model")
	}
	if why := w.s.sendRefusal(p, held); why != "" {
		t.Errorf("…a session held to the connector on a local model: %q", why)
	}
	if why := w.s.sendRefusal(p, sendTarget{tile: "apps/mail"}); why != "" {
		t.Errorf("…a session on Finance's own tile: %q", why)
	}
	if why := w.s.sendRefusal(p, sendTargetOf(term.SessionInfo{Cwd: "apps/mail"})); why == "" {
		t.Error("…a running session on Finance's tile that nothing holds to the connector")
	}
}

// End to end, with a real (scripted) tile agent: the agent starts a session
// on its person's preference and prompts it; then — a harness command, a
// conversation that has since read Finance's data, and a session its
// person switched to a mode that asks nothing — each prompt and queued
// message is refused with its reason, and the person's own still go
// through (final review, 3, 6 and 7).
func TestDelegateSendsLive(t *testing.T) {
	bx, fake := realAgentBins(t)
	t.Setenv(agent.FakeEnv, fake)
	w := delegateWorld(t)
	w.s.AgentWorld = fakeWorld{}
	w.s.Term.BxPath, w.s.Term.Tokens = bx, w.s.Auth
	if err := os.MkdirAll(filepath.Join(w.s.Term.Root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{} })

	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"fake"}`, alice)
	if rec.Code != 200 {
		t.Fatalf("the start on a preference: %d %s", rec.Code, rec.Body.String())
	}
	info := decode[term.SessionInfo](t, rec)
	// Kill only signals: the session writes its history under the world's
	// TempDir as it ends, so the cleanup waits for its teardown (the term
	// tests' endFake, c2f381e8) before TempDir's RemoveAll runs.
	t.Cleanup(func() {
		if err := w.s.Term.StopTileSessions("apps/x"); err != nil {
			t.Error(err)
		}
	})
	if info.Mode != "ask" {
		t.Fatalf("the session's mode %q", info.Mode)
	}
	// turns waits until n turns have ended and the session is idle: the
	// events say so, so nothing here depends on how fast the agent answers
	turns := 0
	idle := func() {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for {
			evs, _, _, err := w.s.Term.AgentEvents(info.ID, 0)
			if err != nil {
				t.Fatal(err)
			}
			ended := 0
			for _, e := range evs {
				if e.Type == agent.EvTurnEnd {
					ended++
				}
			}
			if si, ok := w.s.Term.Info(info.ID); ok && si.Status == agent.StatusIdle && ended >= turns {
				return
			}
			if time.Now().After(deadline) {
				si, _ := w.s.Term.Info(info.ID)
				t.Fatalf("the session never went idle after %d turns (%d ended): %+v", turns, ended, si)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	send := func(route, text string, c cred) (int, string) {
		t.Helper()
		body, _ := json.Marshal(map[string]string{"text": text})
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/"+route, string(body), c)
		if rec.Code == 200 {
			turns++ // a message that went in is a turn to wait out
		}
		return rec.Code, strings.TrimSpace(rec.Body.String())
	}
	both := func(what, text, refusal, reason string) {
		t.Helper()
		for _, route := range []string{"prompt", "queue"} {
			if c, b := send(route, text, alice); c != 403 || !strings.Contains(b, `"refusal":"`+refusal+`"`) || !strings.Contains(b, reason) {
				t.Errorf("%s, %s: %d %s — want 403 %s (%s)", what, route, c, b, refusal, reason)
			}
		}
	}

	idle()
	if c, b := send("prompt", "hello", alice); c != 200 {
		t.Fatalf("the agent prompts the session it started: %d %s", c, b)
	}
	idle()

	// 7: a harness command isn't a message
	both("a harness command", "/model big", "command", "its own commands")
	if c, b := send("prompt", "/review tests", person); c != 200 {
		t.Fatalf("the person's own command: %d %s", c, b)
	}
	idle()

	// 6: the person switches the session to a mode that asks nothing
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/options", `{"id":"mode","value":"yolo"}`, person); rec.Code != 200 {
		t.Fatalf("the person changes the mode: %d %s", rec.Code, rec.Body.String())
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if si, _ := w.s.Term.Info(info.ID); si.Mode == "yolo" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mode never changed")
		}
		time.Sleep(20 * time.Millisecond)
	}
	both("a session switched to yolo", "carry on", "mode", "goes to it only with their approval of that text")
	if c, b := send("prompt", "carry on", person); c != 200 {
		t.Fatalf("the person's own prompt in that mode: %d %s", c, b)
	}
	idle()
	// …and back: the agent's messages go again
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/options", `{"id":"mode","value":"ask"}`, person); rec.Code != 200 {
		t.Fatalf("back to ask: %d %s", rec.Code, rec.Body.String())
	}
	for deadline = time.Now().Add(10 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if si, _ := w.s.Term.Info(info.ID); si.Mode == "ask" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the mode never changed back")
		}
	}
	if c, b := send("prompt", "again", alice); c != 200 {
		t.Fatalf("the agent's prompt once the session asks again: %d %s", c, b)
	}
	idle()

	// 3: reading the session's output puts its tile's orgs in the taint…
	id, _ := testHandles.Load("d-alice")
	if rec := w.do(w.full, "GET", "/api/xbin/term/sessions/"+info.ID+"/events", "", alice); rec.Code != 200 {
		t.Fatalf("the agent reads its session: %d", rec.Code)
	}
	if h, _ := w.store.AgentSession("alice", dHolder, id.(string)); len(h.Tainted) != 1 || h.Tainted[0] != agentpolicy.WorkspaceID {
		t.Fatalf("the taint after reading the session: %v", h.Tainted)
	}
	// …and a conversation that then reads Finance's data can't tell this
	// session (no connector here: nothing says which model it calls)
	if rec := w.do(w.full, "GET", "/api/xbin/components/apps/mail", "", alice); rec.Code != 200 {
		t.Fatalf("read apps/mail: %d %s", rec.Code, rec.Body.String())
	}
	both("a Finance-tainted conversation", "here is what Finance's mail says", "taint", "Finance allows private models only")
	// nothing of the refused messages is held
	if items, err := w.s.Term.AgentUnqueue(info.ID, ""); err != nil || len(items) != 0 {
		t.Fatalf("held messages after the refusals: %v %v", items, err)
	}
}

// Administering the agent isn't having one: the admin role reads the
// sealed component (its system page) and gives no agent acting as the
// admin. Only an entry that names it does — theirs, an org's, the
// defaults' (final review, 6).
func TestAdminRoleIsNoAgent(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentHolder = func() string { return dHolder }
	us := w.s.Auth.Users
	gate := AgentGivenGate(DelegationGate(w.store, true, false), us.Given)
	w.s.Auth.SetDelegationGate(gate)
	w.s.SetDelegationGate(gate)
	alice := delegateCred("d-alice") // alice is an admin
	if u, ok := us.Get("alice"); !ok || !u.IsAdmin() {
		t.Fatalf("the fixture's alice is no admin: %+v", u)
	}
	state := func() map[string]any {
		t.Helper()
		rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", w.cookie("alice"))
		if rec.Code != 200 {
			t.Fatalf("state: %d %s", rec.Code, rec.Body.String())
		}
		return decode[map[string]any](t, rec)
	}
	// she reads the component, as she manages it — and has no agent
	if a, _ := us.Access("alice"); !a.CanReadTile(dHolder) || a.Given(dHolder) || us.Given("alice", dHolder) {
		t.Fatal("an admin with no entry: reads the component, wasn't given it")
	}
	if st := state(); st["access"] != false || st["agent"] != nil || st["who"] == nil {
		t.Fatalf("her state: %v", st)
	}
	rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", alice)
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"no-agent"`) || !strings.Contains(rec.Body.String(), "weren't given") {
		t.Fatalf("a delegate call for an admin who wasn't given an agent: %d %s", rec.Code, rec.Body.String())
	}
	if err := w.s.DelegationOff(dHolder, "alice"); err == nil {
		t.Fatal("the connector's holder rule lets her agent run a model turn")
	}
	// given by her own entry (who: me), she has one
	if code, msg := w.s.setAgentWho(auth.Principal{UserID: "alice"}, "me"); code != 0 {
		t.Fatalf("who: me: %d %s", code, msg)
	}
	if st := state(); st["access"] != true || st["agent"] != dHolder {
		t.Fatalf("her state after who: me: %v", st)
	}
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", alice); rec.Code != 200 {
		t.Fatalf("her agent's call: %d %s", rec.Code, rec.Body.String())
	}
	// …and by the defaults (who: everyone), as everyone is
	if err := us.SetUserTile("alice", dHolder, "none"); err != nil {
		t.Fatal(err)
	}
	if us.Given("alice", dHolder) {
		t.Fatal("an exact none entry still gives it")
	}
	if code, msg := w.s.setAgentWho(auth.Principal{UserID: "alice"}, "everyone"); code != 0 {
		t.Fatalf("who: everyone: %d %s", code, msg)
	}
	if !us.Given("bob", dHolder) || us.Given("nobody", dHolder) {
		t.Fatal("everyone: bob has one, an unknown id none")
	}
}

// A deleted account's delegation records aren't inherited by an account
// made later under the same id: not a handle, a standing approval, a
// ticket, the admin opt-in, the pause or the log (final review, 6).
func TestDeletedPersonLeavesNothingToTheNextOne(t *testing.T) {
	w := delegateWorld(t)
	us := w.s.Auth.Users
	bob := delegateCred("d-bob")
	r, err := w.store.NewRequest(delegation.Request{User: "bob", Agent: dHolder, Kind: delegation.KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x", ContentType: "application/json"}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.store.Decide("bob", r.ID, delegation.GrantAlways, delegation.ScopeTile, "session"); err != nil {
		t.Fatal(err)
	}
	if _, err := w.store.SetState("bob", func(st *delegation.State) { st.AdminChanges = true }); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/sessions/"+handleOfTest(t, "d-bob"), "", bob); rec.Code != 200 {
		t.Fatalf("bob's agent reads its session: %d %s", rec.Code, rec.Body.String())
	}
	if rows, _ := w.store.Audit.Read("bob", time.Time{}, 10); len(rows) == 0 {
		// a read of the agent's own session is no audited row: write one
		if err := w.store.Audit.Append(delegation.Call{User: "bob", Agent: dHolder, Method: "POST", Path: "/api/apps/x/a", Effect: true, Status: 200}); err != nil {
			t.Fatal(err)
		}
	}

	// the account is deleted, and one is made under the same id
	if _, err := us.Delete("bob"); err != nil {
		t.Fatal(err)
	}
	w.s.SweepDelegation() // what boot runs when people change (and the delete hook calls DelegationPersonGone itself)
	if _, err := us.Upsert(users.User{ID: "bob", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelRead}}, "a-new-password-for-bob"); err != nil {
		t.Fatal(err)
	}
	w.s.SweepDelegation()

	if aps, _ := w.store.Approvals("bob"); len(aps) != 0 {
		t.Errorf("the new bob inherited standing approvals: %+v", aps)
	}
	if reqs, _ := w.store.Requests("bob", false); len(reqs) != 0 {
		t.Errorf("…requests: %+v", reqs)
	}
	if hs := w.store.AgentSessions("bob", dHolder); len(hs) != 0 {
		t.Errorf("…agent sessions: %+v", hs)
	}
	if st := w.store.State("bob"); st.AdminChanges || st.Paused {
		t.Errorf("…the first bob's switches: %+v", st)
	}
	if rows, _ := w.store.Audit.Read("bob", time.Time{}, 10); len(rows) != 0 {
		t.Errorf("…the first bob's log: %d rows", len(rows))
	}
	// the first bob's handle is nobody's now
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/approvals", "", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"no-session"`) {
		t.Errorf("a call under the first bob's handle: %d %s", rec.Code, rec.Body.String())
	}
}

func handleOfTest(t *testing.T, tok string) string {
	t.Helper()
	id, ok := testHandles.Load(tok)
	if !ok {
		t.Fatalf("no handle for %s", tok)
	}
	return id.(string)
}

// A held agent does nothing: its calls and its model turns are refused,
// and what was approved before the hold isn't used after it — standing
// approvals are kept for when it clears (final review, 4).
func TestHeldAgentDoesNothing(t *testing.T) {
	w := delegateWorld(t)
	w.s.AgentHolder = func() string { return dHolder }
	held := ""
	w.s.AgentHeld = func() (string, string) { return held, "xbind's own reason" }
	gate := w.s.AgentHeldGate(DelegationGate(w.store, true, false))
	w.s.Auth.SetDelegationGate(gate)
	w.s.SetDelegationGate(gate)
	if err := w.s.Auth.Users.SetUserTile("bob", dHolder, "read"); err != nil { // bob was given an agent
		t.Fatal(err)
	}
	bob := delegateCred("d-bob")
	ticket := func() delegation.Request {
		t.Helper()
		r, err := w.store.NewRequest(delegation.Request{User: "bob", Agent: dHolder, Kind: delegation.KindCall, Method: "POST", Target: "/api/apps/x/a", Tile: "apps/x", ContentType: "application/json"}, []byte(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, _, err := w.store.Decide("bob", r.ID, delegation.GrantOnce, "", "session"); err != nil {
			t.Fatal(err)
		}
		return r
	}
	standing := ticket()
	if _, _, err := w.store.Decide("bob", ticketPending(t, w).ID, delegation.GrantAlways, delegation.ScopeTile, "session"); err != nil {
		t.Fatal(err)
	}
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", bob); rec.Code != 200 {
		t.Fatalf("before the hold: %d %s", rec.Code, rec.Body.String())
	}
	held = "locked"
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", bob); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"held"`) || strings.Contains(rec.Body.String(), "xbind's own reason") {
		t.Fatalf("a call while held: %d %s", rec.Code, rec.Body.String())
	}
	if err := w.s.DelegationOff(dHolder, "bob"); err == nil {
		t.Fatal("a model turn while held")
	}
	w.s.SweepDelegation()
	if got, _ := w.store.Request("bob", standing.ID); got.State != delegation.Expired {
		t.Fatalf("the ticket approved before the hold is %s", got.State)
	}
	if aps, _ := w.store.Approvals("bob"); len(aps) != 1 {
		t.Fatalf("standing approvals across a hold: %d, want the one kept", len(aps))
	}
	held = ""
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/state", "", bob); rec.Code != 200 {
		t.Fatalf("after the hold: %d %s", rec.Code, rec.Body.String())
	}
}

// ticketPending makes a pending request of bob's agent.
func ticketPending(t *testing.T, w *dWorld) delegation.Request {
	t.Helper()
	r, err := w.store.NewRequest(delegation.Request{User: "bob", Agent: dHolder, Kind: delegation.KindCall, Method: "POST", Target: "/api/apps/x/b", Tile: "apps/x", ContentType: "application/json"}, []byte(`{}`))
	if err != nil {
		t.Fatal(err)
	}
	return r
}
