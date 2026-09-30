package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp/acptest"
)

// bodyJSON decodes a response body.
func bodyJSON(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var v map[string]any
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatalf("not JSON: %s", b)
	}
	return v
}

// GET /runs/{id}/harness is the summary, the session and the rules; PATCH
// switches the live adapter's mode and model (an RPC: its refusal in its own
// words is a 502, another process holding the session a 503), an explicit
// mode only for the conversation's owner, and each change reaches the
// stream as a harness event; with no adapter the choice is stored for the
// next start and the summary says so.
func TestHarnessRoutePatch(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "echo hi")
	hwait(t, "the turn", turnOver(ag, run.ID))
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", run.ID), map[string]string{"user": "bob", "role": "participant"}); w.Code != 200 {
		t.Fatalf("members: %d %s", w.Code, w.Body)
	}
	w := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil)
	if w.Code != 200 {
		t.Fatalf("GET: %d %s", w.Code, w.Body)
	}
	got := bodyJSON(t, w.Body.Bytes())
	sess, _ := got["session"].(map[string]any)
	if h, _ := got["harness"].(map[string]any); h == nil || h["name"] != "Fake agent (tests)" || h["state"] != "ready" ||
		sess["gen"] != float64(1) || sess["execId"] == "" || sess["acpSessionId"] == "" || sess["startedAt"] == float64(0) ||
		sess["lastActive"] == float64(0) || got["rules"] == nil {
		t.Fatalf("GET: %s", w.Body)
	}

	path := fmt.Sprintf("/runs/%d/harness", run.ID)
	for _, c := range []struct {
		who  caller
		body any
		code int
		want string
	}{
		{asAlice, map[string]any{}, 400, "mode or option: name one"},
		{asAlice, map[string]any{"mode": "nope"}, 400, "mode: one of ask, yolo"},
		{asBob, map[string]any{"mode": "yolo"}, 403, "only alice can switch Fake agent (tests) to Yolo"},
		{asAlice, map[string]any{"option": map[string]string{"id": "nope", "value": "x"}}, 400, "option: one of model"},
		{asAlice, map[string]any{"option": map[string]string{"id": "model", "value": "x"}}, 400, "value: one of fake-default, fake-fast"},
		{asDave, map[string]any{"mode": "ask"}, 404, "no such run"},
	} {
		if w := callAs(t, mux, c.who, "PATCH", path, c.body); w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Fatalf("%v: %d %s", c.body, w.Code, w.Body)
		}
	}

	evs := watchTree(t, ag, run.ID)
	w = callAs(t, mux, asAlice, "PATCH", path, map[string]any{"mode": "yolo", "option": map[string]string{"id": "model", "value": "fake-fast"}})
	if w.Code != 200 {
		t.Fatalf("PATCH: %d %s", w.Code, w.Body)
	}
	h := bodyJSON(t, w.Body.Bytes())["harness"].(map[string]any)
	if h["mode"].(map[string]any)["current"] != "yolo" || !strings.Contains(w.Body.String(), `"currentValue":"fake-fast"`) {
		t.Fatalf("the answer: %s", w.Body)
	}
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Harness.Mode != "yolo" || cfg.Harness.Options["model"] != "fake-fast" {
		t.Fatalf("stored: %+v", cfg.Harness)
	}
	hwait(t, "the harness event", func() bool {
		for _, ev := range evs.of(evHarness) {
			if b, _ := json.Marshal(ev.Data); strings.Contains(string(b), `"current":"yolo"`) && ev.Run == run.ID {
				return true
			}
		}
		return false
	})
	if st := ag.eng.harnessOf(run.ID).c.State(); st.Modes.CurrentModeID != "yolo" {
		t.Fatalf("the adapter's mode: %+v", st.Modes)
	}

	// the adapter's refusal, in its words: an option value it doesn't take
	hs, _ := ag.db.harnessSession(run.ID)
	hs.Snapshot = strings.Replace(hs.Snapshot, `"value":"fake-fast"`, `"value":"fake-fast"},{"value":"fake-bogus","name":"Bogus"`, 1)
	if err := ag.db.putHarnessSession(hs); err != nil {
		t.Fatal(err)
	}
	if w := callAs(t, mux, asAlice, "PATCH", path, map[string]any{"option": map[string]string{"id": "model", "value": "fake-bogus"}}); w.Code != 502 ||
		!strings.Contains(w.Body.String(), "unknown option or value") {
		t.Fatalf("a refused value: %d %s", w.Code, w.Body)
	}
	// a mode it takes with an option it refuses: the refusal, and the mode
	// stored (the next start keeps it)
	if w := callAs(t, mux, asAlice, "PATCH", path, map[string]any{"mode": "ask", "option": map[string]string{"id": "model", "value": "fake-bogus"}}); w.Code != 502 {
		t.Fatalf("mode and a refused value: %d %s", w.Code, w.Body)
	}
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Harness.Mode != "ask" || cfg.Harness.Options["model"] != "fake-fast" {
		t.Fatalf("stored after a half-refused PATCH: %+v", cfg.Harness)
	}
	if st := ag.eng.harnessOf(run.ID).c.State(); st.Modes.CurrentModeID != "ask" {
		t.Fatalf("the adapter's mode: %+v", st.Modes)
	}
	// another process holding the session: 503, try again
	ag.eng.mu.Lock()
	ag.eng.closing = true
	ag.eng.mu.Unlock()
	w = callAs(t, mux, asAlice, "PATCH", path, map[string]any{"mode": "ask"})
	ag.eng.mu.Lock()
	ag.eng.closing = false
	ag.eng.mu.Unlock()
	if w.Code != 503 || w.Header().Get("Retry-After") != "1" {
		t.Fatalf("during a handoff: %d %s", w.Code, w.Body)
	}

	// no adapter: stored for the next start, and the summary says it
	ag.eng.endHarness(ag.eng.base, run.ID)
	w = callAs(t, mux, asAlice, "PATCH", path, map[string]any{"mode": "ask", "option": map[string]string{"id": "model", "value": "fake-default"}})
	if w.Code != 200 {
		t.Fatalf("PATCH stopped: %d %s", w.Code, w.Body)
	}
	h = bodyJSON(t, w.Body.Bytes())["harness"].(map[string]any)
	if h["state"] != "stopped" || h["mode"].(map[string]any)["current"] != "ask" || !strings.Contains(w.Body.String(), `"currentValue":"fake-default"`) {
		t.Fatalf("the stopped answer: %s", w.Body)
	}
	if cfg, _ := ag.db.runConfig(run.ID); cfg.Harness.Mode != "ask" || cfg.Harness.Options["model"] != "fake-default" {
		t.Fatalf("stored: %+v", cfg.Harness)
	}
	// the next start uses it
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "echo again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the next turn", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo again")
	})
	if s := ag.eng.harnessOf(run.ID); s == nil || s.c.Mode() != "ask" {
		t.Fatalf("the respawned adapter's mode")
	}
}

