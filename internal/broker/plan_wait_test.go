package broker

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/internal/auth"
	"github.com/xbin-dev/xbin/internal/delegation"
	"github.com/xbin-dev/xbin/internal/term"
	"github.com/xbin-dev/xbin/internal/users"
)

// The plan the dice journey makes now that the tool's tile is BUILT, not
// stamped (D243): an empty tile, its tile agent, the assistant — and the
// binding of the assistant's mcp slot to a tile that provides nothing yet.
const waitPlan = `[
	{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"},"why":"the tool's tile"},
	{"kind":"agent.start","args":{"tile":"apps/dice","brief":"Goal: an MCP server with roll_dice.","harness":"claude"},"why":"build it"},
	{"kind":"tile.create","args":{"template":"assistant","path":"apps/assistant"},"why":"the assistant"},
	{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"},"why":"the tool"}]`

const (
	diceProvides = `{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`
	diceMain     = "package main\n\nfunc main() {}\n"
)

// startingWorld is a world whose plans can start tile agents: every check
// of a start runs, and the session is taken as opened (the fixture has no
// sandbox to open one in).
func startingWorld(t *testing.T) *csWorld {
	t.Helper()
	w := changeSetWorld(t)
	planTemplates(t, w)
	w.srv.Term = term.NewManager(w.b.Reg.Root, nil)
	w.srv.Term.AgentPolicy = delegation.OpenPolicy{}
	n := 0
	w.srv.PlanStarter = func(context.Context, auth.Principal, []byte) (string, int, string) {
		n++
		return fmt.Sprintf("sess-%d", n), 200, ""
	}
	return w
}

// waitWorld is startingWorld with waitPlan applied by user — its last line
// waiting.
func waitWorld(t *testing.T, user string) (*csWorld, func(*http.Request), delegation.ChangeSet) {
	t.Helper()
	w := startingWorld(t)
	_, cred := w.session(user)
	cs := w.plan(t, user, waitPlan)
	done := w.approve(t, cs.ID, `{"items":[1,2,3,4]}`, cred)
	if results(done) != "applied,applied,applied,waiting" || done.State != delegation.SetApplied {
		t.Fatalf("the plan applied: %s %s (%s; %s)", done.State, results(done), done.Items[1].Error, done.Items[3].Error)
	}
	return w, cred, w.set(t, user, cs.ID)
}

// writes the dice tile's manifest as its coding agent would, and lets xbind see it.
func (w *csWorld) dice(manifest string) {
	w.write(map[string]string{"apps/dice/xbin.json": manifest, "apps/dice/backend/main.go": diceMain})
	w.rescan()
	w.srv.CheckPlanWaits()
}

func (w *csWorld) waitRows(t *testing.T, user, id string) (ok, refused []delegation.Call) {
	t.Helper()
	rows, _ := w.store.Audit.Read(user, time.Time{}, 0)
	for _, c := range rows {
		if c.Ticket != id || c.Under != "waited" {
			continue
		}
		if c.Status == 200 {
			ok = append(ok, c)
		} else {
			refused = append(refused, c)
		}
	}
	return ok, refused
}

