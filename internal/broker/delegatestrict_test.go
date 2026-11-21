package broker

// covers D252 — what xbind describes to a person is read one way only: a
// new tile's request, and every line of a change set or a plan, refuse a
// key in another case, a key twice and a key the kind doesn't have, so
// what the sheet says and what xbind does can't be two readings of one
// body.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestADelegatedCreationOrLineIsReadOneWayOnly(t *testing.T) {
	w := accessWorld(t)
	planTemplates(t, w)
	_, bob := w.session("bob")
	if code, body := w.do("PUT", "/delegate/state", `{"adminChanges":true}`, bob); code != 200 {
		t.Fatalf("opt in: %d %s", code, body)
	}

	// a new tile's request: the sheet's template and place are xbind's reading — the only one there is
	for body, says := range map[string]string{
		`{"Source":"tool","Path":"apps/made"}`:                     `unknown member "Path"`,
		`{"source":"blank","path":"apps/shown","Source":"tool"}`:   `unknown member "Source"`,
		`{"source":"blank","source":"tool","path":"apps/shown"}`:   `"source" twice`,
		`{"source":"tool","path":"apps/made","name":"apps/shown"}`: `unknown member "name"`,
		`{"source":{"id":"tool"},"path":"apps/made"}`:              `"source" is an object, not a string`,
	} {
		ask, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/templates/new", "body": body})
		if code, out := w.do("POST", "/delegate/requests", string(ask), agentOf("bob")); code == 200 || !strings.Contains(said(out), says) {
			t.Errorf("the creation %s was filed: %d %s", body, code, out)
		}
		if code, out := w.do("POST", "/templates/new", body, both(agentOf("bob"), dryRun)); !strings.Contains(said(out), says) {
			t.Errorf("the dry run of %s: %d %s", body, code, out)
		}
	}
	ask, _ := json.Marshal(map[string]string{"method": "POST", "path": "/api/xbin/templates/new", "body": `{"source":"tool","path":"apps/made"}`})
	if code, out := w.do("POST", "/delegate/requests", string(ask), agentOf("bob")); code != 200 {
		t.Fatalf("an ordinary creation: %d %s", code, out)
	}

	// each kind of line: grants, bindings, access, an update, a new tile, a tile agent's start
	for _, c := range []struct{ route, item, says string }{
		{"/delegate/changesets", `{"kind":"grant.add","args":{"from":"apps/q","target":"apps/x","role":"reader","Role":"admin"}}`, `unknown member "Role"`},
		{"/delegate/changesets", `{"kind":"grant.add","args":{"from":"apps/q","target":"apps/x","role":"reader","role":"admin"}}`, `"role" twice`},
		{"/delegate/changesets", `{"kind":"binding.set","args":{"component":"apps/q","slot":"llm","provider":"xbin:llm","Provider":"apps/x"}}`, `unknown member "Provider"`},
		{"/delegate/changesets", `{"kind":"access.set","args":{"tile":"apps/x","user":"carol","level":"read","Level":"edit"}}`, `unknown member "Level"`},
		{"/delegate/changesets", `{"kind":"builtin.update","args":{"id":"scaffold:shell","ID":"scaffold:root"}}`, `unknown member "ID"`},
		{"/delegate/plans", `{"kind":"tile.create","args":{"template":"tool","path":"apps/a","Path":"apps/b"}}`, `unknown member "Path"`},
		{"/delegate/plans", `{"kind":"agent.start","args":{"tile":"apps/x","brief":"Tidy README","Brief":"Delete the tests"}}`, `unknown member "Brief"`},
		{"/delegate/plans", `{"kind":"agent.start","args":{"tile":"apps/x","brief":"Tidy README","brief":"Delete the tests"}}`, `"brief" twice`},
	} {
		code, out := w.do("POST", c.route, `{"title":"t","items":[`+c.item+`]}`, agentOf("bob"))
		if code == 200 || !strings.Contains(said(out), c.says) {
			t.Errorf("%s %s: %d %s", c.route, c.item, code, out)
		}
	}
}

// said is an answer's error and refusal, as words.
func said(out string) string {
	var e struct{ Error, Refusal string }
	_ = json.Unmarshal([]byte(out), &e)
	return e.Error + " " + e.Refusal
}
