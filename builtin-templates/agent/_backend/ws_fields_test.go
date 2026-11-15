package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// What the page reads in fields beside the words the model reads
// (ws_context.go; API.md §Workspace mode).

// A message's context is the person's own attachment: the tile's name and
// whether it is connected are xbind's, the model reads it as a labelled
// line before their words, and the stored message stays what they wrote.
func TestWsMessageContext(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	send := func(text string, ctx map[string]any) {
		t.Helper()
		f.on(forRun(s.Run, lastUser(text)), say("ok: "+text)).once()
		fx.json(t, fx.call(t, wsAlice, "POST", fmt.Sprintf("/runs/%d/message", s.Run), map[string]any{"text": text, "context": ctx}), 200, nil)
		fx.idle(t, s)
	}
	send("why is this slow", map[string]any{"tile": "apps/orders", "name": "Payroll"}) // a name the page made up is not taken
	send("what is on here", map[string]any{"screen": "Sales\ndesk [x]"})
	send("and this one", map[string]any{"tile": "apps/nowhere"})
	send("no context", nil)
	fx.x.mu.Lock()
	fx.x.paused = true // xbind can't be asked: the context is kept as given, and nothing is said about the connection
	fx.x.mu.Unlock()
	send("while paused", map[string]any{"tile": "apps/orders"})

	rows, err := fx.ag.db.q.Query(`SELECT content, meta FROM messages WHERE run_id=? AND role='user' ORDER BY seq`, s.Run)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var contents []string
	var abouts []*msgAbout
	for rows.Next() {
		var c, m string
		_ = rows.Scan(&c, &m)
		var meta msgMeta
		_ = json.Unmarshal([]byte(m), &meta)
		contents, abouts = append(contents, c), append(abouts, meta.Context)
	}
	if strings.Join(contents, "|") != "why is this slow|what is on here|and this one|no context|while paused" {
		t.Fatalf("the stored messages: %q", contents)
	}
	if a := abouts[0]; a == nil || a.Tile != "apps/orders" || a.Name != "Orders" || a.Connected == nil || !*a.Connected {
		t.Fatalf("the tile's context: %+v", a)
	}
	if a := abouts[1]; a == nil || a.Screen != "Sales desk [x]" || a.Tile != "" {
		t.Fatalf("the screen's context: %+v", a)
	}
	if a := abouts[2]; a == nil || a.Tile != "apps/nowhere" || a.Name != "" || a.Connected == nil || *a.Connected {
		t.Fatalf("an unconnected tile's context: %+v", a)
	}
	if abouts[3] != nil {
		t.Fatalf("no context: %+v", abouts[3])
	}
	if a := abouts[4]; a == nil || a.Tile != "apps/orders" || a.Name != "" || a.Connected != nil {
		t.Fatalf("a context while xbind can't be asked: %+v", a)
	}
	// what the model read
	calls := f.callsFor(s.Run)
	last := calls[len(calls)-1]
	var users []string
	for _, m := range last.Msgs {
		if m.Role == "user" {
			users = append(users, stripReminder(asText(m.Content)))
		}
	}
	want := []string{
		"[context your person attached to this message: the tile \"Orders\" (apps/orders)]\nwhy is this slow",
		"[context your person attached to this message: their screen \"Sales desk (x)\"]\nwhat is on here",
		"[context your person attached to this message: the tile at apps/nowhere — this session is not connected to it]\nand this one",
		"no context",
		"[context your person attached to this message: the tile apps/orders]\nwhile paused",
	}
	if strings.Join(users, "|") != strings.Join(want, "|") {
		t.Fatalf("the model read:\n%s", strings.Join(users, "\n"))
	}

	// outside a workspace agent's session a context is not taken
	setWsMode(t, false)
	if a := wsAboutOf(t.Context(), &Run{ID: s.Run, Origin: originWorkspace}, &msgAbout{Tile: "apps/orders"}); a != nil {
		t.Fatalf("a context outside workspace mode: %+v", a)
	}
}