// A binding to a tile that provides nothing yet is approved on the plan's
// one review and applied by xbind itself the moment the tile declares what
// was approved — once, audited as a line of the person's plan.
func TestPlanLineWaitsForItsProvide(t *testing.T) {
	w := startingWorld(t)
	_, bob := w.session("bob")
	cs := w.plan(t, "bob", waitPlan)
	if got := flags(cs); got != "[] D[1] [] D[1 2 3]" {
		t.Fatalf("the lines' flags: %s", got)
	}
	l := cs.Items[3]
	eff := strings.Join(l.Effects, "\n")
	if l.Line != "Bind apps/assistant's mcp to apps/dice" || l.Blocked != "" || l.Admin || l.Wait == nil ||
		l.Wait.PlanWait != (delegation.PlanWait{Component: "apps/assistant", Slot: "mcp", Tile: "apps/dice", Kind: "http", Service: "mcp", Role: "reader"}) ||
		!strings.Contains(eff, "applies once apps/dice provides mcp; nothing is bound until then") || !strings.Contains(eff, "it waits at most 24 hours after you apply this plan, then lapses") ||
		!strings.Contains(eff, "for the role reader — and only that") || l.AfterText != "apps/dice, once it provides mcp" {
		t.Fatalf("the line that waits, in xbind's words: %+v\n%s", l, eff)
	}
	// without a tile agent started in that tile before it, nothing waits: the line can't be applied, as ever
	for name, items := range map[string]string{
		"no start": `[{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"}},{"kind":"tile.create","args":{"template":"assistant","path":"apps/assistant"}},
			{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"}}]`,
		"the start after the line": `[{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"}},{"kind":"tile.create","args":{"template":"assistant","path":"apps/assistant"}},
			{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"}},{"kind":"agent.start","args":{"tile":"apps/dice","brief":"b","harness":"claude"}}]`,
		"a start in another tile": `[{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"}},{"kind":"agent.start","args":{"tile":"apps/x","brief":"b","harness":"claude"}},
			{"kind":"tile.create","args":{"template":"assistant","path":"apps/assistant"}},{"kind":"binding.set","args":{"component":"apps/assistant","slot":"mcp","provider":"apps/dice"}}]`,
	} {
		other := w.plan(t, "bob", items)
		for _, it := range other.Items {
			if it.Kind == ChangeBindingSet && (it.Wait != nil || !strings.Contains(it.Blocked, `does not provide the "mcp" service`)) {
				t.Errorf("%s: the binding: wait %v, blocked %q — want it blocked, never waiting", name, it.Wait, it.Blocked)
			}
		}
	}
	// the line is applied only with the lines it needs — the start among them
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/approve", `{"items":[1,3,4]}`, bob); code != 400 || !strings.Contains(body, "needs line 2") {
		t.Fatalf("the waiting line without its start: %d %s", code, body)
	}
	done := w.approve(t, cs.ID, `{"items":[1,2,3,4]}`, bob)
	wait := done.Items[3].Wait
	if results(done) != "applied,applied,applied,waiting" || done.State != delegation.SetApplied || wait == nil ||
		time.Until(wait.Until) < 23*time.Hour || time.Until(wait.Until) > 25*time.Hour || w.binding("apps/assistant", "mcp") != "" {
		t.Fatalf("after Apply: %s %s, wait %+v, bound %q", done.State, results(done), wait, w.binding("apps/assistant", "mcp"))
	}
	// nothing yet: checks change nothing
	w.srv.CheckPlanWaits()
	w.dice(`{"runtime":"go"}`)
	if got := w.set(t, "bob", cs.ID); got.Items[3].Result != delegation.ItemWaiting || w.binding("apps/assistant", "mcp") != "" {
		t.Fatalf("before the tile provides anything: %s, bound %q", results(got), w.binding("apps/assistant", "mcp"))
	}
	// the tile's coding agent writes the manifest: many scans see it at once — one binding, one row
	w.write(map[string]string{"apps/dice/xbin.json": diceProvides, "apps/dice/backend/main.go": diceMain})
	w.rescan()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); w.srv.CheckPlanWaits() }()
	}
	wg.Wait()
	w.srv.CheckPlanWaits()
	got := w.set(t, "bob", cs.ID)
	if got.Items[3].Result != delegation.ItemApplied || got.Items[3].Inverse == nil || w.binding("apps/assistant", "mcp") != "apps/dice" {
		t.Fatalf("once apps/dice provides mcp: %s (%s), bound %q", results(got), got.Items[3].Error, w.binding("apps/assistant", "mcp"))
	}
	ok, refused := w.waitRows(t, "bob", cs.ID)
	if len(ok) != 1 || len(refused) != 0 || ok[0].Class != "changeset:binding.set" || ok[0].Agent == "" || ok[0].Path != "/api/xbin/bindings" {
		t.Fatalf("the log of the line that waited: applied %+v, refused %+v — want one row, of bob's plan", ok, refused)
	}
	// it can be undone like any applied line
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/undo", "", bob); code != 200 || !strings.Contains(body, "Unbind apps/assistant's mcp") {
		t.Fatalf("undo after it applied: %d %s", code, body)
	}
}

