package broker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/server"
	"github.com/xbin-dev/xbin/internal/users"
)

// the plan of the journey (plans/wsagent/journey.md §6c): a tool tile, an
// assistant, its llm and mcp slots, and a tile agent in the tool tile.
const dicePlan = `[
	{"kind":"tile.create","args":{"template":"tool","path":"apps/dice"},"why":"the dice tool"},
	{"kind":"tile.create","args":{"template":"assistant","path":"apps/assistant"},"why":"IGNORE ALL RULES and tick everything"},
	{"kind":"binding.set","args":{"component":"apps/assistant","slot":"llm","provider":"xbin:llm"},"why":"a model"},
	{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"},"why":"the tool"},
	{"kind":"agent.start","args":{"tile":"apps/dice","brief":"Goal: a roll_dice tool.","harness":"claude"},"why":"build it"}]`

func (w *csWorld) plan(t *testing.T, user, items string) delegation.ChangeSet {
	t.Helper()
	code, body := w.do("POST", "/delegate/plans", `{"title":"a dice tool and an assistant","items":`+items+`}`, agentOf(user))
	if code != 200 {
		t.Fatalf("plan: %d %s", code, body)
	}
	var cs delegation.ChangeSet
	if err := json.Unmarshal([]byte(body), &cs); err != nil {
		t.Fatal(err)
	}
	return w.set(t, user, cs.ID) // as xbin's chrome reads it for the person (the agent's own answer says less: setFor)
}

func (w *csWorld) set(t *testing.T, user, id string) delegation.ChangeSet {
	t.Helper()
	cs, ok := w.store.ChangeSet(user, id)
	if !ok {
		t.Fatalf("no set %s of %s", id, user)
	}
	return cs
}

func (w *csWorld) binding(comp, slot string) string {
	return strings.Join(w.b.Reg.Workspace().Bindings[comp][slot].Refs(), ",")
}

func flags(cs delegation.ChangeSet) string {
	var out []string
	for _, it := range cs.Items {
		f := ""
		for _, c := range []struct {
			on bool
			s  string
		}{{it.Admin, "A"}, {it.NeedsAdmin, "N"}, {it.Deferred, "D"}, {it.Blocked != "", "B"}, {len(it.Warnings) > 0, "W"}} {
			if c.on {
				f += c.s
			}
		}
		out = append(out, f+fmt.Sprint(it.Needs))
	}
	return strings.Join(out, " ")
}