// GET /ws/state answers the stopping quota from the connector's 429, the
// pause with its time, and each model with whether it is private and where
// it runs.
func TestWsStateFields(t *testing.T) {
	fx := newWsFix(t)
	state := func() map[string]any {
		t.Helper()
		var out map[string]any
		fx.json(t, fx.call(t, wsAlice, "GET", "/ws/state", nil), 200, &out)
		return out
	}
	st := state()
	if _, ok := st["quota"]; ok {
		t.Fatalf("a quota before any: %v", st["quota"])
	}
	models, _ := json.Marshal(st["models"])
	if got := string(models); !strings.Contains(got, `{"id":"big","private":false,"where":"hosted"}`) ||
		!strings.Contains(got, `{"id":"local-llama","private":true,"where":"your organisation's own server"}`) {
		t.Fatalf("the models: %s", got)
	}
	if d := st["delegation"].(map[string]any); d["paused"] != false || d["pausedAt"] != nil {
		t.Fatalf("the delegation: %v", d)
	}

	// the connector's list marks a model private or it doesn't say
	var cat []catalogModel
	if err := json.Unmarshal([]byte(`[{"id":"house/big"},{"id":"own/llama","private":true},{"id":"x/y","private":"yes"},{"id":"x/z","private":false}]`), &cat); err != nil ||
		cat[0].Private != nil || cat[1].Private == nil || !*cat[1].Private || cat[2].Private != nil || cat[3].Private == nil || *cat[3].Private {
		t.Fatalf("the catalog's private marks: %+v %v", cat, err)
	}

	resets := time.Now().Add(3 * time.Hour).UTC().Format(time.RFC3339)
	body := fmt.Sprintf(`{"error":{"message":"LLM quota reached: your daily LLM token quota of 2000000 tokens is used up (2000113 used); it resets at %s","type":"insufficient_quota","code":"xbin_llm_quota_exceeded","quota":"org:sales.tokensPerDay","resets":%q,"limit":2000000,"used":2000113}}`, resets, resets)
	wsLLMStatus(LLMRequest{}, 429, []byte(`{"error":{"code":"xbin_llm_busy","message":"busy"}}`)) // another 429 is not a quota
	if _, ok := state()["quota"]; ok {
		t.Fatal("a busy connector read as a quota")
	}
	wsLLMStatus(LLMRequest{}, 429, []byte(body))
	fx.x.mu.Lock()
	fx.x.paused = true
	fx.x.mu.Unlock()
	st = state()
	q, _ := st["quota"].(map[string]any)
	if q == nil || q["stopped"] != true || q["quota"] != "org:sales.tokensPerDay" || q["source"] != "org" || q["org"] != "sales" || q["unit"] != "tokens" || q["window"] != "day" ||
		q["limit"] != 2000000.0 || q["used"] != 2000113.0 || q["resets"] != resets || q["limitWords"] != "your organisation's 2M tokens for today" ||
		q["raise"] != "an admin of sales, or a workspace admin" {
		t.Fatalf("the quota: %v", q)
	}
	if d := st["delegation"].(map[string]any); d["paused"] != true || d["pausedAt"] != "2026-11-15T08:00:00Z" {
		t.Fatalf("the paused delegation: %v", d)
	}
	// a call that goes through lifts it; a quota past its reset is gone by itself
	wsLLMStatus(LLMRequest{}, 200, nil)
	if _, ok := state()["quota"]; ok {
		t.Fatal("the quota outlived a call that went through")
	}
	past := time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)
	wsLLMStatus(LLMRequest{}, 429, []byte(fmt.Sprintf(`{"error":{"code":"xbin_llm_quota_exceeded","quota":"user.costPerMonth","resets":%q,"limit":5,"used":5.2}}`, past)))
	if _, ok := state()["quota"]; ok {
		t.Fatal("a quota past its reset still stops")
	}
	if q := wsQuotaOf([]byte(`{"error":{"code":"xbin_llm_quota_exceeded","quota":"workspace.costPerMonth","limit":50,"used":50.2}}`)); q == nil ||
		q.LimitWords != "the workspace's $50.00 budget for this month" || q.Raise != "a workspace admin" || q.Source != "workspace" {
		t.Fatalf("a workspace cost quota: %+v", q)
	}
}