// A tile that declares something else than was approved — another role,
// another kind, by instance, twice — is not bound: the line stays waiting,
// says why once, and applies when the tile declares what was approved.
func TestWaitingLineBindsOnlyWhatWasApproved(t *testing.T) {
	for name, c := range map[string]struct{ manifest, held string }{
		"a wider role":    {`{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"writer"}}}`, `declares mcp for the role "writer": what you approved is the role reader`},
		"admin":           {`{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"admin"}}}`, `declares mcp for the role "admin"`},
		"no role":         {`{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp"}}}`, `declares mcp for the role ""`},
		"another kind":    {`{"runtime":"go","provides":{"mcp":{"kind":"net","role":"reader"}}}`, `what you approved is an http interface of the service mcp`},
		"by instance":     {`{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader","instances":true}}}`, `as bound by instance`},
		"declared twice":  {`{"runtime":"go","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"},"more":{"kind":"http","service":"mcp","role":"admin"}}}`, `declares mcp more than once`},
		"another service": {`{"runtime":"go","provides":{"mcp":{"kind":"http","service":"openai","role":"reader"}}}`, `what you approved is an http interface of the service mcp`},
	} {
		t.Run(name, func(t *testing.T) {
			w, _, cs := waitWorld(t, "bob")
			w.dice(c.manifest)
			w.srv.CheckPlanWaits() // a second look says nothing again
			got := w.set(t, "bob", cs.ID).Items[3]
			if got.Result != delegation.ItemWaiting || !strings.Contains(got.Wait.Held, c.held) || w.binding("apps/assistant", "mcp") != "" {
				t.Fatalf("result %s, held %q (want %q), bound %q", got.Result, got.Wait.Held, c.held, w.binding("apps/assistant", "mcp"))
			}
			if ok, refused := w.waitRows(t, "bob", cs.ID); len(ok) != 0 || len(refused) != 1 || !strings.Contains(refused[0].Refused, c.held) {
				t.Fatalf("the log: applied %+v, refused %+v — want it said once", ok, refused)
			}
			// the agent's own view says it waits, not why it is held (the reason is the person's to read)
			if code, body := w.do("GET", "/delegate/changesets/"+cs.ID, "", agentOf("bob")); code != 200 || !strings.Contains(body, `"result":"waiting"`) {
				t.Fatalf("the agent reads: %d %s", code, body)
			}
			w.dice(diceProvides)
			if got := w.set(t, "bob", cs.ID).Items[3]; got.Result != delegation.ItemApplied || got.Wait.Held != "" || w.binding("apps/assistant", "mcp") != "apps/dice" {
				t.Fatalf("once it declares what was approved: %s held %q bound %q", got.Result, got.Wait.Held, w.binding("apps/assistant", "mcp"))
			}
		})
	}
}

