package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// scratchProfile is a profile for a test, outside the registry: what it
// registers is taken out of the kit's tables again.
func scratchProfile(t *testing.T, id string, allow ...string) *coordProfile {
	t.Helper()
	p := &coordProfile{ID: id, Origin: "test-" + id, Allow: allow, tools: map[string]*coordTool{}}
	t.Cleanup(func() {
		for name := range p.tools {
			var rest []*coordProfile
			for _, o := range coordToolOwners[name] {
				if o != p {
					rest = append(rest, o)
				}
			}
			if len(rest) > 0 {
				coordToolOwners[name] = rest
				continue
			}
			delete(coordToolOwners, name)
			delete(coordToolNames, name)
			delete(coordEngineTools, name)
			delete(coordWrites, name)
		}
	})
	return p
}

func scratchTool(name string, write bool) coordTool {
	return coordTool{Spec: toolSpec{Type: "function", Function: funcDef{Name: name, Description: "x", Parameters: obj(nil, map[string]any{})}},
		Write: write, Run: func(context.Context, *Agent, coordCall) (string, error) { return name + " ran", nil }}
}

// A profile's tools are the names on its allowlist and nothing else:
// registering one that isn't there fails and leaves no trace, as does a
// second tool of a name, a tool with no handler or two, and a name that is
// another kind of tool in another profile. Projects' profile registers
// exactly its allowlist, in the order its tools are offered.
func TestCoordProfileToolAllowlist(t *testing.T) {
	p := scratchProfile(t, "scratch", "probe_ok", "probe_engine", "task_create", "research")
	if err := p.register(scratchTool("probe_off_list", false)); err == nil || !strings.Contains(err.Error(), "not on its allowlist") {
		t.Fatalf("a tool off the allowlist: %v", err)
	}
	if coordToolNames["probe_off_list"] || coordWrites["probe_off_list"] || len(coordToolOwners["probe_off_list"]) != 0 || p.tools["probe_off_list"] != nil {
		t.Fatal("a refused tool was recorded")
	}
	if err := p.register(scratchTool("probe_ok", true)); err != nil {
		t.Fatal(err)
	}
	if !coordToolNames["probe_ok"] || !coordWrites["probe_ok"] || !sideEffect("probe_ok", Config{}) {
		t.Fatal("a registered write isn't one to runTool and approval mode")
	}
	if err := p.register(scratchTool("probe_ok", true)); err == nil || !strings.Contains(err.Error(), "registered twice") {
		t.Fatalf("the same name twice: %v", err)
	}
	none := scratchTool("probe_engine", false)
	none.Run = nil
	if err := p.register(none); err == nil || !strings.Contains(err.Error(), "one handler") {
		t.Fatalf("a tool with no handler: %v", err)
	}
	both := scratchTool("probe_engine", false)
	both.Engine = func(*Engine, context.Context, *turnState, toolCall) {}
	if err := p.register(both); err == nil || !strings.Contains(err.Error(), "one handler") {
		t.Fatalf("a tool with two handlers: %v", err)
	}
	// a name Projects has: the same kind of tool or none
	if err := p.register(scratchTool("task_create", false)); err == nil || !strings.Contains(err.Error(), "another kind of tool in profile project") {
		t.Fatalf("task_create as a read: %v", err)
	}
	if err := p.register(scratchTool("research", false)); err == nil || !strings.Contains(err.Error(), "another kind of tool") {
		t.Fatalf("research run by runTool: %v", err)
	}
	if !coordWrites["task_create"] || !coordEngineTools["research"] || coordToolNames["research"] {
		t.Fatal("a refused registration changed what a name means")
	}
	if err := p.register(scratchTool("task_create", true)); err != nil {
		t.Fatalf("task_create as the write it is: %v", err)
	}
	if got := toolNamesIn(p.specs()); len(got) != 2 || !got["probe_ok"] || !got["task_create"] {
		t.Fatalf("the scratch profile's tools: %v", got)
	}

	// Projects: its allowlist, all of it, in coordToolSpecs' order
	var names []string
	for _, s := range coordToolSpecs() {
		names = append(names, s.Function.Name)
	}
	if strings.Join(coordProject.order, " ") != strings.Join(names, " ") || len(coordProject.Allow) != len(names) {
		t.Fatalf("the project profile registers %v; its allowlist is %v, its specs %v", coordProject.order, coordProject.Allow, names)
	}
	for _, n := range names {
		tool := coordProject.tools[n]
		if !hasStr(coordProject.Allow, n) || tool == nil || tool.Write != projCoordWrites[n] || (tool.Engine != nil) != (n == "research") {
			t.Errorf("%s: on the allowlist %v, registered %v", n, hasStr(coordProject.Allow, n), tool != nil)
		}
	}
	if got := coordProfileIDs(); strings.Join(got, " ") != "project workspace" {
		t.Fatalf("the registered profiles: %v", got)
	}
	if coordProject.stateWords(taskCI) != "awaiting CI" || coordProject.stateWords("no-such-state") != "no-such-state" || coordWorkspace.stateWords("x") != "x" {
		t.Fatal("a profile's status words")
	}
}