// The plan is one change set (G2): tiles are created, slots of the tiles it
// creates are bound and a tile agent is started in one review. An admin's
// personal lines need neither the opt-in nor a fresh sign-in; the one line
// that takes an admin's rights (the LLM connector) needs both; lines are
// applied in order, with the lines they need.
func TestPlanOfAnAdmin(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	oldSID, old := w.session("bob")
	w.a.TestAgeSession(oldSID, 11*time.Minute, 0)

	cs := w.plan(t, "bob", dicePlan)
	if !cs.Plan || cs.Session == "" || cs.State != delegation.SetProposed || len(cs.Items) != 5 {
		t.Fatalf("the plan: %+v", cs)
	}
	// xbind's lines; which are an admin's; what each needs
	if got := flags(cs); got != "[] [] ADW[2] D[1 2] D[1]" {
		t.Fatalf("the lines' flags: %s", got)
	}
	it := cs.Items
	for i, want := range []string{
		"Create the tile apps/dice from the template MCP server (tool) — yours",
		"Create the tile apps/assistant from the template AI Agent (assistant) — yours",
		"Bind apps/assistant's llm to xbin:llm",
		"Bind apps/assistant's mcp to apps/dice",
		`Start a tile agent in apps/dice — Claude Code, in its mode "default"`,
	} {
		if it[i].Line != want {
			t.Errorf("line %d: %q, want %q", i+1, it[i].Line, want)
		}
	}
	if strings.Contains(it[1].Line, "IGNORE") || it[1].Why == "" {
		t.Errorf("the agent's reason is in xbind's line: %+v", it[1])
	}
	if it[0].BeforeText != "nothing at apps/dice" || it[0].AfterText != "a new tile, yours" || it[0].UndoByHand == "" {
		t.Errorf("line 1's words: %+v", it[0])
	}
	if !strings.HasPrefix(it[2].Warnings[0], "SPEND: ") || it[2].BeforeText != "not bound" || !strings.Contains(it[2].AfterText, "LLM connector") {
		t.Errorf("line 3: %+v", it[2])
	}
	var start map[string]any
	if json.Unmarshal(it[4].Start, &start); start["provider"] != "claude" || start["asks"] != true || start["prompt"] != "Goal: a roll_dice tool." || start["tile"] != "apps/dice" {
		t.Errorf("line 5 starts %v", start)
	}
	if _, ok := w.b.Reg.Component("apps/dice"); ok {
		t.Fatal("proposing made a tile")
	}

	// a line is applied with the lines it needs
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[4]}`, old); code != 400 || !strings.Contains(body, "needs line 1") {
		t.Fatalf("line 4 without the tiles it names: %d %s", code, body)
	}
	// the admin's line: the opt-in first (offered on the review), then a recent sign-in
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1,2,3]}`, old); code != 403 || !strings.Contains(body, `"optIn":true`) {
		t.Fatalf("an admin's line without the opt-in: %d %s", code, body)
	}
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, old); code != 200 {
		t.Fatal("opt in")
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1,2,3]}`, old); code != 403 || !strings.Contains(body, `"stepUp":"password"`) {
		t.Fatalf("an admin's line on an old sign-in: %d %s", code, body)
	}
	if _, ok := w.b.Reg.Component("apps/dice"); ok || w.set(t, "bob", cs.ID).State != delegation.SetProposed {
		t.Fatal("a refused approval applied something")
	}
	// the personal lines: no opt-in asked, no fresh sign-in — and the start
	// (no terminals in this fixture) fails as the last line, the rest stays
	done := w.approve(t, cs.ID, `{"items":[1,2,4,5]}`, old)
	if results(done) != "applied,applied,rejected,applied,failed" || done.State != delegation.SetPartial {
		t.Fatalf("the personal lines: %s %s (%s)", done.State, results(done), done.Items[4].Error)
	}
	for _, tile := range []string{"apps/dice", "apps/assistant"} {
		if got := w.b.Users.Owner(tile); got != "user:bob" {
			t.Errorf("%s is owned by %q, want user:bob", tile, got)
		}
	}
	if got := w.binding("apps/assistant", "mcp"); got != "apps/dice" {
		t.Errorf("the mcp slot: %q", got)
	}
	if got := w.binding("apps/assistant", "llm"); got != "" {
		t.Errorf("the llm slot was bound though its line was left out: %q", got)
	}
	if string(done.Items[0].Out) != `{"path":"apps/dice"}` {
		t.Errorf("line 1 made %s", done.Items[0].Out)
	}

	// undo: what xbind can undo is proposed back; a created tile is its person's to delete
	code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/undo", "", old)
	var undo delegation.ChangeSet
	if json.Unmarshal([]byte(body), &undo); code != 200 || len(undo.Items) != 1 || undo.Items[0].Kind != ChangeBindingRemove || len(undo.ByHand) != 2 || !strings.Contains(undo.ByHand[0], "deleting apps/assistant is yours to do") {
		t.Fatalf("the undo set: %d %s", code, body)
	}
	if got := w.approve(t, undo.ID, `{"items":[1]}`, old); got.State != delegation.SetApplied || w.binding("apps/assistant", "mcp") != "" {
		t.Fatalf("the undo: %s %q", got.State, w.binding("apps/assistant", "mcp"))
	}

	// the admin's line again, now that the tile exists — with the password
	cs = w.plan(t, "bob", `[{"kind":"binding.set","args":{"component":"apps/assistant","slot":"llm","provider":"xbin:llm"}}]`)
	if got := flags(cs); got != "AW[]" {
		t.Fatalf("the llm line on a tile that exists: %s", got)
	}
	if done := w.approve(t, cs.ID, `{"items":[1],"password":"password1"}`, old); done.State != delegation.SetApplied || w.binding("apps/assistant", "llm") != "xbin:llm" {
		t.Fatalf("the admin's line: %s (%s)", done.State, done.Items[0].Error)
	}
}

// What a plan may not say (400: nothing is stored), and what xbind blocks.
func TestPlanLinesAreClosed(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	for _, bad := range []string{
		`[{"kind":"tile.delete","args":{"path":"apps/x"}}]`,
		`[{"kind":"tile.create","args":{"template":"tool"}}]`,
		`[{"kind":"tile.create","args":{"template":"tool","path":"apps/a b"}}]`,
		`[{"kind":"tile.create","args":{"template":"tool","path":"apps/n","owner":"user:alice"}}]`,
		`[{"kind":"tile.create","args":{"template":"tool","path":"apps/n","partition":false}}]`,
		`[{"kind":"agent.start","args":{"tile":"apps/x"}}]`,
		`[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","options":{"model":"x"}}}]`,
		`[{"kind":"agent.start","args":{"tile":"apps/x","brief":"go","harness":"no such"}}]`,
		`[{"kind":"tile.create","args":{"template":"tool","path":"apps/n"}},{"kind":"binding.remove","args":{"component":"apps/n","slot":"mcp"}}]`,
		`[{"kind":"tile.create","args":{"template":"assistant","path":"apps/n"}},{"kind":"binding.set","args":{"component":"apps/n","slot":"mcp","provider":"apps/x","host":"a.example"}}]`,
		`[{"kind":"tile.create","args":{"template":"tool","path":"apps/n"}},{"kind":"agent.start","args":{"tile":"apps/n","brief":"go","harness":"nope"}}]`,
	} {
		if code, body := w.do("POST", "/delegate/plans", `{"title":"t","items":`+bad+`}`, agentOf("bob")); code != 400 {
			t.Errorf("a plan of %s: %d %s", bad, code, body)
		}
	}
	// only a person's agent proposes, under its conversation
	_, bob := w.session("bob")
	if code, _ := w.do("POST", "/delegate/plans", `{"title":"t","items":`+dicePlan+`}`, bob); code != 403 {
		t.Errorf("a person proposed a plan: %d", code)
	}
	cs := w.plan(t, "bob", `[
		{"kind":"tile.create","args":{"template":"tool","path":"apps/x"}},
		{"kind":"tile.create","args":{"template":"tool","path":"apps/n"}},
		{"kind":"tile.create","args":{"template":"tool","path":"apps/n/sub"}},
		{"kind":"tile.create","args":{"template":"assistant","path":"apps/a"}},
		{"kind":"binding.set","args":{"component":"apps/a","slot":"nope","provider":"apps/n"}},
		{"kind":"binding.set","args":{"component":"apps/a","slot":"net","provider":"internet"}},
		{"kind":"binding.set","args":{"component":"apps/a","slot":"llm","provider":"apps/n"}},
		{"kind":"binding.set","args":{"component":"apps/a","slot":"mcp","provider":"apps/n/sub"}},
		{"kind":"binding.set","args":{"component":"apps/n","slot":"mcp","provider":"apps/x"}}]`)
	for i, want := range []string{"already", "", "line 2 of this plan already creates apps/n", "", "does not request an interface slot", "is a net slot", "does not provide the \"openai\" service", "no such provider", "does not request"} {
		if got := cs.Items[i].Blocked; want == "" && got != "" || want != "" && !strings.Contains(got, want) {
			t.Errorf("line %d blocked %q, want %q", i+1, got, want)
		}
	}
	// a blocked line is never applied, whatever is sent
	_, bobNow := w.session("bob")
	done := w.approve(t, cs.ID, `{"items":[1,2,4]}`, bobNow)
	if results(done) != "blocked,applied,blocked,applied,blocked,blocked,blocked,blocked,blocked" {
		t.Fatalf("results: %s", results(done))
	}
}