// POST /runs/{id}/harness/answer answers the parked question with the
// form's values (an hanswer row), refusing what can't answer it.
func TestHarnessRouteAnswer(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	run := askHarness(t, mux, box, "ask")
	path := fmt.Sprintf("/runs/%d/harness/answer", run.ID)
	p := parkOf(t, ag, run.ID, "question")
	for _, c := range []struct {
		body any
		code int
		want string
	}{
		{map[string]any{"park": "old", "action": "accept", "content": map[string]any{}}, 409, "no longer pending"},
		{map[string]any{"park": p.Park, "action": "maybe"}, 400, "action is accept, decline or cancel"},
		{map[string]any{"park": p.Park, "action": "accept"}, 400, "content: an object"},
		{map[string]any{"park": p.Park, "action": "accept", "content": []string{"x"}}, 400, "content: an object"},
	} {
		if w := callAs(t, mux, asAlice, "POST", path, c.body); w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Fatalf("%v: %d %s", c.body, w.Code, w.Body)
		}
	}
	w := callAs(t, mux, asAlice, "POST", path, map[string]any{"park": p.Park, "action": "accept", "content": map[string]any{"question_0": "Postgres"}})
	if w.Code != 200 || bodyJSON(t, w.Body.Bytes())["ok"] != "true" {
		t.Fatalf("answer: %d %s", w.Code, w.Body)
	}
	hwait(t, "the answer", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), `answers: {"question_0":"Postgres"}`)
	})
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"action": "decline"}); w.Code != 400 || !strings.Contains(w.Body.String(), "no pending question") {
		t.Fatalf("nothing parked: %d %s", w.Code, w.Body)
	}
	// declined, without a park named: the one pending now
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "ask again"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	parkOf(t, ag, run.ID, "question")
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"action": "decline"}); w.Code != 200 {
		t.Fatalf("decline: %d %s", w.Code, w.Body)
	}
	hwait(t, "declined", func() bool { return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "skipped") })
}

