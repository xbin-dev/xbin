package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSessionKeys(t *testing.T) {
	msg := func(typ, conv, sender, thread, id string, assistant bool) *adapterMsg {
		m := &adapterMsg{MessageID: id, Thread: thread, AssistantThread: assistant}
		m.Conversation.Type, m.Conversation.ID, m.Sender.ID = typ, conv, sender
		return m
	}
	var def, main, perUser, parent channelPolicy
	main.DM.Scope = "main"
	perUser.Groups.Scope = "per-user"
	parent.Groups.Threads = "parent"
	for _, tc := range []struct {
		name       string
		p          channelPolicy
		m          *adapterMsg
		key, reply string
	}{
		{"dm", def, msg("dm", "D1", "U1", "", "m1", false), "chan:7:dm:U1", ""},
		{"dm thread follows the dm", def, msg("dm", "D1", "U1", "t1", "m2", false), "chan:7:dm:U1", "t1"},
		{"dm main", main, msg("dm", "D1", "U1", "", "m1", false), "chan:7:main", ""},
		{"assistant thread", def, msg("dm", "D1", "U1", "t9", "m3", true), "chan:7:dm:U1:thread:t9", "t9"},
		{"assistant thread under main", main, msg("dm", "D1", "U1", "t9", "m3", true), "chan:7:dm:U1:thread:t9", "t9"},
		{"a mention starts a thread", def, msg("channel", "C1", "U1", "", "m4", false), "chan:7:group:C1:thread:m4", "m4"},
		{"a reply in it", def, msg("channel", "C1", "U2", "m4", "m5", false), "chan:7:group:C1:thread:m4", "m4"},
		{"per user", perUser, msg("group", "G1", "U1", "", "m6", false), "chan:7:group:G1:user:U1:thread:m6", "m6"},
		{"threads parent", parent, msg("channel", "C1", "U1", "m4", "m7", false), "chan:7:group:C1", "m4"},
		{"a forged id stays one part", def, msg("dm", "D1", "U1:main", "", "m8", false), "chan:7:dm:U1%3Amain", ""},
	} {
		key, addr := sessionKey(7, tc.p, tc.m)
		if key != tc.key || addr.Thread != tc.reply {
			t.Errorf("%s: %s (reply in %q), want %s (%q)", tc.name, key, addr.Thread, tc.key, tc.reply)
		}
	}
}

// --- fixture -------------------------------------------------------------------

func chanFixture(t *testing.T) (*Agent, *http.ServeMux) {
	t.Helper()
	ag, mux := accessFixture(t)
	adapterRoutes(mux)
	return ag, mux
}

// adapterCall is a call from a bound adapter tile: its path, the channel role.
func adapterCall(t *testing.T, mux *http.ServeMux, from, method, target string, body any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, target, bytes.NewReader(b))
	r.Header.Set("X-XBin-From", from)
	r.Header.Set("X-XBin-Role", "channel")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
}

