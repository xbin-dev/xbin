package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"testing"
)

// pagedFixture is a run with a bit of everything, at known times:
//
//	t=90  compacted turn (u0, a0) — hidden, counted
//	t=100 system prompt; note "started"; u1
//	t=101 a1 calls c1 c2 (+ results); llm_call step
//	t=102 a2 text; note
//	t=103 u2 (carries a file)
//	t=104 a3 spawns c3 (+ result); spawn step; its link (+ a second link of that child)
//	t=105 u3
//	t=106 a4 text; finish step
//	also: a link of another child spawned by a call no message holds
type pagedFixture struct {
	id     int64
	ids    map[string]int64 // message name → id
	shown  []int64          // shown message ids, oldest first
	steps  int
	links  int
	childA int64
}

func buildPaged(t *testing.T, ag *Agent) *pagedFixture {
	t.Helper()
	db := ag.db
	id, err := db.createRun("paged", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	fx := &pagedFixture{id: id, ids: map[string]int64{}}
	at := func(name string, m *Message, ts int64, shown bool) {
		t.Helper()
		m.RunID = id
		mid, err := db.addMessage(m)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.q.Exec(`UPDATE messages SET created=?, compacted=? WHERE id=?`, ts, b2i(m.Compacted), mid); err != nil {
			t.Fatal(err)
		}
		fx.ids[name] = mid
		if shown {
			fx.shown = append(fx.shown, mid)
		}
	}
	step := func(kind string, detail any, ts int64) {
		t.Helper()
		s := db.journal(id, kind, detail)
		if _, err := db.q.Exec(`UPDATE steps SET created=? WHERE id=?`, ts, s.ID); err != nil {
			t.Fatal(err)
		}
		fx.steps++
	}
	calls := func(cs ...toolCall) string { b, _ := json.Marshal(cs); return string(b) }
	at("sys", &Message{Role: "system", Content: "you are a test"}, 100, false)
	at("u0", &Message{Role: "user", Content: "old question", Compacted: true}, 90, false)
	at("a0", &Message{Role: "assistant", Content: "old answer", Compacted: true}, 90, false)
	step("note", map[string]string{"text": "started"}, 100)
	at("u1", &Message{Role: "user", Content: "first"}, 100, true)
	at("a1", &Message{Role: "assistant", ToolCalls: calls(tc("c1", "file_list", `{}`), tc("c2", "file_list", `{}`))}, 101, true)
	at("c1", &Message{Role: "tool", ToolCallID: "c1", Name: "file_list", Content: "r1"}, 101, true)
	at("c2", &Message{Role: "tool", ToolCallID: "c2", Name: "file_list", Content: "r2"}, 101, true)
	step("llm_call", map[string]any{"model": "m"}, 101)
	at("a2", &Message{Role: "assistant", Content: "two"}, 102, true)
	step("note", map[string]string{"text": "hm"}, 102)
	at("u2", &Message{Role: "user", Content: "second"}, 103, true)
	if _, err := db.q.Exec(`INSERT INTO message_files (msg_id, run_id, path) VALUES (?, ?, 'a.png')`, fx.ids["u2"], id); err != nil {
		t.Fatal(err)
	}
	childA, _ := db.createRun("child", "", id)
	childB, _ := db.createRun("other child", "", id)
	fx.childA = childA
	at("a3", &Message{Role: "assistant", ToolCalls: calls(tc("c3", "subagent_spawn", `{"task":"x"}`))}, 104, true)
	at("c3", &Message{Role: "tool", ToolCallID: "c3", Name: "subagent_spawn", Content: "spawned"}, 104, true)
	step("spawn", map[string]any{"runId": childA, "toolCallId": "c3"}, 104)
	for _, l := range []struct {
		child int64
		call  string
	}{{childA, "c3"}, {childA, ""}, {childB, "gone"}} {
		if _, err := db.q.Exec(`INSERT INTO links (parent_id, child_id, tool_call_id, mode, state, deadline, created) VALUES (?, ?, ?, 'fg', 'done', 0, 104)`, id, l.child, l.call); err != nil {
			t.Fatal(err)
		}
		fx.links++
	}
	at("u3", &Message{Role: "user", Content: "third"}, 105, true)
	at("a4", &Message{Role: "assistant", Content: "four"}, 106, true)
	step("finish", map[string]string{"result": "four"}, 106)
	return fx
}

type viewPageJSON struct {
	Messages []struct {
		ID   int64  `json:"id"`
		Seq  int    `json:"seq"`
		Role string `json:"role"`
	} `json:"messages"`
	Steps []struct {
		ID      int64 `json:"id"`
		Created int64 `json:"created"`
	} `json:"steps"`
	Links []struct {
		ID         int64  `json:"id"`
		ChildID    int64  `json:"childId"`
		ToolCallID string `json:"toolCallId"`
	} `json:"links"`
	MessageFiles map[string][]string `json:"messageFiles"`
	HasOlder     *bool               `json:"hasOlder"`
	NextBefore   *int                `json:"nextBefore"`
	Compacted    int                 `json:"compacted"`
	LinkCount    int                 `json:"linkCount"`
	Cursor       string              `json:"cursor"`
}

func getView(t *testing.T, id int64, query string) (int, viewPageJSON) {
	t.Helper()
	w := serve(handleView, "GET", fmt.Sprintf("/runs/%d/view%s", id, query), id, nil, "")
	var v viewPageJSON
	if w.Code == 200 {
		if err := json.Unmarshal(w.Body.Bytes(), &v); err != nil {
			t.Fatal(err)
		}
	}
	return w.Code, v
}

// Without before/limit the view is what it always was: everything,
// compacted and system messages included, and none of the paging fields.
func TestViewUnpagedUnchanged(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	fx := buildPaged(t, ag)
	w := serve(handleView, "GET", fmt.Sprintf("/runs/%d/view", fx.id), fx.id, nil, "")
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var raw map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &raw)
	for _, k := range []string{"hasOlder", "nextBefore", "compacted", "linkCount"} {
		if _, ok := raw[k]; ok {
			t.Fatalf("the unpaged view grew %q", k)
		}
	}
	_, v := getView(t, fx.id, "")
	if len(v.Messages) != len(fx.shown)+3 || len(v.Steps) != fx.steps || len(v.Links) != fx.links {
		t.Fatalf("unpaged: %d messages, %d steps, %d links", len(v.Messages), len(v.Steps), len(v.Links))
	}
}

