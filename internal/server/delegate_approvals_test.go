package server

// D250 — one approval, one place (plans/wsagent/approvals-ux.md): the
// person's pick on the approval is the approval; the ticket binds what the
// agent chose; a refusal says what differed; a follow-up continues the
// session that is there; a message to a session that acts without asking
// is an approval. Each safety rule of the spec has a test here, shown red
// without its rule (the run log is in the commit).

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentprefs"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
)

// startBody is a start the agent files for tile with brief, and whatever
// else it names (its suggestion).
func startBody(tile, brief string, kv ...string) string {
	m := map[string]any{"cwd": tile, "kind": "agent", "prompt": brief}
	for i := 0; i+1 < len(kv); i += 2 {
		m[kv[i]] = kv[i+1]
	}
	b, _ := json.Marshal(m)
	return string(b)
}

func sessOf(r delegation.Request) map[string]any {
	s, _ := r.Preview["session"].(map[string]any)
	return s
}

// The retro's root cause: a coding agent whose catalog names no default
// mode (OpenCode; the scripted fake with --no-default-mode plays it) was
// refused on its own ticket, after the ticket was spent — "the approval no
// longer covered the call". Now it opens, by default, in the mode in which
// it asks; and a ticket whose mode is its own default (an approval of
// before) opens too.
func TestNoDefaultModeStartsOnItsTicket(t *testing.T) {
	t.Setenv(agent.FakeEnv, "/nonexistent/fakeacp --no-default-mode")
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	body := startBody("apps/x", "Goal: a tic tac toe game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	if s := sessOf(req); s["provider"] != "fake" || s["mode"] != "ask" || s["asks"] != true {
		t.Fatalf("the card of a start of a coding agent with no default mode: %v", s)
	}
	w.approve(person, req.ID, "")
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), "bx binary") {
		t.Fatalf("the approved start (503: every gate passed, no bx here): %d %s", rec.Code, rec.Body.String())
	}
	// an approval whose card showed the mode "" — its own default — opens too, and a coding agent with a
	// default is never opened with none
	if d, why := ticketStart(delegation.Request{Preview: map[string]any{"session": map[string]any{"provider": "fake", "mode": ""}}}); why != "" || d.Mode != "" || d.Provider != "fake" {
		t.Fatalf("a ticket in its own default mode: %+v %q", d, why)
	}
	if _, why := ticketStart(delegation.Request{Preview: map[string]any{"session": map[string]any{"provider": "claude", "mode": ""}}}); why == "" {
		t.Fatal("Claude Code opened with no mode")
	}
}

