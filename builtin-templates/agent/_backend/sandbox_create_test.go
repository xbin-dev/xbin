package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// createConv is a held coding conversation owned as st says, with carol a
// participant, whose model calls go through the scripted fake.
func createConv(t *testing.T, ag *Agent, st runStamp, tweak func(*Config)) *Run {
	t.Helper()
	cfg := defaultConfig()
	cfg.Class = "coding"
	cfg.Features = map[string]bool{"titles": false, "streaming": false}
	if tweak != nil {
		tweak(&cfg)
	}
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true, Stamp: st})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, 'carol', 'participant', 1)`, r.ID); err != nil {
		t.Fatal(err)
	}
	ag.acl.flush(r.ID)
	return r
}

func lastToolOf(ag *Agent, id int64) string {
	msgs, _ := ag.db.messages(id, false)
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" {
			return msgs[i].Content
		}
	}
	return ""
}

// stepAfter is the model call that answered the latest tool result holding
// s: the tools it was offered and its system prompt.
func stepAfter(t *testing.T, ag *Agent, s string) (string, string) {
	t.Helper()
	f := fakeOf(ag)
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := len(f.calls) - 1; i >= 0; i-- {
		req := f.calls[i]
		if n := len(req.Msgs); n > 0 && req.Msgs[n-1].Role == "tool" && strings.Contains(asString(req.Msgs[n-1].Content), s) {
			var names []string
			for _, sp := range req.Tools {
				names = append(names, sp.Function.Name)
			}
			return " " + strings.Join(names, " ") + " ", asString(req.Msgs[0].Content)
		}
	}
	t.Fatalf("no model call after a tool result with %q", s)
	return "", ""
}

func sandboxesAt(t *testing.T) []*sbxSandbox {
	t.Helper()
	c, _ := sbxDial("apps/cs", "")
	l, err := c.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return l
}

// The owner's grant: parked with what will be made; a participant can't
// allow it but can deny it; once creates, binds and the next step of the
// same turn has the coding tools; an hour lets later creates through, up to
// the cap.
func TestSandboxCreateGrantFlow(t *testing.T) {
	ag, mux := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := createConv(t, ag, alicePrivate, nil)
	cfg, _ := ag.db.runConfig(conv.ID)
	if l := specLine(cfg, 0); !strings.Contains(l, " sandbox_create ") || strings.Contains(l, " bash ") {
		t.Fatalf("unbound coding conversation offers: %s", l)
	}
	f := fakeOf(ag)
	f.on(lastIs("tool", ""), say("done"))
	for i, name := range []string{"api-dev", "db", "cache", "four", "five", "six"} {
		args := fmt.Sprintf(`{"name":%q}`, name)
		if name == "db" {
			args = `{"name":"db","egress":"internet"}`
		}
		f.on(lastUser("box "+name), callTools(tc(fmt.Sprintf("c%d", i), "sandbox_create", args)))
	}
	parked := func() pendingState {
		t.Helper()
		waitStatus(t, ag.db, conv.ID, statusWaiting)
		return parsePending(mustRun(t, ag, conv.ID).Pending)
	}
	approve := func(c caller, body map[string]any) int {
		return callAs(t, mux, c, "POST", fmt.Sprintf("/runs/%d/approve", conv.ID), body).Code
	}

	send(t, ag, conv.ID, "box api-dev")
	p := parked()
	if p.Grant != capSandboxes || p.GrantAsk != `create the coding sandbox "api-dev" at Fake sandboxes (test fixture) — image base, size small, egress none` {
		t.Fatalf("parked on %+v", p)
	}
	var needs struct{ Items []map[string]any }
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/needs", nil).Body.Bytes(), &needs)
	if len(needs.Items) != 1 {
		t.Fatalf("alice's Needs you: %v", needs.Items)
	}
	if got := approve(asCarol, map[string]any{"approve": true}); got != 403 {
		t.Fatalf("a participant allows the owner's grant: %d", got)
	}
	if got := approve(asAlice, map[string]any{"approve": true}); got != 200 { // once
		t.Fatalf("alice allows once: %d", got)
	}
	waitQuiet(t, ag)
	out := lastToolOf(ag, conv.ID)
	if !strings.HasPrefix(out, `Created the sandbox "api-dev" (ref apps/cs|`) || !strings.Contains(out, "egress none") ||
		!strings.Contains(out, "active sandbox") || ag.db.liveGrant(conv.ID, capSandboxes) {
		t.Fatalf("once: %q (kept: %v)", out, ag.db.liveGrant(conv.ID, capSandboxes))
	}
	act, att := sandboxOf(t, ag, conv.ID)
	if act == nil || act.Name != "api-dev" || act.By != "alice" || len(att) != 1 || act.Egress != "none" {
		t.Fatalf("bound: %+v %+v", act, att)
	}
	boxes := sandboxesAt(t)
	if len(boxes) != 1 || boxes[0].Owner.User != "alice" || boxes[0].Labels["xbin.agent/conversation"] != fmt.Sprint(conv.ID) ||
		!hasStr(boxes[0].Members, "carol") || sandboxRef("apps/cs", boxes[0].ID) != act.Ref {
		t.Fatalf("made at the manager: %+v", boxes)
	}
	// the same turn's next step already works in it
	tools, system := stepAfter(t, ag, "Created the sandbox \"api-dev\"")
	if !strings.Contains(tools, " bash ") || !strings.Contains(tools, " read ") || !strings.Contains(system, "# Sandbox") ||
		!strings.Contains(system, `"api-dev"`) {
		t.Fatalf("the next step: tools%s\n%s", tools, system)
	}

	// denied (anyone who steers may deny): nothing is made
	send(t, ag, conv.ID, "box db")
	if p := parked(); !strings.Contains(p.GrantAsk, "egress internet") {
		t.Fatalf("the ask: %q", p.GrantAsk)
	}
	if got := approve(asCarol, map[string]any{"approve": false}); got != 200 {
		t.Fatalf("carol denies: %d", got)
	}
	waitQuiet(t, ag)
	if out := lastToolOf(ag, conv.ID); !strings.HasPrefix(out, "(denied: the owner did not allow creating a sandbox") || len(sandboxesAt(t)) != 1 {
		t.Fatalf("denied: %q", out)
	}

	// for an hour: attached beside the active one, and the next create
	// doesn't ask
	send(t, ag, conv.ID, "box cache")
	parked()
	if got := approve(asAlice, map[string]any{"approve": true, "grant": "hour"}); got != 200 {
		t.Fatalf("alice allows for an hour: %d", got)
	}
	waitQuiet(t, ag)
	if out := lastToolOf(ag, conv.ID); !strings.Contains(out, `It is attached; the active sandbox is still "api-dev"`) || !ag.db.liveGrant(conv.ID, capSandboxes) {
		t.Fatalf("hour: %q", out)
	}
	if tools, _ := stepAfter(t, ag, `Created the sandbox "cache"`); !strings.Contains(tools, " sandbox_copy ") {
		t.Fatalf("attached a second: sandbox_copy not offered next step: %s", tools)
	}
	var view struct {
		Run struct{ Grants []grantView }
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", conv.ID), nil).Body.Bytes(), &view)
	if len(view.Run.Grants) != 1 || view.Run.Grants[0].Cap != capSandboxes || view.Run.Grants[0].Chip != "creates sandboxes" {
		t.Fatalf("the view's grants: %+v", view.Run.Grants)
	}
	for _, name := range []string{"four", "five"} {
		send(t, ag, conv.ID, "box "+name)
		waitQuiet(t, ag)
		if statusOf(ag.db, conv.ID) == statusWaiting || !strings.HasPrefix(lastToolOf(ag, conv.ID), "Created the sandbox \""+name) {
			t.Fatalf("%s within the hour: %s %q", name, statusOf(ag.db, conv.ID), lastToolOf(ag, conv.ID))
		}
	}
	// the cap: refused, and not parked even with the grant gone
	if got := callAs(t, mux, asAlice, "DELETE", fmt.Sprintf("/runs/%d/grants/sandboxes", conv.ID), nil).Code; got != 200 {
		t.Fatalf("revoke: %d", got)
	}
	send(t, ag, conv.ID, "box six")
	waitQuiet(t, ag)
	if out := lastToolOf(ag, conv.ID); statusOf(ag.db, conv.ID) == statusWaiting || !strings.Contains(out, "already created 4 sandboxes") || len(sandboxesAt(t)) != 4 {
		t.Fatalf("the fifth: %s %q (%d made)", statusOf(ag.db, conv.ID), out, len(sandboxesAt(t)))
	}
	if _, att := sandboxOf(t, ag, conv.ID); len(att) != 4 {
		t.Fatalf("attached: %d", len(att))
	}
}

// Where sandbox_create is not offered, and what it refuses before asking
// anyone.
func TestSandboxCreateRefusals(t *testing.T) {
	ag, _ := accessFixture(t)
	coding := Config{Class: "coding"}
	if strings.Contains(specLine(coding, 0), " sandbox_create ") {
		t.Fatal("offered with no manager bound")
	}
	bindSbx(t, "apps/cs")
	for name, c := range map[string]struct {
		cfg   Config
		depth int
	}{
		"a subagent":      {coding, 1},
		"a channel's":     {Config{Class: "coding", Channel: true}, 0},
		"no sandbox tool": {Config{Class: "internal"}, 0},
	} {
		if strings.Contains(specLine(c.cfg, c.depth), " sandbox_create ") {
			t.Errorf("%s: offered", name)
		}
	}
	if !strings.Contains(specLine(coding, 0), " sandbox_create ") {
		t.Fatal("not offered to a coding conversation")
	}
	call := func(r *Run, args string) (string, error) {
		cfg, _ := ag.db.runConfig(r.ID)
		ctx := withGrantOnce(withToolCall(context.Background(), "c1"), capSandboxes)
		return ag.runTool(ctx, r, cfg, "sandbox_create", decodeArgs(args))
	}
	needs := func(r *Run, args string) bool {
		cfg, _ := ag.db.runConfig(r.ID)
		return sandboxesGrantNeeded(ag, r, cfg, []toolCall{tc("c1", "sandbox_create", args)}, nil)
	}
	owned := createConv(t, ag, alicePrivate, nil)
	for _, c := range []struct {
		name string
		run  *Run
		args string
		want string
	}{
		{"a channel's", createConv(t, ag, alicePrivate, func(c *Config) { c.Channel = true }), `{"name":"x"}`, "chat channel's conversation can't create"},
		{"unowned", createConv(t, ag, runStamp{Origin: "api"}, nil), `{"name":"x"}`, "no person owns this conversation"},
		{"an element's", createConv(t, ag, runStamp{Owner: "el:apps/other", Origin: "api"}, nil), `{"name":"x"}`, "no person owns"},
		{"another class", createConv(t, ag, alicePrivate, func(c *Config) { c.Class = "internal" }), `{"name":"x"}`, "not available in this conversation's class"},
		{"an egress the class doesn't allow", owned, `{"name":"x","egress":"open"}`, `class (Coding) doesn't allow a sandbox with egress "open" — it allows none, internet`},
		{"no name", owned, `{"name":"  "}`, "needs a name"},
		{"an unknown manager", owned, `{"name":"x","manager":"apps/nope"}`, `manager "apps/nope" isn't one this conversation can use — it can use apps/cs`},
		{"an unknown image", owned, `{"name":"x","image":"gpu"}`, `has no image "gpu" — it has base`},
		{"an unknown size", owned, `{"name":"x","size":"huge"}`, `has no size "huge" — it has small`},
		{"a cwd in home", owned, `{"name":"x","cwd":"~/src"}`, "cwd: an absolute path"},
	} {
		if needs(c.run, c.args) {
			t.Errorf("%s: parks for the grant", c.name)
		}
		if _, err := call(c.run, c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if len(sandboxesAt(t)) != 0 {
		t.Fatal("a refused create made a sandbox")
	}
	// a subagent's call (a hallucinated one) is refused too
	kid, _ := ag.db.createRun("kid", `{"class":"coding"}`, owned.ID)
	kr, _ := ag.db.getRun(kid)
	if _, err := call(kr, `{"name":"x"}`); err == nil || !strings.Contains(err.Error(), "not a subagent") {
		t.Fatalf("a subagent: %v", err)
	}
	// without the grant: the backstop
	cfg, _ := ag.db.runConfig(owned.ID)
	if _, err := ag.runTool(context.Background(), owned, cfg, "sandbox_create", map[string]any{"name": "x"}); err == nil ||
		!strings.Contains(err.Error(), "needs the conversation owner's permission") {
		t.Fatalf("no grant: %v", err)
	}
	// a cwd that isn't there yet is made; a relative one is under the workdir
	out, err := call(owned, `{"name":"src","cwd":"src/app"}`)
	if err != nil {
		t.Fatal(err)
	}
	act, _ := sandboxOf(t, ag, owned.ID)
	if act == nil || !strings.HasSuffix(act.Cwd, "/work/src/app") || !strings.Contains(out, "the tools work in "+act.Cwd) {
		t.Fatalf("cwd: %+v %q", act, out)
	}
	c, _ := sbxDial("apps/cs", "")
	if st, err := c.Stat(context.Background(), strings.SplitN(act.Ref, "|", 2)[1], act.Cwd); err != nil || st.Type != "dir" {
		t.Fatalf("the cwd was not made: %v", err)
	}
}

// A create a restart cut off keeps its number: the next call with the same
// name gets the manager's sandbox for that clientId instead of another. A
// number the manager already used for a different request is given up.
func TestSandboxCreateDedupesAcrossRestarts(t *testing.T) {
	ag, _ := accessFixture(t)
	bindSbx(t, "apps/cs")
	conv := createConv(t, ag, alicePrivate, nil)
	cfg, _ := ag.db.runConfig(conv.ID)
	call := func(id, args string) string {
		t.Helper()
		ctx := withGrantOnce(withToolCall(context.Background(), id), capSandboxes)
		out, err := ag.runTool(ctx, conv, cfg, "sandbox_create", decodeArgs(args))
		if err != nil {
			t.Fatalf("%s: %v", args, err)
		}
		return out
	}
	// the first attempt reached the manager, then the backend went away
	row, err := ag.db.beginCreate(conv.ID, "api", "c-lost")
	if err != nil {
		t.Fatal(err)
	}
	if row.clientID() != fmt.Sprintf("agent:%d:name:1", conv.ID) {
		t.Fatalf("clientId %s", row.clientID())
	}
	first := mkCreate(t, conv.ID, row, sbxCreate{Name: "api", Image: "base", Size: "small", Egress: "none"})
	out := call("c-again", `{"name":"api"}`)
	boxes := sandboxesAt(t)
	act, _ := sandboxOf(t, ag, conv.ID)
	if len(boxes) != 1 || act == nil || act.Ref != sandboxRef("apps/cs", first.ID) || !strings.HasPrefix(out, "Created") {
		t.Fatalf("re-called: %d sandboxes, bound %+v: %q", len(boxes), act, out)
	}
	// the same call once more: it is already here
	if out := call("c-third", `{"name":"api"}`); !strings.HasPrefix(out, `Already created here: the sandbox "api"`) || len(sandboxesAt(t)) != 1 {
		t.Fatalf("repeated: %q", out)
	}
	// a cut-off call made something else under the number: a new one
	row, _ = ag.db.beginCreate(conv.ID, "web", "c-lost-2")
	orphan := mkCreate(t, conv.ID, row, sbxCreate{Name: "web", Egress: "internet"})
	call("c-web", `{"name":"web"}`)
	_, att := sandboxOf(t, ag, conv.ID)
	if len(sandboxesAt(t)) != 3 || len(att) != 2 || att[1].Ref == sandboxRef("apps/cs", orphan.ID) || att[1].Egress != "none" {
		t.Fatalf("after an exists: %d sandboxes, attached %+v", len(sandboxesAt(t)), att)
	}
	if n := ag.db.madeSandboxes(conv.ID); n != 2 {
		t.Fatalf("made: %d", n)
	}
}

// mkCreate sends a create as sandbox_create would have, for row.
func mkCreate(t *testing.T, root int64, row *sbxCreateRow, req sbxCreate) *sbxSandbox {
	t.Helper()
	req.ClientID = row.clientID()
	forConversation(&req, who{kind: whoUser, user: "alice"}, root)
	return mkSandbox(t, "apps/cs", "alice", req)
}