// Walking the pages newest first covers exactly the shown messages, once
// each; pages never start with a tool result nor split a call from its
// results; steps are partitioned; each page's links cover its subagents.
func TestViewPagesPartitionTheTranscript(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	fx := buildPaged(t, ag)
	for limit := 1; limit <= len(fx.shown)+1; limit++ {
		seenMsg, seenStep := map[int64]int{}, map[int64]int{}
		var order []int64
		var pages []viewPageJSON
		q := fmt.Sprintf("?limit=%d", limit)
		for guard := 0; ; guard++ {
			if guard > 20 {
				t.Fatalf("limit %d: paging does not end", limit)
			}
			code, v := getView(t, fx.id, q)
			if code != 200 || v.HasOlder == nil {
				t.Fatalf("limit %d: %d, hasOlder=%v", limit, code, v.HasOlder)
			}
			if v.Compacted != 2 || v.LinkCount != fx.links || v.Cursor == "" {
				t.Fatalf("limit %d: page fields compacted=%d linkCount=%d cursor=%q", limit, v.Compacted, v.LinkCount, v.Cursor)
			}
			if len(v.Messages) == 0 {
				t.Fatalf("limit %d: an empty page", limit)
			}
			if v.Messages[0].Role == "tool" {
				t.Fatalf("limit %d: a page starts with a tool result", limit)
			}
			if len(v.Messages) < limit && *v.HasOlder {
				t.Fatalf("limit %d: a short page with more before it", limit)
			}
			var ids []int64
			inPage := map[string]bool{}
			for _, m := range v.Messages {
				if m.Role == "system" {
					t.Fatalf("limit %d: the system prompt is in a page", limit)
				}
				seenMsg[m.ID]++
				ids = append(ids, m.ID)
				inPage[fmt.Sprint(m.ID)] = true
			}
			order = append(ids, order...)
			for _, s := range v.Steps {
				seenStep[s.ID]++
			}
			// the calls in this page find their subagent's links
			for _, m := range v.Messages {
				if m.ID == fx.ids["a3"] {
					n := 0
					for _, l := range v.Links {
						if l.ChildID == fx.childA {
							n++
						}
					}
					if n != 2 {
						t.Fatalf("limit %d: the page with the spawn carries %d of the child's 2 links", limit, n)
					}
				}
				if m.ID == fx.ids["u2"] && strings.Join(v.MessageFiles[fmt.Sprint(m.ID)], ",") != "a.png" {
					t.Fatalf("limit %d: messageFiles %v", limit, v.MessageFiles)
				}
			}
			for k := range v.MessageFiles {
				if !inPage[k] {
					t.Fatalf("limit %d: messageFiles of a message not in the page: %s", limit, k)
				}
			}
			pages = append(pages, v)
			if !*v.HasOlder {
				if v.NextBefore != nil {
					t.Fatal("nextBefore without hasOlder")
				}
				break
			}
			if *v.NextBefore != v.Messages[0].Seq {
				t.Fatalf("limit %d: nextBefore %d, first seq %d", limit, *v.NextBefore, v.Messages[0].Seq)
			}
			q = fmt.Sprintf("?limit=%d&before=%d", limit, *v.NextBefore)
		}
		if fmt.Sprint(order) != fmt.Sprint(fx.shown) {
			t.Fatalf("limit %d: pages hold %v, the shown messages are %v", limit, order, fx.shown)
		}
		for id, n := range seenMsg {
			if n != 1 {
				t.Fatalf("limit %d: message %d in %d pages", limit, id, n)
			}
		}
		if len(seenStep) != fx.steps {
			t.Fatalf("limit %d: pages hold %d of %d steps", limit, len(seenStep), fx.steps)
		}
		for id, n := range seenStep {
			if n != 1 {
				t.Fatalf("limit %d: step %d in %d pages", limit, id, n)
			}
		}
		// a step lands in the page whose time window holds it: never before
		// that page's first message when older pages exist
		for _, p := range pages {
			if *p.HasOlder {
				first := p.Messages[0]
				var created int64
				_ = db.q.QueryRow(`SELECT created FROM messages WHERE id=?`, first.ID).Scan(&created)
				for _, s := range p.Steps {
					if s.Created < created {
						t.Fatalf("limit %d: step at %d before its page's first message (%d)", limit, s.Created, created)
					}
				}
			}
		}
	}
}

