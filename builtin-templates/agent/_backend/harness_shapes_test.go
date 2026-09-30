package main

// The STUB-drift guard's backend half (D-harness §4): the real backend
// produces every shape the UI's STUB serves from its fixtures
// (test/backend.mjs, test/harness-fixtures.mjs) — a coding agent's summary
// in each state, its parks' pendingState, its transcript rows and their
// `acp`, /tree nodes, links, conversation rows, /needs items, the catalog,
// GET|PATCH /runs/{id}/harness, authenticate's answers and the stream's
// harness, run and link events — and records their key paths and JSON types
// in testdata/harness_shapes.json. This test fails when the backend no
// longer produces a path the file lists; hack/agent-template-harness-
// shapes.test.mjs fails when a fixture uses a path the file doesn't list.
//
// Regenerate the file after a backend change (review the diff):
//
//	HARNESS_SHAPES_OUT=$PWD/builtin-templates/agent/_backend/testdata/harness_shapes.json \
//	  TILE_TEST_FLAGS="-count=1 -run TestHarnessShapes" ./hack/tile-check.sh agent

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// shapeSet is a shape's key paths and the JSON types seen at each; a
// shapeBook, the shapes by name.
type (
	shapeSet  map[string]map[string]bool
	shapeBook map[string]shapeSet
)

// shapeOpaque are keys whose values are the adapter's own (arbitrary JSON):
// their type is recorded, what is inside them isn't.
var shapeOpaque = map[string]bool{"rawInput": true, "schema": true}

// shapeMaps are keys whose values are maps with keys of their own (a
// sandbox ref): their keys are "*".
var shapeMaps = map[string]bool{"sandboxes": true}

// shapeFold: a summary or a park inside another shape is a shape of its own
// (summary; summaryNode — a /tree node's or a /needs item's compact one;
// pending.<kind>), recorded there — the path keeps only its type. A
// pendingState of no kind (a built-in run's) is its type alone.
func shapeFold(shape, path, key string, x any) (string, bool) {
	m, ok := x.(map[string]any)
	if !ok {
		return "", false
	}
	switch {
	case key == "harness" && m["provider"] != nil:
		if shape == "treeNode" || (shape == "needsItem" && path == "harness") {
			return "summaryNode", true
		}
		return "summary", true
	case key == "data" && shape == "harnessEvent":
		return "summary", true
	case key == "pendingState":
		if k, _ := m["kind"].(string); k != "" {
			return "pending." + k, true
		}
		return "", true
	}
	return "", false
}

func (b shapeBook) add(name string, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	var x any
	if err := json.Unmarshal(raw, &x); err != nil {
		panic(err)
	}
	b.walk(name, "", "", x)
}

func (b shapeBook) walk(shape, path, key string, x any) {
	if path != "" {
		if b[shape] == nil {
			b[shape] = shapeSet{}
		}
		if b[shape][path] == nil {
			b[shape][path] = map[string]bool{}
		}
		b[shape][path][jsonType(x)] = true
		if to, ok := shapeFold(shape, path, key, x); ok {
			if to != "" {
				b.walk(to, "", "", x)
			}
			return
		}
	}
	if shapeOpaque[key] {
		return
	}
	join := func(k string) string {
		if path == "" {
			return k
		}
		return path + "." + k
	}
	switch v := x.(type) {
	case map[string]any:
		for k, c := range v {
			if shapeMaps[key] {
				b.walk(shape, join("*"), "*", c)
			} else {
				b.walk(shape, join(k), k, c)
			}
		}
	case []any:
		for _, c := range v {
			b.walk(shape, path+"[]", key, c)
		}
	}
}

func jsonType(x any) string {
	switch x.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	}
	return "object"
}

// shapeDump is testdata/harness_shapes.json.
type shapeDump struct {
	Note   string                         `json:"note"`
	Shapes map[string]map[string][]string `json:"shapes"`
}

// encode is the file: a line per path, sorted, for readable diffs.
func (d shapeDump) encode() []byte {
	var b strings.Builder
	note, _ := json.Marshal(d.Note)
	fmt.Fprintf(&b, "{\"note\": %s,\n \"shapes\": {", note)
	names := make([]string, 0, len(d.Shapes))
	for n := range d.Shapes {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		if i > 0 {
			b.WriteString(",")
		}
		fmt.Fprintf(&b, "\n  %q: {", n)
		paths := make([]string, 0, len(d.Shapes[n]))
		for p := range d.Shapes[n] {
			paths = append(paths, p)
		}
		sort.Strings(paths)
		for j, p := range paths {
			if j > 0 {
				b.WriteString(",")
			}
			types, _ := json.Marshal(d.Shapes[n][p])
			k, _ := json.Marshal(p)
			fmt.Fprintf(&b, "\n   %s: %s", k, types)
		}
		b.WriteString("\n  }")
	}
	b.WriteString("\n }\n}")
	return []byte(b.String())
}

