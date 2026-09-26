package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// --- a fake Slack: the Web API and a Socket Mode endpoint ---------------------------

type fakeSlack struct {
	t       *testing.T
	srv     *httptest.Server
	mu      sync.Mutex
	opens   int
	conns   []*websocket.Conn
	acks    []string
	posts   []map[string]any
	status  []map[string]string
	postErr string // chat.postMessage answers this error once
	connCh  chan *websocket.Conn
	closed  bool
}

func newFakeSlack(t *testing.T) *fakeSlack {
	f := &fakeSlack{t: t, connCh: make(chan *websocket.Conn, 4)}
	mux := http.NewServeMux()
	ok := func(w http.ResponseWriter, v map[string]any) {
		v["ok"] = true
		_ = json.NewEncoder(w).Encode(v)
	}
	mux.HandleFunc("POST /api/auth.test", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer xoxb-good" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": "invalid_auth"})
			return
		}
		ok(w, map[string]any{"team": "Acme", "team_id": "T1", "user": "agentbot", "user_id": "UBOT", "bot_id": "B1"})
	})
	mux.HandleFunc("POST /api/apps.connections.open", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.opens++
		f.mu.Unlock()
		ok(w, map[string]any{"url": "ws" + strings.TrimPrefix(f.srv.URL, "http") + "/link"})
	})
	mux.HandleFunc("POST /api/users.info", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		ok(w, map[string]any{"user": map[string]any{"name": strings.ToLower(r.Form.Get("user")), "profile": map[string]string{"display_name": "Ann"}}})
	})
	mux.HandleFunc("POST /api/conversations.info", func(w http.ResponseWriter, r *http.Request) {
		ok(w, map[string]any{"channel": map[string]any{"name": "general"}})
	})
	mux.HandleFunc("POST /api/chat.postMessage", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.postErr != "" {
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error": f.postErr})
			f.postErr = ""
			return
		}
		f.posts = append(f.posts, b)
		ok(w, map[string]any{"ts": fmt.Sprintf("1700.%04d", len(f.posts))})
	})
	mux.HandleFunc("POST /api/assistant.threads.setStatus", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]string
		_ = json.NewDecoder(r.Body).Decode(&b)
		f.mu.Lock()
		f.status = append(f.status, b)
		f.mu.Unlock()
		ok(w, map[string]any{})
	})
	up := websocket.Upgrader{}
	mux.HandleFunc("GET /link", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		closed := f.closed
		f.mu.Unlock()
		if closed {
			http.Error(w, "gone", 503)
			return
		}
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		f.mu.Lock()
		f.conns = append(f.conns, c)
		f.mu.Unlock()
		_ = c.WriteJSON(map[string]any{"type": "hello", "num_connections": 1})
		f.connCh <- c
		for {
			_, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			var a struct {
				EnvelopeID string `json:"envelope_id"`
			}
			_ = json.Unmarshal(data, &a)
			f.mu.Lock()
			f.acks = append(f.acks, a.EnvelopeID)
			f.mu.Unlock()
		}
	})
	f.srv = httptest.NewServer(mux)
	// the tile keeps running: cut its connections, or Close waits on them
	t.Cleanup(func() {
		f.mu.Lock()
		f.closed = true
		for _, c := range f.conns {
			_ = c.Close()
		}
		f.mu.Unlock()
		f.srv.CloseClientConnections()
		f.srv.Close()
	})
	return f
}

func (f *fakeSlack) conn() *websocket.Conn {
	f.t.Helper()
	select {
	case c := <-f.connCh:
		return c
	case <-time.After(5 * time.Second):
		f.t.Fatal("the tile never connected")
	}
	return nil
}

func event(envID, typ string, ev map[string]any) map[string]any {
	ev["type"] = typ
	return map[string]any{"type": "events_api", "envelope_id": envID, "payload": map[string]any{
		"type": "event_callback", "event_id": "Ev" + envID, "team_id": "T1", "event": ev}}
}

// --- a fake agent: the agent-inbox contract -------------------------------------------