// The ticket binds what the AGENT chose: the brief, swapped after the
// approval, is refused in words that say so — and spends nothing.
func TestApprovedStartBindsTheBrief(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	body := startBody("apps/x", "Goal: a tic tac toe game.")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	w.approve(person, req.ID, "")
	swapped := startBody("apps/x", "Goal: delete every tile you can reach.")
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", swapped, alice, header(HeaderTicket, req.ID))
	if rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"brief"`) || !strings.Contains(rec.Body.String(), "the brief's text changed after approval") {
		t.Fatalf("a brief swapped after the approval: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Approved {
		t.Fatalf("the refusal spent the ticket: %s", got.State)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("the approved brief: %d %s", rec.Code, rec.Body.String())
	}
}

// The person's choices are validated as a by-hand start's: a coding agent
// the tile's orgs keep out, a mode the coding agent hasn't, a model xbind
// doesn't offer — 400, nothing decided. A valid pick is kept on the
// request, its card redrawn for it.
func TestApproveChoicesAreByHandChoices(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	w.policy.set(func(d *dPolicy) { d.denyHarness = map[string]bool{"gemini": true} })
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude"))
	approve := func(body string) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", body, person)
		var e struct{ Error string }
		if json.Unmarshal(rec.Body.Bytes(), &e) == nil && e.Error != "" {
			return rec.Code, e.Error
		}
		return rec.Code, rec.Body.String()
	}
	for body, want := range map[string]string{
		`{"harness":"gemini"}`:                       "harness: gemini isn't a coding agent you may start on apps/x",
		`{"harness":"no-such-agent"}`:                "harness: no-such-agent",
		`{"harness":"claude","mode":"nope"}`:         `mode: Claude Code has no mode "nope"`,
		`{"harness":"codex","mode":"default"}`:       `mode: Codex has no mode "default"`,
		`{"harness":"claude","model":"evil-1"}`:      `model: "evil-1" isn't a model offered for Claude Code`,
		`{"model":"evil-2"}`:                         `model: "evil-2" isn't a model offered`,
		`{"harness":"claude","remember":true,"x":1}`: "need {grant?",
	} {
		if c, b := approve(body); c != 400 || !strings.Contains(b, want) {
			t.Errorf("%s: %d %s", body, c, b)
		}
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Pending {
		t.Fatalf("a refused pick decided the request: %s", got.State)
	}
	// the agent's own credential can't pick for its person
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"harness":"codex"}`, alice); rec.Code != 403 && rec.Code != 404 {
		t.Fatalf("the agent approved with a pick: %d", rec.Code)
	}
	if c, b := approve(`{"harness":"codex","mode":"read-only","remember":true}`); c != 200 {
		t.Fatalf("a valid pick: %d %s", c, b)
	}
	got, _ := w.store.Request("alice", req.ID)
	s := sessOf(got)
	if got.Chosen == nil || got.Chosen.Provider != "codex" || got.Chosen.Mode != "read-only" || s["provider"] != "codex" || s["asks"] != true || fmt.Sprint(s["from"]) != "map[harness:you mode:you model:you]" {
		t.Fatalf("the approved pick: %+v, card %v", got.Chosen, s)
	}
	if ts := w.s.AgentPrefs.Get("alice").AgentSessions.Tiles["apps/x"]; ts.Harness != "codex" || ts.Mode != "read-only" {
		t.Fatalf("remember for this tile: %+v", ts)
	}
	if d, _ := ticketStart(got); d == nil || d.Provider != "codex" || d.Mode != "read-only" || !d.Picked {
		t.Fatalf("the ticket's start: %+v", d)
	}
}

// The agent's suggestion never selects a mode that doesn't ask, nor a
// sign-in; the person's preference for the tile outranks the suggestion.
func TestSuggestionNeverPicksANonAskingModeOrSignin(t *testing.T) {
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	for _, mode := range []string{"acceptEdits", "auto", "bypassPermissions"} {
		r := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude", "mode", mode))
		if s := sessOf(r); s["mode"] != "default" || s["asks"] != true || s["modeNotTaken"] != mode || fmt.Sprint(s["suggested"]) != "map[harness:claude mode:"+mode+"]" {
			t.Errorf("suggested %s: the card %v", mode, s)
		}
		// approved as shown (an older client: no pick), it opens in the asking mode
		w.approve(w.cookie("alice"), r.ID, "")
		got, _ := w.store.Request("alice", r.ID)
		if d, _ := ticketStart(got); d == nil || d.Mode != "default" {
			t.Errorf("suggested %s: the ticket's start %+v", mode, d)
		}
	}
	// an asking suggestion is taken, below the person's own choice for the tile
	if s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude", "mode", "plan"))); s["mode"] != "plan" || fmt.Sprint(s["from"]) != "map[harness:agent mode:agent model:default]" {
		t.Errorf("an asking suggestion: %v", s)
	}
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "codex", Mode: "read-only"}, 1); err != nil {
		t.Fatal(err)
	}
	if s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude", "mode", "plan"))); s["provider"] != "codex" || s["mode"] != "read-only" || fmt.Sprint(s["from"]) != "map[harness:tile mode:tile model:default]" {
		t.Errorf("the person's choice for the tile outranks the suggestion: %v", s)
	}
	// a sign-in is never the agent's to name
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/term/sessions", "body": startBody("apps/x", "go", "signin", "si_1")})
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice); rec.Code == 200 {
		if r := decode[delegation.Request](t, rec); r.ID != "" {
			w.approve(w.cookie("alice"), r.ID, "")
			if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "signin", "si_1"), alice, header(HeaderTicket, r.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), "doesn't pick a saved sign-in") {
				t.Fatalf("a start naming a sign-in: %d %s", rec.Code, rec.Body.String())
			}
		}
	}
}

// Starting without asking is unchanged: the person's preference — and now
// their choice for the tile — picks the coding agent; a start that names
// another needs their approval; the mode is only ever the preference's,
// one that asks; never a sign-in.
func TestUnaskedStartIgnoresSuggestion(t *testing.T) {
	w := delegateWorld(t)
	alice := delegateCred("d-alice")
	w.policy.set(func(d *dPolicy) { d.prefs["alice"] = delegation.StartPrefs{Mode: "read-only", API: true} })
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "codex", Mode: "agent"}, 1); err != nil { // a mode that doesn't ask: the sheet's preselection only
		t.Fatal(err)
	}
	create := func(body string) (int, string) {
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice)
		return rec.Code, rec.Body.String()
	}
	if c, b := create(startBody("apps/x", "go", "provider", "claude")); c != 403 || !strings.Contains(b, "your person's choice for apps/x is codex") {
		t.Fatalf("an unasked start naming another coding agent than the person's: %d %s", c, b)
	}
	if c, b := create(startBody("apps/x", "go")); c != http.StatusServiceUnavailable {
		t.Fatalf("an unasked start naming none (the person's codex): %d %s", c, b)
	}
	if c, b := create(startBody("apps/x", "go", "mode", "agent")); c != 403 {
		t.Fatalf("an unasked start naming a mode: %d %s", c, b)
	}
	rows, _ := w.store.Audit.Read("alice", time.Time{}, 0)
	if len(rows) == 0 || rows[0].Under != "preference" {
		t.Fatalf("audit: %+v", rows[0])
	}
	// the dry run agrees with the start
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go", "provider", "claude"), alice, header(HeaderDryRun, "1"))
	if !strings.Contains(rec.Body.String(), `"needsApproval":true`) {
		t.Fatalf("the dry run of a start naming another coding agent: %s", rec.Body.String())
	}
}

// What the person keeps for the tile is theirs: another person sees none of
// it, the agent reads its own person's chosen line and writes nothing.
func TestStartPrefsArePersonal(t *testing.T) {
	w := delegateWorld(t)
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "codex"}, 1); err != nil {
		t.Fatal(err)
	}
	read := func(c cred) map[string]any {
		rec := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", c)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		ch, _ := out["chosen"].(map[string]any)
		return ch
	}
	if ch := read(delegateCred("d-alice")); ch["harness"] != "codex" || fmt.Sprint(ch["from"]) != "map[harness:tile mode:default model:default]" {
		t.Fatalf("alice's agent reads her choice for apps/x: %v", ch)
	}
	if _, err := w.s.Auth.Users.Upsert(users.User{ID: "dave", Role: users.RoleUser, Tiles: map[string]string{"apps/x": users.LevelTerminal}}, ""); err != nil {
		t.Fatal(err)
	}
	if ch := read(delegateCred("d-dave")); ch["harness"] == "codex" {
		t.Fatalf("dave's agent reads alice's choice: %v", ch)
	}
	// the agent can't write it: /agent/prefs is its person's own
	if rec := w.do(w.full, "PUT", "/api/xbin/agent/prefs", `{"agentSessions":{"harness":"claude"}}`, delegateCred("d-alice")); rec.Code != 403 {
		t.Fatalf("the agent set its person's preference: %d %s", rec.Code, rec.Body.String())
	}
	if w.s.AgentPrefs.Get("alice").AgentSessions.Harness != "" {
		t.Fatal("the agent's write landed")
	}
}

// A start that failed to open for a reason that isn't the agent's is said
// so, and the person tries it again — the agent files nothing new.
func TestFailedStartRetryRoute(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	body := startBody("apps/x", "go")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	w.approve(person, req.ID, "")
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID))
	if rec.Code != http.StatusServiceUnavailable || !strings.Contains(rec.Body.String(), `"refusal":"start-failed"`) || !strings.Contains(rec.Body.String(), `"retry":true`) ||
		!strings.Contains(rec.Body.String(), "didn't start: agent sessions need the bx binary") {
		t.Fatalf("a start that failed to open: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", req.ID); got.State != delegation.Failed || got.Failure == nil || !got.Failure.Retry {
		t.Fatalf("the request: %+v", got)
	}
	// the agent can't use it again, nor try it again itself
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"failed"`) {
		t.Fatalf("the agent's second use: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/retry", "", alice); rec.Code != 403 {
		t.Fatalf("the agent tried it again: %d", rec.Code)
	}
	// listed for the person (?failed=1), and tried again by them
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/requests?failed=1", "", person); !strings.Contains(rec.Body.String(), req.ID) {
		t.Fatalf("the failed start isn't listed: %s", rec.Body.String())
	}
	if rec := w.do(w.full, "GET", "/api/xbin/delegate/requests", "", person); strings.Contains(rec.Body.String(), req.ID) {
		t.Fatalf("an older client's list holds a failed start: %s", rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/retry", "", person); rec.Code != 200 {
		t.Fatalf("Try again: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID)); rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("the start, tried again: %d %s", rec.Code, rec.Body.String())
	}
}

// An older client approving a start sends no choices: today's defaults,
// as shown; one that doesn't know "continue" approves a new session.
func TestOlderClientApprovals(t *testing.T) {
	w := delegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "go"))
	before := sessOf(req)
	w.approve(person, req.ID, "once")
	got, _ := w.store.Request("alice", req.ID)
	if got.Chosen != nil || sessOf(got)["provider"] != before["provider"] || sessOf(got)["mode"] != before["mode"] {
		t.Fatalf("an approval with no pick: %+v %v", got.Chosen, sessOf(got))
	}
	// a request that offers to continue (its card's continue), approved without knows: a new session
	got.Preview["session"].(map[string]any)["continue"] = map[string]any{"session": "s-1", "provider": "claude"}
	if d, _ := ticketStart(got); d.Continue != "" {
		t.Fatalf("continued for a client that never showed it: %+v", d)
	}
}