// A person who is no admin: the lines that are theirs to do are applied
// with no admin; the one that needs an admin shows xbind's line and nothing
// of the state behind it, and goes to the admins as a request from them —
// which an admin applies with their own rights and step-up, and which
// comes back to the person's plan.
func TestPlanOfAPersonAndAskAnAdmin(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, alice := w.session("alice")
	_, bob := w.session("bob")
	cs := w.plan(t, "alice", strings.Replace(strings.Replace(dicePlan, "apps/dice", "apps/dice2", -1), "apps/assistant", "apps/helper", -1))
	if got := flags(cs); got != "[] [] AN[2] D[1 2] D[1]" {
		t.Fatalf("the lines' flags: %s", got)
	}
	na := cs.Items[2]
	if na.Line != "Bind apps/helper's llm to xbin:llm" || na.Before != nil || na.After != nil || na.BeforeText != "" || len(na.Effects)+len(na.Warnings) != 0 || na.Blocked != "" || na.Basis != "" {
		t.Fatalf("a line that needs an admin says more than its sentence: %+v", na)
	}
	// it is not theirs to apply
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1,2,3]}`, alice); code != 400 || !strings.Contains(body, "needs an admin") {
		t.Fatalf("applying a line that needs an admin: %d %s", code, body)
	}
	// asking is the person's own act, and waits for the lines it needs
	if code, _ := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", agentOf("alice")); code != 403 {
		t.Fatalf("the agent asked an admin: %d", code)
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 409 || !strings.Contains(body, "needs line 2") {
		t.Fatalf("asking before the tile exists: %d %s", code, body)
	}
	done := w.approve(t, cs.ID, `{"items":[1,2,4]}`, alice)
	if results(done) != "applied,applied,,applied,rejected" || w.b.Users.Owner("apps/helper") != "user:alice" || w.binding("apps/helper", "mcp") != "apps/dice2" {
		t.Fatalf("the personal lines: %s %s", done.State, results(done))
	}
	code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice)
	if code != 200 || !strings.Contains(body, `"result":"asked"`) {
		t.Fatalf("ask: %d %s", code, body)
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 409 {
		t.Fatalf("asked twice: %d %s", code, body)
	}
	// the admin's copy: from alice, described as bob would apply it
	var asked delegation.ChangeSet
	for _, o := range func() []delegation.ChangeSet { s, _ := w.store.ChangeSets("bob"); return s }() {
		if o.From == "alice" {
			asked = o
		}
	}
	if asked.ID == "" || asked.AskOf != cs.ID || len(asked.Items) != 1 || asked.Items[0].Of != 3 || len(asked.Items[0].Warnings) != 1 || asked.Items[0].BeforeText != "not bound" || asked.Items[0].NeedsAdmin {
		t.Fatalf("bob's copy: %+v", asked)
	}
	// not his agent's to read, withdraw or apply; nor alice's
	if code, body := w.do("GET", "/delegate/changesets", "", agentOf("bob")); code != 200 || strings.Contains(body, asked.ID) {
		t.Fatalf("bob's agent reads a request from alice: %d %s", code, body)
	}
	for name, c := range map[string]func(*http.Request){"bob's agent": agentOf("bob"), "alice": alice, "alice's agent": agentOf("alice")} {
		if code, _ := w.do("GET", "/delegate/changesets/"+asked.ID, "", c); code != 404 && code != 403 {
			t.Errorf("%s reads the admin's copy: %d", name, code)
		}
		if code, _ := w.do("DELETE", "/delegate/changesets/"+asked.ID, "", c); code == 200 {
			t.Errorf("%s withdrew the admin's copy", name)
		}
	}
	// an admin's act: a recent sign-in, no opt-in of his own
	oldSID, old := w.session("bob")
	w.a.TestAgeSession(oldSID, 11*time.Minute, 0)
	if code, body := w.do("POST", "/delegate/changesets/"+asked.ID+"/approve", `{"items":[1]}`, old); code != 403 || !strings.Contains(body, `"stepUp"`) {
		t.Fatalf("an old sign-in applies a request: %d %s", code, body)
	}
	// while alice's agent is paused nothing of hers is applied, by anyone
	_, _ = w.store.SetState("alice", func(st *delegation.State) { st.Paused = true })
	if code, body := w.do("POST", "/delegate/changesets/"+asked.ID+"/approve", `{"items":[1]}`, bob); code != 409 || !strings.Contains(body, `"refusal":"paused"`) {
		t.Fatalf("applied while the asker's agent is paused: %d %s", code, body)
	}
	_, _ = w.store.SetState("alice", func(st *delegation.State) { st.Paused = false })
	if got := w.approve(t, asked.ID, `{"items":[1]}`, bob); got.State != delegation.SetApplied || w.binding("apps/helper", "llm") != "xbin:llm" {
		t.Fatalf("bob applies it: %s %q", got.State, w.binding("apps/helper", "llm"))
	}
	if got := w.set(t, "alice", cs.ID); got.Items[2].Result != delegation.ItemApplied {
		t.Fatalf("alice's line after the admin applied it: %+v", got.Items[2])
	}
	// both people's logs say what happened, truthfully: bob's that he applied alice's request (her agent's
	// proposal, not his), alice's that bob applied her line
	find := func(user string) (out delegation.Call) {
		rows, _ := w.store.Audit.Read(user, time.Time{}, 0)
		for _, c := range rows {
			if c.Class == "changeset:binding.set" && c.Status == 200 && strings.HasSuffix(c.Path, "/bindings") && (c.From != "" || c.By != "") {
				out = c
			}
		}
		return out
	}
	if row := find("bob"); row.From != "alice" || row.By != "" || row.Ticket != asked.ID {
		t.Errorf("bob's log of applying alice's request: %+v", row)
	}
	if row := find("alice"); row.By != "bob" || row.From != "" || row.Ticket != cs.ID {
		t.Errorf("alice's log of her line being applied by bob: %+v", row)
	}

	// a request an admin turns down; and one withdrawn when the asker's agent stops for good
	for _, end := range []string{"reject", "off"} {
		cs = w.plan(t, "alice", `[{"kind":"grant.add","args":{"from":"apps/helper","target":"apps/x","role":"reader"}}]`)
		if got := flags(cs); got != "AN[]" {
			t.Fatalf("a grant, for a person: %s", got)
		}
		if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", `{"items":[1]}`, alice); code != 200 {
			t.Fatalf("ask: %d %s", code, body)
		}
		sets, _ := w.store.ChangeSets("bob")
		for _, o := range sets {
			if o.From == "alice" && o.AskOf == cs.ID {
				asked = o
			}
		}
		switch end {
		case "reject":
			if code, _ := w.do("POST", "/delegate/changesets/"+asked.ID+"/reject", "", bob); code != 200 {
				t.Fatal("reject")
			}
			if got := w.set(t, "alice", cs.ID).Items[0]; got.Result != delegation.ItemRejected || !strings.Contains(got.Error, "bob") {
				t.Fatalf("alice's line after the admin turned it down: %+v", got)
			}
		case "off": // workspace agents are switched off: nothing her agent proposed stays to be applied
			if code, body := w.do("PUT", "/delegate/workspace", `{"enabled":false}`, bob); code != 200 {
				t.Fatalf("off: %d %s", code, body)
			}
			if got := w.set(t, "bob", asked.ID); got.State != delegation.SetWithdrawn || got.WithdrawnBy != delegation.WithdrawnByXbin {
				t.Fatalf("a request of a stopped agent's person still waits: %s by %q", got.State, got.WithdrawnBy)
			}
		}
	}
}

// A personal line is applied only while it is still one: what was the
// person's own to do when it was proposed, and takes an admin's rights by
// the time it is applied, is not applied — opt-in or not.
func TestPersonalLineThatBecameAnAdmins(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	done := w.approve(t, w.plan(t, "bob", `[{"kind":"tile.create","args":{"template":"tool","path":"apps/dice"}},{"kind":"tile.create","args":{"template":"assistant","path":"apps/assistant"}}]`).ID, "", bob)
	if done.State != delegation.SetApplied {
		t.Fatalf("the tiles: %s", results(done))
	}
	cs := w.plan(t, "bob", `[{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"}}]`)
	if got := flags(cs); got != "[]" {
		t.Fatalf("a binding between bob's own tiles: %s", got)
	}
	if err := w.b.Users.SetOwner("apps/dice", ""); err != nil { // meanwhile the provider becomes the workspace's
		t.Fatal(err)
	}
	done = w.approve(t, cs.ID, `{"items":[1]}`, bob)
	if results(done) != "failed" || !strings.Contains(done.Items[0].Error, "admin's rights") || w.binding("apps/assistant", "mcp") != "" {
		t.Fatalf("a personal line that now needs an admin was applied: %s %q %q", results(done), done.Items[0].Error, w.binding("apps/assistant", "mcp"))
	}
}

// Each line is held to what the person's AGENT may touch — the tile's own
// "agent" level and its orgs' agent rules — when it is described and again
// when it is applied.
func TestPlanLinesFollowTheAgentRules(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatal("opt in")
	}
	w.write(map[string]string{"apps/x/xbin.json": `{"runtime":"go","agent":"off","interfaces":{"mcp":{"kind":"http","service":"mcp"}}}`})
	w.rescan()
	const line = `[{"kind":"org.share","args":{"org":"fin","tile":"apps/x","level":"read"}}]`
	cs := w.plan(t, "bob", line)
	if !strings.Contains(cs.Items[0].Blocked, `agent level is "off"`) {
		t.Fatalf("a line on a tile that turns agents away: %+v", cs.Items[0])
	}
	// "read" is about calls to the tile's API: a line is no call, and is the person's own change
	w.write(map[string]string{"apps/pu/xbin.json": `{"runtime":"go","partition":["user"],"agent":"read"}`})
	w.rescan()
	if cs = w.plan(t, "bob", `[{"kind":"org.share","args":{"org":"fin","tile":"apps/pu","level":"read"}}]`); cs.Items[0].Blocked != "" {
		t.Fatalf("a line on a tile whose agent level is read: %+v", cs.Items[0])
	}
	// …and the review says what that means, in xbind's words
	if eff := strings.Join(cs.Items[0].Effects, "\n"); !strings.Contains(eff, "apps/pu: this tile's author marked it read-only for agents: this changes how it is wired, not its data") {
		t.Fatalf("a line that manages a read-only tile doesn't say so: %s", eff)
	}
	// an org's rule that arrives between the proposal and the approval
	cs = w.plan(t, "bob", `[{"kind":"org.share","args":{"org":"fin","tile":"apps/pg","level":"read"}}]`)
	if cs.Items[0].Blocked != "" {
		t.Fatalf("a line on apps/pg: %+v", cs.Items[0])
	}
	w.b.AgentPolicy = policyFunc(func(s delegation.Subject) error {
		if s.Tile == "apps/pg" {
			return fmt.Errorf("Finance doesn't let workspace agents in")
		}
		return nil
	})
	done := w.approve(t, cs.ID, `{"items":[1],"password":"password1"}`, bob)
	if results(done) != "failed" || !strings.Contains(done.Items[0].Error, "Finance") {
		t.Fatalf("applied past an org's agent rule: %s %q", results(done), done.Items[0].Error)
	}
	// the admin kinds proposed the old way are as they were
	old := w.propose(t, line)
	if old.Plan || old.Items[0].Blocked != "" && strings.Contains(old.Items[0].Blocked, "agent level") {
		t.Fatalf("an admin change set proposed the old way changed: %+v", old.Items[0])
	}
}

