//go:build linux && integration

package isolated

// wsagent_build_test.go — "make a tool that rolls dice and give it to a new
// assistant", with the tool BUILT (D243): there is no template to stamp an
// MCP tile from. On an xbind as an install runs one, through the workspace
// agent's own plan_propose and xbin's own routes:
//
//   - one plan: an empty tile, its tile agent with the mcp-tool brief, the
//     assistant, its llm binding, and its mcp binding to a tile that
//     provides nothing yet — a line that waits;
//   - one approval: four lines applied, the binding waiting, nothing bound;
//   - the tile's coding agent (hack/fakeacp, the scripted one) builds the
//     MCP server from /docs/mcp.md — read from inside its sandbox, which is
//     what the brief tells a real one to do — asking its person before each
//     command; the person answers each (xbind counts them here);
//   - the moment the manifest declares provides.mcp as approved, xbind
//     applies the waiting line by itself: the conversation is told, the
//     log says "waited", and the assistant's GET /tools names roll_dice
//     from apps/dice — which the workspace agent reads itself.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// wsBuildSteps is what the scripted coding agent does with the brief: the
// server out of the docs page (its marked go block), the module file, the
// manifest that provides mcp — last, so the first build has everything —
// and the brief's own check.
var wsBuildSteps = strings.Join([]string{
	`mkdir -p backend && curl -fsS -H "Authorization: Bearer $XBIN_TOKEN" "$XBIN_URL/docs/mcp.md?raw=1" | awk '/^<!-- examples\/mcp-server\/backend\/main.go/{f=1;next} f&&/^` + "```" + `go$/{g=1;next} g&&/^` + "```" + `$/{exit} g' > backend/main.go && grep -c roll_dice backend/main.go`,
	`printf 'module dice\n\ngo 1.24\n\nrequire github.com/xbin-dev/xbin/sdk v0.0.0\n' > go.mod && echo go.mod`,
	`printf '%s\n' '{"title":"Dice","runtime":"go","expose":{"roles":{"reader":"MCP at POST /mcp"}},"provides":{"mcp":{"kind":"http","service":"mcp","role":"reader"}}}' > xbin.json && echo xbin.json`,
	`for i in $(seq 1 150); do curl -s -H "Authorization: Bearer $XBIN_TOKEN" -H 'Content-Type: application/json' -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}' "$XBIN_URL/api/$XBIN_COMPONENT/mcp" | grep -q roll_dice && echo "tools/list names roll_dice" && exit 0; sleep 2; done; echo "no answer from /mcp"; exit 1`,
}, " ||| ")