// ---- with real sessions: bx and the scripted coding agent -------------------------------------

// realDelegateWorld is delegateWorld with bx and the scripted coding agent:
// sessions open for real on apps/x (unsandboxed, as the term tests run
// them). A stand-in leads PATH for every real coding agent's CLI.
func realDelegateWorld(t *testing.T, fakeArgs ...string) *dWorld {
	t.Helper()
	bx, fake := realAgentBins(t)
	t.Setenv(agent.FakeEnv, strings.Join(append([]string{fake}, fakeArgs...), " "))
	stand, _ := filepath.Abs("../../hack/ui-harness/fakebin")
	t.Setenv("PATH", stand+string(os.PathListSeparator)+os.Getenv("PATH")) // never a real coding agent on this host
	w := delegateWorld(t)
	w.s.Term.BxPath = bx
	w.s.Term.Tokens = &recTokens{a: w.s.Auth}
	if err := os.MkdirAll(filepath.Join(w.s.Term.Root, "apps", "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, si := range w.s.Term.ListFor("alice", "", nil) {
			w.s.Term.Kill(si.ID)
		}
	})
	return w
}

// waitIdle waits for a session to end its turn.
func waitIdle(t *testing.T, w *dWorld, id string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := w.s.Term.Info(id); ok && info.Status == "idle" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	info, _ := w.s.Term.Info(id)
	t.Fatalf("session %s never idle: %+v", id, info)
}