// The workspace profile is a slot: no class, nothing on its allowlist, no
// hook — so it can register no tool, and a run stamped with its origin
// (which no code path does) is nobody's coordinator: no tools, no wake, no
// updates, no prompt block, no knowledge store, and the project tools
// answer it Projects' own refusal.
func TestCoordWorkspaceSlotEmpty(t *testing.T) {
	fx := newCoordFix(t)
	w := coordWorkspace
	if w == nil || w.ID != "workspace" || w.Origin != originWorkspace || coordProfiles.byOrigin[originWorkspace] != w {
		t.Fatalf("the workspace profile: %+v", w)
	}
	if w.Class != "" || len(w.Deny) != 0 || len(w.Allow) != 0 || len(w.tools) != 0 || len(w.order) != 0 || w.Words != nil ||
		w.Is != nil || w.Resolve != nil || w.Pending != nil || w.Deliver != nil || w.Prompt != nil || w.Knowledge != nil {
		t.Fatalf("the workspace slot isn't empty: %+v", w)
	}
	for _, name := range []string{"workspace_map", "tile_call", "task_create", "research"} {
		if err := w.register(scratchTool(name, false)); err == nil || !strings.Contains(err.Error(), "not on its allowlist") {
			t.Errorf("the empty slot took %s: %v", name, err)
		}
	}
	if len(w.tools) != 0 {
		t.Fatal("the empty slot has tools")
	}
	if _, err := w.forcedClass(); err == nil {
		t.Fatal("the empty slot has a class to make a conversation in")
	}
	if _, err := w.resolve(fx.ag, fx.ag.db, nil, Config{}); err == nil {
		t.Fatal("the empty slot resolves a coordinator")
	}

	// a run forged with its origin, as no route makes one
	st := runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}
	chat, err := fx.ag.startRunOpts(runOpts{Title: "chat", Cfg: defaultConfig(), Hold: true, Stamp: st})
	if err != nil {
		t.Fatal(err)
	}
	st.Origin, st.SessionKey = originWorkspace, "ws:coord:alice"
	forged, err := fx.ag.startRunOpts(runOpts{Title: "forged", Cfg: defaultConfig(), Hold: true, Stamp: st})
	if err != nil {
		t.Fatal(err)
	}
	cfg, _ := fx.ag.db.runConfig(forged.ID)
	if forged.Origin != originWorkspace || coordOriginOf(forged) != w {
		t.Fatalf("the forged run: origin %q", forged.Origin)
	}
	if coordProfileOf(fx.ag.db, forged) != nil {
		t.Fatal("a run with the workspace origin is a coordinator")
	}
	ccfg, _ := fx.ag.db.runConfig(chat.ID)
	if got, want := toolNamesIn(runToolSpecs(cfg, forged, nil)), toolNamesIn(runToolSpecs(ccfg, chat, nil)); len(got) != len(want) {
		t.Fatalf("the forged run is offered %d tools, a chat %d", len(got), len(want))
	}
	if coordToolsFor(cfg, forged) != nil || wikiToolsFor(cfg, forged) != nil {
		t.Fatal("the forged run is offered a coordinator's or the knowledge store's tools")
	}
	if coordWakes(fx.ag.db, forged) {
		t.Fatal("the forged run takes a wake")
	}
	if fx.ag.coordPromptFor(forged, cfg) != "" || fx.ag.projectPromptFor(forged, cfg) != "" {
		t.Fatal("the forged run gets a coordinator's block")
	}
	if _, err := fx.ag.wikiRoleOf(forged, cfg); err != errNoWiki {
		t.Fatalf("the forged run's role in a knowledge store: %v", err)
	}
	for _, name := range []string{"task_list", "task_create"} {
		if _, err := fx.ag.runTool(context.Background(), forged, cfg, name, map[string]any{}); err == nil ||
			!strings.Contains(err.Error(), "the project tools are a project coordinator's own") {
			t.Errorf("%s from the forged run: %v", name, err)
		}
	}
	// and a project's coordinator is still Projects'
	coord := fx.coordinator(t, asAlice, fx.p.ID)
	if coordProfileOf(fx.ag.db, coord) != coordProject {
		t.Fatal("a project's coordinator isn't the project profile's")
	}
}

