package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// wsTools is everything a workspace agent is offered, by name: the core
// tools every conversation has, the subagent toolset's reading half (its
// research subagents), and the profile's own. Pinned: a tool that appears
// here by accident is a new reach.
var wsTools = []string{
	"account_lookup", "admin_changes", "admin_propose", "ask_user", "docs_read", "docs_search", "finish", "memory_delete", "memory_get", "memory_set", "message_get",
	"note", "notebook_archive", "notebook_edit", "notebook_read", "notebook_search", "notebook_write", "plan", "plan_propose", "preview", "recall", "research",
	"subagent_cancel", "subagent_result", "subagent_status", "subagent_wait",
	"tile_agent_brief", "tile_agent_cancel", "tile_agent_message", "tile_agent_result", "tile_agent_start", "tile_agents",
	"tile_api_doc", "tile_call", "tile_create", "tile_logs", "tile_status", "updates", "workspace_map",
}

// In workspace mode only the workspace profile's surface exists: every
// other route of the template — every table's, the adapters', the mailbox —
// answers 404 before its guard runs; no other conversation can be made, by
// a route or by the engine; the only class is the fixed workspace class;
// and a session is offered exactly the pinned tools.
func TestWsModeIsClosed(t *testing.T) {
	fx := newWsFix(t)
	adapterRoutes(fx.raw)
	s := fx.main(t)
	table := append(append(append(append(append(append(routeTable(), sandboxRoutes()...), liveRoutes()...), probeRoutes()...), harnessRoutes()...),
		harnessRelayRoutes()...), append(harnessAPIRoutes(), signinRoutes()...)...)
	for _, more := range routeTables {
		table = append(table, more()...)
	}
	if len(table) < 150 {
		t.Fatalf("only %d routes in the template's tables", len(table))
	}
	param := regexp.MustCompile(`\{[^}]*\}`)
	open := 0
	for _, rt := range table {
		method, path, ok := strings.Cut(rt.pattern, " ")
		if !ok { // a route for every method
			method, path = "POST", rt.pattern
		}
		path = param.ReplaceAllString(strings.Replace(path, "{id}", strconv.FormatInt(s.Run, 10), 1), "1")
		r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
		r.Header.Set("X-XBin-From", "user:alice")
		r.Header.Set("X-XBin-Role", "admin")
		r.Header.Set("X-XBin-User", "alice")
		r.Header.Set("X-XBin-User-Level", "terminal")
		ctx, cancel := context.WithCancel(r.Context())
		cancel() // a stream that is let through would wait for events
		w := httptest.NewRecorder()
		fx.mux.ServeHTTP(w, r.WithContext(ctx))
		switch {
		case wsMay[rt.pattern]:
			open++
			if w.Code == http.StatusNotFound && strings.Contains(w.Body.String(), `"not found"`) {
				t.Errorf("%s is in wsMay and gated", rt.pattern)
			}
		case w.Code != http.StatusNotFound:
			t.Errorf("%s answers %d in workspace mode: %s", rt.pattern, w.Code, clip(w.Body.String(), 120))
		}
	}
	if open < 15 {
		t.Fatalf("only %d of the template's routes are a workspace agent's", open)
	}
	for _, c := range []struct{ method, path string }{
		{"POST", "/runs"}, {"POST", "/ask"}, {"GET", "/config"}, {"PUT", "/classes"}, {"GET", "/classes"}, {"GET", "/models"}, {"POST", "/mailbox"},
		{"POST", "/projects"}, {"GET", "/sandboxes"}, {"POST", "/schedules"}, {"POST", "/join"}, {"GET", "/conversations"}, {"POST", "/adapter/x/in"},
		{"PATCH", fmt.Sprintf("/runs/%d", s.Run)}, {"DELETE", fmt.Sprintf("/runs/%d", s.Run)}, {"POST", fmt.Sprintf("/runs/%d/members", s.Run)},
		{"GET", "/no/such/route"},
	} {
		if w := fx.call(t, wsAlice, c.method, c.path, map[string]any{"text": "hello", "goal": "hello"}); w.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d %s", c.method, c.path, w.Code, w.Body)
		}
	}
	// the engine makes nothing else either
	for _, origin := range []string{"chat", "api", originProject, "channel", ""} {
		if _, err := fx.ag.startRunOpts(runOpts{Title: "x", Cfg: defaultConfig(), Hold: true, Stamp: runStamp{Owner: "alice", Origin: origin}}); err != errWsOnly {
			t.Errorf("a %q conversation in workspace mode: %v", origin, err)
		}
	}
	var n int
	_ = fx.ag.db.q.QueryRow(`SELECT count(*) FROM runs WHERE origin<>?`, originWorkspace).Scan(&n)
	if n != 0 {
		t.Fatalf("%d conversations that aren't a workspace agent's", n)
	}
	// one class, whatever is stored
	oldClasses := classStore.Load()
	t.Cleanup(func() { classStore.Store(oldClasses) })
	_ = fx.ag.db.putSetting("classes", `{"classes":[{"id":"workspace","name":"W","toolsets":["internal","web","sandbox","harness"]},{"id":"internal","toolsets":["internal"]}]}`)
	loadClasses(fx.ag.db)
	st := currentClasses()
	if len(st.list) != 1 || st.list[0].ID != classWorkspace || strings.Join(st.list[0].Toolsets, " ") != tsSubagents {
		t.Fatalf("the classes in workspace mode: %+v", st.list)
	}
	run, _ := fx.ag.db.getRun(s.Run)
	cfg, _ := fx.ag.db.runConfig(s.Run)
	cls := classOf(cfg)
	if cls.ID != classWorkspace || cls.has(tsInternal) || cls.has(tsWeb) || cls.has(tsSandbox) || cls.has(tsHarness) || cls.egress() || cfg.Approve {
		t.Fatalf("the session's class: %+v", cls)
	}
	var got []string
	for name := range toolNamesIn(runToolSpecs(cfg, run, nil)) {
		got = append(got, name)
	}
	sort.Strings(got)
	if strings.Join(got, " ") != strings.Join(wsTools, " ") {
		t.Fatalf("a workspace agent's tools:\n got %v\nwant %v", got, wsTools)
	}
	for _, name := range []string{"xbin_call", "web_fetch", "bash", "subagent_spawn", "schedule", "wiki_write", "task_create"} {
		if _, err := fx.ag.runTool(context.Background(), run, cfg, name, map[string]any{}); err == nil {
			t.Errorf("%s ran for a workspace agent", name)
		}
	}
}