// The person's pick is what opens: their coding agent, mode and model,
// whatever the agent suggested — and what ran is what the agent is told
// (the session's row).
func TestPersonsPickIsTheStart(t *testing.T) {
	w := realDelegateWorld(t, "--auto-mode")
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	if _, err := w.s.AgentPrefs.Update("alice", []byte(`{"harnesses":{"fake":{"model":"fake-fast"}}}`)); err != nil {
		t.Fatal(err)
	}
	body := startBody("apps/x", "Goal: a tic tac toe game.", "provider", "claude", "mode", "acceptEdits")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"harness":"fake","mode":"auto","model":"fake-fast"}`, person); rec.Code != 200 {
		t.Fatalf("the person's pick: %d %s", rec.Code, rec.Body.String())
	}
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID))
	var info struct{ ID, Provider, Mode, Model, Origin string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatalf("the approved start: %d %s", rec.Code, rec.Body.String())
	}
	if info.Provider != "fake" || info.Mode != "auto" || info.Origin != "agent" {
		t.Fatalf("what opened: %+v (the agent suggested claude in acceptEdits; the person picked fake, auto, fake-fast)", info)
	}
	waitIdle(t, w, info.ID)
	if got, _ := w.s.Term.Info(info.ID); got.Model != "fake-fast" {
		t.Fatalf("the model: %+v", got)
	}
}

// A message to a session that acts without asking is an approval: the
// person's approval sends exactly that text, once, to that session — and
// is refused when the session's mode changed since.
func TestMessageApproval(t *testing.T) {
	w := realDelegateWorld(t, "--auto-mode")
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	body := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", body)
	w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"harness":"fake","mode":"ask"}`, person)
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", body, alice, header(HeaderTicket, req.ID))
	var info struct{ ID string }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &info) != nil {
		t.Fatalf("the start: %d %s", rec.Code, rec.Body.String())
	}
	waitIdle(t, w, info.ID)
	// the retro's case: the person switches the session to a mode that doesn't ask
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/options", `{"id":"mode","value":"auto"}`, person); rec.Code != 200 {
		t.Fatalf("the person's mode switch: %d %s", rec.Code, rec.Body.String())
	}
	path := "/api/xbin/term/sessions/" + info.ID + "/queue"
	msg := `{"text":"Fix the stray O after a Reset."}`
	// unapproved: refused, as ever
	if rec := w.do(w.full, "POST", path, msg, alice); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"refusal":"mode"`) {
		t.Fatalf("an unapproved message to a session that doesn't ask: %d %s", rec.Code, rec.Body.String())
	}
	in, _ := json.Marshal(map[string]string{"method": "POST", "path": path, "body": msg, "summary": "the fix"})
	rec = w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice)
	m := decode[delegation.Request](t, rec)
	shown, _ := m.Preview["message"].(map[string]any)
	if rec.Code != 200 || m.Kind != delegation.KindMessage || shown["text"] != "Fix the stray O after a Reset." || shown["asks"] != false || shown["provider"] != "fake" || shown["mode"] != "auto" {
		t.Fatalf("the message request: %d %+v", rec.Code, m)
	}
	w.approve(person, m.ID, "")
	// another text under the ticket: refused, nothing spent
	if rec := w.do(w.full, "POST", path, `{"text":"rm -rf everything"}`, alice, header(HeaderTicket, m.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), "the message's text changed after approval") {
		t.Fatalf("another text: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", path, msg, alice, header(HeaderTicket, m.ID)); rec.Code != 200 {
		t.Fatalf("the approved message: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", path, msg, alice, header(HeaderTicket, m.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), "an approval runs once") {
		t.Fatalf("the approved message again: %d %s", rec.Code, rec.Body.String())
	}
	waitIdle(t, w, info.ID)
	// approved, then the person switches the session's mode: not what they approved
	m2 := decode[delegation.Request](t, w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in), alice))
	w.approve(person, m2.ID, "")
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/options", `{"id":"mode","value":"yolo"}`, person); rec.Code != 200 {
		t.Fatalf("the person's mode switch: %d %s", rec.Code, rec.Body.String())
	}
	if rec := w.do(w.full, "POST", path, msg, alice, header(HeaderTicket, m2.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"session"`) || !strings.Contains(rec.Body.String(), `not fake in mode \"auto\" as approved`) {
		t.Fatalf("a message approved before the mode changed: %d %s", rec.Code, rec.Body.String())
	}
	// a session that asks needs no approval: the request is refused
	in2, _ := json.Marshal(map[string]string{"method": "POST", "path": path, "body": msg})
	w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/options", `{"id":"mode","value":"ask"}`, person)
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests", string(in2), alice); rec.Code != 400 || !strings.Contains(rec.Body.String(), "needs no approval") {
		t.Fatalf("a message request to a session that asks: %d %s", rec.Code, rec.Body.String())
	}
}

