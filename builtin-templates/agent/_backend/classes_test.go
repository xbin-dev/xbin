package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

// useClasses puts classes in force as PUT /classes does, and the built-ins
// back after the test.
func useClasses(t *testing.T, list ...agentClass) {
	t.Helper()
	classStore.Store(newClassState(classSettings{Classes: list}))
	t.Cleanup(func() { classStore.Store(nil) })
}

func specNames(cfg Config, depth int, mcp []toolSpec) map[string]bool {
	out := map[string]bool{}
	for _, s := range toolSpecs(cfg, depth, mcp) {
		out[s.Function.Name] = true
	}
	return out
}

var testMCP = []toolSpec{
	{Type: "function", Function: funcDef{Name: "mcp:comm:get_thread"}},
	{Type: "function", Function: funcDef{Name: "mcp:hr:salaries"}},
}

// Each class offers exactly its toolsets (the core tools always), and runTool
// refuses what the class doesn't hold, whatever the model was offered.
func TestClassToolMatrix(t *testing.T) {
	useClasses(t,
		agentClass{ID: "notes", Name: "Notes", Toolsets: []string{tsFiles, tsSkills}},
		agentClass{ID: "comm", Name: "Comm", Toolsets: []string{tsInternal}, MCP: classSet{Names: []string{"comm"}}})
	core := []string{"memory_set", "memory_get", "note", "finish", "yield", "ask_user", "recall"}
	for _, tc := range []struct {
		class     string
		want, not []string
	}{
		{classInternal,
			[]string{"xbin_call", "mcp:comm:get_thread", "mcp:hr:salaries", "file_write", "js_eval", "subagent_spawn", "schedule", "threads_list", "skills_list"},
			[]string{"web_search", "web_fetch"}},
		{classWeb,
			[]string{"web_search", "web_fetch", "file_write", "js_eval", "subagent_spawn", "schedule", "threads_list", "skills_list"},
			[]string{"xbin_call", "mcp:comm:get_thread"}},
		{classCoding,
			[]string{"web_search", "web_fetch", "file_write", "subagent_spawn", "skills_list"},
			[]string{"xbin_call", "mcp:comm:get_thread", "js_eval", "schedule", "unschedule", "threads_list"}},
		{"notes",
			[]string{"file_write", "file_read", "skills_list"},
			[]string{"web_search", "xbin_call", "mcp:comm:get_thread", "js_eval", "subagent_spawn", "schedule", "threads_list"}},
		{"comm",
			[]string{"xbin_call", "mcp:comm:get_thread"},
			[]string{"mcp:hr:salaries", "web_search", "file_write", "js_eval", "subagent_spawn", "schedule", "threads_list", "skills_list"}},
	} {
		got := specNames(Config{Class: tc.class, Subagents: true}, 0, testMCP)
		for _, n := range append(append([]string(nil), core...), tc.want...) {
			if !got[n] {
				t.Errorf("class %s: %s missing", tc.class, n)
			}
		}
		for _, n := range tc.not {
			if got[n] {
				t.Errorf("class %s offers %s", tc.class, n)
			}
		}
	}

	db := newTestDB(t)
	ag := &Agent{db: db}
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	ctx := context.Background()
	for class, names := range map[string][]string{
		"notes":  {"js_eval", "schedule", "xbin_call", "web_fetch", "threads_list"},
		"comm":   {"mcp:hr:salaries", "file_write", "skill_view"},
		"coding": {"js_eval", "schedule", "xbin_call"},
	} {
		for _, n := range names {
			_, err := ag.runTool(ctx, run, Config{Class: class}, n, map[string]any{"code": "1", "path": "/api/x", "url": "https://example.com"})
			if err == nil || !strings.Contains(err.Error(), "not available") {
				t.Errorf("class %s ran %s (err=%v)", class, n, err)
			}
		}
	}
	// a schedule the agent makes inherits its class and the lane it started in
	ag.noGateway = true
	out, err := ag.runTool(ctx, run, Config{Class: classInternal, Toolset: "web"}, "schedule", map[string]any{"cron": "@every 1h", "goal": "look"})
	var sid int64
	if _, e := fmt.Sscanf(out, "scheduled #%d", &sid); err != nil || e != nil {
		t.Fatalf("schedule: %q %v", out, err)
	}
	if s, _ := db.getSchedule(sid); s.Class != classInternal || s.Toolset != "web" || s.class().has(tsInternal) {
		t.Fatalf("the agent's schedule: %+v", s)
	}
	// the subagent tools are dispatched by the engine: refused there too
	if err := classAllows(classOf(Config{Class: "notes"}), "subagent_spawn"); err == nil {
		t.Error("a class without subagents may spawn")
	}
	if err := classAllows(classOf(Config{Class: "comm"}), "mcp:comm:get_thread"); err != nil {
		t.Errorf("the class's own MCP server: %v", err)
	}
}