// The property the whole design rests on: a line xbind calls personal is
// one its person can do by hand with no admin right. For every kind, over
// a corpus of arguments, for an admin and for a user: a line that is not
// an admin's (and not blocked) is described without a refusal for the same
// person stripped of every admin right.
func TestPersonalLinesNeedNoAdmin(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	_, alice := w.session("alice")
	for user, cred := range map[string]func(*http.Request){"bob": bob, "alice": alice} {
		done := w.approve(t, w.plan(t, user, fmt.Sprintf(`[{"kind":"tile.create","args":{"template":"tool","path":"apps/%[1]s-tool"}},{"kind":"tile.create","args":{"template":"assistant","path":"apps/%[1]s-asst"}}]`, user)).ID, "", cred)
		if done.State != delegation.SetApplied {
			t.Fatalf("%s's tiles: %s", user, results(done))
		}
	}
	tiles := []string{"apps/x", "apps/pg", "apps/bob-tool", "apps/bob-asst", "apps/alice-tool", "apps/alice-asst", "users/alice/mcp", "apps/q", "apps/nope"}
	var ops []delegation.ChangeOp
	op := func(kind string, args map[string]any) {
		ops = append(ops, delegation.ChangeOp{Kind: kind, Args: mustJSON(args)})
	}
	for _, a := range tiles {
		for _, b := range append([]string{"xbin:llm", "internet", "none", "set:corp"}, tiles...) {
			for _, slot := range []string{"mcp", "llm", "net"} {
				op(ChangeBindingSet, map[string]any{"component": a, "slot": slot, "provider": b})
			}
			op(ChangeGrantAdd, map[string]any{"from": a, "target": b, "role": "reader"})
			op(ChangeGrantRevoke, map[string]any{"from": a, "target": b, "role": "reader"})
		}
		op(ChangeBindingRemove, map[string]any{"component": a, "slot": "mcp"})
		op(ChangeOrgShare, map[string]any{"org": "fin", "tile": a, "level": "read"})
		op(ChangeLimitsSet, map[string]any{"tile": a, "kind": "memMiB", "value": 4096})
		for _, owner := range []string{"", "org:fin"} {
			for _, tpl := range []string{"tool", "assistant", "nope"} {
				op(ChangeTileCreate, map[string]any{"template": tpl, "path": a + "/new", "owner": owner})
				op(ChangeTileCreate, map[string]any{"template": tpl, "path": "tiles/new", "owner": owner})
				op(ChangeTileCreate, map[string]any{"template": tpl, "path": "apps/new-" + tpl, "owner": owner})
			}
		}
	}
	personal := map[string]int{}
	for _, user := range []string{"bob", "alice"} {
		p := personP(t, w.partWS, user)
		// the same person with no admin right of any kind
		plainUser := *p.User
		plainUser.Role = users.RoleUser
		plain := p
		plain.User = &plainUser
		for _, o := range ops {
			pv, err := w.b.PreviewPlanned(p, o, nil)
			if err != nil || pv.Admin || pv.Blocked != "" {
				continue
			}
			personal[user+" "+o.Kind]++
			got, err := w.b.PreviewPlanned(plain, o, nil)
			if err != nil || got.Blocked != "" {
				t.Errorf("%s: %s %s is called personal, and takes an admin's rights: %v %q", user, o.Kind, o.Args, err, got.Blocked)
			}
			if !slices.Contains([]string{ChangeTileCreate, ChangeBindingSet, ChangeBindingRemove}, o.Kind) {
				t.Errorf("%s: a %s line is called personal", user, o.Kind)
			}
		}
	}
	// an admin's access says yes to every org: creating in one they hold no Create in is not their own act
	if pv, err := w.b.PreviewPlanned(personP(t, w.partWS, "bob"), delegation.ChangeOp{Kind: ChangeTileCreate, Args: mustJSON(map[string]any{"template": "tool", "path": "apps/fin-tool", "owner": "org:fin"})}, nil); err != nil || !pv.Admin || pv.Blocked != "" {
		t.Errorf("an admin creating in an org they are no member of: admin %v blocked %q err %v", pv.Admin, pv.Blocked, err)
	}
	// the corpus reaches the personal lines there are
	for _, k := range []string{"bob tile.create", "bob binding.set", "bob binding.remove", "alice tile.create", "alice binding.set", "alice binding.remove"} {
		if personal[k] == 0 {
			t.Errorf("no personal %s line in the corpus: the property is checked on nothing (%v)", k, personal)
		}
	}
}