func TestWorkspaceAgentBuildsATool(t *testing.T) {
	const dice, assistant = "apps/dice", "apps/assistant"
	e := startWsAgentAt(t, dice)
	d := e.d
	d.Must(t, "PUT", "/api/xbin/agents/policy", map[string]any{"credentials": "connector-only"}, 200)
	e.person(wsAdmin, "admin", nil)
	d.Must(t, "PUT", "/api/xbin/delegate/workspace", map[string]any{"enabled": true, "who": "everyone"}, 200, e.as(wsAdmin)...)
	ada := e.open(wsAdmin, map[string]any{"main": true})
	t.Cleanup(func() {
		if t.Failed() {
			ada.transcript(t)
		}
	})

	plan, _ := json.Marshal(map[string]any{"title": "A dice tool and an assistant that can use it", "changes": []any{
		map[string]any{"kind": "tile.create", "args": map[string]any{"template": "starter", "name": "Dice"}, "why": "The tool lives in a tile of its own"},
		map[string]any{"kind": "agent.start", "args": map[string]any{"tile": "Dice", "template": "mcp-tool", "harness": "fake", "goal": "fakeacp-steps: " + wsBuildSteps,
			"tool": map[string]any{"name": "roll_dice", "arguments": "sides: integer, default 6; count: integer, default 1", "result": "each roll and their sum"}}, "why": "A coding agent builds the tool"},
		map[string]any{"kind": "tile.create", "args": map[string]any{"template": "agent", "name": "Assistant"}, "why": "The assistant"},
		map[string]any{"kind": "binding.set", "args": map[string]any{"component": "Assistant", "slot": "llm", "provider": "xbin:llm"}, "why": "A model"},
		map[string]any{"kind": "binding.set", "args": map[string]any{"component": "Assistant", "slot": "mcp", "provider": "Dice"}, "why": "The tool"},
	}})
	turn := ada.say("ws: plan_propose " + string(plan))
	if got := turn.tool("plan_propose", 0); !strings.Contains(got, "Proposed as one plan") || !strings.Contains(got, "Line 5 waits by design") {
		t.Fatalf("plan_propose: %s", got)
	}
	all := e.sets(e.as(wsAdmin)...)
	if len(all) != 1 || len(all[0].Items) != 5 {
		t.Fatalf("ada's change sets: %+v, want one plan of five lines", all)
	}
	cs := all[0]
	type waitOf struct {
		Wait *struct {
			Tile, Service, Role, Held string
			Until                     time.Time
		}
	}
	waits := func() (out []waitOf) {
		var set struct{ Items []waitOf }
		d.Must(t, "GET", "/api/xbin/delegate/changesets/"+cs.ID, nil, 200, e.as(wsAdmin)...).Decode(t, &set)
		return set.Items
	}
	if w := waits()[4].Wait; w == nil || w.Tile != dice || w.Service != "mcp" || w.Role != "reader" || cs.Items[4].Blocked != "" || !cs.Items[4].Deferred ||
		!strings.Contains(strings.Join(cs.Items[4].Effects, "\n"), "applies once "+dice+" provides mcp; nothing is bound until then") ||
		!strings.Contains(cs.Items[1].Start.Prompt, `"provides": {"mcp": {"kind": "http", "service": "mcp", "role": "reader"}}`) || !strings.Contains(cs.Items[1].Start.Prompt, "/docs/mcp.md") {
		t.Fatalf("the plan as xbind describes it: line 5 %+v (wait %+v); the brief:\n%s", cs.Items[4], w, cs.Items[1].Start.Prompt)
	}

	// one approval (an admin's line is among it: ada signed in a moment ago)
	approved := time.Now()
	var done wsPlanSet
	d.Must(t, "POST", "/api/xbin/delegate/changesets/"+cs.ID+"/approve", map[string]any{"items": []int{1, 2, 3, 4, 5}}, 200, e.as(wsAdmin)...).Decode(t, &done)
	results := func(cs wsPlanSet) string {
		var out []string
		for _, it := range cs.Items {
			out = append(out, fmt.Sprintf("%d:%s", it.N, it.Result))
		}
		return strings.Join(out, " ")
	}
	if results(done) != "1:applied 2:applied 3:applied 4:applied 5:waiting" {
		t.Fatalf("the plan applied: %s (%s; %s)", results(done), done.Items[1].Error, done.Items[4].Error)
	}
	bound := func() string {
		var b struct {
			Bindings map[string]map[string]json.RawMessage `json:"bindings"`
		}
		d.Must(t, "GET", "/api/xbin/bindings", nil, 200, e.as(wsAdmin)...).Decode(t, &b)
		return string(b.Bindings[assistant]["mcp"])
	}
	if got := bound(); strings.Contains(got, dice) {
		t.Fatalf("the assistant's mcp slot is bound before %s provides anything: %s", dice, got)
	}

	// the tile agent builds the server, asking before each command; ada answers each
	sessAPI := "/api/xbin/term/sessions/" + done.Items[1].Out.Session
	answers, last := 0, ""
	xbindtest.Eventually(t, 12*time.Minute, "the tile agent built the MCP server and ended its turn", func() (bool, string) {
		var detail struct {
			Status      string
			Permissions []struct{ Pid string }
			Last        string
		}
		r := d.Call(t, "GET", sessAPI, nil, e.as(wsAdmin)...)
		if r.Status != 200 || json.Unmarshal(r.Body, &detail) != nil {
			return false, r.String()
		}
		for _, p := range detail.Permissions {
			if d.Call(t, "POST", sessAPI+"/permissions/"+p.Pid, map[string]any{"optionId": "once"}, e.as(wsAdmin)...).Status == 200 {
				answers++
			}
		}
		for _, s := range e.tileAgents(wsAdmin) {
			if s.ID == done.Items[1].Out.Session {
				last = s.Status
			}
		}
		return last == "idle" && answers >= 4, fmt.Sprintf("status %s, %d answers, %d waiting", last, answers, len(detail.Permissions))
	})
	if answers != 4 {
		t.Errorf("ada answered %d permission requests, want one for each of the coding agent's four commands", answers)
	}

	// xbind applied the waiting line by itself, as ada
	xbindtest.Eventually(t, 2*time.Minute, "the waiting line was applied", func() (bool, string) {
		for _, s := range e.sets(e.as(wsAdmin)...) {
			if s.ID == cs.ID {
				return s.Items[4].Result == "applied", results(s) + " " + s.Items[4].Error
			}
		}
		return false, "no set"
	})
	if got := bound(); !strings.Contains(got, dice) {
		t.Fatalf("the assistant's mcp slot after the line applied: %s", got)
	}
	waited := 0
	for _, row := range e.audit(wsAdmin) {
		if row.Ticket == cs.ID && row.Under == "waited" && row.Status == 200 && row.Class == "changeset:binding.set" {
			waited++
		}
	}
	if waited != 1 {
		t.Errorf("ada's log has %d applied rows of the line that waited, want 1", waited)
	}
	t.Logf("from the approval to the binding: %s, %d permission answers", time.Since(approved).Round(time.Second), answers)

	// the conversation was told: the tile agent's turn, and the line that waited
	xbindtest.Eventually(t, 2*time.Minute, "the conversation is told the waiting line applied", func() (bool, string) {
		for _, m := range ada.messages() {
			if m.Role == "user" && strings.Contains(m.Content, "Line 5 of the plan, which waited for "+dice+" to provide mcp, was applied by xbin now") {
				return true, ""
			}
		}
		return false, wsTail(ada.messages(), 6)
	})
	xbindtest.Eventually(t, time.Minute, "the plan's card", func() (bool, string) {
		c := ada.cards()
		for _, q := range c.Requests {
			if q.Plan && len(q.Lines) == 5 {
				return q.Lines[4].Result == "applied" && q.State == "applied", fmt.Sprintf("%+v", q)
			}
		}
		return false, fmt.Sprintf("%+v", c.Requests)
	})

	// verify before done: the workspace agent reads the tile agent's result and the assistant's tool list itself
	xbindtest.Eventually(t, 8*time.Minute, "the assistant's GET /tools names roll_dice from "+dice, func() (bool, string) {
		r := d.Call(t, "GET", "/api/"+assistant+"/tools", nil, e.as(wsAdmin)...)
		return r.Status == 200 && strings.Contains(r.String(), `"roll_dice"`) && strings.Contains(r.String(), dice), clipLine(r.String())
	})
	turn = ada.say(`ws: tile_agent_result {"agent":"#1"} ;; tile_call {"tile":"` + assistant + `","method":"GET","path":"/tools"}`)
	if got := turn.tool("tile_agent_result", 0); !strings.Contains(got, "steps done: tools/list names roll_dice") {
		t.Errorf("tile_agent_result: %s", clipLine(got))
	}
	if got := turn.tool("tile_call", 0); !strings.Contains(got, "GET /tools → 200") || !strings.Contains(got, "roll_dice") || !strings.Contains(got, dice) {
		t.Errorf("the agent's own check of the hook-up: %s", clipLine(got))
	}
}
