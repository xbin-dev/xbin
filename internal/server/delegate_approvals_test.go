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
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/agent"
	"github.com/xbin-dev/xbin/internal/agentprefs"
	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/users"
	"github.com/xbin-dev/xbin/internal/util"
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
	// start-defaults says so too: what the agent's backend reads to know a session in that mode asks (the
	// approvalsUx pass: its tile agent was said to "act without asking" in its asking mode)
	sd := w.do(w.full, "GET", "/api/xbin/delegate/start-defaults?tile=apps/x", "", alice)
	var d struct {
		Harnesses []struct {
			ID, Mode string
			Asks     bool
		}
	}
	_ = json.Unmarshal(sd.Body.Bytes(), &d)
	for _, h := range d.Harnesses {
		if h.ID == "fake" && (h.Mode != "ask" || !h.Asks) {
			t.Fatalf("start-defaults of a coding agent with no default mode: %+v", h)
		}
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
	// (remembered: what the person changed — the coding agent; read-only came with it: Codex's default, not theirs)
	if ts := w.s.AgentPrefs.Get("alice").AgentSessions.Tiles["apps/x"]; ts.Harness != "codex" || ts.Mode != "" {
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
	// a sign-in is never the agent's to name: whatever its person approved, the start is refused before its ticket
	// is spent (D250 review: this used to be asserted only where the filing happened to succeed)
	named := startBody("apps/x", "go", "signin", "si_1")
	r := w.ask(alice, "POST", "/api/xbin/term/sessions", named)
	w.approve(w.cookie("alice"), r.ID, "")
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", named, alice, header(HeaderTicket, r.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), "doesn't pick a saved sign-in") {
		t.Fatalf("a start naming a sign-in: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", r.ID); got.State != delegation.Approved {
		t.Fatalf("a start naming a sign-in spent its ticket: %s", got.State)
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
	req := w.askKnows(alice, "POST", "/api/xbin/term/sessions", body, "retry") // (a backend that knows Failed: D250 review L3)
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
	// (one that doesn't know "continue": through the route, in TestContinueOnlyOwnSessionSameTile)
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
		// every session ended AND torn down (its history saved) before the test's temp dirs go: a kill alone
		// raced the removal under load ("directory not empty")
		tiles := map[string]bool{}
		for _, si := range w.s.Term.ListFor("alice", "", nil) {
			tiles[strings.Trim(si.Cwd, "/")] = true
		}
		for tile := range tiles {
			if err := w.s.Term.StopTileSessions(tile); err != nil {
				t.Errorf("the test's sessions on %s didn't end: %v", tile, err)
			}
		}
	})
	return w
}