// syncBuf is a log sink the adapter's goroutines may write to.
type syncBuf struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuf) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuf) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// POST /runs/{id}/harness/authenticate: a participant who is a person
// allowed to use the sandbox (asked of the manager now), with a confirm on
// a sandbox others may use; an API key signs the adapter in and is never
// stored, logged or echoed; a device code answers 202 with its URL; the
// Needs list says `login`, naming the coding agent.
func TestHarnessRouteAuthenticate(t *testing.T) {
	ag, mux, box := harnessFixture(t, false, "--require-login", "--device-ms=300")
	logs := &syncBuf{}
	log.SetOutput(logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	shared := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "shared", Egress: "internet", Members: []string{"bob"}})
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "echo shared", "class": "coding",
		"harness": map[string]any{"provider": "fake"}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", shared.ID)}})
	if w.Code != 200 {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	var run Run
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	parkOf(t, ag, run.ID, "login")
	for _, m := range []map[string]string{{"user": "carol", "role": "participant"}, {"user": "bob", "role": "participant"}} {
		if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", run.ID), m); w.Code != 200 {
			t.Fatalf("members: %d %s", w.Code, w.Body)
		}
	}
	// Needs: a sign-in, the coding agent named
	var needs struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/needs", nil).Body.Bytes(), &needs)
	found := false
	for _, it := range needs.Items {
		if it["reason"] == "login" && it["run"].(map[string]any)["id"] == float64(run.ID) {
			h, _ := it["harness"].(map[string]any)
			found = h != nil && h["name"] == "Fake agent (tests)" && h["options"] == nil && h["login"].(map[string]any)["methods"] == nil
		}
	}
	if !found {
		t.Fatalf("needs: %+v", needs.Items)
	}
	// … and its push says the same
	if ps := ag.needsPushes(run.ID); len(ps) == 0 || ps[0].state != needLogin || ps[0].body != "Fake agent (tests) needs you to sign in to it." {
		t.Fatalf("the push: %+v", ps)
	}

	const key = "sk-route-secret-0815"
	path := fmt.Sprintf("/runs/%d/harness/authenticate", run.ID)
	for _, c := range []struct {
		who  caller
		body map[string]any
		code int
		want string
	}{
		{asSystem, map[string]any{"method": "fake-api-key", "apiKey": key}, 403, "only someone who may use shared can sign it in"},
		{asCarol, map[string]any{"method": "fake-api-key", "apiKey": key, "confirm": true}, 403, "only someone who may use shared can sign it in"},
		{asAlice, map[string]any{"method": "nope"}, 400, "method: one of fake-api-key, fake-device"},
		{asAlice, map[string]any{"method": "fake-api-key"}, 400, "apiKey: needed for"},
		{asAlice, map[string]any{"method": "fake-device", "apiKey": key}, 400, "apiKey: only for an API-key method"},
		{asAlice, map[string]any{"method": "fake-api-key", "apiKey": key}, 409, `"confirm":true`},
		{asBob, map[string]any{"method": "fake-api-key", "apiKey": "bad", "confirm": true}, 502, "invalid API key"},
	} {
		w := callAs(t, mux, c.who, "POST", path, c.body)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) || strings.Contains(w.Body.String(), key) {
			t.Fatalf("%v: %d %s", c.body, w.Code, w.Body)
		}
	}
	w = callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "fake-api-key", "apiKey": key, "confirm": true})
	if w.Code != 200 || w.Body.String() != `{"ok":"true","state":"ready"}`+"\n" {
		t.Fatalf("sign in: %d %q", w.Code, w.Body)
	}
	hwait(t, "the held prompt", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo shared")
	})
	if w := callAs(t, mux, asAlice, "POST", path, map[string]any{"method": "fake-api-key", "apiKey": key, "confirm": true}); w.Code != 409 ||
		!strings.Contains(w.Body.String(), "Fake agent (tests) is signed in") {
		t.Fatalf("signed in: %d %s", w.Code, w.Body)
	}

	// a device code on alice's own sandbox: no confirm needed there
	dev := askHarness(t, mux, box, "echo device")
	parkOf(t, ag, dev.ID, "login")
	w = callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/harness/authenticate", dev.ID), map[string]any{"method": "fake-device"})
	if w.Code != 202 || !strings.Contains(w.Body.String(), `"url":"https://example.invalid/device"`) || !strings.Contains(w.Body.String(), "FAKE-1234") {
		t.Fatalf("device: %d %s", w.Code, w.Body)
	}
	hwait(t, "the device sign-in", func() bool {
		return turnOver(ag, dev.ID)() && strings.Contains(fullText(ag.db, dev.ID), "echo: echo device")
	})

	// the key: in no row of any table, in no log line
	tables := scanStrings(t, ag.db, `SELECT name FROM sqlite_master WHERE type='table'`)
	for _, tbl := range tables {
		rows, err := ag.db.q.Query(`SELECT * FROM "` + tbl + `"`)
		if err != nil {
			continue
		}
		cols, _ := rows.Columns()
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			_ = rows.Scan(ptrs...)
			for i, v := range vals {
				if strings.Contains(fmt.Sprint(v), key) || strings.Contains(fmt.Sprintf("%s", v), key) {
					rows.Close()
					t.Fatalf("the key is stored in %s.%s", tbl, cols[i])
				}
			}
		}
		rows.Close()
	}
	if strings.Contains(logs.String(), key) {
		t.Fatalf("the key was logged")
	}
}

