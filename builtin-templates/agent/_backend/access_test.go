package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// caller is an identity as xbind injects it on a proxied call.
type caller struct {
	from, user, level, viewedBy string
}

var (
	asSystem   = caller{from: "owner"}
	asCron     = caller{from: "xbin/cron"}
	asAlice    = caller{from: "apps/agent", user: "alice", level: "read"}
	asBob      = caller{from: "apps/agent", user: "bob", level: "read"}
	asCarol    = caller{from: "apps/agent", user: "carol", level: "read"}
	asDave     = caller{from: "apps/agent", user: "dave", level: "read"}
	asMgr      = caller{from: "apps/agent", user: "mgr", level: "write"}
	asViewAs   = caller{from: "apps/agent", user: "alice", level: "read", viewedBy: "mgr"}
	asElement  = caller{from: "apps/other"}
	asNobody   = caller{}
	allCallers = map[string]caller{"system": asSystem, "cron": asCron, "alice": asAlice, "bob": asBob, "carol": asCarol,
		"dave": asDave, "mgr": asMgr, "view-as-alice": asViewAs, "element": asElement, "nobody": asNobody}
)

// call sends a request through the real route table.
func callAs(t *testing.T, mux *http.ServeMux, c caller, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	r := httptest.NewRequest(method, target, rd)
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if c.from != "" {
		r.Header.Set("X-XBin-From", c.from)
		r.Header.Set("X-XBin-Role", "admin")
	}
	if c.user != "" {
		r.Header.Set("X-XBin-User", c.user)
		r.Header.Set("X-XBin-User-Level", c.level)
	}
	if c.viewedBy != "" {
		r.Header.Set("X-XBin-Viewed-By", c.viewedBy)
	}
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func accessFixture(t *testing.T) (*Agent, *http.ServeMux) {
	t.Helper()
	t.Setenv("XBIN_COMPONENT", "apps/agent")
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	useGlobalAgent(t, ag)
	mux := http.NewServeMux()
	routes(mux)
	return ag, mux
}

// runAs starts a run owned as the stamp says, with carol a participant and
// dave a viewer when members is set.
func runAs(t *testing.T, ag *Agent, st runStamp, members bool) int64 {
	t.Helper()
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: defaultConfig(), Hold: true, Stamp: st})
	if err != nil {
		t.Fatal(err)
	}
	if members {
		for u, role := range map[string]string{"carol": roleParticipant, "dave": roleViewer} {
			if _, err := ag.db.q.Exec(`INSERT INTO run_members (run_id, user, role, created) VALUES (?, ?, ?, 1)`, r.ID, u, role); err != nil {
				t.Fatal(err)
			}
		}
		ag.acl.flush(r.ID) // what adding a member does
	}
	return r.ID
}