// What ends a wait: its time, the person, their agent's end, a change to
// its tiles or the slot, and every check a line gets at Apply — run again
// at the moment it would apply. Ended, it never applies.
func TestWaitingLineEnds(t *testing.T) {
	type world struct {
		*csWorld
		cred func(*http.Request)
		cs   delegation.ChangeSet
	}
	cases := []struct {
		name string
		user string
		end  func(t *testing.T, w world)
		// want: the result once the tile then declares what was approved, and words of its reason
		want, why string
	}{
		{"it lapses after 24 hours", "bob", func(t *testing.T, w world) {
			if _, err := w.store.UpdateChangeSet("bob", w.cs.ID, func(c *delegation.ChangeSet) error {
				x := *c.Items[3].Wait
				x.Until = time.Now().Add(-time.Minute)
				c.Items[3].Wait = &x
				return nil
			}); err != nil {
				t.Fatal(err)
			}
		}, delegation.ItemLapsed, "did not provide mcp within 24 hours"},
		{"the person takes the plan back", "bob", func(t *testing.T, w world) {
			if code, body := w.do("POST", "/delegate/changesets/"+w.cs.ID+"/reject", "", w.cred); code != 200 {
				t.Fatalf("reject: %d %s", code, body)
			}
		}, delegation.ItemCancelled, "you cancelled it"},
		{"the agent takes the plan back", "bob", func(t *testing.T, w world) {
			if code, body := w.do("DELETE", "/delegate/changesets/"+w.cs.ID, "", agentOf("bob")); code != 200 {
				t.Fatalf("withdraw: %d %s", code, body)
			}
		}, delegation.ItemCancelled, "your agent took the plan back"},
		{"the person undoes the plan", "bob", func(t *testing.T, w world) {
			if code, body := w.do("POST", "/delegate/changesets/"+w.cs.ID+"/undo", "", w.cred); code != 409 && code != 200 {
				t.Fatalf("undo: %d %s", code, body)
			}
		}, delegation.ItemCancelled, "you undid the plan"},
		{"workspace agents are switched off", "bob", func(t *testing.T, w world) {
			if code, body := w.do("PUT", "/delegate/workspace", `{"enabled":false}`, w.cred); code != 200 {
				t.Fatalf("off: %d %s", code, body)
			}
			if code, body := w.do("PUT", "/delegate/workspace", `{"enabled":true}`, w.cred); code != 200 { // and on again: what was ended stays ended
				t.Fatalf("on: %d %s", code, body)
			}
		}, delegation.ItemCancelled, "your workspace agent was stopped"},
		{"the provider is deleted", "bob", func(t *testing.T, w world) {
			if err := os.RemoveAll(filepath.Join(w.b.Reg.Root, "apps", "dice")); err != nil {
				t.Fatal(err)
			}
			w.rescan()
			w.srv.CheckPlanWaits()
			if got := w.set(t, "bob", w.cs.ID).Items[3]; got.Result != delegation.ItemCancelled {
				t.Fatalf("at the deletion already: %s", got.Result)
			}
		}, delegation.ItemCancelled, "apps/dice no longer exists"},
		{"the consumer is deleted", "bob", func(t *testing.T, w world) {
			if err := os.RemoveAll(filepath.Join(w.b.Reg.Root, "apps", "assistant")); err != nil {
				t.Fatal(err)
			}
			w.rescan()
		}, delegation.ItemCancelled, "apps/assistant no longer exists"},
		{"the provider changes owner", "bob", func(t *testing.T, w world) {
			if err := w.b.Users.SetOwner("apps/dice", "user:alice"); err != nil {
				t.Fatal(err)
			}
		}, delegation.ItemCancelled, "changed owner since you approved it"},
		{"the consumer changes owner", "bob", func(t *testing.T, w world) {
			if err := w.b.Users.SetOwner("apps/assistant", "org:fin"); err != nil {
				t.Fatal(err)
			}
		}, delegation.ItemCancelled, "changed owner since you approved it"},
		{"the slot is bound by someone meanwhile", "bob", func(t *testing.T, w world) {
			w.write(map[string]string{"apps/other/xbin.json": diceProvides, "apps/other/backend/main.go": diceMain})
			w.rescan()
			if code, body := w.do("POST", "/bindings", `{"component":"apps/assistant","slot":"mcp","provider":"apps/other"}`, w.cred); code != 200 {
				t.Fatalf("bind: %d %s", code, body)
			}
		}, delegation.ItemCancelled, "was bound by someone since you approved this"},
		{"the account is disabled", "alice", func(t *testing.T, w world) {
			u, _ := w.b.Users.Get("alice")
			u.Disabled = true
			if _, err := w.b.Users.Upsert(*u, ""); err != nil {
				t.Fatal(err)
			}
		}, delegation.ItemCancelled, "can no longer apply changes"},
		{"the tile turns agents away", "bob", func(t *testing.T, w world) {
			w.dice(`{"runtime":"go","agent":"off","provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}`)
		}, delegation.ItemCancelled, ""},
		{"the conversation that proposed it is over", "bob", func(t *testing.T, w world) {
			if ok, err := w.store.DeleteAgentSession("bob", "apps/q", w.cs.Session); err != nil || !ok {
				t.Fatal(ok, err)
			}
		}, delegation.ItemCancelled, "the conversation that proposed this is over"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cw, cred, cs := waitWorld(t, c.user)
			c.end(t, world{cw, cred, cs})
			before := cw.binding("apps/assistant", "mcp")
			if _, err := os.Stat(filepath.Join(cw.b.Reg.Root, "apps", "dice")); err == nil {
				cw.dice(strings.Replace(diceProvides, `{"runtime":"go"`, `{"runtime":"go","agent":"`+agentLevelOf(c.name)+`"`, 1))
			} else {
				cw.srv.CheckPlanWaits()
			}
			got := cw.set(t, c.user, cs.ID).Items[3]
			if got.Result != c.want || !strings.Contains(got.Error, c.why) || cw.binding("apps/assistant", "mcp") != before {
				t.Fatalf("result %s (%q), want %s (%q); the slot: %q, was %q", got.Result, got.Error, c.want, c.why, cw.binding("apps/assistant", "mcp"), before)
			}
			if ok, _ := cw.waitRows(t, c.user, cs.ID); len(ok) != 0 {
				t.Fatalf("an ended line was applied: %+v", ok)
			}
			// ended is ended: nothing brings it back
			cw.srv.CheckPlanWaits()
			if again := cw.set(t, c.user, cs.ID).Items[3]; again.Result != c.want {
				t.Fatalf("after another look: %s", again.Result)
			}
		})
	}
}