func TestHarnessShapes(t *testing.T) {
	ag, mux, box := harnessFixture(t, false, "--require-login", "--device-ms=300")
	shapes := shapeBook{}
	add := shapes.add
	call := func(method, target string, body any) map[string]any {
		t.Helper()
		w := callAs(t, mux, asAlice, method, target, body)
		if w.Code >= 300 && w.Code != 409 {
			t.Fatalf("%s %s: %d %s", method, target, w.Code, w.Body)
		}
		return bodyJSON(t, w.Body.Bytes())
	}
	// the harness parts of a view: its run (runSummary + the view's own),
	// its config, its transcript by role, its links
	view := func(id int64) map[string]any {
		t.Helper()
		v := viewOf(t, mux, id)
		add("viewRun", v["run"])
		add("viewConfig", v["config"])
		for _, m := range msgsOf(v) {
			add("message."+fmt.Sprint(m["role"]), m)
		}
		links, _ := v["links"].([]any)
		for _, l := range links {
			add("link", l)
		}
		return v
	}
	lists := func() {
		t.Helper()
		for _, it := range call("GET", "/conversations", nil)["items"].([]any) {
			add("convRow", it)
		}
		for _, it := range call("GET", "/needs", nil)["items"].([]any) {
			add("needsItem", it)
		}
	}
	events := func(evs *hEvents) {
		for typ, name := range map[string]string{evHarness: "harnessEvent", evRun: "runEvent", evLink: "linkEvent"} {
			for _, ev := range evs.of(typ) {
				if typ == evRun {
					if d, _ := ev.Data.(map[string]any); d["engine"] != engineHarness {
						continue
					}
				}
				add(name, ev)
			}
		}
	}
	ask := func(b *sbxSandbox, text string) int64 {
		t.Helper()
		body := call("POST", "/ask", map[string]any{"text": text, "class": "coding", "harness": map[string]any{"provider": "fake"},
			"sandbox": map[string]any{"ref": sandboxRef("apps/cs", b.ID)}})
		add("askAnswer", body)
		return int64(body["id"].(float64))
	}
	answered := func(id int64, text string) {
		t.Helper()
		hwait(t, text, func() bool { return turnOver(ag, id)() && strings.Contains(fullText(ag.db, id), text) })
	}

	// signed out, on a sandbox others may use: the login park; a confirm;
	// a device code, which signs it in by itself
	shared := mkSandbox(t, "apps/cs", "alice", sbxCreate{Name: "shared", Egress: "internet", Members: []string{"bob"}})
	login := ask(shared, "echo one")
	evs := watchTree(t, ag, login)
	parkOf(t, ag, login, "login")
	view(login)
	lists()
	add("authenticate", call("POST", fmt.Sprintf("/runs/%d/harness/authenticate", login), map[string]any{"method": "fake-device"}))
	add("authenticate", call("POST", fmt.Sprintf("/runs/%d/harness/authenticate", login), map[string]any{"method": "fake-device", "confirm": true}))
	add("harnessGet", call("GET", fmt.Sprintf("/runs/%d/harness", login), nil)) // the requester's: login.device's code
	view(login)                                                                 // login.device: who
	answered(login, "echo: echo one")
	events(evs)
	// an API key on alice's own
	key := ask(box, "echo two")
	parkOf(t, ag, key, "login")
	add("authenticate", call("POST", fmt.Sprintf("/runs/%d/harness/authenticate", key), map[string]any{"method": "fake-api-key", "apiKey": "k"}))
	answered(key, "echo: echo two")

	// a transcript with a card of each kind, a subagent, thinking and a plan
	cards := ask(box, "cards")
	evs = watchTree(t, ag, cards)
	answered(cards, "cards done")
	for text, end := range map[string]string{"subagent": "the subagent found it", "think": "thought it through", "todo": "todo done"} {
		call("POST", fmt.Sprintf("/runs/%d/message", cards), map[string]any{"text": text})
		answered(cards, end)
	}
	hwait(t, "its commands", func() bool { r, _ := ag.db.getRun(cards); return harnessHasCommand(r, "compact") })
	add("patch", call("PATCH", fmt.Sprintf("/runs/%d/harness", cards), map[string]any{"mode": "ask", "option": map[string]string{"id": "model", "value": "fake-fast"}}))
	ag.eng.harnessOf(cards).activity("tool", "Run go vet ./...")
	add("harnessGet", call("GET", fmt.Sprintf("/runs/%d/harness", cards), nil))
	for _, n := range call("GET", fmt.Sprintf("/runs/%d/tree", cards), nil)["nodes"].([]any) {
		add("treeNode", n)
	}
	view(cards)
	lists()
	events(evs)
	for _, h := range call("GET", "/harnesses", nil)["harnesses"].([]any) {
		add("catalog", h)
	}
	// usage with a cost, as claude reports it (stored as the adapter sent it)
	hs, _ := ag.db.harnessSession(key)
	hs.Usage = `{"used":52000,"size":200000,"cost":{"amount":0.41,"currency":"USD"}}`
	if err := ag.db.putHarnessSession(hs); err != nil {
		t.Fatal(err)
	}
	view(key)

	// the parks: a permission, a plan approval, a question
	for _, c := range []struct{ text, kind string }{{"perm", "approval"}, {"plan", "approval"}, {"ask", "question"}} {
		id := ask(box, c.text)
		p := parkOf(t, ag, id, c.kind)
		view(id)
		lists()
		if c.kind == "question" {
			add("ok", call("POST", fmt.Sprintf("/runs/%d/harness/answer", id), map[string]any{"park": p.Park, "action": "decline"}))
		}
	}

	// a built-in conversation with coding agents below it: one parked, one done
	root := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	evs = watchTree(t, ag, root)
	kid := harnessChild(t, ag, root, cards, "perm")
	done := harnessChild(t, ag, root, cards, "echo kid")
	parkOf(t, ag, kid, "approval")
	answered(done, "echo: echo kid")
	hwait(t, "the done child's link", func() bool {
		var st string
		_ = ag.db.q.QueryRow(`SELECT state FROM links WHERE child_id=?`, done).Scan(&st)
		return st != linkRunning
	})
	for _, n := range call("GET", fmt.Sprintf("/runs/%d/tree", root), nil)["nodes"].([]any) {
		add("treeNode", n)
	}
	view(root)
	lists()
	events(evs)

	// a coding agent as the adapters may describe it (descriptions, a cost,
	// a plan's priorities, remembered rules, a sign-in with a device code),
	// stored as the engine stores it, read back through the views
	st := acp.SessionState{SessionID: "s1",
		Modes: &acp.SessionModes{CurrentModeID: "ask", AvailableModes: []acp.ModeEntry{{ID: "ask", Name: "Ask", Description: "Asks first"}, {ID: "yolo", Name: "Yolo", Description: "Never asks"}}},
		Options: []acp.ConfigOption{{ID: "model", Name: "Model", Description: "Which model", Category: "model", Type: "select", CurrentValue: "fake-default",
			Options: []acp.ConfigValue{{Value: "fake-default", Name: "Default", Description: "The default"}}}},
		Commands: []acp.Command{{Name: "review", Description: "Review", Hint: "what"}}}
	snap, _ := json.Marshal(st)
	opts, _ := json.Marshal(st.Options)
	if err := ag.db.setHarnessOptions(fakeHarness, opts); err != nil {
		t.Fatal(err)
	}
	loginFull, _ := json.Marshal(hLogin{Command: "fake login", Methods: []hLoginMethod{{ID: "fake-api-key", Name: "API key", Kind: "api-key"}},
		Device: &hDevice{By: "alice"}}) // as stored: the code is the requester's alone (GET …/harness)
	ag.eng.endHarness(ag.eng.base, key)
	hs, _ = ag.db.harnessSession(key)
	hs.State, hs.ExecID, hs.Snapshot, hs.Login, hs.Title = hsLogin, "", string(snap), string(loginFull), "Fix it"
	hs.Plan = `{"entries":[{"content":"Write the test","status":"completed","priority":"high"}]}`
	hs.Rules = `[{"kind":"execute","title":"go test ./..."}]`
	if err := ag.db.putHarnessSession(hs); err != nil {
		t.Fatal(err)
	}
	ps, _ := json.Marshal(pendingState{Kind: "login", Park: "Xq3", Harness: &hPark{Login: loginFull}})
	if err := ag.db.setStatus(key, statusWaiting, 0, "", string(ps)); err != nil {
		t.Fatal(err)
	}
	view(key)
	lists()
	add("harnessGet", call("GET", fmt.Sprintf("/runs/%d/harness", key), nil))
	for _, n := range call("GET", fmt.Sprintf("/runs/%d/tree", key), nil)["nodes"].([]any) {
		add("treeNode", n)
	}
	for _, h := range call("GET", "/harnesses", nil)["harnesses"].([]any) {
		add("catalog", h)
	}

	// every field the serializers have, filled in: the acp lift, a park's
	// card data, the login (what the fake adapter never sends)
	ec := 0
	full := &harnessMeta{Kind: "execute", Title: "go test", Label: "Run the tests", Tool: "Bash", Status: "completed", Parent: "h1:t0",
		Subagent: true, PlanReview: true, Locations: []hLocation{{Path: "/w/x.go", Line: &ec}}, Files: []string{"/w/x.go"}, ExitCode: &ec,
		Output: "ok", OutputTruncated: 1, Diffs: []hDiff{{Path: "/w/x.go", Status: "modified", Add: 1, Del: 1, Patch: "@@", Truncated: true}}}
	meta, _ := json.Marshal(msgMeta{Harness: full})
	add("message.tool", messageView(&Message{ID: 1, RunID: 1, Seq: 1, Role: "tool", Content: "ok", ToolCallID: "h1:t1", Name: "acp:execute", Meta: meta}))
	meta, _ = json.Marshal(msgMeta{Harness: &harnessMeta{Parent: "h1:t0"}, Reasoning: "r", ReasoningMs: 1})
	add("message.assistant", messageView(&Message{ID: 2, RunID: 1, Seq: 2, Role: "assistant", Content: "x", Meta: meta,
		ToolCalls: `[{"id":"h1:t1","type":"function","function":{"name":"acp:execute","arguments":"{}"}}]`}))
	add("pending.approval", pendingState{Kind: "approval", Park: "p", ToolCalls: []toolCall{{ID: "h1:t1", Type: "function"}},
		Harness: &hPark{CallID: "h1:t1", Options: []hOption{{OptionID: "o", Name: "O", Kind: acp.AllowOnce, Explicit: true}},
			Tool: &hParkTool{Title: "t", Kind: "execute", Name: "Bash", Label: "l", Command: "c", RawInput: json.RawMessage(`{}`), Content: json.RawMessage(`[]`)},
			Rule: &acp.Rule{Kind: "execute", Title: "t"}, DefaultToNo: true, Description: "d", PlanApproval: true, Plan: "p", PID: "p1", RPCID: `"r"`}})
	add("pending.question", pendingState{Kind: "question", Park: "p", Harness: &hPark{EID: "e", CallID: "h1:t1", Message: "m", Schema: json.RawMessage(`{}`)}})
	add("pending.login", pendingState{Kind: "login", Park: "p", Harness: &hPark{Login: loginFull}})

	// the dump: paths and types, sorted
	gen := shapeDump{Shapes: map[string]map[string][]string{},
		Note: "D-harness §4 shapes the real backend produces (TestHarnessShapes); the UI's STUB fixtures must use only these — regenerate with HARNESS_SHAPES_OUT (see harness_shapes_test.go)"}
	for name, set := range shapes {
		gen.Shapes[name] = map[string][]string{}
		for p, types := range set {
			var ts []string
			for ty := range types {
				ts = append(ts, ty)
			}
			sort.Strings(ts)
			gen.Shapes[name][p] = ts
		}
	}
	out := gen.encode()
	if path := os.Getenv("HARNESS_SHAPES_OUT"); path != "" {
		if err := os.WriteFile(path, append(out, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
	}
	raw, err := os.ReadFile("testdata/harness_shapes.json")
	if err != nil {
		t.Fatalf("testdata/harness_shapes.json: %v — generate it with HARNESS_SHAPES_OUT (see this file)", err)
	}
	var have shapeDump
	if err := json.Unmarshal(raw, &have); err != nil {
		t.Fatal(err)
	}
	var missing []string
	for name, paths := range have.Shapes {
		for p, types := range paths {
			for _, ty := range types {
				if !hasStr(gen.Shapes[name][p], ty) {
					missing = append(missing, fmt.Sprintf("%s: %s (%s)", name, p, ty))
				}
			}
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("the backend no longer produces what testdata/harness_shapes.json says (fix the backend, or regenerate the file and the STUB fixtures):\n  %s",
			strings.Join(missing, "\n  "))
	}
}
