package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// research: a subagent reads one tile's code through xbind — list, glob,
// grep, read — with no git, no wiki tools and nothing else, within its
// budget; its answer comes back as the tool's result, and with save_as
// becomes a notebook page stamped with the tile, the revision read and
// the tile's org.
func TestWsResearchReadsThroughXbind(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	fx.x.code["apps/invoices"] = map[string]string{
		"backend/export.go": "package main\n\n// exportPDF renders one invoice.\nfunc exportPDF(id int) error {\n\treturn render(id)\n}\n",
		"backend/main.go":   "package main\n\nfunc main() {}\n",
		"index.html":        "<h1>Invoices</h1>",
	}
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("how does the export work")), callTools(tc("r1", "research", `{"tile":"Invoices","question":"How does the PDF export work?","save_as":"how-invoices-export-works"}`))).once()
	f.on(forRun(s.Run, lastTool("research")), say("The export renders one invoice at a time.")).once()
	var child int64
	isChild := func(r LLMRequest) bool { return r.Run != s.Run && strings.Contains(systemOf(r), "research subagent") }
	f.on(func(r LLMRequest) bool {
		return isChild(r) && lastUser("How does the PDF export work?")(r) && len(r.Msgs) == 2
	}, func(r LLMRequest) (LLMReply, error) {
		child = r.Run
		return callTools(tc("q1", "ls", `{}`), tc("q2", "glob", `{"pattern":"**/*.go"}`), tc("q3", "grep", `{"pattern":"exportPDF","glob":"*.go"}`),
			tc("q4", "read", `{"path":"backend/export.go"}`), tc("q5", "read", `{"path":"../orders/main.go"}`), tc("q6", "git", `{"args":["log"]}`),
			tc("q7", "wiki_write", `{"slug":"x","body":"y","note":"z"}`), tc("q8", "bash", `{"command":"ls"}`))(r)
	}).once()
	f.on(isChild, callTools(tc("q9", "finish", `{"result":"exportPDF in backend/export.go:4 renders one invoice by calling render(id)."}`))).once()
	fx.say(t, s, "how does the export work?")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "research"); !strings.Contains(got, "exportPDF in backend/export.go:4") {
		t.Fatalf("the research's answer: %q", got)
	}
	res := func(id string) string {
		var c string
		_ = fx.ag.db.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND tool_call_id=?`, child, id).Scan(&c)
		return c
	}
	for id, want := range map[string]string{
		"q1": "backend/ (2 files)", "q2": "backend/export.go\nbackend/main.go", "q3": "backend/export.go:4: func exportPDF(id int) error {",
		"q4": "3\t// exportPDF renders one invoice.", "q5": "a path inside the tile", "q6": "git is not available to a research subagent",
		"q7": "wiki_write is not available", "q8": "bash is not available to a research subagent",
	} {
		if !strings.Contains(res(id), want) {
			t.Errorf("%s: %q lacks %q", id, res(id), want)
		}
	}
	var offered []string
	for _, c := range f.callsFor(child) {
		for n := range toolNamesIn(c.Tools) {
			if !hasStr(offered, n) {
				offered = append(offered, n)
			}
		}
	}
	if len(offered) != 6 { // note, finish, read, ls, glob, grep
		t.Fatalf("a research subagent is offered %v", offered)
	}
	store, _ := fx.ag.db.knowID(wsNotebook("alice"), false)
	p, err := fx.ag.db.wikiPage(store, "how-invoices-export-works")
	if err != nil || !strings.Contains(p.Body, "renders one invoice") || p.UpdatedBy != "research: apps/invoices" {
		t.Fatalf("the saved page: %+v %v", p, err)
	}
	st := fx.ag.db.wsStamp(store, p.Slug)
	if st.Tile != "apps/invoices" || !strings.HasPrefix(st.Rev, "files-") || len(st.Orgs) != 1 || st.Orgs[0] != "sales" {
		t.Fatalf("the page's stamp: %+v", st)
	}
	// the reader's budget: the files it may open
	rd := &wsReader{ag: fx.ag, s: s, run: 999, tile: "apps/invoices"}
	rd.use().files = wsReadFiles
	if _, err := rd.Read(context.Background(), map[string]any{"path": "index.html"}); err == nil || !strings.Contains(err.Error(), "this research's limit") {
		t.Fatalf("past the budget: %v", err)
	}
	// a tile outside the session: nothing to research
	f.on(forRun(s.Run, lastUser("and the ledger")), callTools(tc("r2", "research", `{"tile":"Ledger","question":"How are balances computed?"}`))).once()
	fx.say(t, s, "and the ledger?")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "research"); !strings.Contains(got, `no tile "Ledger" is connected in this session`) {
		t.Fatalf("research on an unconnected tile: %q", got)
	}
}

// The brief's card: the person edits a brief, and the approval already
// asked for the old words is withdrawn and asked again with theirs — on
// the same parked call; "Review and start…" answers the request to open;
// where their preferences let tile agents start without xbind asking, the
// click on the brief is the confirmation and nothing starts before it.
func TestWsBriefCard(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	brief := `{"tile":"apps/orders","goal":"Export orders as CSV","done_when":"The Export button downloads a CSV of the visible orders"}`
	f.on(forRun(s.Run, lastUser("csv export")), callTools(tc("b1", "tile_agent_brief", brief), tc("b2", "tile_agent_start", `{"brief":1,"dry_run":true}`), tc("b3", "tile_agent_start", `{"brief":1}`))).once()
	fx.say(t, s, "add a csv export to Orders")
	waitStatus(t, fx.ag.db, s.Run, statusWaiting)
	var dry string
	_ = fx.ag.db.q.QueryRow(`SELECT content FROM messages WHERE run_id=? AND tool_call_id='b2'`, s.Run).Scan(&dry)
	if !strings.Contains(dry, "To start a tile agent in apps/orders would run as your person") || !strings.Contains(dry, "must allow it first") || !strings.Contains(dry, "never to you") {
		t.Fatalf("the start's dry run: %q", dry)
	}
	first := fx.x.pending(t)
	base := fmt.Sprintf("/ws/sessions/%d", s.Run)
	var cards struct {
		Requests []wsReq
		Briefs   []wsBrief
		Agents   []json.RawMessage
	}
	fx.json(t, fx.call(t, wsAlice, "GET", base+"/cards", nil), 200, &cards)
	if len(cards.Briefs) != 1 || cards.Briefs[0].State != "asked" || len(cards.Requests) != 1 || cards.Requests[0].Ref != first.ID || cards.Requests[0].State != "pending" {
		t.Fatalf("the cards: %+v", cards)
	}
	// the person edits the brief: the old request is withdrawn, a new one filed with their words
	fx.json(t, fx.call(t, wsAlice, "PUT", base+"/briefs/1", map[string]any{"doneWhen": "The Export button downloads every order of the month as CSV"}), 200, nil)
	second := fx.x.pending(t)
	var asked wsAgentStart
	_ = json.Unmarshal(second.call.Body, &asked)
	if second.ID == first.ID || fx.x.requests[first.ID].State != "withdrawn" || !strings.Contains(asked.Prompt, "every order of the month") {
		t.Fatalf("after the edit: %s / first %s", second.call.Body, fx.x.requests[first.ID].State)
	}
	var start struct {
		Request string
		Started bool
	}
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/1/start", nil), 200, &start)
	if start.Request != second.ID || start.Started {
		t.Fatalf("Review and start: %+v", start)
	}
	fx.x.decide(second.ID, "approved")
	fx.notify(t)
	fx.idle(t, s)
	agents := fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)
	if len(agents) != 1 || !strings.Contains(fx.x.said[agents[0].Session][0], "every order of the month") {
		t.Fatalf("the tile agent was told the old words: %+v", fx.x.said)
	}
	if got := fx.toolResult(t, s, "tile_agent_start"); !strings.Contains(got, "tile agent #1 started in apps/orders") {
		t.Fatalf("the parked start after the edit: %q", got)
	}

	// their preferences let a tile agent start without xbind asking: the click on the brief confirms
	fx.x.mu.Lock()
	fx.x.startAsk = false
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("pdf export")), callTools(tc("b4", "tile_agent_brief", strings.Replace(brief, "CSV", "PDF", -1)), tc("b5", "tile_agent_start", `{"brief":2}`))).once()
	fx.say(t, s, "and a pdf export")
	waitStatus(t, fx.ag.db, s.Run, statusWaiting)
	fx.notify(t)
	if n := len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)); n != 1 {
		t.Fatalf("%d tile agents before the person pressed Start", n)
	}
	var needs struct {
		Items []map[string]any `json:"items"`
	}
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/needs", nil), 200, &needs)
	if len(needs.Items) != 1 || needs.Items[0]["local"] != true || needs.Items[0]["brief"] != float64(2) {
		t.Fatalf("needs: %+v", needs.Items)
	}
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/2/start", nil), 200, &start)
	fx.idle(t, s)
	if !start.Started || len(fx.ag.db.wsAgents(`WHERE run_id=?`, s.Run)) != 2 {
		t.Fatalf("Start on the brief: %+v", start)
	}
	if got := fx.toolResult(t, s, "tile_agent_start"); !strings.Contains(got, "tile agent #2 started in apps/orders") {
		t.Fatalf("the parked start after Start: %q", got)
	}
	// a brief the agent wrote and didn't ask for: the card's button asks
	fx.x.mu.Lock()
	fx.x.startAsk = true
	fx.x.mu.Unlock()
	f.on(forRun(s.Run, lastUser("xlsx")), callTools(tc("b6", "tile_agent_brief", strings.Replace(brief, "CSV", "XLSX", -1)))).once()
	fx.say(t, s, "and xlsx")
	fx.idle(t, s)
	fx.json(t, fx.call(t, wsAlice, "POST", base+"/briefs/3/start", nil), 200, &start)
	if third := fx.x.pending(t); start.Request != third.ID || third.Kind != "session" {
		t.Fatalf("the card's own request: %+v", start)
	}
	// the list the agent reads
	f.on(forRun(s.Run, lastUser("where are we")), callTools(tc("b7", "tile_agents", `{}`))).once()
	fx.say(t, s, "where are we?")
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_agents"); !strings.Contains(got, "Tile agent #1 in apps/orders (Claude Code, asks before acting) is working") || !strings.Contains(got, "Brief 3 for apps/orders (asked)") {
		t.Fatalf("tile_agents: %q", got)
	}
}

// The empty state is built from the person's own tiles as they stand,
// without a model call: this session's numbers in the can / can't lists, a
// failed tile as a Fix starter, blanks to fill, nothing destructive, and
// no console among them.
func TestWsEmptyState(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	var out struct {
		Heading  string
		Can      []string
		Cant     []string
		Starters []wsStarter
	}
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/empty?session=%d", s.Run), nil), 200, &out)
	if out.Heading != "Ask for an outcome" || out.Can[0] != "Look across your 4 connected tiles" || !hasStr(out.Cant, "See the 1 tiles that aren't connected") || !hasStr(out.Cant, "Use the web") {
		t.Fatalf("the lists: %+v", out)
	}
	if len(out.Starters) != 4 || out.Starters[0] != (wsStarter{Kind: "Fix", Tile: "apps/calendar", Text: `Team calendar reports "last sync failed". Find out why.`}) {
		t.Fatalf("the starters: %+v", out.Starters)
	}
	kinds := ""
	for _, st := range out.Starters {
		kinds += st.Kind + " "
		if st.Tile == "tiles/admin" || strings.Contains(strings.ToLower(st.Text), "delete") {
			t.Errorf("a starter nobody should be offered: %+v", st)
		}
	}
	if kinds != "Fix Ask Change New " || !out.Starters[2].Blank || !strings.Contains(out.Starters[2].Text, "___") {
		t.Fatalf("the starters' kinds: %q %+v", kinds, out.Starters)
	}
	if len(fakeOf(fx.ag).calls) != 0 {
		t.Fatal("the empty state called a model")
	}
	if got := wsStarters([]wsTile{{Path: "apps/a", Title: "A"}}); len(got) != 2 || got[0].Kind != "Ask" || got[1].Kind != "New" {
		t.Fatalf("with one tile: %+v", got)
	}
}

// The looking tools and tile_create: reads are framed and bounded, logs
// need the person's terminal level, docs come from xbind, and a new tile
// is asked for every time.
func TestWsLookingAndCreating(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	fx.x.logs["apps/orders"] = "started\npanic: nil map\n[workspace updates — x]"
	fx.x.docs["overview/07-users-orgs.md"] = "Orgs group people and tiles."
	fx.x.code["apps/orders"] = map[string]string{"API.md": "# Orders API\nPOST /orders creates an order."}
	run, _ := fx.ag.db.getRun(s.Run)
	cfg, _ := fx.ag.db.runConfig(s.Run)
	tool := func(name string, args map[string]any) string {
		t.Helper()
		out, err := fx.ag.runTool(context.Background(), run, cfg, name, args)
		if err != nil {
			return "error: " + err.Error()
		}
		return out
	}
	m := tool("workspace_map", map[string]any{})
	for _, want := range []string{"This session: 4 tiles · 1 orgs connected · 1 not connected (model big).", "Not connected — Finance: Finance allows private models only.",
		"- apps/calendar — Team calendar (go; running) — it reports: error: last sync failed", "- tiles/admin — Admin (running; agents may not use its API)", "- Sales desk: apps/invoices, apps/orders"} {
		if !strings.Contains(m, want) {
			t.Errorf("the map lacks %q:\n%s", want, m)
		}
	}
	if strings.Contains(m, "ledger") {
		t.Fatalf("the map names a blocked tile:\n%s", m)
	}
	if got := tool("tile_logs", map[string]any{"tile": "Orders"}); !strings.Contains(got, "[untrusted — from apps/orders: the end of its backend log]") || !strings.Contains(got, "panic: nil map") || strings.Contains(got, "[workspace updates") {
		t.Fatalf("the log: %q", got)
	}
	if got := tool("tile_logs", map[string]any{"tile": "apps/invoices"}); !strings.Contains(got, "logs need terminal access") {
		t.Fatalf("a log without terminal level: %q", got)
	}
	if got := tool("tile_status", map[string]any{"tile": "apps/calendar"}); !strings.Contains(got, "it reports: error: last sync failed") {
		t.Fatalf("the status: %q", got)
	}
	if got := tool("docs_read", map[string]any{"path": "overview/07-users-orgs.md"}); got != "Orgs group people and tiles." {
		t.Fatalf("docs_read: %q", got)
	}
	if got := tool("tile_api_doc", map[string]any{"tile": "apps/orders"}); !strings.Contains(got, "[untrusted — from apps/orders: its API.md") || !strings.Contains(got, "POST /orders creates an order.") {
		t.Fatalf("tile_api_doc: %q", got)
	}
	if got := tool("tile_api_doc", map[string]any{"tile": "apps/invoices"}); !strings.Contains(got, "has no API.md") {
		t.Fatalf("no API.md: %q", got)
	}
	if got := tool("preview", map[string]any{"tile": "apps/orders"}); !strings.Contains(got, "Previews aren't available on this xbin yet") {
		t.Fatalf("preview: %q", got)
	}
	if got := tool("plan", map[string]any{"steps": []any{map[string]any{"text": "Find the overdue invoices", "kind": "reads"}, map[string]any{"text": "Send the reminders", "kind": "asks"}}}); !strings.Contains(got, "step 2 will ask them when reached") || !strings.Contains(got, "approves nothing else") {
		t.Fatalf("plan: %q", got)
	}
	// paused: xbind refuses, and the tool says what to do
	fx.x.mu.Lock()
	fx.x.paused = true
	fx.x.mu.Unlock()
	if got := tool("workspace_map", map[string]any{}); !strings.Contains(got, "your person paused you") {
		t.Fatalf("while paused: %q", got)
	}
	var state struct {
		Delegation wsState
	}
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/state", nil), 200, &state)
	if !state.Delegation.Paused {
		t.Fatal("the state doesn't say paused")
	}
	fx.x.mu.Lock()
	fx.x.paused = false
	fx.x.mu.Unlock()

	// tile_create: the list, the dry run, the request, the tile
	f := fakeOf(fx.ag)
	f.on(forRun(s.Run, lastUser("a tile for our holidays")), callTools(tc("n1", "tile_create", `{}`), tc("n2", "tile_create", `{"template":"builtin:starter","name":"Team holidays","dry_run":true}`),
		tc("n3", "tile_create", `{"template":"builtin:starter","name":"Team holidays","why":"A place for the holiday calendar"}`))).once()
	fx.say(t, s, "make a tile for our holidays")
	waitStatus(t, fx.ag.db, s.Run, statusWaiting)
	req := fx.x.pending(t)
	if req.Kind != "template" || !strings.Contains(string(req.call.Body), `"path":"apps/team-holidays"`) {
		t.Fatalf("the create asked for: %s", req.call.Body)
	}
	fx.x.decide(req.ID, "approved")
	fx.notify(t)
	fx.idle(t, s)
	if got := fx.toolResult(t, s, "tile_create"); !strings.Contains(got, "the tile apps/team-holidays now exists") || fx.x.count("TileCreate apps/team-holidays") != 1 {
		t.Fatalf("the create: %q", got)
	}
}

// The notebook's routes are the person's: every page listed for them, a
// page a session can't read marked and still theirs to open, an edit from
// the version they saw, and deleting makes the agent forget.
func TestWsNotebookRoutes(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t) // on big: Finance isn't connected
	fx.json(t, fx.call(t, wsAlice, "PUT", "/ws/notebook/preferences", map[string]any{"title": "How Ana likes things done", "body": "Reports in bullet points.", "base": 0}), 200, nil)
	store, _ := fx.ag.db.knowID(wsNotebook("alice"), false)
	zero := int64(0)
	if _, err := fx.ag.db.wikiPut(store, wikiActor{By: "the workspace agent"}, wikiChange{Slug: "ledger-notes", Body: "The ledger closes on the 5th.", Base: &zero}); err != nil {
		t.Fatal(err)
	}
	_ = fx.ag.db.wsPutStamp(store, "ledger-notes", wsStamp{Orgs: []string{"finance"}, Summary: "When the ledger closes"})
	var list struct {
		Pages []struct {
			Slug, Summary, LockedWhy string
			Locked                   bool
			Version                  int64
		}
	}
	fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/notebook?session=%d", s.Run), nil), 200, &list)
	if len(list.Pages) != 2 {
		t.Fatalf("the pages: %+v", list.Pages)
	}
	for _, p := range list.Pages {
		if (p.Slug == "ledger-notes") != p.Locked || (p.Locked && p.LockedWhy != "Not available in this session: finance isn't connected") {
			t.Errorf("the page %s: %+v", p.Slug, p)
		}
	}
	if !strings.Contains(fx.ag.wsPromptFor(wsRunOf(t, fx.ag, s.Run), Config{}), "Reports in bullet points.") || strings.Contains(fx.ag.wsPromptFor(wsRunOf(t, fx.ag, s.Run), Config{}), "ledger") {
		t.Fatalf("the prompt's notebook block:\n%s", fx.ag.wsPromptFor(wsRunOf(t, fx.ag, s.Run), Config{}))
	}
	var page struct {
		Page struct {
			Body    string
			Version int64
		}
	}
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/notebook/ledger-notes", nil), 200, &page) // theirs to read, whatever a session is connected to
	fx.json(t, fx.call(t, wsAlice, "PUT", "/ws/notebook/preferences", map[string]any{"body": "x", "base": 7}), 409, nil)
	fx.json(t, fx.call(t, wsAlice, "PUT", "/ws/notebook/preferences", map[string]any{"body": "Reports as short paragraphs.", "base": 1}), 200, &page)
	if page.Page.Version != 2 {
		t.Fatalf("the edit: %+v", page)
	}
	fx.json(t, fx.call(t, wsAlice, "DELETE", "/ws/notebook/preferences", nil), 200, nil)
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/notebook", nil), 200, &list)
	if len(list.Pages) != 1 || strings.Contains(fx.ag.wsPromptFor(wsRunOf(t, fx.ag, s.Run), Config{}), "short paragraphs") {
		t.Fatalf("after the delete: %+v", list.Pages)
	}
	fx.json(t, fx.call(t, caller{from: "user:bob", user: "bob", level: "read"}, "GET", "/ws/notebook/ledger-notes", nil), 404, nil)
	var prefs wsPrefsData
	fx.json(t, fx.call(t, wsAlice, "PUT", "/ws/prefs", map[string]any{"model": "local-llama", "lastSession": s.Run}), 200, &prefs)
	if prefs.Model != "local-llama" || prefs.LastSession != s.Run {
		t.Fatalf("the prefs: %+v", prefs)
	}
}

func wsRunOf(t *testing.T, ag *Agent, id int64) *Run {
	t.Helper()
	r, err := ag.db.getRun(id)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// The prompt: stable bytes, under its budget, the generated map inside it,
// worded without shouting; and a session's model calls carry its handle —
// a normal conversation's carry nothing.
func TestWsPromptAndModelCalls(t *testing.T) {
	sys := wsSystem()
	if len(sys) > 16<<10 || len(wsMapText()) > 6<<10 || len(wsMapText()) < 2<<10 {
		t.Fatalf("the prompt is %d bytes (the map %d)", len(sys), len(wsMapText()))
	}
	if sys != wsSystem() || strings.Contains(sys, "Generated from") || !strings.Contains(sys, "# How xbin works\n") || !strings.Contains(sys, "- Tile agents: An agent session is") {
		t.Fatal("the prompt's map")
	}
	for _, shout := range []string{"NEVER", "ALWAYS", "MUST", "IMPORTANT", "CRITICAL", "DO NOT"} {
		if strings.Contains(wsPromptText, shout) {
			t.Errorf("the prompt shouts %s", shout)
		}
	}
	for _, c := range coordWorkspace.specs() {
		if d := c.Function.Description; len(d) < 80 || len(d) > 1100 {
			t.Errorf("%s's description is %d bytes", c.Function.Name, len(d))
		}
		if coordWorkspace.tools[c.Function.Name].Write && !strings.Contains(mustJSON(c.Function.Parameters), "dry_run") {
			t.Errorf("%s changes something and has no dry run", c.Function.Name)
		}
	}
	fx := newWsFix(t)
	s := fx.main(t)
	h := http.Header{}
	wsLLMHeader(LLMRequest{Run: s.Run}, h)
	if h.Get(wsSessionHeader) != s.Handle || s.Handle == "" {
		t.Fatalf("the handle on a model call: %q", h.Get(wsSessionHeader))
	}
	cfg, _ := fx.ag.db.runConfig(s.Run)
	if cfg.Pick != "big" || cfg.Models != (ModelTiers{General: "big", Code: "big", Memory: "big", VLM: "big"}) || cfg.feature("vision") || cfg.feature("parallelTools") {
		t.Fatalf("the session's models and features: %+v", cfg)
	}
	setWsMode(t, false)
	h = http.Header{}
	wsLLMHeader(LLMRequest{Run: s.Run}, h)
	if len(h) != 0 {
		t.Fatal("a normal instance marks its model calls")
	}
}

// The tables are additive: a database from before them gains them at the
// next open, with what it held untouched, and opening again changes nothing.
func TestWsSchemaFromBase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	d, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	id, err := d.createRun("kept", "{}", 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"ws_sessions", "ws_requests", "ws_agents", "ws_held", "ws_briefs", "ws_pages"} { // as the base commit's schema: none of them
		if _, err := d.q.Exec(`DROP TABLE ` + tbl); err != nil {
			t.Fatal(err)
		}
	}
	_ = d.sql.Close()
	for i := 0; i < 2; i++ {
		if d, err = openDB(path); err != nil {
			t.Fatal(err)
		}
		if _, err := d.q.Exec(`INSERT OR REPLACE INTO ws_sessions (run_id, owner, main) VALUES (?, 'alice', 1)`, id); err != nil {
			t.Fatal(err)
		}
		if r, err := d.getRun(id); err != nil || r.Title != "kept" {
			t.Fatalf("the run after the open: %v", err)
		}
		if _, err := d.q.Exec(`INSERT INTO ws_sessions (run_id, owner, main) VALUES (?, 'alice', 1)`, id+1); err == nil {
			t.Fatal("a person has two Main sessions")
		}
		_ = d.sql.Close()
	}
}

// wsHTTP against a stand-in for xbind on a gateway socket: the delegate
// scheme and the handle on every call, the routes and bodies it assumes,
// a ticket and a dry run as headers, xbind's refusals as the SDK's errors,
// the taint refusal, a tile's own error as an answer, and the map composed
// from the older reads when xbind has no map route.
func TestWsHTTPShapes(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "gw.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Skipf("no unix socket here: %v", err)
	}
	t.Setenv("XBIN_GATEWAY", sock)
	type seen struct {
		method, path, auth, session, ticket, dry string
		body                                     string
	}
	var got []seen
	mux := http.NewServeMux()
	reply := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		got = append(got, seen{r.Method, r.URL.RequestURI(), r.Header.Get("Authorization"), r.Header.Get(wsSessionHeader), r.Header.Get("X-XBin-Ticket"), r.Header.Get("X-XBin-Dry-Run"), string(b)})
		switch r.Method + " " + r.URL.Path {
		case "POST /api/xbin/delegate/sessions":
			reply(w, 200, map[string]any{"id": "h1", "model": "big", "scope": nil, "tainted": []string{}, "connection": map[string]any{"summary": "2 tiles · 1 orgs connected", "counts": map[string]int{"connected": 2}}})
		case "PATCH /api/xbin/delegate/sessions/h1":
			reply(w, 409, map[string]any{"code": "model_taint", "orgs": []string{"finance"}, "reason": "Finance allows private models only"})
		case "GET /api/xbin/delegate/map":
			reply(w, 404, map[string]any{"error": "not found"})
		case "GET /api/xbin/components":
			reply(w, 200, []map[string]any{{"path": "apps/mail", "title": "Mail", "runtime": "go", "hasIndex": true, "agent": "read"}, {"path": "lib/x"}})
		case "GET /api/xbin/tile-report":
			reply(w, 200, map[string]any{"statuses": map[string]any{"apps/mail": map[string]string{"level": "error", "message": "IMAP down"}}})
		case "POST /api/apps/mail/send":
			switch {
			case r.Header.Get("X-XBin-Dry-Run") == "1":
				reply(w, 200, map[string]any{"dryRun": true, "allowed": true, "class": "tile-write", "sideEffect": true, "tile": "apps/mail", "level": "write", "needsApproval": true})
			case r.Header.Get("X-XBin-Ticket") == "":
				reply(w, 403, map[string]any{"error": "needs approval", "refusal": "needs-approval"})
			default:
				reply(w, 422, map[string]any{"error": "no recipient"}) // the tile's own answer
			}
		case "POST /api/xbin/delegate/requests":
			reply(w, 200, map[string]any{"id": "r1", "kind": "call", "state": "pending", "tile": "apps/mail", "expires": "2026-11-16T10:00:00Z"})
		case "POST /api/xbin/term/sessions":
			reply(w, 200, map[string]any{"id": "s1", "cwd": "apps/mail", "kind": "agent", "provider": "claude", "mode": "default", "status": "starting", "origin": "agent"})
		case "POST /api/xbin/term/sessions/s1/queue":
			reply(w, 200, map[string]any{"outcome": "prompted"})
		case "GET /api/xbin/term/sessions/s1":
			reply(w, 200, map[string]any{"session": map[string]any{"id": "s1", "cwd": "apps/mail", "kind": "agent", "provider": "claude", "status": "waiting_permission", "origin": "agent"},
				"permissions": []map[string]any{{"pid": "p1", "toolCall": map[string]string{"title": "go test ./..."}}}})
		case "GET /api/xbin/term/sessions/s1/events":
			reply(w, 200, map[string]any{"events": []map[string]any{
				{"type": "message.delta", "data": map[string]string{"role": "user", "text": "the brief"}},
				{"type": "tool.call", "data": map[string]any{"id": "t1", "title": "go test ./...", "kind": "execute", "status": "completed", "exitCode": 0}},
				{"type": "files.changed", "data": map[string]any{"turn": 1, "changes": []map[string]any{{"path": "send.go", "add": 4, "del": 1}}, "patch": map[string]string{"text": "+secret code"}}},
				{"type": "message.delta", "data": map[string]string{"role": "agent", "text": "Fixed the sender."}},
				{"type": "turn.end", "data": map[string]any{"turn": 1, "stopReason": "end_turn"}}}})
		case "POST /api/xbin/preview":
			reply(w, 404, map[string]any{"error": "not found"})
		case "GET /api/xbin/logs":
			reply(w, 403, map[string]any{"error": "your person paused their agent", "refusal": "paused"})
		default:
			reply(w, 404, map[string]any{"error": "not found"})
		}
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })

	ctx := context.Background()
	var h wsHTTP
	hd, err := h.OpenSession(ctx, nil, "big")
	if err != nil || hd.ID != "h1" || hd.Connection.Counts.Connected != 2 || got[0].body != `{"model":"big","scope":null}` || !strings.HasPrefix(got[0].auth, "XBin-Delegate") {
		t.Fatalf("OpenSession: %+v %v / %+v", hd, err, got[0])
	}
	var taint *wsTaintError
	if _, err := h.UpdateSession(ctx, "h1", wsHandleChange{Model: "big"}); !errors.As(err, &taint) || taint.Orgs[0] != "finance" {
		t.Fatalf("UpdateSession: %v", err)
	}
	m, err := h.Map(ctx, "h1")
	if err != nil || len(m.Tiles) != 1 || m.Tiles[0].Path != "apps/mail" || m.Tiles[0].Agent != "read" || m.Tiles[0].Report != "error: IMAP down" {
		t.Fatalf("Map without xbind's map route: %+v %v", m, err)
	}
	c := wsCall{Method: "POST", Path: "/api/apps/mail/send", Body: []byte(`{"to":"ada"}`), ContentType: "application/json"}
	dc := c
	dc.DryRun = true
	if a, err := h.Call(ctx, "h1", dc); err != nil || a.Dry == nil || !a.Dry.NeedsApproval || a.Dry.Tile != "apps/mail" {
		t.Fatalf("the dry run: %+v %v", a, err)
	}
	if _, err := h.Call(ctx, "h1", c); !errors.Is(err, xbin.ErrNeedsApproval) {
		t.Fatalf("without a ticket: %v", err)
	}
	req, err := h.Ask(ctx, "h1", c, "send it")
	if err != nil || req.ID != "r1" || req.Expires == 0 {
		t.Fatalf("Ask: %+v %v", req, err)
	}
	asked := got[len(got)-1]
	if asked.session != "h1" || !strings.Contains(asked.body, `"path":"/api/apps/mail/send"`) || !strings.Contains(asked.body, `"bodyBase64":"eyJ0byI6ImFkYSJ9"`) || !strings.Contains(asked.body, `"summary":"send it"`) {
		t.Fatalf("the request as filed: %+v", asked)
	}
	tk := c
	tk.Ticket = "r1"
	if a, err := h.Call(ctx, "h1", tk); err != nil || a.Status != 422 || !strings.Contains(string(a.Body), "no recipient") || got[len(got)-1].ticket != "r1" {
		t.Fatalf("the ticketed call: %+v %v", a, err)
	}
	ag, _, err := h.AgentStart(ctx, "h1", wsAgentStart{Tile: "apps/mail", Prompt: "the brief", Name: "fix"}, "r1", false)
	if err != nil || ag.ID != "s1" || !ag.Mine || ag.Status != "starting" {
		t.Fatalf("AgentStart: %+v %v", ag, err)
	}
	if start, say := got[len(got)-2], got[len(got)-1]; !strings.Contains(start.body, `"cwd":"apps/mail"`) || strings.Contains(start.body, "provider") || strings.Contains(start.body, "mode") ||
		say.path != "/api/xbin/term/sessions/s1/queue" || say.body != `{"text":"the brief"}` {
		t.Fatalf("the start and its brief: %+v / %+v", start, say)
	}
	if a, err := h.Agent(ctx, "h1", "s1"); err != nil || a.Status != "running" || a.Pending != 1 || a.Waiting != "permission: go test ./..." {
		t.Fatalf("Agent: %+v %v", a, err)
	}
	if a, err := h.Agent(ctx, "h1", "gone"); err != nil || a.Status != "ended" {
		t.Fatalf("a session that left the directory: %+v %v", a, err)
	}
	rep, err := h.AgentReport(ctx, "h1", "s1")
	if err != nil || rep.Answer != "Fixed the sender." || len(rep.Files) != 1 || rep.Files[0] != (wsFileStat{Path: "send.go", Add: 4, Del: 1}) || rep.Verified[0] != "go test ./... → exit 0" ||
		strings.Contains(mustJSON(rep), "secret code") {
		t.Fatalf("AgentReport: %+v %v", rep, err)
	}
	if _, err := h.Preview(ctx, "h1", wsPreviewAsk{Tile: "apps/mail"}); !errors.Is(err, errWsUnavailable) {
		t.Fatalf("Preview: %v", err)
	}
	if _, err := h.Logs(ctx, "h1", "apps/mail", 10); !errors.Is(err, xbin.ErrAgentPaused) {
		t.Fatalf("a refusal's code: %v", err)
	}
	for _, s := range got {
		if !strings.HasPrefix(s.auth, "XBin-Delegate") {
			t.Fatalf("%s %s went out as the component, not the person", s.method, s.path)
		}
	}
}