func helloAs(t *testing.T, mux *http.ServeMux, from, account string) int64 {
	t.Helper()
	w := adapterCall(t, mux, from, "POST", "/adapter/hello", map[string]any{"protocol": 1, "platform": "slack",
		"account": map[string]string{"id": account, "name": "Acme"}, "bot": map[string]string{"id": "B1", "name": "agentbot"}})
	var out struct {
		ChannelID int64  `json:"channelId"`
		State     string `json:"state"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil || out.ChannelID == 0 {
		t.Fatalf("hello: %d %s", w.Code, w.Body)
	}
	return out.ChannelID
}

var evSeq int

func chMsg(ch int64, typ, conv, sender, text string) map[string]any {
	evSeq++
	return map[string]any{"channelId": ch, "eventId": fmt.Sprintf("ev%d", evSeq), "messageId": fmt.Sprintf("m%d", evSeq),
		"conversation": map[string]string{"id": conv, "type": typ, "name": "general"},
		"sender":       map[string]string{"id": sender, "name": strings.ToUpper(sender[:1]) + sender[1:]}, "text": text}
}

func chPost(t *testing.T, mux *http.ServeMux, m map[string]any) msgVerdict {
	t.Helper()
	w := adapterCall(t, mux, "apps/slack", "POST", "/adapter/message", m)
	var v msgVerdict
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &v) != nil {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	return v
}

func outbox(ag *Agent, ch int64) []*OutRow {
	return ag.db.outRows(`WHERE channel_id=? ORDER BY id`, ch)
}

func outOfKind(ag *Agent, ch int64, kind string) []*OutRow {
	var out []*OutRow
	for _, o := range outbox(ag, ch) {
		if o.Kind == kind {
			out = append(out, o)
		}
	}
	return out
}

func claim(t *testing.T, mux *http.ServeMux, ch int64, policy any) {
	t.Helper()
	if w := callAs(t, mux, asMgr, "POST", fmt.Sprintf("/channels/%d/claim", ch), map[string]any{"policy": policy}); w.Code != 200 {
		t.Fatalf("claim: %d %s", w.Code, w.Body)
	}
}

// --- the lifecycle -------------------------------------------------------------

// A channel announces itself, is inert until claimed, pairs a stranger by
// code, and then talks: the reply lands in the outbox with where to post it,
// in a web-lane run that can't schedule, and the adapter's ack settles it.
func TestChannelLifecycle(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(lastUser("hi"), say("hello there"))

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/adapter/hello", strings.NewReader(`{"protocol":1}`))
	r.Header.Set("X-XBin-From", "apps/x")
	r.Header.Set("X-XBin-Role", "writer")
	mux.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatalf("a caller without the channel role: %d", w.Code)
	}
	ch := helloAs(t, mux, "apps/slack", "T1")
	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hi")); v.Accepted || v.Reason != "unclaimed" {
		t.Fatalf("unclaimed: %+v", v)
	}
	if w := callAs(t, mux, asBob, "POST", fmt.Sprintf("/channels/%d/claim", ch), nil); w.Code != 403 {
		t.Fatalf("a reader claimed a channel: %d", w.Code)
	}
	claim(t, mux, ch, nil)

	// a stranger gets a pairing code, once per ten minutes
	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hi")); v.Reason != "pairing" {
		t.Fatalf("stranger: %+v", v)
	}
	chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hi again"))
	notes := outOfKind(ag, ch, "notice")
	if len(notes) != 1 || !strings.Contains(notes[0].Body.Text, "pairing code") {
		t.Fatalf("pairing notices: %+v", notes)
	}
	var sum map[string]int
	_ = json.Unmarshal(callAs(t, mux, asMgr, "GET", "/automations?summary=1", nil).Body.Bytes(), &sum)
	if sum["attention"] != 1 {
		t.Fatalf("a pairing request waits on the owner: %v", sum)
	}
	code := ag.db.getPeer(ch, "uma").Code
	if w := callAs(t, mux, asMgr, "POST", fmt.Sprintf("/channels/%d/pair", ch), map[string]string{"code": "WRONG123"}); w.Code != 404 {
		t.Fatalf("a wrong code: %d", w.Code)
	}
	if w := callAs(t, mux, asMgr, "POST", fmt.Sprintf("/channels/%d/pair", ch), map[string]string{"code": strings.ToLower(code)}); w.Code != 200 {
		t.Fatalf("pair: %d %s", w.Code, w.Body)
	}

	m := chMsg(ch, "dm", "D1", "uma", "hi")
	v := chPost(t, mux, m)
	if !v.Accepted || v.RunID == 0 || v.SessionKey != fmt.Sprintf("chan:%d:dm:uma", ch) {
		t.Fatalf("paired: %+v", v)
	}
	run, _ := ag.db.getRun(v.RunID)
	cfg, _ := ag.db.runConfig(v.RunID)
	if run.Owner != "mgr" || run.Origin != "channel" || run.OriginID != ch || run.Visibility != visPrivate || cfg.toolset() != "web" || !cfg.denied("schedule") {
		t.Fatalf("the session's run: %+v lane %s deny %v", run, cfg.toolset(), cfg.Deny)
	}
	if dup := chPost(t, mux, m); !dup.Dup {
		t.Fatalf("a repeated event: %+v", dup)
	}
	waitFor(t, "the answer in the outbox", func() bool { return len(outOfKind(ag, ch, "answer")) == 1 })
	ans := outOfKind(ag, ch, "answer")[0]
	var addr channelAddr
	_ = json.Unmarshal(ans.Address, &addr)
	if ans.Body.Text != "hello there" || addr.Conversation != "D1" || ans.RunID != v.RunID || ans.SessionKey != v.SessionKey {
		t.Fatalf("answer: %+v %+v", ans, addr)
	}
	for _, c := range f.callsFor(v.RunID) {
		for _, s := range c.Tools {
			if s.Function.Name == "schedule" || s.Function.Name == "xbin_call" {
				t.Fatalf("a channel session was offered %s", s.Function.Name)
			}
		}
	}

	// another adapter can't touch this channel or its rows
	if w := adapterCall(t, mux, "apps/evil", "POST", "/adapter/message", chMsg(ch, "dm", "D1", "uma", "hi")); w.Code != 404 {
		t.Fatalf("another adapter posted: %d", w.Code)
	}
	ack := map[string]any{"acks": []map[string]any{{"id": ans.ID, "ok": true, "ref": "1700.1"}}}
	if w := adapterCall(t, mux, "apps/evil", "POST", "/adapter/ack", ack); !strings.Contains(w.Body.String(), `"settled":0`) {
		t.Fatalf("another adapter acked: %s", w.Body)
	}
	if w := adapterCall(t, mux, "apps/slack", "POST", "/adapter/ack", ack); !strings.Contains(w.Body.String(), `"settled":1`) {
		t.Fatalf("ack: %s", w.Body)
	}
	if o := ag.db.outRows(`WHERE id=?`, ans.ID)[0]; o.State != "delivered" || o.Ref != "1700.1" {
		t.Fatalf("acked row: %+v", o)
	}

	// the owner sees it among the automations; bob doesn't
	var list struct{ Items []AutomationItem }
	_ = json.Unmarshal(callAs(t, mux, asMgr, "GET", "/automations", nil).Body.Bytes(), &list)
	if len(list.Items) != 1 || list.Items[0].Kind != "channel" || list.Items[0].Access != "owner" || list.Items[0].Runs != 1 {
		t.Fatalf("the owner's automations: %+v", list.Items)
	}
	_ = json.Unmarshal(callAs(t, mux, asBob, "GET", "/automations", nil).Body.Bytes(), &list)
	if len(list.Items) != 0 {
		t.Fatalf("bob sees a private channel: %+v", list.Items)
	}
}

// In a group the agent answers when mentioned (in a thread of its own) and
// keeps following that thread; silence (NO_REPLY) posts nothing; senders are
// named in the text.
func TestChannelGroups(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(lastUser("lunch"), say("NO_REPLY"))
	f.on(lastUser("deploy"), say("deploying"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"groups": map[string]any{"allow": []string{"C1"}}})

	if v := chPost(t, mux, chMsg(ch, "channel", "C2", "ann", "deploy")); v.Reason != "not-allowed" {
		t.Fatalf("a group not allowed: %+v", v)
	}
	if v := chPost(t, mux, chMsg(ch, "channel", "C1", "ann", "deploy")); v.Reason != "mention-required" {
		t.Fatalf("no mention: %+v", v)
	}
	m := chMsg(ch, "channel", "C1", "ann", "deploy please")
	m["mentioned"] = true
	v := chPost(t, mux, m)
	if !v.Accepted || v.SessionKey != fmt.Sprintf("chan:%d:group:C1:thread:%s", ch, m["messageId"]) {
		t.Fatalf("mentioned: %+v", v)
	}
	waitFor(t, "the answer", func() bool { return len(outOfKind(ag, ch, "answer")) == 1 })
	if !strings.Contains(transcript(ag.db, v.RunID), "[Slack #general] Ann: deploy please") {
		t.Fatalf("the sender is named: %s", transcript(ag.db, v.RunID))
	}
	reply := chMsg(ch, "channel", "C1", "bo", "lunch anyone?")
	reply["thread"] = m["messageId"]
	if r := chPost(t, mux, reply); !r.Accepted || r.RunID != v.RunID {
		t.Fatalf("a reply in the agent's thread: %+v", r)
	}
	waitFor(t, "the quiet turn", func() bool { return strings.Contains(transcript(ag.db, v.RunID), "NO_REPLY") })
	waitQuiet(t, ag)
	if n := len(outOfKind(ag, ch, "answer")); n != 1 {
		t.Fatalf("NO_REPLY was posted: %d answers", n)
	}
	other := chMsg(ch, "channel", "C1", "bo", "deploy")
	other["thread"] = "someone-elses-thread"
	if r := chPost(t, mux, other); r.Reason != "mention-required" {
		t.Fatalf("a thread the agent isn't in: %+v", r)
	}
}

// /new starts a new conversation (the old one stays listed), /status and
// /help answer from the agent, and an unknown /word is just a message.
func TestChannelCommands(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(nil, say("ok"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "open"}})

	first := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "hello"))
	waitStatus(t, ag.db, first.RunID, statusIdle)
	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "/status")); v.Command != "status" {
		t.Fatalf("status: %+v", v)
	}
	if n := outOfKind(ag, ch, "notice"); len(n) != 1 || !strings.Contains(n[0].Body.Text, "web lane") {
		t.Fatalf("status notice: %+v", n)
	}
	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "/new what's up")); v.Command != "new" || v.RunID == first.RunID || v.RunID == 0 {
		t.Fatalf("/new with text: %+v (first %d)", v, first.RunID)
	}
	if v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "/frobnicate")); v.Command != "" || v.RunID == 0 {
		t.Fatalf("an unknown command is a message: %+v", v)
	}
	if n := len(ag.db.outRows(`WHERE kind='notice'`)); n != 1 {
		t.Fatalf("notices: %d", n)
	}
}

// While halted a message is kept and the sender told; it never lifts the
// halt. A trusted peer gets the private lane when the channel opens it;
// revoking trust moves the conversation to a new web-lane run.
func TestChannelHaltAndTrust(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(nil, say("ok"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"privateLane": true})
	callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d/peers/uma", ch), map[string]any{"state": "allowed", "trusted": true})

	_ = ag.db.putSetting("halt", "1")
	v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "are you there"))
	if !v.Accepted || ag.db.getSetting("halt") != "1" {
		t.Fatalf("halted: %+v halt=%q", v, ag.db.getSetting("halt"))
	}
	if n := outOfKind(ag, ch, "notice"); len(n) != 1 || !strings.Contains(n[0].Body.Text, "paused") {
		t.Fatalf("paused notice: %+v", n)
	}
	if len(f.callsFor(v.RunID)) != 0 {
		t.Fatal("a halted agent answered")
	}
	cfg, _ := ag.db.runConfig(v.RunID)
	if cfg.toolset() != "private" {
		t.Fatalf("a trusted peer's lane: %s", cfg.toolset())
	}
	_ = ag.db.putSetting("halt", "")
	callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d/peers/uma", ch), map[string]any{"trusted": false})
	v2 := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "still there?"))
	cfg2, _ := ag.db.runConfig(v2.RunID)
	if v2.RunID == v.RunID || cfg2.toolset() != "web" {
		t.Fatalf("after trust is revoked: run %d (was %d), lane %s", v2.RunID, v.RunID, cfg2.toolset())
	}
	if w := callAs(t, mux, asMgr, "PUT", fmt.Sprintf("/channels/%d", ch), map[string]any{"policy": map[string]any{"privateLane": true, "dm": map[string]string{"policy": "open"}}}); w.Code != 400 {
		t.Fatalf("private lane with open DMs was accepted: %d", w.Code)
	}
	if w := callAs(t, mux, asBob, "DELETE", fmt.Sprintf("/channels/%d", ch), nil); w.Code != 404 {
		t.Fatalf("bob removed a channel he can't see: %d", w.Code)
	}
}

// ask_user in a channel conversation is a question row; the peer's next
// message answers it.
func TestChannelQuestion(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	f.on(lastUser("book"), callTools(tc("q1", "ask_user", `{"question":"which day?"}`))).once()
	f.on(lastUser("friday"), say("booked for friday"))
	ch := helloAs(t, mux, "apps/slack", "T1")
	claim(t, mux, ch, map[string]any{"dm": map[string]any{"policy": "open"}})
	v := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "book a room"))
	waitFor(t, "the question", func() bool { return len(outOfKind(ag, ch, "question")) == 1 })
	if q := outOfKind(ag, ch, "question")[0]; q.Body.Text != "which day?" {
		t.Fatalf("question: %+v", q)
	}
	if r := chPost(t, mux, chMsg(ch, "dm", "D1", "uma", "friday")); r.RunID != v.RunID {
		t.Fatalf("the answer went elsewhere: %+v", r)
	}
	waitFor(t, "the answer", func() bool {
		a := outOfKind(ag, ch, "answer")
		return len(a) == 1 && a[0].Body.Text == "booked for friday"
	})
}

// The outbox stream: hello, the pending rows, new ones as they are written,
// status hints — only the adapter's own channels.
func TestOutboxStream(t *testing.T) {
	ag, mux := chanFixture(t)
	ch := helloAs(t, mux, "apps/slack", "T1")
	other := helloAs(t, mux, "apps/other", "T2")
	_ = ag.db.Tx(func(d *DB) error {
		d.outboxAdd(ch, "k", 0, "notice", `{"conversation":"D1"}`, "first")
		d.outboxAdd(other, "k", 0, "notice", `{"conversation":"D9"}`, "not yours")
		return nil
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", srv.URL+"/adapter/outbox?since=0", nil)
	req.Header.Set("X-XBin-From", "apps/slack")
	req.Header.Set("X-XBin-Role", "channel")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	events := make(chan [2]string, 16)
	go func() {
		sc := bufio.NewScanner(resp.Body)
		var ev string
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "event: ") {
				ev = strings.TrimPrefix(line, "event: ")
			} else if strings.HasPrefix(line, "data: ") {
				events <- [2]string{ev, strings.TrimPrefix(line, "data: ")}
			}
		}
	}()
	next := func() [2]string {
		select {
		case e := <-events:
			return e
		case <-time.After(5 * time.Second):
			t.Fatal("no event")
		}
		return [2]string{}
	}
	if e := next(); e[0] != "hello" || !strings.Contains(e[1], fmt.Sprintf(`"channels":[%d]`, ch)) {
		t.Fatalf("hello: %v", e)
	}
	if e := next(); e[0] != "out" || !strings.Contains(e[1], `"first"`) {
		t.Fatalf("pending row: %v", e)
	}
	_ = ag.db.Tx(func(d *DB) error { d.outboxAdd(ch, "k", 0, "answer", `{"conversation":"D1"}`, "second"); return nil })
	if e := next(); e[0] != "out" || !strings.Contains(e[1], `"second"`) {
		t.Fatalf("a new row: %v", e)
	}
	outStatus("apps/slack", outStatusEv{ChannelID: ch, SessionKey: "k", Address: json.RawMessage(`{}`), State: "working"})
	if e := next(); e[0] != "status" || !strings.Contains(e[1], "working") {
		t.Fatalf("status: %v", e)
	}
}

// A denied tool is hidden and refused, and subagents inherit the list.
func TestConfigDeny(t *testing.T) {
	ag, _ := chanFixture(t)
	cfg := defaultConfig()
	cfg.Deny = []string{"schedule", "mcp:*"}
	for _, s := range toolSpecs(cfg, 0, []toolSpec{{Type: "function", Function: funcDef{Name: "mcp:x.y"}}}) {
		if s.Function.Name == "schedule" || strings.HasPrefix(s.Function.Name, "mcp:") {
			t.Fatalf("offered %s", s.Function.Name)
		}
	}
	id := newRun(t, ag, cfg, "x")
	r, _ := ag.db.getRun(id)
	if _, err := ag.runTool(context.Background(), r, cfg, "schedule", map[string]any{"cron": "@daily", "goal": "x"}); err == nil {
		t.Fatal("a denied tool ran")
	}
	if c := childConfig(cfg, ""); !c.denied("schedule") || !c.denied("mcp:a.b") || c.denied("finish") {
		t.Fatalf("child deny: %v", c.Deny)
	}
}