// A proposal is no way to read admin state. Of a line that takes an admin's
// rights, or that xbind can't apply, the agent — its credential, its
// conversation, its page — reads xbind's one sentence and nothing else: not
// what is there now, not what it would open, not why it is refused; the
// person's own session reads the whole line. An admin who lets their agent
// read admin state (adminChanges) lets it read these too.
func TestPlanShowsTheAgentNoAdminState(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	frame := func(r *http.Request) {
		rq := httptest.NewRequest("GET", "/", nil)
		bob(rq)
		p, _ := w.a.FromRequest(rq)
		r.Header.Set(auth.FrameTokenHeader, w.a.MintFrameTokenFor(p, "apps/q", time.Minute))
	}
	// apps/x holds grants an agent has no business reading; fin has members
	const lines = `[{"kind":"grant.revoke","args":{"from":"apps/x","target":"apps/pu","role":"reader"}},
		{"kind":"grant.add","args":{"from":"apps/x","target":"users/alice/mcp","role":"reader"}},
		{"kind":"binding.set","args":{"component":"apps/x","slot":"nope","provider":"apps/pg"}},
		{"kind":"org.share","args":{"org":"fin","tile":"apps/x","level":"read"}},
		{"kind":"tile.create","args":{"template":"tool","path":"apps/x"}}]`
	secrets := []string{`"roles"`, `"before"`, `"beforeText"`, `"after"`, `"effects"`, `"warnings"`, `"inverse"`, `"basis"`, "does not request", "already exists", "member(s)"}
	leaks := func(body string) (out []string) {
		for _, s := range secrets {
			if strings.Contains(body, s) {
				out = append(out, s)
			}
		}
		return out
	}
	code, body := w.do("POST", "/delegate/plans", `{"title":"probe","items":`+lines+`}`, agentOf("bob"))
	var cs delegation.ChangeSet
	if json.Unmarshal([]byte(body), &cs); code != 200 || len(cs.Items) != 5 {
		t.Fatalf("plan: %d %s", code, body)
	}
	if l := leaks(body); len(l) > 0 {
		t.Fatalf("the agent reads admin state from its own proposal's answer: %v\n%s", l, body)
	}
	for i, it := range cs.Items {
		if it.Line == "" || (i == 2 || i == 4) && it.Blocked != "xbin can't apply this line as proposed: your person sees why on xbin's review" {
			t.Errorf("line %d as the agent reads it: %+v", i+1, it)
		}
	}
	for name, c := range map[string]func(*http.Request){"the agent": agentOf("bob"), "its page": frame} {
		for _, path := range []string{"/delegate/changesets", "/delegate/changesets/" + cs.ID} {
			code, body := w.do("GET", path, "", c)
			if l := leaks(body); code != 200 || len(l) > 0 || !strings.Contains(body, "Take away apps/x's reader access to apps/pu") {
				t.Errorf("%s reads %s: %d, admin state %v", name, path, code, l)
			}
		}
	}
	// the person's own session reads the whole line
	if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", bob); code != 200 || len(leaks(body)) < 6 || !strings.Contains(body, "does not request") {
		t.Fatalf("bob's own review is short of what xbind knows: %d %v", code, leaks(body))
	}
	// applied or failed is all the agent learns of an admin's line it proposed blind
	_, _ = w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob)
	done := w.approve(t, cs.ID, `{"items":[1,2],"password":"password1"}`, bob)
	_, _ = w.do("PUT", "/delegate/state", `{"adminChanges":false}`, bob)
	if results(done) != "applied,applied,blocked,rejected,blocked" {
		t.Fatalf("bob applies: %s", results(done))
	}
	if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", agentOf("bob")); code != 200 || len(leaks(body)) > 0 || !strings.Contains(body, `"result":"applied"`) || strings.Contains(body, `"error"`) {
		t.Fatalf("after the approval the agent reads: %d %v %s", code, leaks(body), body)
	}
	// with the opt-in on, the agent reads what the person reads
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatal("opt in")
	}
	if code, body := w.do("POST", "/delegate/plans", `{"title":"probe","items":`+lines+`}`, agentOf("bob")); code != 200 || len(leaks(body)) < 4 {
		t.Fatalf("an opted-in admin's agent: %d %v", code, leaks(body))
	}
}