// The firewall is the class's: the web class never reaches inside, internal
// never out, and an edit to a class never carries a conversation across it.
func TestClassFirewall(t *testing.T) {
	for _, cfg := range []Config{{Class: classWeb}, {Toolset: "web"}, {Class: classCoding}} {
		got := specNames(cfg, 0, testMCP)
		if got["xbin_call"] || got["mcp:comm:get_thread"] {
			t.Errorf("%+v reaches inside", cfg)
		}
		if cfg.toolset() != "web" {
			t.Errorf("%+v lane %s", cfg, cfg.toolset())
		}
	}
	for _, cfg := range []Config{{}, {Class: classInternal}, {Toolset: "private"}, {Class: "gone"}} {
		got := specNames(cfg, 0, testMCP)
		if got["web_search"] || got["web_fetch"] {
			t.Errorf("%+v reaches outside", cfg)
		}
		if cfg.toolset() != "private" {
			t.Errorf("%+v lane %s", cfg, cfg.toolset())
		}
	}

	// the web class, edited to hold internal reach too (confirmed mixed):
	// a conversation that started reaching outside never gains it
	useClasses(t,
		agentClass{ID: classWeb, Name: "Web", Toolsets: []string{tsWeb, tsInternal, tsFiles}, MCP: classSet{All: true}},
		agentClass{ID: classInternal, Name: "Internal", Toolsets: []string{tsWeb, tsFiles}},
		agentClass{ID: "bridge", Name: "Bridge", Toolsets: []string{tsInternal, tsWeb}, MCP: classSet{All: true}})
	if got := specNames(Config{Class: classWeb, Toolset: "web"}, 0, testMCP); got["xbin_call"] || got["mcp:comm:get_thread"] || !got["web_search"] {
		t.Errorf("a web conversation after its class gained internal reach: %v", got)
	}
	// internal, edited to reach outside instead: its old conversations don't
	if got := specNames(Config{Class: classInternal, Toolset: "private"}, 0, testMCP); got["web_search"] || got["xbin_call"] {
		t.Errorf("a private conversation after its class went outward: %v", got)
	}
	// a confirmed mixed class started as one keeps both
	if got := specNames(Config{Class: "bridge", Toolset: "private"}, 0, testMCP); !got["web_search"] || !got["xbin_call"] {
		t.Errorf("a mixed conversation: %v", got)
	}
	// what a clamped conversation starts is held to the lane it started in
	child := childConfig(Config{Class: classInternal, Toolset: "web"}, "")
	if child.Class != classInternal || child.Toolset != "web" {
		t.Errorf("a child of a web conversation: %+v", child)
	}
	if got := specNames(child, 1, testMCP); got["xbin_call"] {
		t.Error("a child of a web conversation reaches inside")
	}
}