func scanStrings(t *testing.T, db *DB, q string, args ...any) []string {
	t.Helper()
	rows, err := db.q.Query(q, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if rows.Scan(&s) == nil {
			out = append(out, s)
		}
	}
	return out
}

// The built-in agent's routes on a coding agent's run (§4.2.11), the
// coding agent's routes on a built-in run, and automations that would
// drive a coding agent's conversation.
func TestHarnessRouteRefusals(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "later", "hold": true, "class": "coding",
		"harness": map[string]any{"provider": "fake"}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	id := run.ID
	builtin := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	for _, c := range []struct {
		method, path string
		body         any
		code         int
		want         string
	}{
		{"PUT", fmt.Sprintf("/runs/%d/memory", id), map[string]string{"key": "k", "value": "v"}, 409, "a coding agent has no memory"},
		{"DELETE", fmt.Sprintf("/runs/%d/memory?key=k", id), nil, 409, "a coding agent has no memory"},
		{"POST", fmt.Sprintf("/runs/%d/learn", id), nil, 409, "a coding agent can't learn a skill"},
		{"POST", fmt.Sprintf("/runs/%d/compact", id), nil, 409, "Fake agent (tests) has no /compact"},
		{"PATCH", fmt.Sprintf("/runs/%d", id), map[string]any{"model": "x"}, 400, "a coding agent's model is an option"},
		{"PATCH", fmt.Sprintf("/runs/%d", id), map[string]any{"sandbox": nil}, 400, "a coding agent's sandbox is fixed"},
		{"PATCH", fmt.Sprintf("/runs/%d", id), map[string]any{"detach": sandboxRef("apps/cs", box.ID)}, 400, "a coding agent's sandbox is fixed"},
		{"PATCH", fmt.Sprintf("/runs/%d", id), map[string]any{"pinned": true}, 200, ""},
		{"GET", fmt.Sprintf("/runs/%d/harness", builtin), nil, 409, "not a coding-agent conversation"},
		{"PATCH", fmt.Sprintf("/runs/%d/harness", builtin), map[string]any{"mode": "ask"}, 409, "not a coding-agent conversation"},
		{"POST", fmt.Sprintf("/runs/%d/harness/answer", builtin), map[string]any{"action": "decline"}, 409, "not a coding-agent conversation"},
		{"POST", fmt.Sprintf("/runs/%d/harness/authenticate", builtin), map[string]any{"method": "x"}, 409, "not a coding-agent conversation"},
		{"POST", fmt.Sprintf("/runs/%d/harness/answer", id), map[string]any{"action": "decline"}, 400, "no pending question"},
		{"POST", fmt.Sprintf("/runs/%d/harness/authenticate", id), map[string]any{"method": "x"}, 409, "Fake agent (tests) is signed in"},
		{"POST", "/schedules", map[string]any{"cron": "0 * * * *", "goal": "g", "harness": map[string]any{"provider": "fake"}}, 400, "schedules run the built-in agent"},
		{"POST", "/schedules", map[string]any{"cron": "0 * * * *", "goal": "g", "mode": "conversation", "targetRun": id}, 400, "a coding agent's conversation takes messages from people"},
		{"POST", "/triggers", map[string]any{"name": "t", "source": "push", "sourceRef": "apps/webhooks", "goal": "g", "harness": map[string]any{"provider": "fake"}}, 400, "triggers run the built-in agent"},
		{"POST", "/triggers", map[string]any{"name": "t2", "source": "push", "sourceRef": "apps/webhooks", "goal": "g", "mode": "conversation", "targetRun": id}, 400, "a coding agent's conversation takes messages from people"},
	} {
		w := callAs(t, mux, asAlice, c.method, c.path, c.body)
		if w.Code != c.code || !strings.Contains(w.Body.String(), c.want) {
			t.Fatalf("%s %s %v: %d %s", c.method, c.path, c.body, w.Code, w.Body)
		}
	}
	// the built-in run keeps its routes
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/runs/%d/memory", builtin), map[string]string{"key": "k", "value": "v"}); w.Code != 200 {
		t.Fatalf("built-in memory: %d %s", w.Code, w.Body)
	}
	// a coding agent that advertises /compact takes it
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", id), map[string]any{"text": "echo hi"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	hwait(t, "the turn", func() bool { return turnOver(ag, id)() && strings.Contains(fullText(ag.db, id), "echo: echo hi") })
	hwait(t, "its commands", func() bool { return harnessHasCommand(&run, "compact") }) // advertised just after session/new
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/compact", id), nil); w.Code != 200 && w.Code != 202 {
		t.Fatalf("compact: %d %s", w.Code, w.Body)
	}
	hwait(t, "/compact sent", func() bool {
		return turnOver(ag, id)() && strings.Contains(fullText(ag.db, id), "echo: /compact")
	})
}

