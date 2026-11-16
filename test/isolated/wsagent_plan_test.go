//go:build linux && integration

package isolated

// wsagent_plan_test.go — a plan (D238; docs/protocol/delegation.md §Plans)
// end to end on an xbind as an install runs one, through the workspace
// agent's own tool and xbin's own routes: an admin's plan that creates a
// tile and starts a tile agent IN it, in one approval; and a plan of a
// person who is no admin, whose line that takes an admin's rights goes to
// the admins with Ask an admin and is applied by one. The model is
// hack/fakeopenai's script and the coding agent hack/fakeacp.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// wsPlanSet is a change set as xbin's review reads it.
type wsPlanSet struct {
	ID, State, From, AskOf string
	Plan                   bool
	Items                  []struct {
		N, Of                              int
		Kind, Line, Result, Error, Blocked string
		Admin, NeedsAdmin, Deferred        bool
		Needs                              []int
		Before, After                      json.RawMessage
		Effects, Warnings                  []string
		Start                              struct {
			Tile, Provider, Mode, Prompt string
			Asks                         bool
		}
		Out struct{ Path, Session string }
	}
}

// sets are the change sets `who` reads with header(s) h, newest first.
func (e *wsEnv) sets(h ...xbindtest.Header) []wsPlanSet {
	e.t.Helper()
	var out struct{ Changesets []wsPlanSet }
	e.d.Must(e.t, "GET", "/api/xbin/delegate/changesets", nil, 200, h...).Decode(e.t, &out)
	return out.Changesets
}