func agentLevelOf(name string) string {
	if name == "the tile turns agents away" {
		return "off"
	}
	return "full"
}

// While its person's agent is paused the line neither applies nor ends: it
// applies when they resume — if its time isn't up.
func TestWaitingLineWaitsOutAPause(t *testing.T) {
	w, bob, cs := waitWorld(t, "bob")
	if code, body := w.do("PUT", "/delegate/state", `{"paused":true}`, bob); code != 200 {
		t.Fatalf("pause: %d %s", code, body)
	}
	w.dice(diceProvides)
	if got := w.set(t, "bob", cs.ID).Items[3]; got.Result != delegation.ItemWaiting || w.binding("apps/assistant", "mcp") != "" {
		t.Fatalf("while paused: %s, bound %q", got.Result, w.binding("apps/assistant", "mcp"))
	}
	if code, body := w.do("PUT", "/delegate/state", `{"paused":false}`, bob); code != 200 {
		t.Fatalf("resume: %d %s", code, body)
	}
	w.srv.CheckPlanWaits()
	if got := w.set(t, "bob", cs.ID).Items[3]; got.Result != delegation.ItemApplied || w.binding("apps/assistant", "mcp") != "apps/dice" {
		t.Fatalf("after the resume: %s (%s), bound %q", got.Result, got.Error, w.binding("apps/assistant", "mcp"))
	}
}

// A person's own waiting line is theirs with no admin; one that takes an
// admin's rights is the admin's to approve — and the rights are read again
// when it would apply: an admin who is one no longer gets nothing bound.
func TestWaitingLineIsHeldToItsPersonsRights(t *testing.T) {
	// alice, no admin: her own two tiles
	w, _, cs := waitWorld(t, "alice")
	if cs.Items[3].Admin || cs.Items[3].NeedsAdmin {
		t.Fatalf("a binding between alice's own tiles: %+v", cs.Items[3])
	}
	w.dice(diceProvides)
	if got := w.set(t, "alice", cs.ID).Items[3]; got.Result != delegation.ItemApplied || w.binding("apps/assistant", "mcp") != "apps/dice" {
		t.Fatalf("alice's own line: %s (%s)", got.Result, got.Error)
	}

	// bob, an admin, binds the workspace's tile apps/x (an admin's line) to a tile his plan builds — then is an admin no longer
	w = startingWorld(t)
	w.write(map[string]string{"apps/x/xbin.json": `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true}}}`})
	w.rescan()
	_, bob := w.session("bob")
	adm := w.plan(t, "bob", `[{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"}},{"kind":"agent.start","args":{"tile":"apps/dice","brief":"b","harness":"claude"}},
		{"kind":"binding.set","args":{"component":"apps/x","slot":"mcp","provider":"apps/dice"}}]`)
	if l := adm.Items[2]; !l.Admin || l.Wait == nil || l.Blocked != "" {
		t.Fatalf("an admin's waiting line: %+v", l)
	}
	done := w.approve(t, adm.ID, `{"items":[1,2,3],"password":"password1"}`, bob)
	if results(done) != "applied,applied,waiting" {
		t.Fatalf("the admin's plan: %s (%s)", results(done), done.Items[2].Error)
	}
	u, _ := w.b.Users.Get("bob")
	u.Role, u.Tiles = users.RoleUser, map[string]string{"apps/x": users.LevelWrite} // he still sees the tile; binding it is no longer his
	if _, err := w.b.Users.Upsert(*u, ""); err != nil {
		t.Fatal(err)
	}
	w.dice(diceProvides)
	if got := w.set(t, "bob", adm.ID).Items[2]; got.Result != delegation.ItemCancelled || !strings.Contains(got.Error, "not approvable by you") || w.binding("apps/x", "mcp") != "" {
		t.Fatalf("an admin's line of someone who is an admin no longer: %s (%q), bound %q", got.Result, got.Error, w.binding("apps/x", "mcp"))
	}
}