// The whole matrix: every principal against a private, a team-view, a
// team-participant, a legacy and an element-owned conversation, through the
// real route table — reading (view), talking (message), owning (rename via
// delete is destructive, so: members) and the tile-wide routes.
func TestAccessMatrix(t *testing.T) {
	ag, mux := accessFixture(t)
	alicePrivate := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, true)
	teamView := runAs(t, ag, runStamp{Owner: "alice", Visibility: visTeam, TeamRole: roleViewer, Origin: "chat"}, false)
	teamTalk := runAs(t, ag, runStamp{Owner: "alice", Visibility: visTeam, TeamRole: roleParticipant, Origin: "chat"}, false)
	legacy := runAs(t, ag, runStamp{}, false)
	elRun := runAs(t, ag, runStamp{Owner: "el:apps/other", Visibility: visPrivate, TeamRole: roleViewer, Origin: "api"}, false)
	kid, err := ag.db.createRun("kid", "{}", alicePrivate)
	if err != nil {
		t.Fatal(err)
	}

	// who → run → expected status for GET view (read) and POST message (talk)
	type exp struct{ view, talk int }
	matrix := map[string]map[int64]exp{
		"alice":         {alicePrivate: {200, 200}, teamView: {200, 200}, teamTalk: {200, 200}, legacy: {200, 200}, elRun: {404, 404}},
		"bob":           {alicePrivate: {404, 404}, teamView: {200, 403}, teamTalk: {200, 200}, legacy: {200, 200}, elRun: {404, 404}},
		"carol":         {alicePrivate: {200, 200}, teamView: {200, 403}},
		"dave":          {alicePrivate: {200, 403}, teamView: {200, 403}},
		"mgr":           {alicePrivate: {404, 404}, teamView: {200, 403}, legacy: {200, 200}},
		"view-as-alice": {alicePrivate: {404, 404}, teamView: {200, 403}, legacy: {200, 403}},
		"system":        {alicePrivate: {200, 200}, elRun: {200, 200}},
		"element":       {alicePrivate: {404, 404}, elRun: {200, 200}, legacy: {404, 404}},
		"cron":          {alicePrivate: {403, 403}},
		"nobody":        {alicePrivate: {403, 403}},
	}
	for name, runs := range matrix {
		c := allCallers[name]
		for id, want := range runs {
			if got := callAs(t, mux, c, "GET", fmt.Sprintf("/runs/%d/view", id), nil).Code; got != want.view {
				t.Errorf("%s views run #%d: %d, want %d", name, id, got, want.view)
			}
			w := callAs(t, mux, c, "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "hi"})
			if w.Code != want.talk {
				t.Errorf("%s writes to run #%d: %d, want %d (%s)", name, id, w.Code, want.talk, strings.TrimSpace(w.Body.String()))
			}
		}
	}
	// A subagent is exactly as visible as its root.
	if got := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/view", kid), nil).Code; got != 404 {
		t.Errorf("bob sees alice's private subagent: %d", got)
	}
	if got := callAs(t, mux, asCarol, "GET", fmt.Sprintf("/runs/%d/view", kid), nil).Code; got != 200 {
		t.Errorf("a member sees the subagent: %d", got)
	}
	// Only the owner (or a manager, on unowned runs) deletes.
	if got := callAs(t, mux, asCarol, "DELETE", fmt.Sprintf("/runs/%d", alicePrivate), nil).Code; got != 403 {
		t.Errorf("a participant may not delete: %d", got)
	}
	if got := callAs(t, mux, asBob, "DELETE", fmt.Sprintf("/runs/%d", legacy), nil).Code; got != 403 {
		t.Errorf("a non-manager may not delete an unowned run: %d", got)
	}
	if got := callAs(t, mux, asMgr, "DELETE", fmt.Sprintf("/runs/%d", legacy), nil).Code; got != 200 {
		t.Errorf("a manager owns unowned runs: %d", got)
	}
	// Tile-wide settings are the managers'.
	for _, c := range []struct {
		name string
		c    caller
		want int
	}{{"bob", asBob, 403}, {"mgr", asMgr, 200}, {"system", asSystem, 200}} {
		if got := callAs(t, mux, c.c, "GET", "/config", nil).Code; got != c.want {
			t.Errorf("%s reads config: %d, want %d", c.name, got, c.want)
		}
		if got := callAs(t, mux, c.c, "PUT", "/halt", map[string]bool{"on": false}).Code; got != c.want {
			t.Errorf("%s moves the halt: %d, want %d", c.name, got, c.want)
		}
	}
	if got := callAs(t, mux, asCron, "POST", "/ask", map[string]string{"text": "x"}).Code; got != 403 {
		t.Errorf("cron may only fire schedules: %d", got)
	}
	if got := callAs(t, mux, asBob, "GET", "/engine/hold", nil).Code; got != 403 {
		t.Errorf("the hold is the tile's own: %d", got)
	}
}