func TestWorkspaceAgentPlan(t *testing.T) {
	const (
		made  = "apps/made"  // the tile ada's plan creates, and starts a tile agent in
		plain = "apps/plain" // a page
		roles = "apps/roles" // a page whose API has roles to grant
	)
	e := startWsAgentAt(t, made)
	d := e.d
	e.staticTile(plain, "Plain")
	d.WriteTile(t, roles, map[string]string{
		"xbin.json":  `{"title":"Roles","expose":{"roles":{"reader":"read","writer":"write"}}}` + "\n",
		"index.html": "<!doctype html><html><head></head><body>roles</body></html>\n",
	})
	d.Must(t, "PUT", "/api/xbin/agents/policy", map[string]any{"credentials": "connector-only"}, 200)
	e.person(wsAdmin, "admin", nil)
	e.person("bea", "user", map[string]string{plain: "read", roles: "read"})
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone"}, 200, e.as(wsAdmin)...)
	approve := func(id string) string { return "/api/xbin/delegate/changesets/" + id + "/approve" }
	results := func(cs wsPlanSet) string {
		var out []string
		for _, it := range cs.Items {
			out = append(out, fmt.Sprintf("%d:%s", it.N, it.Result))
		}
		return strings.Join(out, " ")
	}

	t.Run("1 a tile and a tile agent in it, in one approval", func(t *testing.T) {
		e.in(t)
		ada := e.open(wsAdmin, map[string]any{"main": true})
		t.Cleanup(func() {
			if t.Failed() {
				ada.transcript(t)
			}
		})
		turn := ada.say(`ws: plan_propose {"title":"A worker","changes":[{"kind":"tile.create","args":{"template":"starter","name":"Made"},"why":"a place to work"},` +
			`{"kind":"agent.start","args":{"tile":"Made","goal":"List what is in the tile","done_when":"the files are listed","harness":"fake"},"why":"the work"}]}`)
		if got := turn.tool("plan_propose", 0); !strings.Contains(got, "Proposed as one plan") || !strings.Contains(got, "Create the tile "+made) {
			t.Fatalf("plan_propose: %s", got)
		}
		all := e.sets(e.as(wsAdmin)...)
		if len(all) != 1 || !all[0].Plan || all[0].State != "proposed" || len(all[0].Items) != 2 {
			t.Fatalf("ada's change sets: %+v, want one plan of two lines", all)
		}
		cs := all[0]
		if l := cs.Items[1]; cs.Items[0].Admin || l.Admin || !l.Deferred || len(l.Needs) != 1 || l.Needs[0] != 1 || l.Start.Provider != "fake" || !l.Start.Asks || l.Start.Tile != made ||
			!strings.Contains(l.Start.Prompt, "List what is in the tile") {
			t.Fatalf("the plan as xbind describes it: %+v, want two personal lines, the start deferred on line 1 with the fake coding agent in a mode that asks", cs.Items)
		}
		if r := d.Call(t, "GET", "/api/xbin/components", nil, e.as(wsAdmin)...); strings.Contains(r.String(), `"`+made+`"`) || len(e.tileAgents(wsAdmin)) != 0 {
			t.Fatalf("proposing made the tile or started something: %v", e.tileAgents(wsAdmin))
		}
		// the agent's own page can't apply it
		if r := d.Call(t, "POST", approve(cs.ID), map[string]any{"items": []int{1, 2}}, ada.fr); r.Status != 403 {
			t.Errorf("the agent's page approves its own plan: %d %s, want 403", r.Status, r)
		}
		// one approval — the person's, on personal lines: no recent sign-in asked for
		after := ada.last()
		var done wsPlanSet
		d.Must(t, "POST", approve(cs.ID), map[string]any{"items": []int{1, 2}}, 200, e.as(wsAdmin)...).Decode(t, &done)
		if results(done) != "1:applied 2:applied" || done.Items[0].Out.Path != made || done.Items[1].Out.Session == "" {
			t.Fatalf("the plan applied: %s (%s; %s), want both lines applied, the tile made and a session started", results(done), done.Items[0].Error, done.Items[1].Error)
		}
		if r := d.Must(t, "GET", "/api/xbin/components", nil, 200, e.as(wsAdmin)...).String(); !strings.Contains(r, `"`+made+`"`) {
			t.Errorf("the tile %s is not among the components", made)
		}
		// the tile agent runs in the tile the same plan made: ada's session, asking, started by her agent
		var sess wsTileSession
		xbindtest.Eventually(t, 2*time.Minute, "the tile agent in "+made+" asks ada for a permission", func() (bool, string) {
			got := e.tileAgents(wsAdmin)
			if len(got) != 1 {
				return false, fmt.Sprintf("%+v", got)
			}
			sess = got[0]
			return sess.Status == "waiting_permission" && sess.Pending == 1, fmt.Sprintf("%+v", sess)
		})
		if sess.ID != done.Items[1].Out.Session || sess.Cwd != made || sess.Provider != "fake" || sess.Origin != "agent" {
			t.Errorf("the tile agent's session: %+v, want the plan's (%s), the fake coding agent in %s, origin agent", sess, done.Items[1].Out.Session, made)
		}
		// the conversation is told, follows the tile agent, and its card keeps each line's outcome
		ada.woken(after, time.Minute, "the plan was applied", "Line 2 started tile agent #1 in "+made)
		xbindtest.Eventually(t, time.Minute, "the plan's card and the tile agent's", func() (bool, string) {
			c := ada.cards()
			if len(c.Agents) != 1 || len(c.Requests) != 1 {
				return false, fmt.Sprintf("%+v", c)
			}
			a, q := c.Agents[0], c.Requests[0]
			ok := a.Session == sess.ID && a.Tile == made && a.Waits && q.Plan && len(q.Lines) == 2 && q.Lines[0].Result == "applied" && q.Lines[1].Result == "applied" &&
				q.Lines[0].Line == cs.Items[0].Line
			return ok, fmt.Sprintf("agent %+v, plan %+v", a, q)
		})
		// the audit log: both lines, ada's, under the plan
		n := 0
		for _, row := range e.audit(wsAdmin) {
			if row.Ticket == cs.ID && strings.HasPrefix(row.Class, "changeset:") && row.Status == 200 && row.From == "" && row.By == "" {
				n++
			}
		}
		if n != 2 {
			t.Errorf("ada's log has %d applied lines of the plan, want 2", n)
		}
	})

	t.Run("2 a person who is no admin asks one", func(t *testing.T) {
		e.in(t)
		bea := e.open("bea", map[string]any{"main": true})
		t.Cleanup(func() {
			if t.Failed() {
				bea.transcript(t)
			}
		})
		turn := bea.say(`ws: plan_propose {"title":"Notes that can read the roles tile","changes":[{"kind":"tile.create","args":{"template":"starter","name":"Bea notes"},"why":"mine"},` +
			`{"kind":"grant.add","args":{"from":"` + plain + `","target":"` + roles + `","role":"reader"},"why":"so the page can read it"}]}`)
		if got := turn.tool("plan_propose", 0); !strings.Contains(got, "needs an admin") || !strings.Contains(got, "Ask an admin") {
			t.Fatalf("plan_propose for a person who is no admin: %s", got)
		}
		mine := e.sets(e.as("bea")...)
		if len(mine) != 1 || len(mine[0].Items) != 2 {
			t.Fatalf("bea's change sets: %+v", mine)
		}
		cs := mine[0]
		// the line that takes an admin: xbind's sentence, and nothing of the state behind it — for bea and for her agent
		if l := cs.Items[1]; !l.NeedsAdmin || !l.Admin || l.Line == "" || len(l.Before) != 0 || len(l.After) != 0 || len(l.Effects) != 0 || len(l.Warnings) != 0 || cs.Items[0].NeedsAdmin {
			t.Fatalf("bea's plan: %+v, want line 2 to need an admin and carry xbind's sentence only", cs.Items)
		}
		if r := d.Call(t, "POST", approve(cs.ID), map[string]any{"items": []int{1, 2}}, e.as("bea")...); r.Status != 400 {
			t.Errorf("bea applies a line that needs an admin: %d %s, want 400", r.Status, r)
		}
		var done wsPlanSet
		d.Must(t, "POST", approve(cs.ID), map[string]any{"items": []int{1}}, 200, e.as("bea")...).Decode(t, &done)
		if done.Items[0].Result != "applied" || done.Items[1].Result != "" {
			t.Fatalf("bea applies her own line: %s (%s)", results(done), done.Items[0].Error)
		}
		// Ask an admin is the person's own act
		ask := "/api/xbin/delegate/changesets/" + cs.ID + "/ask"
		if r := d.Call(t, "POST", ask, nil, bea.fr); r.Status != 403 {
			t.Errorf("the agent's page asks an admin for its person: %d %s, want 403", r.Status, r)
		}
		if got := e.sets(e.as(wsAdmin)...); len(got) != 1 {
			t.Fatalf("before bea asked, ada's change sets: %+v, want her own one", got)
		}
		d.Must(t, "POST", ask, nil, 200, e.as("bea")...).Decode(t, &done)
		if done.Items[1].Result != "asked" {
			t.Fatalf("after Ask an admin: %s", results(done))
		}
		// the admin's side: a request from bea, described as ada would apply it; only her own session reads it
		var asked wsPlanSet
		for _, s := range e.sets(e.as(wsAdmin)...) {
			if s.From == "bea" {
				asked = s
			}
		}
		if asked.ID == "" || asked.AskOf != cs.ID || len(asked.Items) != 1 || asked.Items[0].Of != 2 || asked.Items[0].Line != cs.Items[1].Line || asked.Items[0].Blocked != "" || len(asked.Items[0].Before) == 0 {
			t.Fatalf("ada's copy of bea's request: %+v, want one line (bea's line 2) with what it changes", asked)
		}
		adaPage := e.agentPage(wsAdmin)
		for _, s := range e.sets(adaPage) {
			if s.From != "" {
				t.Errorf("ada's agent's page lists a request from %s", s.From)
			}
		}
		if r := d.Call(t, "GET", "/api/xbin/delegate/changesets/"+asked.ID, nil, adaPage); r.Status == 200 {
			t.Errorf("ada's agent's page reads bea's request: %s", r)
		}
		granted := func() bool {
			var out struct{ Grants []json.RawMessage }
			d.Must(t, "GET", "/api/xbin/grants", nil, 200, e.as(wsAdmin)...).Decode(t, &out)
			for _, g := range out.Grants {
				if strings.Contains(string(g), `"`+plain+`"`) && strings.Contains(string(g), `"`+roles+`"`) {
					return true
				}
			}
			return false
		}
		if granted() {
			t.Fatal("the grant exists before an admin applied it")
		}
		if r := d.Call(t, "POST", approve(asked.ID), map[string]any{"items": []int{1}}, e.as("bea")...); r.Status == 200 {
			t.Fatalf("bea applies the admins' copy of her own request: %s", r)
		}
		e.signIn(wsAdmin) // a recent sign-in: an admin's act takes one
		var applied wsPlanSet
		d.Must(t, "POST", approve(asked.ID), map[string]any{"items": []int{1}}, 200, e.as(wsAdmin)...).Decode(t, &applied)
		if applied.Items[0].Result != "applied" || !granted() {
			t.Fatalf("ada applies bea's request: %s (%s); granted %v", results(applied), applied.Items[0].Error, granted())
		}
		// the result is back on bea's line; both logs say who asked and who applied
		for _, s := range e.sets(e.as("bea")...) {
			if s.ID == cs.ID && (s.Items[1].Result != "applied" || s.Items[0].Result != "applied") {
				t.Errorf("bea's plan after the admin applied her line: %s", results(s))
			}
		}
		var fromBea, byAda bool
		for _, row := range e.audit(wsAdmin) {
			fromBea = fromBea || row.Class == "changeset:grant.add" && row.From == "bea" && row.Ticket == asked.ID && row.Status == 200
		}
		for _, row := range e.audit("bea") {
			byAda = byAda || row.Class == "changeset:grant.add" && row.By == wsAdmin && row.Ticket == cs.ID && row.Status == 200
		}
		if !fromBea || !byAda {
			t.Errorf("the logs: ada's names bea as who asked: %v; bea's names ada as who applied: %v", fromBea, byAda)
		}
		// nothing left to take back
		if r := d.Call(t, "POST", ask, map[string]any{"retract": true}, e.as("bea")...); r.Status != 409 {
			t.Errorf("taking back a request that was decided: %d %s, want 409", r.Status, r)
		}
	})
}