// Nothing but a binding to a tile an earlier line starts a tile agent in
// can wait: over every kind, with a start before it in every tile it could
// name, on tiles that exist and tiles the plan makes.
func TestOnlyABindingWaits(t *testing.T) {
	w := changeSetWorld(t)
	planTemplates(t, w)
	w.write(map[string]string{
		"apps/empty/xbin.json": `{}`,
		"apps/asst/xbin.json":  `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true},"llm":{"kind":"http","service":"openai","multi":true},"net":{"kind":"net"}}}`,
		"apps/tool/xbin.json":  diceProvides,
	})
	w.rescan()
	planned := []delegation.PlannedTile{
		{N: 1, Path: "apps/p-blank", Template: "blank", Owner: "user:bob", Manifest: []byte(`{}`)},
		{N: 2, Path: "apps/p-asst", Template: "assistant", Owner: "user:bob", Manifest: []byte(`{"interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true},"llm":{"kind":"http","service":"openai","multi":true},"net":{"kind":"net"}}}`)},
	}
	tiles := []string{"apps/empty", "apps/asst", "apps/tool", "apps/x", "apps/pg", "apps/p-blank", "apps/p-asst", "apps/nope"}
	var starts []delegation.PlannedStart
	for i, tile := range tiles {
		starts = append(starts, delegation.PlannedStart{N: 3 + i, Tile: tile})
	}
	var ops []delegation.ChangeOp
	op := func(kind string, args map[string]any) {
		ops = append(ops, delegation.ChangeOp{Kind: kind, Args: mustJSON(args)})
	}
	for _, a := range tiles {
		for _, b := range append([]string{"xbin:llm", "internet", "none"}, tiles...) {
			for _, slot := range []string{"mcp", "llm", "net", "nope"} {
				op(ChangeBindingSet, map[string]any{"component": a, "slot": slot, "provider": b})
				op(ChangeBindingSet, map[string]any{"component": a, "slot": slot, "providers": []string{b, "apps/tool"}})
				op(ChangeBindingSet, map[string]any{"component": a, "slot": slot, "provider": b, "add": true})
				op(ChangeBindingRemove, map[string]any{"component": a, "slot": slot, "provider": b})
			}
			op(ChangeGrantAdd, map[string]any{"from": a, "target": b, "role": "reader"})
			op(ChangeGrantRevoke, map[string]any{"from": a, "target": b, "role": "reader"})
		}
		op(ChangeBindingRemove, map[string]any{"component": a, "slot": "mcp"})
		op(ChangeOrgShare, map[string]any{"org": "fin", "tile": a, "level": "read"})
		op(ChangeLimitsSet, map[string]any{"tile": a, "kind": "memMiB", "value": 4096})
		op(ChangeTileCreate, map[string]any{"template": "blank", "path": a + "/in"})
		op(ChangeTileCreate, map[string]any{"template": "tool", "path": a})
	}
	waits := 0
	for _, user := range []string{"bob", "alice"} {
		p := personP(t, w.partWS, user)
		for _, o := range ops {
			pv, err := w.b.PreviewPlan(p, o, planned, starts)
			if err != nil || pv.Waits == nil {
				// and with no start before it, nothing waits at all
				continue
			}
			waits++
			var body bindingBody
			_ = json.Unmarshal(o.Args, &body)
			wt := *pv.Waits
			if o.Kind != ChangeBindingSet || body.Provider == "" || body.Add || wt.Tile != body.Provider || wt.Component != body.Component || wt.Slot != body.Slot ||
				wt.Kind != "http" || wt.Service == "" || wt.Role != delegation.WaitRole || body.Provider == "xbin:llm" || !pv.Deferred || pv.Pin == "" {
				t.Errorf("%s: %s %s waits (%+v): only a binding of one slot to one tile, pinned, may", user, o.Kind, o.Args, wt)
			}
			if pc, ok := w.b.Reg.Component(wt.Tile); ok && !providesNone(pc, wt.Service) {
				t.Errorf("%s: %s waits on %s, which provides %s already", user, o.Args, wt.Tile, wt.Service)
			}
			if none, err := w.b.PreviewPlan(p, o, planned, nil); err != nil || none.Waits != nil {
				t.Errorf("%s: %s waits with no start before it (err %v)", user, o.Args, err)
			}
		}
	}
	if waits == 0 {
		t.Fatal("no line of the corpus waits: the property is checked on nothing")
	}
}