// limit alone is the newest page; before alone uses the default size;
// a call's results come with it.
func TestViewPageShapes(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	fx := buildPaged(t, ag)
	msgIDs := func(v viewPageJSON) []int64 {
		var out []int64
		for _, m := range v.Messages {
			out = append(out, m.ID)
		}
		return out
	}
	_, v := getView(t, fx.id, "?limit=2")
	if fmt.Sprint(msgIDs(v)) != fmt.Sprint([]int64{fx.ids["u3"], fx.ids["a4"]}) || !*v.HasOlder {
		t.Fatalf("newest two: %v older=%v", msgIDs(v), *v.HasOlder)
	}
	// limit 1 before u2 would start at c2: it reaches back to a1 and its results
	var seqU2 int
	_ = db.q.QueryRow(`SELECT seq FROM messages WHERE id=?`, fx.ids["u2"]).Scan(&seqU2)
	// the two before u2 are c2 and a2; c2 is a result, so the page reaches
	// back through c1 to the call a1
	_, v = getView(t, fx.id, fmt.Sprintf("?limit=2&before=%d", seqU2))
	if fmt.Sprint(msgIDs(v)) != fmt.Sprint([]int64{fx.ids["a1"], fx.ids["c1"], fx.ids["c2"], fx.ids["a2"]}) || !*v.HasOlder {
		t.Fatalf("page before u2: %v older=%v", msgIDs(v), *v.HasOlder)
	}
	_, v = getView(t, fx.id, fmt.Sprintf("?before=%d", seqU2))
	if len(v.Messages) != 5 || *v.HasOlder { // u1 a1 c1 c2 a2
		t.Fatalf("before alone: %v older=%v", msgIDs(v), *v.HasOlder)
	}
	// the oldest page holds the steps from before the live window too
	sort.Slice(v.Steps, func(i, j int) bool { return v.Steps[i].ID < v.Steps[j].ID })
	if len(v.Steps) == 0 || v.Steps[0].Created != 100 {
		t.Fatalf("oldest page steps: %+v", v.Steps)
	}
	// huge limits clamp; bad values are 400
	if code, _ := getView(t, fx.id, "?limit=100000"); code != 200 {
		t.Fatalf("a huge limit: %d", code)
	}
	for _, q := range []string{"?limit=0", "?limit=x", "?before=-1", "?before=x&limit=3"} {
		if code, _ := getView(t, fx.id, q); code != http.StatusBadRequest {
			t.Fatalf("%s: %d", q, code)
		}
	}
	// a run with nothing shown: an empty page, nothing older
	empty, _ := db.createRun("empty", "", 0)
	code, v := getView(t, empty, "?limit=5")
	if code != 200 || len(v.Messages) != 0 || *v.HasOlder {
		t.Fatalf("empty run: %d %+v", code, v)
	}
}

// The paged view goes through the same guard as the view: someone who may
// not see the run gets 404 whatever the query.
func TestViewPageAccess(t *testing.T) {
	ag, mux := accessFixture(t)
	id := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	if got := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/view?limit=5", id), nil).Code; got != 404 {
		t.Fatalf("bob pages alice's private run: %d", got)
	}
	if got := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view?limit=5", id), nil).Code; got != 200 {
		t.Fatalf("alice pages her run: %d", got)
	}
}