// toolset in the APIs names a built-in; class is accepted beside it; a stored
// config from before classes resolves from its lane; the view says which
// class a conversation has; PATCH can't change it.
func TestClassLegacyToolset(t *testing.T) {
	ag, mux := accessFixture(t)
	start := func(target string, body map[string]any, want int) int64 {
		t.Helper()
		w := callAs(t, mux, asAlice, "POST", target, body)
		if w.Code != want {
			t.Fatalf("%s %v: %d %s", target, body, w.Code, w.Body)
		}
		var r struct{ ID int64 }
		_ = json.Unmarshal(w.Body.Bytes(), &r)
		return r.ID
	}
	for _, tc := range []struct {
		target      string
		body        map[string]any
		class, lane string
	}{
		{"/ask", map[string]any{"text": "hi", "hold": true, "toolset": "web"}, classWeb, "web"},
		{"/ask", map[string]any{"text": "hi", "hold": true}, classInternal, "private"},
		{"/ask", map[string]any{"text": "hi", "hold": true, "class": "coding"}, classCoding, "web"},
		{"/ask", map[string]any{"text": "hi", "hold": true, "class": "coding", "toolset": "private"}, classCoding, "web"},
		{"/runs", map[string]any{"goal": "go", "toolset": "web"}, classWeb, "web"},
		{"/runs", map[string]any{"goal": "go", "class": "internal"}, classInternal, "private"},
	} {
		id := start(tc.target, tc.body, 200)
		cfg, _ := ag.db.runConfig(id)
		if cfg.Class != tc.class || cfg.Toolset != tc.lane || cfg.toolset() != tc.lane {
			t.Errorf("%s %v: class %q toolset %q lane %s", tc.target, tc.body, cfg.Class, cfg.Toolset, cfg.toolset())
		}
	}
	start("/ask", map[string]any{"text": "hi", "class": "nope"}, 400)
	start("/runs", map[string]any{"goal": "go", "class": "nope"}, 400)

	for raw, want := range map[string]string{`{"toolset":"web"}`: classWeb, `{}`: classInternal, `{"toolset":"private"}`: classInternal,
		`{"class":"gone","toolset":"web"}`: classWeb, `{"class":"coding","toolset":"web"}`: classCoding} {
		if got := classOf(parseConfig(raw)).ID; got != want {
			t.Errorf("stored %s: class %s, want %s", raw, got, want)
		}
	}

	id := start("/ask", map[string]any{"text": "hi", "hold": true, "toolset": "web"}, 200)
	var v struct {
		Class struct {
			ID    string
			Lane  string
			Mixed bool
		}
	}
	w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", id), nil)
	if json.Unmarshal(w.Body.Bytes(), &v) != nil || v.Class.ID != classWeb || v.Class.Lane != "web" || v.Class.Mixed {
		t.Fatalf("the view's class: %+v %s", v, w.Body)
	}
	if w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", id), map[string]any{"class": "internal"}); w.Code != 400 {
		t.Fatalf("PATCH class: %d", w.Code)
	}
	if w := callAs(t, mux, asAlice, "PATCH", fmt.Sprintf("/runs/%d", id), map[string]any{"class": "web", "title": "same"}); w.Code != 200 {
		t.Fatalf("PATCH the same class: %d %s", w.Code, w.Body)
	}
}