// A waiting line that takes an admin is asked of one like any other: in
// the admin's copy it waits on the tile agent the asker's plan started,
// the admin approves it once, and when it applies both sets and both logs
// say so.
func TestWaitingLineAskedOfAnAdmin(t *testing.T) {
	w := startingWorld(t)
	w.write(map[string]string{"apps/x/xbin.json": `{"runtime":"go","interfaces":{"mcp":{"kind":"http","service":"mcp","multi":true}}}`})
	w.rescan()
	_, alice := w.session("alice")
	_, bob := w.session("bob")
	cs := w.plan(t, "alice", `[{"kind":"tile.create","args":{"template":"blank","path":"apps/dice"}},{"kind":"agent.start","args":{"tile":"apps/dice","brief":"b","harness":"claude"}},
		{"kind":"binding.set","args":{"component":"apps/x","slot":"mcp","provider":"apps/dice"}}]`)
	if got := flags(cs); got != "[] D[1] AN[1 2]" || cs.Items[2].Wait != nil {
		t.Fatalf("alice's plan: %s (wait %v)", got, cs.Items[2].Wait)
	}
	if done := w.approve(t, cs.ID, `{"items":[1,2]}`, alice); results(done) != "applied,applied," {
		t.Fatalf("alice's own lines: %s (%s)", results(done), done.Items[1].Error)
	}
	if code, body := w.do("POST", "/delegate/changesets/"+cs.ID+"/ask", "", alice); code != 200 {
		t.Fatalf("ask: %d %s", code, body)
	}
	var asked delegation.ChangeSet
	sets, _ := w.store.ChangeSets("bob")
	for _, o := range sets {
		if o.From == "alice" && o.AskOf == cs.ID {
			asked = o
		}
	}
	if len(asked.Items) != 1 || asked.Items[0].Wait == nil || asked.Items[0].Blocked != "" || !strings.Contains(strings.Join(asked.Items[0].Effects, "\n"), "applies once apps/dice provides mcp") {
		t.Fatalf("the admin's copy: %+v", asked.Items)
	}
	done := w.approve(t, asked.ID, `{"items":[1],"password":"password1"}`, bob)
	if results(done) != "waiting" || w.set(t, "alice", cs.ID).Items[2].Result != delegation.ItemWaiting {
		t.Fatalf("approved by the admin: his copy %s, alice's line %s", results(done), w.set(t, "alice", cs.ID).Items[2].Result)
	}
	w.dice(diceProvides)
	if got := w.set(t, "bob", asked.ID).Items[0]; got.Result != delegation.ItemApplied || w.binding("apps/x", "mcp") != "apps/dice" {
		t.Fatalf("once apps/dice provides mcp: %s (%s), bound %q", got.Result, got.Error, w.binding("apps/x", "mcp"))
	}
	if got := w.set(t, "alice", cs.ID).Items[2]; got.Result != delegation.ItemApplied {
		t.Fatalf("alice's line: %s (%s)", got.Result, got.Error)
	}
	if ok, _ := w.waitRows(t, "bob", asked.ID); len(ok) != 1 || ok[0].From != "alice" {
		t.Fatalf("the admin's log: %+v", ok)
	}
	if ok, _ := w.waitRows(t, "alice", cs.ID); len(ok) != 1 || ok[0].By != "bob" {
		t.Fatalf("alice's log: %+v", ok)
	}
}