// The list shows each caller what they may see — and nothing else.
func TestListIsFiltered(t *testing.T) {
	ag, mux := accessFixture(t)
	alice := runAs(t, ag, runStamp{Owner: "alice", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	team := runAs(t, ag, runStamp{Owner: "alice", Visibility: visTeam, TeamRole: roleViewer, Origin: "chat"}, false)
	bob := runAs(t, ag, runStamp{Owner: "bob", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	ids := func(c caller) map[int64]bool {
		w := callAs(t, mux, c, "GET", "/runs?roots=1", nil)
		var runs []*Run
		_ = json.Unmarshal(w.Body.Bytes(), &runs)
		out := map[int64]bool{}
		for _, r := range runs {
			out[r.ID] = true
		}
		return out
	}
	if got := ids(asAlice); !got[alice] || !got[team] || got[bob] {
		t.Errorf("alice's list: %v", got)
	}
	if got := ids(asBob); got[alice] || !got[team] || !got[bob] {
		t.Errorf("bob's list: %v", got)
	}
	if got := ids(asMgr); got[alice] || got[bob] || !got[team] {
		t.Errorf("a manager doesn't see private conversations: %v", got)
	}
	if got := ids(asSystem); !got[alice] || !got[bob] {
		t.Errorf("the owner token sees everything: %v", got)
	}
}

// A person's new chat is theirs, private; while the agent is paused only a
// manager may ask it for work.
func TestNewChatAndHalt(t *testing.T) {
	ag, mux := accessFixture(t)
	w := callAs(t, mux, asBob, "POST", "/ask", map[string]any{"text": "hello", "hold": true})
	var run Run
	_ = json.Unmarshal(w.Body.Bytes(), &run)
	if w.Code != 200 || run.Owner != "bob" || run.Visibility != visPrivate || run.Origin != "chat" {
		t.Fatalf("bob's quick ask: %d %+v", w.Code, run)
	}
	_ = ag.db.putSetting("halt", "1")
	if got := callAs(t, mux, asBob, "POST", "/ask", map[string]any{"text": "go"}).Code; got != 423 {
		t.Errorf("a non-manager while halted: %d, want 423", got)
	}
	if ag.db.getSetting("halt") != "1" {
		t.Fatal("…and the brake stays on")
	}
	if got := callAs(t, mux, asMgr, "POST", "/ask", map[string]any{"text": "go", "hold": true}).Code; got != 200 {
		t.Errorf("a manager's hold ask while halted: %d", got)
	}
}

// Schedules are their owner's; managers oversee (see, switch off, delete)
// but can't read or change someone else's private one; the agent's
// unschedule tool is limited to its own conversation's.
func TestScheduleAccess(t *testing.T) {
	ag, mux := accessFixture(t)
	mk := func(owner, vis string) int64 {
		id, err := ag.db.createSchedule(&Schedule{Name: "s", Cron: "@daily", Goal: "secret goal", Owner: owner, Visibility: vis})
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	alices := mk("alice", visPrivate)
	legacy := mk("", visTeam)
	list := func(c caller) map[int64]*Schedule {
		var out []*Schedule
		_ = json.Unmarshal(callAs(t, mux, c, "GET", "/schedules", nil).Body.Bytes(), &out)
		m := map[int64]*Schedule{}
		for _, s := range out {
			m[s.ID] = s
		}
		return m
	}
	if l := list(asBob); l[alices] != nil || l[legacy] == nil {
		t.Errorf("bob's schedules: %v", l)
	}
	if l := list(asMgr); l[alices] == nil || l[alices].Goal != "" {
		t.Errorf("a manager sees alice's automation exists, not what it does: %+v", l[alices])
	}
	if got := callAs(t, mux, asBob, "POST", fmt.Sprintf("/schedules/%d/trigger", alices), nil).Code; got != 404 {
		t.Errorf("bob runs alice's schedule: %d", got)
	}
	if got := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/schedules/%d", alices), map[string]any{"goal": "mine now", "enabled": false}).Code; got != 200 {
		t.Errorf("a manager switches it off: %d", got)
	}
	if s, _ := ag.db.getSchedule(alices); s.Goal != "secret goal" || s.Enabled || s.Owner != "alice" {
		t.Errorf("…but only that: %+v", s)
	}
	// the tool
	bobRun := runAs(t, ag, runStamp{Owner: "bob", Visibility: visPrivate, TeamRole: roleViewer, Origin: "chat"}, false)
	r, _ := ag.db.getRun(bobRun)
	if _, err := ag.runTool(t.Context(), r, defaultConfig(), "unschedule", map[string]any{"id": float64(alices)}); err == nil {
		t.Error("bob's agent removed alice's schedule")
	}
}

// Viewers never see the static MCP servers' headers (they can carry tokens).
func TestViewRedactsMCPHeaders(t *testing.T) {
	ag, mux := accessFixture(t)
	cfg := defaultConfig()
	cfg.MCP = []MCPServer{{Name: "x", URL: "https://mcp", Headers: map[string]string{"Authorization": "Bearer s3cret"}}}
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: cfg, Hold: true, Stamp: runStamp{Owner: "alice", Visibility: visTeam, TeamRole: roleViewer}})
	if err != nil {
		t.Fatal(err)
	}
	w := callAs(t, mux, asBob, "GET", fmt.Sprintf("/runs/%d/view", r.ID), nil)
	if w.Code != 200 || strings.Contains(w.Body.String(), "s3cret") {
		t.Fatalf("view: %d, leaks the header: %v", w.Code, strings.Contains(w.Body.String(), "s3cret"))
	}
	if !strings.Contains(w.Body.String(), `"access":"viewer"`) {
		t.Error("the view says what the caller may do")
	}
}