// Classes are the managers' to define; a mixed one takes a confirmation; a
// managers-only one is theirs to start; a deleted built-in comes back.
func TestClassesRoutes(t *testing.T) {
	ag, mux := accessFixture(t)
	t.Cleanup(func() { classStore.Store(nil) })
	put := func(c caller, body map[string]any) (int, map[string]any) {
		t.Helper()
		w := callAs(t, mux, c, "PUT", "/classes", body)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return w.Code, out
	}
	type listed struct {
		Classes []struct {
			ID     string
			Mixed  bool
			Who    string
			Stored bool
		}
		Default string
	}
	get := func(c caller) (l listed) {
		t.Helper()
		w := callAs(t, mux, c, "GET", "/classes", nil)
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &l) != nil {
			t.Fatalf("GET /classes: %d %s", w.Code, w.Body)
		}
		return l
	}
	ids := func(l listed) string {
		var s []string
		for _, c := range l.Classes {
			s = append(s, c.ID)
		}
		return strings.Join(s, ",")
	}
	if l := get(asAlice); ids(l) != "internal,web,coding" || l.Default != classInternal {
		t.Fatalf("the built-ins: %+v", l)
	}

	bridge := map[string]any{"id": "bridge", "name": "Bridge", "toolsets": []string{"internal", "web"}}
	ops := map[string]any{"id": "ops", "name": "Ops", "toolsets": []string{"internal", "files"}, "who": "managers"}
	if code, _ := put(asAlice, map[string]any{"classes": []any{ops}}); code != 403 {
		t.Fatalf("a non-manager saved classes: %d", code)
	}
	code, out := put(asMgr, map[string]any{"classes": []any{bridge, ops}})
	if code != http.StatusConflict || fmt.Sprint(out["mixed"]) != "[bridge]" {
		t.Fatalf("a mixed class without confirmation: %d %v", code, out)
	}
	if code, out := put(asMgr, map[string]any{"classes": []any{bridge, ops}, "confirmMixed": true, "default": "ops"}); code != 200 {
		t.Fatalf("confirmed: %d %v", code, out)
	}
	if l := get(asAlice); ids(l) != "internal,web,coding,bridge" || l.Default != classInternal {
		t.Fatalf("alice sees: %+v", l)
	}
	l := get(asMgr)
	if ids(l) != "internal,web,coding,bridge,ops" || l.Default != "ops" || !l.Classes[3].Mixed || l.Classes[4].Who != "managers" {
		t.Fatalf("a manager sees: %+v", l)
	}
	if l.Classes[0].Stored || !l.Classes[3].Stored || !l.Classes[4].Stored {
		t.Fatalf("stored: only the saved classes, not a built-in left at its default: %+v", l)
	}
	// an edited built-in keeps its place: the built-ins first, in their order
	web := map[string]any{"id": "web", "name": "Web", "toolsets": []string{"web"}}
	if code, out := put(asMgr, map[string]any{"classes": []any{bridge, web, ops}, "confirmMixed": true, "default": "ops"}); code != 200 {
		t.Fatalf("edit web: %d %v", code, out)
	}
	if l := get(asMgr); ids(l) != "internal,web,coding,bridge,ops" || !l.Classes[1].Stored || l.Classes[2].Stored {
		t.Fatalf("the order after editing a built-in: %+v", l)
	}
	if w := callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "hi", "hold": true, "class": "ops"}); w.Code != 403 {
		t.Fatalf("alice started a managers' class: %d", w.Code)
	}
	w := callAs(t, mux, asMgr, "POST", "/ask", map[string]any{"text": "hi", "hold": true})
	var r struct{ ID int64 }
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	if cfg, _ := ag.db.runConfig(r.ID); w.Code != 200 || cfg.Class != "ops" {
		t.Fatalf("a manager's default: %d class %q", w.Code, cfg.Class)
	}
	w = callAs(t, mux, asAlice, "POST", "/ask", map[string]any{"text": "hi", "hold": true, "class": "bridge"})
	_ = json.Unmarshal(w.Body.Bytes(), &r)
	var v struct{ Class struct{ Mixed bool } }
	if w := callAs(t, mux, asAlice, "GET", fmt.Sprintf("/runs/%d/view", r.ID), nil); json.Unmarshal(w.Body.Bytes(), &v) != nil || !v.Class.Mixed {
		t.Fatalf("a mixed class's conversation says so: %s", w.Body)
	}

	for name, body := range map[string]map[string]any{
		"bad id":          {"classes": []any{map[string]any{"id": "Bad Id", "toolsets": []string{"files"}}}},
		"unknown toolset": {"classes": []any{map[string]any{"id": "x", "toolsets": []string{"teleport"}}}},
		"bad egress":      {"classes": []any{map[string]any{"id": "x", "toolsets": []string{"sandbox"}, "sandboxEgress": []string{"moon"}}}},
		"twice":           {"classes": []any{map[string]any{"id": "x"}, map[string]any{"id": "x"}}},
		"unknown default": {"classes": []any{}, "default": "nope"},
		"bad who":         {"classes": []any{map[string]any{"id": "x", "who": "friends"}}},
	} {
		if code, out := put(asMgr, body); code != 400 {
			t.Errorf("%s: %d %v", name, code, out)
		}
	}
	// a sandbox class with no egress named gets none; a deleted built-in is back
	if code, out := put(asMgr, map[string]any{"classes": []any{map[string]any{"id": "box", "toolsets": []string{"sandbox"}}}}); code != 200 {
		t.Fatalf("a sandbox class: %d %v", code, out)
	}
	box := classOf(Config{Class: "box"})
	if !box.allowsEgress("none") || box.allowsEgress("internet") || !box.allowsManager("apps/fsb") || box.egress() {
		t.Fatalf("a sandbox class's defaults: %+v", box)
	}
	if l := get(asMgr); ids(l) != "internal,web,coding,box" || l.Default != classInternal {
		t.Fatalf("after deleting: %+v", l)
	}
	coding := classOf(Config{Class: classCoding})
	if !coding.allowsManager("apps/fsb") || !coding.allowsEgress("internet") || coding.allowsEgress("open") || classOf(Config{}).allowsManager("apps/fsb") {
		t.Fatalf("coding: %+v", coding)
	}
}