type fakeAgent struct {
	mu     sync.Mutex
	srv    *httptest.Server
	hellos int
	msgs   []agentMsg
	acks   []ackItem
	rows   chan string // SSE "out" data to stream
}

func newFakeAgent(t *testing.T) *fakeAgent {
	a := &fakeAgent{rows: make(chan string, 16)}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /adapter/hello", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		a.hellos++
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(helloReply{ChannelID: 7, State: "active"})
	})
	mux.HandleFunc("POST /adapter/message", func(w http.ResponseWriter, r *http.Request) {
		var m agentMsg
		_ = json.NewDecoder(r.Body).Decode(&m)
		a.mu.Lock()
		a.msgs = append(a.msgs, m)
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(verdict{Accepted: true, RunID: 1})
	})
	mux.HandleFunc("POST /adapter/ack", func(w http.ResponseWriter, r *http.Request) {
		var b struct{ Acks []ackItem }
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.mu.Lock()
		a.acks = append(a.acks, b.Acks...)
		a.mu.Unlock()
		_, _ = io.WriteString(w, `{"settled":1}`)
	})
	mux.HandleFunc("GET /adapter/outbox", func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "event: hello\ndata: {\"cursor\":0,\"channels\":[7]}\n\n")
		fl.Flush()
		for {
			select {
			case d := <-a.rows:
				ev := "out"
				if strings.HasPrefix(d, "status:") {
					ev, d = "status", strings.TrimPrefix(d, "status:")
				}
				fmt.Fprintf(w, "event: %s\ndata: %s\n\n", ev, d)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(func() { a.srv.CloseClientConnections(); a.srv.Close() })
	return a
}

// --- memory store --------------------------------------------------------------------

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func (s *memStore) Get(k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("not found")
}
func (s *memStore) Put(k string, v []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[k] = v
	return nil
}
func (s *memStore) Delete(k string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, k)
	return nil
}
func (s *memStore) List(p string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for k := range s.m {
		if strings.HasPrefix(k, p) {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out, nil
}

func newTestTile(t *testing.T, slack *fakeSlack, agent *fakeAgent, kv *memStore) *Tile {
	t.Helper()
	if kv == nil {
		kv = &memStore{m: map[string][]byte{}}
	}
	cfg, _ := json.Marshal(config{APIBase: slack.srv.URL + "/api/"})
	kv.m["config"] = cfg
	secrets := map[string]string{secretBot: "xoxb-good", secretApp: "xapp-good"}
	return newTile(kv, func(n string) string { return secrets[n] }, newAgentClient(agent.srv.URL, http.DefaultClient))
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The whole path: connect (auth.test, hello to the agent, Socket Mode),
// events acked and reported — a DM, a mention with the mention stripped, a
// thread reply; not the bot's own, not an edit, not an unaddressed channel
// message — and a refresh-disconnect reconnects at once.
func TestInbound(t *testing.T) {
	slack, agent := newFakeSlack(t), newFakeAgent(t)
	tile := newTestTile(t, slack, agent, nil)
	go tile.run()
	c := slack.conn()
	waitUntil(t, "connected", func() bool { tile.mu.Lock(); defer tile.mu.Unlock(); return tile.st.Phase == "connected" })
	for _, env := range []map[string]any{
		event("e1", "message", map[string]any{"channel": "D1", "channel_type": "im", "user": "U1", "text": "hi there", "ts": "1.1"}),
		event("e2", "app_mention", map[string]any{"channel": "C1", "user": "U1", "text": "<@UBOT> deploy <https://x.dev|site>", "ts": "2.1"}),
		event("e3", "message", map[string]any{"channel": "C1", "channel_type": "channel", "user": "U2", "text": "<@UBOT> deploy", "ts": "2.1"}),
		event("e4", "message", map[string]any{"channel": "C1", "channel_type": "channel", "user": "U2", "text": "and staging", "ts": "2.5", "thread_ts": "2.1"}),
		event("e5", "message", map[string]any{"channel": "C1", "channel_type": "channel", "user": "U2", "text": "lunch?", "ts": "3.1"}),
		event("e6", "message", map[string]any{"channel": "D1", "channel_type": "im", "user": "UBOT", "text": "my own", "ts": "4.1"}),
		event("e7", "message", map[string]any{"channel": "D1", "channel_type": "im", "subtype": "message_changed", "ts": "5.1"}),
	} {
		if err := c.WriteJSON(env); err != nil {
			t.Fatal(err)
		}
	}
	waitUntil(t, "7 acks", func() bool { slack.mu.Lock(); defer slack.mu.Unlock(); return len(slack.acks) == 7 })
	waitUntil(t, "3 messages", func() bool { agent.mu.Lock(); defer agent.mu.Unlock(); return len(agent.msgs) == 3 })
	time.Sleep(50 * time.Millisecond)
	agent.mu.Lock()
	msgs := append([]agentMsg(nil), agent.msgs...)
	agent.mu.Unlock()
	if len(msgs) != 3 {
		t.Fatalf("messages: %+v", msgs)
	}
	dm, mention, reply := msgs[0], msgs[1], msgs[2]
	if dm.Conversation.Type != "dm" || !dm.Mentioned || dm.Text != "hi there" || dm.ChannelID != 7 || dm.EventID != "Eve1" || dm.Sender.Name != "Ann" {
		t.Errorf("dm: %+v", dm)
	}
	if mention.Conversation.Type != "channel" || !mention.Mentioned || mention.Text != "deploy site (https://x.dev)" || mention.Conversation.Name != "general" {
		t.Errorf("mention: %+v", mention)
	}
	if reply.Mentioned || reply.Thread != "2.1" || reply.MessageID != "2.5" {
		t.Errorf("thread reply: %+v", reply)
	}
	if keys, _ := tile.kv.List("spool/"); len(keys) != 0 {
		t.Errorf("the spool kept delivered events: %v", keys)
	}
	// Slack refreshes the connection: a new one opens at once
	_ = c.WriteJSON(map[string]any{"type": "disconnect", "reason": "refresh_requested"})
	slack.conn()
	slack.mu.Lock()
	opens := slack.opens
	slack.mu.Unlock()
	if opens != 2 {
		t.Fatalf("connections opened: %d", opens)
	}
}

// Replies: posted where the address says (in the thread, as mrkdwn), acked
// with the ts; a row already posted before a crash is only acked; a
// permanent error is acked as failed; assistant threads show a status.
func TestOutbound(t *testing.T) {
	slack, agent := newFakeSlack(t), newFakeAgent(t)
	kv := &memStore{m: map[string][]byte{"posted/41": []byte("1699.0001"), "assist/D1/9.9": []byte("1")}}
	tile := newTestTile(t, slack, agent, kv)
	go tile.run()
	slack.conn()
	agent.rows <- `{"id":41,"channelId":7,"kind":"answer","address":{"conversation":"D1","type":"dm"},"body":{"text":"already"}}`
	agent.rows <- `{"id":42,"channelId":7,"kind":"answer","address":{"conversation":"C1","type":"channel","thread":"2.1"},"body":{"text":"**done** — see [log](https://x.dev)"}}`
	waitUntil(t, "two acks", func() bool { agent.mu.Lock(); defer agent.mu.Unlock(); return len(agent.acks) == 2 })
	agent.mu.Lock()
	a41, a42 := agent.acks[0], agent.acks[1]
	agent.mu.Unlock()
	slack.mu.Lock()
	posts := append([]map[string]any(nil), slack.posts...)
	slack.mu.Unlock()
	if a41.ID != 41 || !a41.OK || a41.Ref != "1699.0001" || len(posts) != 1 {
		t.Fatalf("a replayed row was posted again: %+v, %d posts", a41, len(posts))
	}
	if p := posts[0]; p["channel"] != "C1" || p["thread_ts"] != "2.1" || p["text"] != "*done* — see <https://x.dev|log>" {
		t.Fatalf("post: %+v", p)
	}
	if a42.ID != 42 || !a42.OK || a42.Ref != "1700.0001" {
		t.Fatalf("ack: %+v", a42)
	}
	if _, err := kv.Get("posted/42"); err == nil {
		t.Fatal("the posted record outlived its ack")
	}
	slack.mu.Lock()
	slack.postErr = "channel_not_found"
	slack.mu.Unlock()
	agent.rows <- `{"id":43,"channelId":7,"kind":"answer","address":{"conversation":"CX","type":"channel"},"body":{"text":"lost"}}`
	waitUntil(t, "the failed ack", func() bool { agent.mu.Lock(); defer agent.mu.Unlock(); return len(agent.acks) == 3 })
	agent.mu.Lock()
	a43 := agent.acks[2]
	agent.mu.Unlock()
	if a43.OK || a43.Error != "slack: channel_not_found" {
		t.Fatalf("permanent error: %+v", a43)
	}
	agent.rows <- `status:{"channelId":7,"address":{"conversation":"D1","thread":"9.9"},"state":"working"}`
	agent.rows <- `status:{"channelId":7,"address":{"conversation":"D2","thread":"1.1"},"state":"working"}`
	waitUntil(t, "an assistant status", func() bool { slack.mu.Lock(); defer slack.mu.Unlock(); return len(slack.status) == 1 })
	if s := slack.status[0]; s["channel_id"] != "D1" || s["thread_ts"] != "9.9" || s["status"] == "" {
		t.Fatalf("status: %+v", s)
	}
}

// Events acked before a crash are delivered by the next process.
func TestSpoolReplay(t *testing.T) {
	slack, agent := newFakeSlack(t), newFakeAgent(t)
	payload, _ := json.Marshal(event("z", "message", map[string]any{"channel": "D1", "channel_type": "im", "user": "U1", "text": "from before", "ts": "1.1"})["payload"])
	item, _ := json.Marshal(spoolItem{Type: "events_api", Payload: payload})
	kv := &memStore{m: map[string][]byte{"spool/00000000000000000001-z": item}}
	tile := newTestTile(t, slack, agent, kv)
	tile.replaySpool()
	go tile.run()
	waitUntil(t, "the replayed message", func() bool { agent.mu.Lock(); defer agent.mu.Unlock(); return len(agent.msgs) == 1 })
	if agent.msgs[0].Text != "from before" {
		t.Fatalf("replayed: %+v", agent.msgs[0])
	}
	waitUntil(t, "the spool emptied", func() bool { k, _ := kv.List("spool/"); return len(k) == 0 })
}

// A bad token says so and waits; an API base must be Slack or loopback.
func TestSetup(t *testing.T) {
	slack, agent := newFakeSlack(t), newFakeAgent(t)
	kv := &memStore{m: map[string][]byte{}}
	cfg, _ := json.Marshal(config{APIBase: slack.srv.URL + "/api/"})
	kv.m["config"] = cfg
	tile := newTile(kv, func(n string) string { return map[string]string{secretBot: "xoxb-bad", secretApp: "xapp-x"}[n] },
		newAgentClient(agent.srv.URL, http.DefaultClient))
	if err := tile.session(); err == nil || !strings.Contains(err.Error(), "invalid_auth") {
		t.Fatalf("a bad token: %v", err)
	}
	noTokens := newTile(kv, func(string) string { return "" }, newAgentClient(agent.srv.URL, http.DefaultClient))
	if err := noTokens.session(); err != errNotReady || noTokens.st.Phase != "needs-tokens" {
		t.Fatalf("no tokens: %v %s", err, noTokens.st.Phase)
	}
	for base, ok := range map[string]bool{"https://slack.com/api/": true, "http://127.0.0.1:9/api/": true,
		"https://evil.example/api/": false, "file:///etc": false} {
		if apiBaseOK(base) != ok {
			t.Errorf("apiBaseOK(%q) != %v", base, ok)
		}
	}
}