// No route makes a workspace coordinator: whatever a request names — an
// origin, a profile, a session key, a class, a config — the conversation it
// makes has a route's own origin; and in the source only the slot's file
// names the origin at all.
func TestCoordWorkspaceUnreachable(t *testing.T) {
	fx := newCoordFix(t)
	hostile := func(more map[string]any) map[string]any {
		body := map[string]any{"origin": originWorkspace, "Origin": originWorkspace, "profile": "workspace", "coord": "workspace",
			"sessionKey": "ws:coord:alice", "session_key": "ws:coord:alice", "stamp": map[string]any{"origin": originWorkspace},
			"config": map[string]any{"coord": "workspace", "origin": originWorkspace, "project": map[string]any{"id": fx.p.ID, "role": "coordinator"}}}
		for k, v := range more {
			body[k] = v
		}
		return body
	}
	for _, c := range []struct {
		method, path string
		body         map[string]any
	}{
		{"POST", "/ask", hostile(map[string]any{"text": "hello", "hold": true})},
		{"POST", "/ask", hostile(map[string]any{"text": "hello", "hold": true, "class": "workspace"})},
		{"POST", "/ask", hostile(map[string]any{"text": "hello", "hold": true, "toolset": "workspace"})},
		{"POST", "/runs", hostile(map[string]any{"goal": "hello", "title": "x"})},
		{"POST", "/runs", hostile(map[string]any{"goal": "hello", "class": "workspace"})},
		{"POST", fmt.Sprintf("/projects/%d/coordinator", fx.p.ID), hostile(nil)},
		{"POST", fmt.Sprintf("/projects/%d/tasks", fx.p.ID), hostile(map[string]any{"text": "do it"})},
		{"POST", "/projects", hostile(map[string]any{"name": "W", "kind": "workspace"})},
	} {
		w := callAs(t, fx.mux, asAlice, c.method, c.path, c.body)
		t.Logf("%s %s → %d", c.method, c.path, w.Code)
	}
	var made, ws int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*), COALESCE(SUM(origin=?), 0) FROM runs`, originWorkspace).Scan(&made, &ws)
	if made < 2 || ws != 0 {
		t.Fatalf("%d run(s) made, %d with the workspace origin", made, ws)
	}
	for _, id := range scanIDs(fx.ag.db.q.Query(`SELECT id FROM runs`)) {
		run, _ := fx.ag.db.getRun(id)
		if p := coordProfileOf(fx.ag.db, run); p != nil && p != coordProject {
			t.Fatalf("run #%d (origin %q) is a %s coordinator", id, run.Origin, p.ID)
		}
		if strings.HasPrefix(run.SessionKey, "ws:") {
			t.Fatalf("run #%d took the request's session key", id)
		}
	}
	// the source: the origin is named in the slot's file and nowhere else
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) < 100 {
		t.Fatalf("the backend's sources: %d files, %v", len(files), err)
	}
	lit := regexp.MustCompile(`(?i)origin[^\n"']{0,24}["']workspace["']`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "coord_workspace.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "originWorkspace") || lit.Match(b) {
			t.Errorf("%s names the workspace coordinator's origin: only coord_workspace.go may until the profile is built", f)
		}
	}
}

// The kit carries a second profile with nothing of Projects in it: a
// profile that only fills its hooks gets its tools offered and dispatched
// with the scope its Resolve answered, the lane held by the kit whatever
// Resolve allowed, its wakes coalesced, its updates written as a message
// and marked, and its prompt block — while a run of that origin its Is
// refuses gets none of it.
func TestCoordKitSecondProfile(t *testing.T) {
	fx := newCoordFix(t)
	type scope struct{ who string }
	pending, marked := true, int64(0)
	p := scratchProfile(t, "probe", "probe_do", "probe_look")
	p.Class, p.Words = classWeb, coordStates{"busy": "working on it"}
	p.Is = func(_ *DB, run *Run) bool { return run.SessionKey == "probe:"+run.Owner }
	p.Resolve = func(_ *Agent, _ *DB, run *Run, _ Config) (any, error) { return &scope{who: run.Owner}, nil }
	p.Pending = func(*DB, *Run) bool { return pending }
	p.Deliver = func(*DB, *Run) *coordUpdate {
		return &coordUpdate{Text: "[probe updates]\nsomething moved", Meta: msgMeta{Origin: "probe", Label: "probe updates"}, Mark: func(id int64) { marked = id }}
	}
	p.Prompt = func(_ *Agent, run *Run, _ Config) string {
		return coordAssemble(coordPromptParts{Heading: "# Probe", Role: "You are " + run.Owner + "'s probe.\n", Facts: "Limits: none.\n",
			Rules: []string{"Look first.", coordRuleNeverAnswers("session")}, Index: "The notebook has no index page yet."})
	}
	coordProfiles.byID[p.ID], coordProfiles.byOrigin[p.Origin] = p, p
	t.Cleanup(func() { delete(coordProfiles.byID, p.ID); delete(coordProfiles.byOrigin, p.Origin) })
	if err := p.register(scratchTool("probe_look", false), coordTool{Spec: scratchTool("probe_do", true).Spec, Write: true,
		Run: func(_ context.Context, _ *Agent, c coordCall) (string, error) {
			return "did it for " + c.Scope.(*scope).who + " with " + fmt.Sprint(c.Args["x"]), nil
		}}); err != nil {
		t.Fatal(err)
	}
	cls, err := p.forcedClass()
	if err != nil || cls.ID != classWeb {
		t.Fatalf("the probe profile's class: %+v %v", cls, err)
	}
	mk := func(key string, cfg Config) (*Run, Config) {
		run, err := fx.ag.startRunOpts(runOpts{Title: "probe", Cfg: cfg, Hold: true, Stamp: runStamp{Owner: "alice", Visibility: visPrivate,
			TeamRole: roleViewer, Origin: p.Origin, SessionKey: key}})
		if err != nil {
			t.Fatal(err)
		}
		stored, _ := fx.ag.db.runConfig(run.ID)
		return run, stored
	}
	base := defaultConfig()
	base.Deny = []string{"note"}
	run, cfg := mk("probe:alice", p.forcedConfig(base, cls, "web_search"))
	if cfg.Class != classWeb || !cfg.denied("note") || !cfg.denied("web_search") || len(base.Deny) != 1 {
		t.Fatalf("the forced config: class %q deny %v (the base's: %v)", cfg.Class, cfg.Deny, base.Deny)
	}
	if coordProfileOf(fx.ag.db, run) != p {
		t.Fatal("the probe's run isn't its coordinator")
	}
	if got := toolNamesIn(runToolSpecs(cfg, run, nil)); !got["probe_look"] || !got["probe_do"] || got["task_create"] || got["wiki_read"] {
		t.Fatalf("the probe coordinator is offered %v", got)
	}
	ctx := context.Background()
	if out, err := fx.ag.runTool(ctx, run, cfg, "probe_do", map[string]any{"x": 7}); err != nil || out != "did it for alice with 7" {
		t.Fatalf("probe_do: %q %v", out, err)
	}
	if _, err := fx.ag.runTool(ctx, run, cfg, "task_list", nil); err == nil || !strings.Contains(err.Error(), "a project coordinator's own") {
		t.Fatalf("a project tool from the probe coordinator: %v", err)
	}
	// a run of the origin that isn't one (Is), and a subagent: nothing
	other, ocfg := mk("something-else", cfg)
	if coordProfileOf(fx.ag.db, other) != nil || coordToolsFor(ocfg, other) != nil || fx.ag.coordPromptFor(other, ocfg) != "" {
		t.Fatal("a run its profile's Is refuses is treated as a coordinator")
	}
	sub := *run
	sub.Depth, sub.ParentID = 1, run.ID
	if coordToolsFor(cfg, &sub) != nil || coordProfileOf(fx.ag.db, &sub) != nil {
		t.Fatal("a coordinator's subagent gets its tools")
	}
	// the lane is the kit's: a class with internal reach acts on nothing,
	// whatever Resolve said
	internal := defaultConfig()
	icls, _ := currentClasses().find(classInternal)
	internal.setClass(icls, false)
	irun, icfg := mk("probe:alice", internal)
	if !classOf(icfg).has(tsInternal) {
		t.Skip("the internal class has no internal reach in this build")
	}
	if _, err := fx.ag.runCoordTool(ctx, irun, icfg, "probe_do", nil); err != errCoordLane {
		t.Fatalf("a coordinator's tool in a class with internal reach: %v", err)
	}
	// wakes are coalesced by the kit; nothing pending, no wake
	coordForget(run.ID)
	if !coordWakes(fx.ag.db, run) {
		t.Fatal("a pending change didn't wake the coordinator")
	}
	if coordWakes(fx.ag.db, run) {
		t.Fatal("a second wake within the interval")
	}
	coordForget(run.ID)
	pending = false
	if coordWakes(fx.ag.db, run) || coordWakes(fx.ag.db, other) {
		t.Fatal("a wake with nothing pending, or for a run that is no coordinator's origin match")
	}
	coordForget(run.ID)
	// the updates: one message, marked with its id
	e := fx.ag.eng
	if e == nil {
		t.Fatal("the fixture has no engine to deliver with")
	}
	if err := fx.ag.db.Tx(func(tx *DB) error {
		if !e.deliverCoordUpdates(tx, &turnState{run: run, root: run.ID, cfg: cfg}) {
			t.Error("the probe's update wasn't delivered")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	msgs, _ := fx.ag.db.messages(run.ID, false)
	last := msgs[len(msgs)-1]
	if marked == 0 || last.ID != marked || last.Role != "user" || !strings.HasPrefix(asString(last.Content), "[probe updates]") ||
		!strings.Contains(string(last.Meta), `"probe updates"`) {
		t.Fatalf("the update's message: %+v (marked %d)", last, marked)
	}
	// the prompt: the parts in order, the shared rule worded by the kit
	want := "\n\n# Probe\nYou are alice's probe.\nLimits: none.\nHow you work:\n- Look first.\n" +
		"- A session waiting for a person gets your message only after that person answered: you never answer a session's question or approval.\n" +
		"The notebook has no index page yet."
	if got := fx.ag.coordPromptFor(run, cfg); got != want {
		t.Fatalf("the probe's block:\n%q\nwant:\n%q", got, want)
	}
	// and Projects' block is built of the same parts
	coord := fx.coordinator(t, asAlice, fx.p.ID)
	pc, _ := fx.ag.db.runConfig(coord.ID)
	block := fx.ag.projectPromptFor(coord, pc)
	for _, part := range []string{"\n\n# Project\nYou are alice's coordinator", "How you work:\n- Name tasks by their number", "- " + coordRuleNeverAnswers("task"),
		"- Use research for any question about the code", "\nThe wiki has no index page yet"} {
		if !strings.Contains(block, part) {
			t.Errorf("the project coordinator's block lacks %q", part)
		}
	}
	if fx.ag.coordPromptFor(coord, pc) != "" {
		t.Fatal("a project's coordinator got its block twice")
	}
}