// A request's outcome is in fields: how it was allowed and until when, by
// whom, the call's status and a line of result, and when it expires.
func TestWsRequestOutcomeFields(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	fx.x.api["POST /api/apps/invoices/send"] = func([]byte) (int, string) { return 200, `{"sent":true}` }
	f := fakeOf(fx.ag)
	send := func(n int) string {
		return fmt.Sprintf(`{"tile":"apps/invoices","method":"POST","path":"/send","body":{"id":%d},"why":"remind"}`, n)
	}
	file := func(n int) {
		t.Helper()
		f.on(forRun(s.Run, lastUser(fmt.Sprintf("send %d", n))), callTools(tc(fmt.Sprintf("k%d", n), "tile_call", send(n)))).once()
		f.on(forRun(s.Run, lastTool("tile_call")), say("Waiting for you.")).once()
		fx.say(t, s, fmt.Sprintf("send %d", n))
		fx.idle(t, s)
	}
	cards := func() map[int64]map[string]any {
		t.Helper()
		var out struct {
			Requests []map[string]any `json:"requests"`
		}
		fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/sessions/%d/cards", s.Run), nil), 200, &out)
		by := map[int64]map[string]any{}
		for _, r := range out.Requests {
			by[int64(r["id"].(float64))] = r
		}
		return by
	}
	decide := func(n int, state, grant string) {
		t.Helper()
		f.on(forRun(s.Run, lastUser("[workspace updates")), say("Noted.")).once()
		fx.x.decideFor(fx.x.requestWith(t, fmt.Sprintf(`"id":%d`, n)).ID, state, grant)
		fx.notify(t)
		fx.rest(t, s)
	}
	file(1)
	if r := cards()[1]; r["state"] != "pending" || r["grant"] != nil || r["by"] != nil || r["expiresMs"].(float64) <= float64(nowMs()) || r["resultStatus"] != nil {
		t.Fatalf("a pending request: %v", r)
	}
	decide(1, "approved", "once")
	if r := cards()[1]; r["state"] != "used" || r["grant"] != "once" || r["grantUntilMs"] != nil || r["by"] != "person" || r["via"] != "session" ||
		r["resultStatus"] != 200.0 || r["result"] != "POST /send → 200" {
		t.Fatalf("approved once: %v", r)
	}
	file(2)
	before := nowMs()
	decide(2, "approved", "hour")
	if r := cards()[2]; r["grant"] != "call" || r["grantUntilMs"].(float64) < float64(before+59*60*1000) || r["by"] != "person" || r["resultStatus"] != 200.0 {
		t.Fatalf("approved for an hour: %v", r)
	}
	if up := fx.updates(t, s); !strings.Contains(up[len(up)-1], "approved by your person, for an hour — it ran: POST /send → 200") {
		t.Fatalf("the hour's update: %q", up[len(up)-1])
	}
	// a third send is covered by the standing approval, so the denial is of another call
	fx.x.api["POST /api/apps/invoices/void"] = func([]byte) (int, string) { return 200, `{}` }
	f.on(forRun(s.Run, lastUser("void 3")), callTools(tc("k3", "tile_call", `{"tile":"apps/invoices","method":"POST","path":"/void","body":{"id":3},"why":"void"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_call")), say("Waiting for you.")).once()
	fx.say(t, s, "void 3")
	fx.idle(t, s)
	decide(3, "denied", "")
	if r := cards()[3]; r["state"] != "denied" || r["grant"] != nil || r["by"] != "person" || r["via"] != "session" || r["resultStatus"] != nil || r["result"] != nil {
		t.Fatalf("denied: %v", r)
	}
}

// A refusal is a card of its own — its kind, xbind's sentence, the tool
// call it answers — beside the words the model reads, which stay the same.
func TestWsRefusalItems(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	cards := func() map[string]wsRefused {
		t.Helper()
		var out struct {
			Refusals []wsRefused `json:"refusals"`
		}
		fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/sessions/%d/cards", s.Run), nil), 200, &out)
		by := map[string]wsRefused{}
		for _, r := range out.Refusals {
			by[r.CallID] = r
		}
		return by
	}
	f.on(forRun(s.Run, lastUser("look around")), callTools(
		tc("r1", "tile_status", `{"tile":"apps/o/finance/ledger"}`),                                                            // Finance keeps this model out
		tc("r2", "tile_call", `{"tile":"tiles/admin","method":"DELETE","path":"/components/apps/invoices","why":"delete it"}`), // the tile's rule
		tc("r3", "tile_status", `{"tile":"apps/nowhere"}`),                                                                     // no such tile
		tc("r4", "tile_status", `{"tile":"apps/orders"}`),                                                                      // fine
	)).once()
	f.on(forRun(s.Run, lastTool("tile_status")), say("Finance's ledger isn't open to this model.")).once()
	fx.say(t, s, "look around")
	fx.idle(t, s)
	by := cards()
	if len(by) != 3 {
		t.Fatalf("the refusals: %+v", by)
	}
	if r := by["r1"]; r.Kind != "policy" || r.Tool != "tile_status" || r.Tile != "apps/o/finance/ledger" || !strings.Contains(r.Reason, "Finance") || r.Created == 0 {
		t.Fatalf("an org's rule: %+v", r)
	}
	if r := by["r2"]; r.Kind != "policy" || r.Tool != "tile_call" || r.Tile != "tiles/admin" || r.Reason != "Admin doesn't let agents change it" {
		t.Fatalf("the tile's rule: %+v", r)
	}
	if r := by["r3"]; r.Kind != "not-connected" || r.Reason != "" || r.Tile != "apps/nowhere" {
		t.Fatalf("no such tile: %+v", r)
	}
	if got := fx.toolResult(t, s, "tile_call"); !strings.Contains(got, "would be refused: Admin doesn't let agents change it") {
		t.Fatalf("the words the model read: %q", got)
	}
	// a tile the person left out of the session's scope
	fx.json(t, fx.call(t, wsAlice, "PATCH", fmt.Sprintf("/ws/sessions/%d", s.Run), map[string]any{"scope": map[string]any{"orgs": []string{"sales"}, "tiles": []string{}}}), 200, nil)
	f.on(forRun(s.Run, lastUser("and the calendar")), callTools(tc("r5", "tile_status", `{"tile":"apps/calendar"}`))).once()
	f.on(forRun(s.Run, lastTool("tile_status")), say("The calendar is outside this session.")).once()
	fx.say(t, s, "and the calendar")
	fx.idle(t, s)
	if r := cards()["r5"]; r.Kind != "hidden" || r.Tile != "apps/calendar" || !strings.Contains(r.Reason, "hidden by you") {
		t.Fatalf("a tile outside the scope: %+v", r)
	}
	if got := fx.toolResult(t, s, "tile_status"); !strings.Contains(got, `no tile "apps/calendar" is connected in this session`) {
		t.Fatalf("the words the model read: %q", got)
	}

	// each of xbind's refusals has its kind, with xbind's sentence
	for _, c := range []struct {
		refusal, msg, kind, reason string
	}{
		{"hidden", "hidden by you", "hidden", "hidden by you"},
		{"tile", "Finance allows private models only", "policy", "Finance allows private models only"},
		{"not-delegated", "deleting a tile is its owner's", "cannot", "deleting a tile is its owner's"},
		{"admin-read", "admin state", "cannot", "admin state"},
		{"paused", "paused", "paused", "paused"},
		{"workspace-off", "off here", "off", "off here"},
		{"rate-limited", "slow down", "slow", "slow down"},
		{"model_taint", "holds Finance data", "taint", "holds Finance data"},
		{"ticket", "used", "", ""},
		{"", "the tile's own 422", "", ""},
	} {
		err := fakeRefusal(c.refusal, c.msg)
		if kind, reason := wsRefusalKind(err); kind != c.kind || reason != c.reason {
			t.Errorf("%s: kind %q reason %q", c.refusal, kind, reason)
		}
		if kind, _ := wsRefusalKind(wsWorded(err)); kind != c.kind { // worded for a tool, still what it was
			t.Errorf("%s, worded: kind %q", c.refusal, kind)
		}
		if wsRefusal(wsWorded(err)) != wsRefusal(err) {
			t.Errorf("%s: worded twice", c.refusal)
		}
	}
	if kind, reason := wsRefusalKind(&wsTaintError{Model: "big", Orgs: []string{"finance"}, Reason: "Finance allows private models only"}); kind != "taint" || reason != "Finance allows private models only" {
		t.Errorf("a model move's taint: %q %q", kind, reason)
	}
}

// ask_user's kind reaches the page in fields: the needs list, the
// session's item and its cards say which kind of question waits, its
// choices, and for a redirect the tile it concerns.
func TestWsAskKinds(t *testing.T) {
	fx := newWsFix(t)
	s := fx.main(t)
	f := fakeOf(fx.ag)
	ask := func(user, args string) (need map[string]any, card map[string]any) {
		t.Helper()
		f.on(forRun(s.Run, lastUser(user)), callTools(tc("a-"+user, "ask_user", args))).once()
		fx.say(t, s, user)
		waitFor(t, "the question", func() bool {
			r, err := fx.ag.db.getRun(s.Run)
			a := wsAskOf(fx.ag.db, s.Run)
			return err == nil && r.Status == statusWaiting && a != nil && a.CallID == "a-"+user
		})
		var needs struct {
			Items []map[string]any `json:"items"`
		}
		fx.json(t, fx.call(t, wsAlice, "GET", "/ws/needs", nil), 200, &needs)
		for _, it := range needs.Items {
			if it["kind"] == "question" {
				need = it
			}
		}
		var cards struct {
			Ask map[string]any `json:"ask"`
		}
		fx.json(t, fx.call(t, wsAlice, "GET", fmt.Sprintf("/ws/sessions/%d/cards", s.Run), nil), 200, &cards)
		return need, cards.Ask
	}
	need, card := ask("line 40", `{"question":"Hand this to Orders' own agent, or tell me the outcome?","kind":"redirect","tile":"apps/orders","choices":["Hand it to Orders' agent as written","Tell me the outcome instead"]}`)
	if need == nil || need["ask"] != "redirect" || need["tile"] != "apps/orders" || len(need["choices"].([]any)) != 2 || need["callId"] != "a-line 40" ||
		need["text"] != "Hand this to Orders' own agent, or tell me the outcome?" {
		t.Fatalf("the redirect in needs: %v", need)
	}
	if card == nil || card["kind"] != "redirect" || card["tile"] != "apps/orders" || card["question"] != need["text"] {
		t.Fatalf("the redirect's card: %v", card)
	}
	var list struct {
		Sessions []map[string]any `json:"sessions"`
	}
	fx.json(t, fx.call(t, wsAlice, "GET", "/ws/sessions", nil), 200, &list)
	if a, _ := list.Sessions[0]["ask"].(map[string]any); a == nil || a["kind"] != "redirect" {
		t.Fatalf("the session's item: %v", list.Sessions[0])
	}
	need, _ = ask("delete it", `{"question":"I can't delete a tile. What instead?","kind":"cant","tile":"apps/orders","choices":["Show what still uses it","Show where its menu is"]}`)
	if need["ask"] != "cant" || need["tile"] != nil {
		t.Fatalf("the can't in needs: %v", need)
	}
	need, _ = ask("fix it", `{"question":"Which one?","choices":["A","B"]}`)
	if need["ask"] != "choices" {
		t.Fatalf("choices without a kind: %v", need)
	}
	need, card = ask("hm", `{"question":"What is the customer's name?","kind":"something"}`)
	if need["ask"] != "question" || card["kind"] != "question" || card["choices"] != nil {
		t.Fatalf("an open question: %v %v", need, card)
	}
	// the spec says the kinds
	for _, spec := range f.callsFor(s.Run)[0].Tools {
		if spec.Function.Name == "ask_user" {
			raw, _ := json.Marshal(spec.Function.Parameters)
			if !strings.Contains(string(raw), `"enum":["choices","redirect","cant"]`) || !strings.Contains(string(raw), `"tile"`) {
				t.Fatalf("ask_user as offered: %s", raw)
			}
		}
	}
}