// Schedules, triggers and channels name a class, or a lane that names a
// built-in; what they start runs in it.
func TestClassAutomations(t *testing.T) {
	ag, mux := chanFixture(t)
	fakeOf(ag).on(nil, say("ok"))
	useClasses(t,
		agentClass{ID: "research", Name: "Research", Toolsets: []string{tsWeb, tsFiles}, System: "Cite your sources."},
		agentClass{ID: "ops", Name: "Ops", Toolsets: []string{tsInternal}, Who: "managers"},
		agentClass{ID: "bridge", Name: "Bridge", Toolsets: []string{tsInternal, tsWeb}})

	// schedules
	sched := func(c caller, body map[string]any, want int) Schedule {
		t.Helper()
		body["cron"], body["goal"] = "@every 1h", "look"
		w := callAs(t, mux, c, "POST", "/schedules", body)
		var s Schedule
		if w.Code != want || (want == 200 && json.Unmarshal(w.Body.Bytes(), &s) != nil) {
			t.Fatalf("POST /schedules %v: %d %s", body, w.Code, w.Body)
		}
		return s
	}
	if s := sched(asAlice, map[string]any{"toolset": "web"}, 200); s.Class != classWeb || s.Toolset != "web" {
		t.Fatalf("a legacy web schedule: %+v", s)
	}
	s := sched(asAlice, map[string]any{"class": "research"}, 200)
	if s.Class != "research" || s.Toolset != "web" {
		t.Fatalf("a research schedule: %+v", s)
	}
	sched(asAlice, map[string]any{"class": "ops"}, 403)
	sched(asAlice, map[string]any{"class": "nope"}, 400)
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/schedules/%d", s.ID), map[string]any{"class": "internal", "toolset": "private"}); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"class":"research"`) {
		t.Fatalf("a schedule's class is fixed: %d %s", w.Code, w.Body)
	}
	old, _ := ag.db.createSchedule(&Schedule{Name: "old", Cron: "@every 1h", Goal: "g", Toolset: "web"})
	if o, _ := ag.db.getSchedule(old); o.Class != classWeb {
		t.Fatalf("a schedule from before classes: %+v", o)
	}
	got, _ := ag.db.getSchedule(s.ID)
	ag.fireSchedule(got)
	got, _ = ag.db.getSchedule(s.ID)
	cfg, _ := ag.db.runConfig(got.LastRunID)
	if got.LastRunID == 0 || cfg.Class != "research" || cfg.toolset() != "web" || !strings.Contains(cfg.System, "Cite your sources.") {
		t.Fatalf("a fired research schedule's run: #%d %+v", got.LastRunID, cfg)
	}

	// triggers
	tr := mkTrigger(t, mux, asAlice, map[string]any{"name": "news", "source": "push", "sourceRef": "apps/webhooks", "match": "news",
		"goal": "Read {{topic}}", "class": "research", "dataClass": "public"})
	if tr.Class != "research" || tr.Toolset != "web" {
		t.Fatalf("a research trigger: %+v", tr)
	}
	if lt := mkTrigger(t, mux, asAlice, map[string]any{"name": "legacy", "source": "push", "sourceRef": "apps/webhooks", "match": "legacy",
		"goal": "g", "toolset": "web", "dataClass": "public"}); lt.Class != classWeb {
		t.Fatalf("a legacy trigger: %+v", lt)
	}
	for name, body := range map[string]map[string]any{
		"private data, outward class": {"class": "research", "dataClass": "private"},
		"public data, mixed class":    {"class": "bridge", "dataClass": "public"},
		"unknown class":               {"class": "nope"},
	} {
		body["name"], body["source"], body["sourceRef"], body["goal"] = "x-"+strings.ReplaceAll(name, " ", "-"), "push", "apps/webhooks", "g"
		if w := callAs(t, mux, asAlice, "POST", "/triggers", body); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	if w := callAs(t, mux, asAlice, "POST", "/triggers", map[string]any{"name": "ops", "source": "push", "sourceRef": "apps/webhooks",
		"goal": "g", "class": "ops"}); w.Code != 403 {
		t.Errorf("alice's trigger in a managers' class: %d", w.Code)
	}
	// an edit that echoes the lane keeps the class; switching the lane names the built-in
	if w := callAs(t, mux, asAlice, "PUT", fmt.Sprintf("/triggers/%d", tr.ID), map[string]any{"toolset": "web", "goal": "Read {{topic}} now"}); w.Code != 200 ||
		!strings.Contains(w.Body.String(), `"class":"research"`) {
		t.Fatalf("an echoed lane: %d %s", w.Code, w.Body)
	}
	_, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "news/today", "eventId": "n1", "dataClass": "public"})
	if len(res) != 1 || !res[0].Accepted {
		t.Fatalf("push: %+v", res)
	}
	if cfg, _ := ag.db.runConfig(res[0].RunID); cfg.Class != "research" || cfg.toolset() != "web" {
		t.Fatalf("a trigger's run: %+v", cfg)
	}

	// channels: everyone else's class must reach outside with no internal reach
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"webClass": "research"})
	for name, pol := range map[string]map[string]any{
		"an inward class for strangers": {"webClass": "internal"},
		"a mixed class for strangers":   {"webClass": "bridge"},
		"an unknown trusted class":      {"privateLane": true, "privateClass": "nope"},
	} {
		if w := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d", ch), map[string]any{"policy": pol}); w.Code != 400 {
			t.Errorf("%s: %d %s", name, w.Code, w.Body)
		}
	}
	callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d/peers/uma", ch), map[string]any{"state": "allowed"})
	v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello"))
	cfg, _ = ag.db.runConfig(v.RunID)
	if v.RunID == 0 || cfg.Class != "research" || cfg.Toolset != "web" || !cfg.Channel {
		t.Fatalf("a stranger's conversation: %+v %+v", v, cfg)
	}
	// the owner opens the private lane to a trusted uma, in the bridge class
	if w := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d", ch), map[string]any{"policy": map[string]any{"privateLane": true, "privateClass": "bridge", "webClass": "research"}}); w.Code != 200 {
		t.Fatalf("the private class: %d %s", w.Code, w.Body)
	}
	callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d/peers/uma", ch), map[string]any{"trusted": true})
	v2 := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello again"))
	cfg2, _ := ag.db.runConfig(v2.RunID)
	if v2.RunID == v.RunID || cfg2.Class != "bridge" || cfg2.Toolset != "private" {
		t.Fatalf("a trusted peer's conversation: run %d (was %d) %+v", v2.RunID, v.RunID, cfg2)
	}
	if v3 := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "and again")); v3.RunID != v2.RunID {
		t.Fatalf("the same session moved: %d vs %d", v3.RunID, v2.RunID)
	}
}