// scoped is user's agent under a conversation limited to tiles.
func (w *csWorld) scoped(t *testing.T, user string, tiles ...string) func(*http.Request) {
	t.Helper()
	h, err := w.store.NewAgentSession(user, "apps/q", &delegation.SessionScope{Tiles: tiles, Orgs: []string{}}, "local/test")
	if err != nil {
		t.Fatal(err)
	}
	return func(r *http.Request) {
		agentOf(user)(r)
		r.Header.Set(server.HeaderAgentSession, h.ID)
	}
}

func (w *csWorld) planAs(t *testing.T, user string, cred func(*http.Request), items string) delegation.ChangeSet {
	t.Helper()
	code, body := w.do("POST", "/delegate/plans", `{"title":"t","items":`+items+`}`, cred)
	var cs delegation.ChangeSet
	if json.Unmarshal([]byte(body), &cs); code != 200 {
		t.Fatalf("plan: %d %s", code, body)
	}
	return w.set(t, user, cs.ID)
}

// Every tile a line names is held to what the conversation may see: its
// scope, and the tiles hidden from its person — the consumer and the
// provider of a binding, both ends of a grant, a start's tile. A tile
// that is hidden and one that isn't there get one answer.
func TestPlanLinesNameOnlyWhatTheConversationSees(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	if code, _ := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatal("opt in")
	}
	narrow := w.scoped(t, "bob", "apps/x")
	cs := w.planAs(t, "bob", narrow, `[
		{"kind":"grant.add","args":{"from":"apps/pu","target":"apps/x","role":"writer"}},
		{"kind":"grant.add","args":{"from":"apps/pg","target":"apps/x","role":"reader"}},
		{"kind":"binding.set","args":{"component":"apps/pu","slot":"mcp","provider":"apps/x"}},
		{"kind":"org.share","args":{"org":"fin","tile":"apps/pg","level":"read"}},
		{"kind":"org.share","args":{"org":"fin","tile":"apps/x","level":"read"}},
		{"kind":"tile.create","args":{"template":"tool","path":"apps/new"}}]`)
	for i, want := range []bool{true, true, true, true, false, true} {
		if got := strings.Contains(cs.Items[i].Blocked, "hidden by you"); got != want {
			t.Errorf("line %d (%s) outside the scope: blocked %q", i+1, cs.Items[i].Line, cs.Items[i].Blocked)
		}
	}
	// narrowed after the proposal: refused at Apply
	wide := w.planAs(t, "bob", agentOf("bob"), `[{"kind":"org.share","args":{"org":"fin","tile":"apps/pg","level":"read"}}]`)
	if wide.Items[0].Blocked != "" {
		t.Fatalf("with no scope: %+v", wide.Items[0])
	}
	h, _ := csHandles.Load("bob")
	if _, err := w.store.UpdateAgentSession("bob", "apps/q", h.(string), func(s *delegation.AgentSession) error {
		s.Scope = &delegation.SessionScope{Tiles: []string{"apps/x"}, Orgs: []string{}}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	done := w.approve(t, wide.ID, `{"items":[1],"password":"password1"}`, bob)
	if results(done) != "failed" || !strings.Contains(done.Items[0].Error, "hidden by you") {
		t.Fatalf("applied past the conversation's scope: %s %q", results(done), done.Items[0].Error)
	}
	// hidden from its person, or not there: one answer
	w.write(map[string]string{"secret/t/xbin.json": `{"runtime":"go"}`, "secret/t/backend/main.go": "package main\n"})
	w.rescan()
	a := w.planAs(t, "alice", agentOf("alice"), `[{"kind":"agent.start","args":{"tile":"secret/t","brief":"go"}},{"kind":"agent.start","args":{"tile":"secret/none","brief":"go"}}]`)
	if a.Items[0].Blocked == "" || a.Items[0].Blocked != a.Items[1].Blocked || strings.Contains(a.Items[0].Blocked, "secret/") {
		t.Fatalf("a tile hidden from alice and one that isn't there are told apart:\n%q\n%q", a.Items[0].Blocked, a.Items[1].Blocked)
	}
}