// "Continue the session that is there" reaches only a live session of the
// person's that this agent started, on the same tile; approved, the start
// sends its brief there instead of opening another.
func TestContinueOnlyOwnSessionSameTile(t *testing.T) {
	w := realDelegateWorld(t)
	alice, person := delegateCred("d-alice"), w.cookie("alice")
	first := startBody("apps/x", "Goal: a game.", "provider", "fake")
	req := w.ask(alice, "POST", "/api/xbin/term/sessions", first)
	w.do(w.full, "POST", "/api/xbin/delegate/requests/"+req.ID+"/approve", `{"harness":"fake","mode":"ask"}`, person)
	rec := w.do(w.full, "POST", "/api/xbin/term/sessions", first, alice, header(HeaderTicket, req.ID))
	var a struct{ ID string }
	_ = json.Unmarshal(rec.Body.Bytes(), &a)
	waitIdle(t, w, a.ID)
	// the person's own session there: never offered
	own := w.do(w.full, "POST", "/api/xbin/term/sessions", `{"cwd":"apps/x","kind":"agent","provider":"fake","mode":"ask"}`, person)
	var o struct{ ID string }
	_ = json.Unmarshal(own.Body.Bytes(), &o)
	waitIdle(t, w, o.ID)
	for name, sid := range map[string]string{"the person's own": o.ID, "no such": "nope"} {
		if s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "next", "provider", "fake", "continue", sid))); s["continue"] != nil {
			t.Errorf("%s session offered to continue: %v", name, s["continue"])
		}
	}
	// its own session on the tile: offered — even where the person's choice for the tile is another coding agent,
	// which a NEW session would run — and approved by a client that knows it, continued on its own coding agent
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "codex"}, 1); err != nil {
		t.Fatal(err)
	}
	next := startBody("apps/x", "Goal: add a computer player.", "continue", a.ID)
	r2 := w.ask(alice, "POST", "/api/xbin/term/sessions", next)
	if c, _ := sessOf(r2)["continue"].(map[string]any); c == nil || c["session"] != a.ID || c["provider"] != "fake" || sessOf(r2)["provider"] != "codex" {
		t.Fatalf("the agent's own idle session isn't offered: %v", sessOf(r2))
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r2.ID+"/approve", `{"knows":["continue"]}`, person); rec.Code != 200 {
		t.Fatalf("approve continue: %d %s", rec.Code, rec.Body.String())
	}
	rec = w.do(w.full, "POST", "/api/xbin/term/sessions", next, alice, header(HeaderTicket, r2.ID))
	var c struct {
		ID        string
		Continued bool
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &c) != nil || !c.Continued || c.ID != a.ID {
		t.Fatalf("the continued start: %d %s", rec.Code, rec.Body.String())
	}
	if n := len(w.s.Term.ListFor("alice", "", nil)); n != 2 {
		t.Fatalf("a second session of the agent's opened: %d sessions", n)
	}
	// approved, and the session ends before it is used: refused, nothing spent
	waitIdle(t, w, a.ID)
	r3 := w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: polish.", "provider", "fake", "continue", a.ID))
	w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r3.ID+"/approve", `{"knows":["continue"]}`, person)
	w.s.Term.Kill(a.ID)
	deadline := time.Now().Add(10 * time.Second)
	for _, live := w.s.Term.Info(a.ID); live && time.Now().Before(deadline); _, live = w.s.Term.Info(a.ID) {
		time.Sleep(50 * time.Millisecond)
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: polish.", "provider", "fake", "continue", a.ID), alice, header(HeaderTicket, r3.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"session"`) {
		t.Fatalf("continuing a session that ended: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", r3.ID); got.State != delegation.Approved {
		t.Fatalf("the refusal spent the ticket: %s", got.State)
	}
}