// Outside workspace mode nothing of it is reachable: its routes aren't
// registered (404 from the mux itself), the doorbell isn't there, the
// class doesn't exist, and the gate is the mux.
func TestWsModeOffIsUnreachable(t *testing.T) {
	ag, mux := accessFixture(t)
	if wsMode() {
		t.Fatal("a normal instance is in workspace mode")
	}
	h := wsGate(mux, mux)
	for _, rt := range wsRouteTable() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		path = regexp.MustCompile(`\{[^}]*\}`).ReplaceAllString(path, "1")
		r := httptest.NewRequest(method, path, strings.NewReader(`{"main":true}`))
		r.Header.Set("X-XBin-From", "apps/agent")
		r.Header.Set("X-XBin-Role", "admin")
		r.Header.Set("X-XBin-User", "alice")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		if w.Code != http.StatusNotFound && w.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s in a normal instance: %d %s", rt.pattern, w.Code, w.Body)
		}
	}
	r := httptest.NewRequest("POST", "/delegate/notify", strings.NewReader(`{}`))
	r.Header.Set("X-XBin-From", "xbin/mail")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != http.StatusNotFound {
		t.Fatalf("the doorbell in a normal instance: %d", w.Code)
	}
	if _, ok := currentClasses().find(classWorkspace); ok {
		t.Fatal("a normal instance has the workspace class")
	}
	if w := callAs(t, mux, asAlice, "GET", "/classes", nil); w.Code != 200 || strings.Contains(w.Body.String(), `"workspace"`) {
		t.Fatalf("GET /classes: %d %s", w.Code, w.Body)
	}
	var n int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM ws_sessions`).Scan(&n)
	if n != 0 {
		t.Fatal("a normal instance has workspace sessions")
	}
	// the mode is the environment's, and an unknown one is refused
	for v, want := range map[string]bool{"": false, wsModeValue: true} {
		t.Setenv(wsModeEnv, v)
		if err := detectWsMode(); err != nil || wsMode() != want {
			t.Errorf("%s=%q: mode %v, %v", wsModeEnv, v, wsMode(), err)
		}
	}
	t.Setenv(wsModeEnv, "org")
	if err := detectWsMode(); err == nil {
		t.Error("an unknown mode is run")
	}
	os.Unsetenv(wsModeEnv)
	wsModeOn.Store(false)
}

// Sessions: Main is made once, on first use, with its handle; it can be
// renamed, never archived or deleted; a further session copies the scope
// and model of the one in view, is named from its first request, and takes
// its handle with it when deleted; a handle xbind forgot is made again; and
// each is its person's alone.
func TestWsSessions(t *testing.T) {
	fx := newWsFix(t)
	main := fx.main(t)
	if again := fx.main(t); again.Run != main.Run || fx.x.count("OpenSession") != 1 {
		t.Fatalf("Main was made twice: %d %d", main.Run, again.Run)
	}
	run, _ := fx.ag.db.getRun(main.Run)
	if run.Title != "Main" || run.Origin != originWorkspace || run.Owner != "alice" || run.Visibility != visPrivate || main.Model != "big" || main.Handle == "" {
		t.Fatalf("Main: %+v / %+v", run, main)
	}
	if coordProfileOf(fx.ag.db, run) != coordWorkspace {
		t.Fatal("Main isn't the workspace profile's coordinator")
	}
	base := fmt.Sprintf("/ws/sessions/%d", main.Run)
	fx.json(t, fx.call(t, wsAlice, "PATCH", base, map[string]any{"archived": true}), 409, nil)
	fx.json(t, fx.call(t, wsAlice, "DELETE", base, nil), 409, nil)
	fx.json(t, fx.call(t, wsAlice, "PATCH", base, map[string]any{"name": "Everything"}), 200, nil)
	if run, _ = fx.ag.db.getRun(main.Run); run.Title != "Everything" {
		t.Fatalf("the rename: %q", run.Title)
	}
	// a scope: only Sales
	var out struct {
		Session struct {
			ID      int64
			Summary string
			Name    string
		} `json:"session"`
	}
	fx.json(t, fx.call(t, wsAlice, "PATCH", base, map[string]any{"scope": map[string]any{"orgs": []string{"sales"}, "tiles": []string{}}}), 200, &out)
	if out.Session.Summary != "2 tiles · 1 orgs connected · 3 not connected" {
		t.Fatalf("the scoped connection: %q", out.Session.Summary)
	}
	// "New session…": the scope and model of the one in view
	fx.json(t, fx.call(t, wsAlice, "POST", "/ws/sessions", map[string]any{"from": main.Run}), 200, &out)
	second, _ := fx.ag.db.wsSession(out.Session.ID)
	if second.Main || second.Scope == nil || second.Scope.Orgs[0] != "sales" || second.Model != "big" || second.Handle == main.Handle {
		t.Fatalf("the second session: %+v", second)
	}
	fakeOf(fx.ag).on(forRun(second.Run, nil), say("Looking."))
	fx.say(t, second, "Which invoices are overdue?")
	fx.idle(t, second)
	// scope back to everything (null), then archive and delete
	fx.json(t, fx.call(t, wsAlice, "PATCH", fmt.Sprintf("/ws/sessions/%d", second.Run), map[string]any{"scope": nil, "archived": true}), 200, &out)
	if cur, _ := fx.ag.db.wsSession(second.Run); cur.Scope != nil {
		t.Fatalf("the scope after null: %+v", cur.Scope)
	}
	var list struct {
		Sessions []struct {
			ID         int64
			Main       bool
			ArchivedAt int64
		} `json:"sessions"`
	}
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/sessions", nil), 200, &list)
	if len(list.Sessions) != 2 || !list.Sessions[0].Main || list.Sessions[1].ArchivedAt == 0 {
		t.Fatalf("the list: %+v", list.Sessions)
	}
	// xbind forgot Main's handle: the next call makes another, and what the conversation held stays on record
	_, _ = fx.ag.db.q.Exec(`UPDATE ws_sessions SET conn=json_set(conn, '$.tainted', json_array('sales')) WHERE run_id=?`, main.Run)
	fx.x.mu.Lock()
	delete(fx.x.handles, main.Handle)
	fx.x.mu.Unlock()
	var conn struct{ Summary string }
	fx.json(t, fx.call(t, wsAlice, "GET", base+"/connection", nil), 200, &conn)
	if cur, _ := fx.ag.db.wsSession(main.Run); cur.Handle == main.Handle || cur.Handle == "" || len(cur.Conn.Tainted) != 1 || fx.x.count("OpenSession") != 3 {
		t.Fatalf("after xbind forgot the handle: %+v", cur)
	}
	fx.json(t, fx.call(t, wsAlice, "DELETE", fmt.Sprintf("/ws/sessions/%d", second.Run), nil), 200, nil)
	if _, err := fx.ag.db.getRun(second.Run); err == nil || fx.x.count("CloseSession "+second.Handle) != 1 {
		t.Fatalf("the deleted session: run %v, xbind %v", err, fx.x.calls)
	}
	// another person: nothing of alice's
	bob := caller{from: "user:bob", user: "bob", level: "read"}
	fx.json(t, fx.call(t, bob, "PATCH", base, map[string]any{"name": "mine"}), 404, nil)
	fx.json(t, fx.call(t, bob, "GET", base+"/cards", nil), 404, nil)
	fx.json(t, fx.call(t, bob, "GET", "/ws/sessions", nil), 200, &list)
	if len(list.Sessions) != 0 {
		t.Fatalf("bob sees %d of alice's sessions", len(list.Sessions))
	}
	// in a person's partition the agent is that person's and nobody else's
	setMode(t, modeUser, "alice")
	fx.json(t, fx.call(t, bob, "GET", "/ws/sessions", nil), 403, nil)
	fx.json(t, fx.call(t, bob, "POST", "/ws/sessions", map[string]any{"main": true}), 403, nil)
	// the doorbell is xbind's
	if w := fx.call(t, bob, "POST", "/delegate/notify", map[string]any{}); w.Code != 403 {
		t.Fatalf("bob rang the doorbell: %d", w.Code)
	}
}