// The views (§4.3.6–§4.3.9): a built-in conversation with a coding agent
// below it parked on a permission — the tree's node carries its engine and
// compact summary, the link's child its pendingState with the card data,
// the conversation row `waiting` and `kids`, Needs names the coding agent,
// and the child's run events reach the conversation's stream with its
// root, parent, engine and summary.
func TestHarnessViews(t *testing.T) {
	ag, mux, box := harnessFixture(t, false)
	tmpl := askHarness(t, mux, box, "echo template")
	hwait(t, "the template's turn", turnOver(ag, tmpl.ID))
	root := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	evs := watchTree(t, ag, root)
	kid := harnessChild(t, ag, root, tmpl.ID, "perm")
	parkOf(t, ag, kid, "approval")

	var tree struct {
		Nodes []map[string]any `json:"nodes"`
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/tree", root), nil).Body.Bytes(), &tree)
	var node map[string]any
	for _, n := range tree.Nodes {
		if n["id"] == float64(kid) {
			node = n
		} else if n["engine"] != "" || n["harness"] != nil {
			t.Fatalf("the built-in node: %v", n)
		}
	}
	if node == nil || node["engine"] != "harness" || node["rawStatus"] != "waiting_input" || node["updated"] == nil {
		t.Fatalf("the child's node: %v", node)
	}
	h, _ := node["harness"].(map[string]any)
	if h == nil || h["options"] != nil || h["commands"] != nil || h["mode"].(map[string]any)["available"] != nil ||
		h["pending"].(map[string]any)["kind"] != "approval" || h["provider"] != "fake" {
		t.Fatalf("the node's harness: %v", h)
	}

	v := viewOf(t, mux, root)
	links, _ := v["links"].([]any)
	if len(links) != 1 {
		t.Fatalf("links: %v", v["links"])
	}
	child := links[0].(map[string]any)["child"].(map[string]any)
	ps, _ := child["pendingState"].(map[string]any)
	if child["engine"] != "harness" || child["harness"] == nil || ps["kind"] != "approval" || ps["harness"].(map[string]any)["options"] == nil {
		t.Fatalf("the link's child: %v", child)
	}

	var convs struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/conversations", nil).Body.Bytes(), &convs)
	for _, it := range convs.Items {
		switch it["id"] {
		case float64(root):
			kids, _ := it["kids"].(map[string]any)
			if it["waiting"] != true || kids["harness"] != float64(1) || kids["waiting"] != float64(1) {
				t.Fatalf("the root's row: %v", it)
			}
		case float64(tmpl.ID):
			if it["waiting"] != nil || it["kids"] != nil {
				t.Fatalf("an idle row: %v", it)
			}
		}
	}

	var needs struct {
		Items []map[string]any `json:"items"`
	}
	_ = json.Unmarshal(callAs(t, mux, asAlice, "GET", "/needs", nil).Body.Bytes(), &needs)
	if len(needs.Items) != 1 || needs.Items[0]["reason"] != "approval" || needs.Items[0]["subRun"] != float64(kid) ||
		needs.Items[0]["harness"].(map[string]any)["provider"] != "fake" {
		t.Fatalf("needs: %v", needs.Items)
	}

	hwait(t, "the child's run event", func() bool {
		for _, ev := range evs.of(evRun) {
			d, _ := ev.Data.(map[string]any)
			if ev.Run == kid && d["rootId"] == root && d["parentId"] == root && d["engine"] == "harness" && d["harness"] != nil {
				return true
			}
		}
		return false
	})
}

// harnessChild starts a coding agent below parent (a link, as a spawn
// makes it), with the config of harness run like and its first prompt.
func harnessChild(t *testing.T, ag *Agent, parent, like int64, prompt string) int64 {
	t.Helper()
	cfg, _ := ag.db.runConfig(like)
	raw, _ := json.Marshal(cfg)
	var kid int64
	if err := ag.db.Tx(func(t2 *DB) error {
		var err error
		if kid, err = t2.createRunStamped("kid "+prompt, string(raw), parent, statusIdle, runStamp{Engine: engineHarness}); err != nil {
			return err
		}
		if _, err := t2.q.Exec(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, deadline, label, created) VALUES (?, ?, ?, 'bg', 0, ?, ?)`,
			parent, kid, fmt.Sprintf("call-%d", kid), "kid "+prompt, now()); err != nil {
			return err
		}
		_, _, err = t2.enqueue(kid, inboxHPrompt, inboxBody{Text: prompt, Source: "parent", From: parent}, "")
		return err
	}); err != nil {
		t.Fatal(err)
	}
	ag.eng.Poke(kid)
	return kid
}

// A coding agent the sdk catalog doesn't know is called what its manager
// advertises (the session row keeps the title from the spawn).
func TestHarnessAdvertisedName(t *testing.T) {
	ag, mux, box, m := harnessFixtureWith(t, nil, false)
	argv := acptest.Command()
	m.Harnesses = append(m.Harnesses, fsbHarness{ID: "house-agent", Title: "House agent", Argv: argv, Login: argv[0] + " acptest login"})
	forgetHellos()
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "echo hi", "class": "coding",
		"harness": map[string]any{"provider": "house-agent"}, "sandbox": map[string]any{"ref": sandboxRef("apps/cs", box.ID)}})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	hwait(t, "the answer", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	h := bodyJSON(t, callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil).Body.Bytes())["harness"].(map[string]any)
	if h["provider"] != "house-agent" || h["name"] != "House agent" {
		t.Fatalf("the summary: %v", h)
	}
}

// Bypass modes are default-deny (§4.3.12): a harness the sdk catalog lacks
// has no known-safe mode, so every mode but the one its adapter opened its
// first session in by itself is the root owner's — to PATCH, and to name at
// POST /ask for anyone but a person (the owner-to-be); the summary marks
// them explicit. Its sign-in errors use the manager's name for it.
func TestHarnessModeDefaultDeny(t *testing.T) {
	ag, mux, box, m := harnessFixtureWith(t, nil, false)
	argv := acptest.Command()
	m.Harnesses = append(m.Harnesses, fsbHarness{ID: "house-agent", Title: "House agent", Argv: argv, Login: argv[0] + " acptest login"})
	forgetHellos()
	ref := sandboxRef("apps/cs", box.ID)
	viewAs := caller{from: "apps/agent", user: "alice", level: "read", viewedBy: "admin"}
	if w := callAs(t, mux, viewAs, "POST", "/ask", map[string]any{"text": "x", "class": "coding",
		"harness": map[string]any{"provider": "house-agent", "mode": "ask"}, "sandbox": map[string]any{"ref": ref}}); w.Code != 403 ||
		!strings.Contains(w.Body.String(), "only a person can start House agent in ask") {
		t.Fatalf("a mode the catalog doesn't know, not from a person: %d %s", w.Code, w.Body)
	}
	w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "echo hi", "class": "coding",
		"harness": map[string]any{"provider": "house-agent"}, "sandbox": map[string]any{"ref": ref}})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	hwait(t, "the answer", func() bool {
		return turnOver(ag, run.ID)() && strings.Contains(fullText(ag.db, run.ID), "echo: echo hi")
	})
	if hs, _ := ag.db.harnessSession(run.ID); hs.StartMode != "ask" {
		t.Fatalf("the start mode: %q", hs.StartMode)
	}
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/members", run.ID), map[string]string{"user": "bob", "role": "participant"}); w.Code != 200 {
		t.Fatalf("members: %d %s", w.Code, w.Body)
	}
	h := bodyJSON(t, callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/harness", run.ID), nil).Body.Bytes())["harness"].(map[string]any)
	if b, _ := json.Marshal(h["mode"]); string(b) != `{"available":[{"id":"ask","name":"Ask"},{"explicit":true,"id":"yolo","name":"Yolo"}],"current":"ask"}` {
		t.Fatalf("the modes: %s", b)
	}
	path := fmt.Sprintf("/runs/%d/harness", run.ID)
	if w := callAs(t, mux, asBob, "PATCH", path, map[string]any{"mode": "yolo"}); w.Code != 403 || !strings.Contains(w.Body.String(), "only alice can switch House agent to Yolo") {
		t.Fatalf("bob to yolo: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asAlice, "PATCH", path, map[string]any{"mode": "yolo"}); w.Code != 200 {
		t.Fatalf("alice to yolo: %d %s", w.Code, w.Body)
	}
	if w := callAs(t, mux, asBob, "PATCH", path, map[string]any{"mode": "ask"}); w.Code != 200 {
		t.Fatalf("bob back to the one it started in: %d %s", w.Code, w.Body)
	}
	// a plan approval's allow that switches it to a mode it didn't start in
	// (auto: known safe only to the catalog's providers) raises it
	m.Harnesses = append(m.Harnesses, fsbHarness{ID: "house-auto", Title: "House auto", Argv: acptest.Command("--auto-mode")})
	forgetHellos()
	w = callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "plan", "class": "coding",
		"harness": map[string]any{"provider": "house-auto"}, "sandbox": map[string]any{"ref": ref}})
	var planRun Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &planRun) != nil {
		t.Fatalf("ask: %d %s", w.Code, w.Body)
	}
	raised := map[string]bool{}
	for _, o := range parkOf(t, ag, planRun.ID, "approval").Harness.Options {
		raised[o.OptionID] = o.Explicit
	}
	if len(raised) != 4 || !raised["auto"] || raised["exit-plan-default"] || raised["exit-plan-clear-auto"] || raised["reject"] {
		t.Fatalf("the plan's options: %v", raised)
	}

	// its sign-in's errors name it as its manager does
	ag.eng.endHarness(ag.eng.base, run.ID)
	if _, err := ag.eng.harnessAuthenticate(ag.eng.base, &run, "x", ""); err == nil || !strings.Contains(err.Error(), "House agent is signed in") {
		t.Fatalf("authenticate: %v", err)
	}
}

// A coding agent's park as a push says what it wants in its words — and
// something still when its tool call names nothing.
func TestHarnessNeedWords(t *testing.T) {
	run := &Run{ID: 1}
	for _, c := range []struct {
		p           pendingState
		state, body string
	}{
		{pendingState{Kind: "approval", Park: "a", Harness: &hPark{Tool: &hParkTool{Title: "go test ./..."}}}, needApproval,
			"The coding agent wants to run go test ./... — approve or deny."},
		{pendingState{Kind: "approval", Park: "b", Harness: &hPark{Tool: &hParkTool{}}}, needApproval,
			"The coding agent wants to run a command — approve or deny."},
		{pendingState{Kind: "question", Park: "c", Harness: &hPark{Message: "Which library?"}}, needQuestion, "Which library?"},
		{pendingState{Kind: "login", Park: "d", Harness: &hPark{}}, needLogin, "The coding agent needs you to sign in to it."},
	} {
		state, body, fp := harnessNeed(run, c.p)
		if state != c.state || body != c.body || fp != c.p.Park {
			t.Fatalf("%+v: %q %q %q", c.p, state, body, fp)
		}
	}
}