// waitIdle waits for a session to end its turn.
// waitMode waits for session id to run in mode — a switch the person makes
// is the coding agent's to take, and xbind's record follows it.
func waitMode(t *testing.T, w *dWorld, id, mode string) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if info, ok := w.s.Term.Info(id); ok && info.Mode == mode {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	info, _ := w.s.Term.Info(id)
	t.Fatalf("session %s never in mode %s: %+v", id, mode, info)
}

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
	waitMode(t, w, info.ID, "auto")
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
	waitMode(t, w, info.ID, "yolo")
	if rec := w.do(w.full, "POST", path, msg, alice, header(HeaderTicket, m2.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"session"`) || !strings.Contains(rec.Body.String(), `not fake in mode \"auto\" as approved`) {
		t.Fatalf("a message approved before the mode changed: %d %s", rec.Code, rec.Body.String())
	}
	// a session that asks needs no approval: the request is refused
	in2, _ := json.Marshal(map[string]string{"method": "POST", "path": path, "body": msg})
	w.do(w.full, "POST", "/api/xbin/term/sessions/"+info.ID+"/options", `{"id":"mode","value":"ask"}`, person)
	waitMode(t, w, info.ID, "ask")
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
	// the agent's own session on another tile, and another agent's (of the same person) on this one: never offered
	startAs := func(c cred, tile string) string {
		t.Helper()
		b := startBody(tile, "Goal: mail.", "provider", "fake")
		r := w.ask(c, "POST", "/api/xbin/term/sessions", b)
		w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r.ID+"/approve", `{"harness":"fake","mode":"ask"}`, person)
		rec := w.do(w.full, "POST", "/api/xbin/term/sessions", b, c, header(HeaderTicket, r.ID))
		var x struct{ ID string }
		if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &x) != nil || x.ID == "" {
			t.Fatalf("a start on %s: %d %s", tile, rec.Code, rec.Body.String())
		}
		waitIdle(t, w, x.ID)
		return x.ID
	}
	if err := os.MkdirAll(filepath.Join(w.s.Term.Root, "apps", "mail"), 0o755); err != nil {
		t.Fatal(err)
	}
	onMail := startAs(alice, "apps/mail")
	t.Cleanup(auth.TestSealDelegate("xbin/other"))
	w.s.Auth.RegisterInstancePartition("d-alice-other", "xbin/other", "", util.UserPartition("alice"), "uid-alice")
	h, err := w.store.NewAgentSession("alice", "xbin/other", nil, "local/test")
	if err != nil {
		t.Fatal(err)
	}
	testHandles.Store("d-alice-other", h.ID)
	t.Cleanup(func() { testHandles.Delete("d-alice-other") })
	byOther := startAs(delegateCred("d-alice-other"), "apps/x")
	for name, sid := range map[string]string{"the person's own": o.ID, "no such": "nope", "its own on another tile": onMail, "another agent's": byOther} {
		if s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "next", "provider", "fake", "continue", sid))); s["continue"] != nil {
			t.Errorf("%s session offered to continue: %v", name, s["continue"])
		}
	}
	sessions := len(w.s.Term.ListFor("alice", "", nil))
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
	if n := len(w.s.Term.ListFor("alice", "", nil)); n != sessions {
		t.Fatalf("a second session of the agent's opened: %d sessions, %d before", n, sessions)
	}
	// a client that doesn't know "continue" approves a start offering it (no picks): through the route, a NEW
	// session opens — never a follow-up into one the person wasn't shown (on the scripted agent: codex has no stand-in)
	if err := w.s.AgentPrefs.RememberTile("alice", "apps/x", agentprefs.TileStart{Harness: "fake"}, 1); err != nil {
		t.Fatal(err)
	}
	var older delegation.Request
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		waitIdle(t, w, a.ID)
		older = w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: older.", "provider", "fake", "continue", a.ID))
		if sessOf(older)["continue"] != nil {
			break
		}
		w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+older.ID, "", alice)
		if time.Now().After(deadline) {
			t.Fatalf("the idle session never offered to continue: %v", sessOf(older))
		}
	}
	if rec := w.do(w.full, "POST", "/api/xbin/delegate/requests/"+older.ID+"/approve", `{"grant":"once"}`, person); rec.Code != 200 {
		t.Fatalf("an older client's approval: %d %s", rec.Code, rec.Body.String())
	}
	rec = w.do(w.full, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: older.", "provider", "fake", "continue", a.ID), alice, header(HeaderTicket, older.ID))
	var fresh struct {
		ID        string
		Continued bool
	}
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &fresh) != nil || fresh.Continued || fresh.ID == a.ID || fresh.ID == "" {
		t.Fatalf("an older client's approval of a start offering to continue: %d %s", rec.Code, rec.Body.String())
	}
	// approved, and the session ends before it is used: refused, nothing spent
	// (idle again after the follow-up's turn: an idle read can come before that turn began, so the offer — or the
	// approval, which checks again and refuses a busy session, nothing decided — is tried until the turn is over)
	var r3 delegation.Request
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(100 * time.Millisecond) {
		waitIdle(t, w, a.ID)
		r3 = w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: polish.", "provider", "fake", "continue", a.ID))
		rec := httptest.NewRecorder()
		if sessOf(r3)["continue"] != nil {
			if rec = w.do(w.full, "POST", "/api/xbin/delegate/requests/"+r3.ID+"/approve", `{"knows":["continue"]}`, person); rec.Code == 200 {
				break
			}
			if rec.Code != http.StatusConflict || !strings.Contains(rec.Body.String(), "is busy") {
				t.Fatalf("approve the continue: %d %s", rec.Code, rec.Body.String())
			}
		}
		w.do(w.full, "DELETE", "/api/xbin/delegate/requests/"+r3.ID, "", alice)
		if time.Now().After(deadline) {
			t.Fatalf("the idle session never offered to continue: %v %d %s", sessOf(r3), rec.Code, rec.Body.String())
		}
	}
	// busy (waiting for its person's answer): not offered, and the approved follow-up refused, nothing spent
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions/"+a.ID+"/prompt", `{"text":"ask"}`, person); rec.Code != 200 {
		t.Fatalf("the person's question turn: %d %s", rec.Code, rec.Body.String())
	}
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if info, _ := w.s.Term.Info(a.ID); info.Questions > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session never asked its person")
		}
	}
	if s := sessOf(w.ask(alice, "POST", "/api/xbin/term/sessions", startBody("apps/x", "next", "provider", "fake", "continue", a.ID))); s["continue"] != nil {
		t.Errorf("a busy session offered to continue: %v", s["continue"])
	}
	if rec := w.do(w.full, "POST", "/api/xbin/term/sessions", startBody("apps/x", "Goal: polish.", "provider", "fake", "continue", a.ID), alice, header(HeaderTicket, r3.ID)); rec.Code != 403 || !strings.Contains(rec.Body.String(), `"reason":"session"`) {
		t.Fatalf("continuing a busy session: %d %s", rec.Code, rec.Body.String())
	}
	if got, _ := w.store.Request("alice", r3.ID); got.State != delegation.Approved {
		t.Fatalf("the busy refusal spent the ticket: %s", got.State)
	}
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
