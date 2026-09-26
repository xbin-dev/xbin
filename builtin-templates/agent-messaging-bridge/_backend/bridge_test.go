package main

import (
	"bytes"
	"context"
	"encoding/base64"
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
)

// --- a fake agent: the agent-inbox contract ----------------------------------------

type fakeAgent struct {
	mu      sync.Mutex
	srv     *httptest.Server
	hellos  []map[string]any
	msgs    []agentMsg
	uploads map[string][]byte // file id → bytes
	acks    []ackItem
	rows    chan string
	files   map[string][]byte // "row/i" → bytes to download
}

func newFakeAgent(t *testing.T) *fakeAgent {
	a := &fakeAgent{rows: make(chan string, 16), uploads: map[string][]byte{}, files: map[string][]byte{}}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /adapter/hello", func(w http.ResponseWriter, r *http.Request) {
		var b map[string]any
		_ = json.NewDecoder(r.Body).Decode(&b)
		a.mu.Lock()
		a.hellos = append(a.hellos, b)
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(helloReply{ChannelID: 7, State: "active"})
	})
	mux.HandleFunc("POST /adapter/files", func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		a.mu.Lock()
		id := fmt.Sprintf("f%d-%s", len(a.uploads), r.URL.Query().Get("name"))
		a.uploads[id] = b
		a.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]string{"fileId": id})
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
	mux.HandleFunc("GET /adapter/files/{oid}/{i}", func(w http.ResponseWriter, r *http.Request) {
		a.mu.Lock()
		b, ok := a.files[r.PathValue("oid")+"/"+r.PathValue("i")]
		a.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(b)
	})
	mux.HandleFunc("GET /adapter/outbox", func(w http.ResponseWriter, r *http.Request) {
		fl := w.(http.Flusher)
		fmt.Fprintf(w, "event: hello\ndata: {\"cursor\":0}\n\n")
		fl.Flush()
		for {
			select {
			case d := <-a.rows:
				ev := "out" // or "status:{…}", "channel:{…}"
				if i := strings.Index(d, ":"); i > 0 && !strings.HasPrefix(d, "{") {
					ev, d = d[:i], d[i+1:]
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

func (a *fakeAgent) snapshot() ([]agentMsg, []ackItem) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]agentMsg(nil), a.msgs...), append([]ackItem(nil), a.acks...)
}

// --- memory store ----------------------------------------------------------------------

type memStore struct {
	mu sync.Mutex
	m  map[string][]byte
}

func newMem() *memStore { return &memStore{m: map[string][]byte{}} }
func (s *memStore) Get(k string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.m[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("not found")
}
func (s *memStore) Put(k string, v []byte) error { s.mu.Lock(); s.m[k] = v; s.mu.Unlock(); return nil }
func (s *memStore) Delete(k string) error        { s.mu.Lock(); delete(s.m, k); s.mu.Unlock(); return nil }
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

func newTestTile(t *testing.T, a *fakeAgent, kv *memStore, platform string, secrets map[string]string) (*Tile, *http.ServeMux) {
	t.Helper()
	if kv == nil {
		kv = newMem()
	}
	cfg, _ := json.Marshal(config{Platform: platform})
	kv.m["config"] = cfg
	if secrets == nil {
		secrets = map[string]string{}
	}
	var smu sync.Mutex
	tile := newTile(kv, func(n string) string { smu.Lock(); defer smu.Unlock(); return secrets[n] }, newAgentClient(a.srv.URL, http.DefaultClient))
	tile.setSecret = func(n, v string) error { smu.Lock(); defer smu.Unlock(); secrets[n] = v; return nil }
	mux := http.NewServeMux()
	tile.routes(mux)
	return tile, mux
}

func call(mux *http.ServeMux, method, target string, body any) *httptest.ResponseRecorder {
	b, _ := json.Marshal(body)
	r := httptest.NewRequest(method, target, bytes.NewReader(b))
	r.Header.Set("X-XBin-From", "apps/messaging-bridge")
	r.Header.Set("X-XBin-Role", "admin")
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	return w
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

func connectedOn(tile *Tile, account string) func() bool {
	return func() bool { _, ok := tile.channelOf(account); return ok }
}

// The console end to end: a pretend DM with a file reaches the agent as an
// upload plus a message naming it; the agent's reply with a file lands in the
// transcript and is acked with the platform's ref.
func TestConsole(t *testing.T) {
	a := newFakeAgent(t)
	tile, mux := newTestTile(t, a, nil, "console", nil)
	go tile.run()
	waitUntil(t, "the console account", connectedOn(tile, "console"))
	if h := a.hellos[0]; h["platform"] != "console" || h["account"].(map[string]any)["id"] != "console" {
		t.Fatalf("hello: %+v", h)
	}
	w := call(mux, "POST", "/console/send", map[string]any{"as": map[string]string{"id": "u1", "name": "Uma"},
		"conversation": map[string]string{"id": "D1", "type": "dm"}, "text": "look at this",
		"files": []map[string]string{{"name": "a.txt", "mime": "text/plain", "data": base64.StdEncoding.EncodeToString([]byte("hello file"))}}})
	if w.Code != 200 {
		t.Fatalf("send: %d %s", w.Code, w.Body)
	}
	waitUntil(t, "the message", func() bool { m, _ := a.snapshot(); return len(m) == 1 })
	m, _ := a.snapshot()
	if m[0].ChannelID != 7 || m[0].Sender.ID != "u1" || !m[0].Mentioned || m[0].Conversation.Type != "dm" || len(m[0].Files) != 1 {
		t.Fatalf("message: %+v", m[0])
	}
	if got := string(a.uploads[m[0].Files[0]]); got != "hello file" {
		t.Fatalf("upload: %q", got)
	}
	a.mu.Lock()
	a.files["41/0"] = []byte("PNGDATA")
	a.mu.Unlock()
	a.rows <- `{"id":41,"channelId":7,"kind":"answer","address":{"conversation":"D1","type":"dm"},"body":{"text":"here","files":[{"name":"chart.png","mime":"image/png","bytes":7}]}}`
	waitUntil(t, "the ack", func() bool { _, acks := a.snapshot(); return len(acks) == 1 })
	_, acks := a.snapshot()
	if !acks[0].OK || !strings.HasPrefix(acks[0].Ref, "b") || tr0(t, mux).Conv.ID != "D1" {
		t.Fatalf("ack: %+v", acks[0])
	}
	var tr struct{ Lines []consoleLine }
	_ = json.Unmarshal(call(mux, "GET", "/console/transcript", nil).Body.Bytes(), &tr)
	last := tr.Lines[len(tr.Lines)-1]
	if last.Dir != "out" || last.Text != "here" || len(last.Files) != 1 || last.Files[0].Name != "chart.png" {
		t.Fatalf("transcript: %+v", tr.Lines)
	}
	if w := call(mux, "GET", "/console/files/"+last.Files[0].ID, nil); w.Body.String() != "PNGDATA" {
		t.Fatalf("the reply's file: %q", w.Body)
	}
}

// tr0 is the transcript's first line, read back through its JSON (lowercase
// keys, as the page and the harness read them).
func tr0(t *testing.T, mux *http.ServeMux) consoleLine {
	var raw struct {
		Lines []map[string]any `json:"lines"`
	}
	_ = json.Unmarshal(call(mux, "GET", "/console/transcript", nil).Body.Bytes(), &raw)
	if len(raw.Lines) == 0 {
		t.Fatal("empty transcript")
	}
	conv, _ := raw.Lines[0]["conversation"].(map[string]any)
	return consoleLine{Conv: Conversation{ID: fmt.Sprint(conv["id"])}}
}

// --- a test platform --------------------------------------------------------------------

type testPlat struct {
	mu    sync.Mutex
	sent  []Outgoing
	calls int
	fetch string
}

func (p *testPlat) Info() Info {
	return Info{Name: "testplat", Title: "Test", Secrets: []SecretField{{Name: "token", Label: "Token", Prefix: "tp-"}}, Egress: "internet:*.test:443"}
}
func (p *testPlat) Start(ctx context.Context, b Bridge) error {
	if err := b.Account(ctx, Account{ID: "acct", Name: "Acme"}); err != nil {
		return err
	}
	_ = b.Receive(Event{Account: "acct", ID: "e1", Conversation: Conversation{ID: "C1", Type: "channel", Name: "general"},
		MessageID: "m1", Sender: Person{ID: "u2", Name: "Bo"}, Mentioned: true, Text: "see file",
		Files: []FileRef{{ID: "F1", Name: "remote.bin", URL: "https://files.test/F1"}}})
	_ = b.Receive(Event{Account: "acct", ID: "e2", Sender: Person{ID: "bot", Bot: true}, Text: "my own"})
	<-ctx.Done()
	return ctx.Err()
}
func (p *testPlat) Send(ctx context.Context, account string, to Address, msg Outgoing) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls++
	switch {
	case to.Conversation == "gone":
		return "", Permanent(fmt.Errorf("channel_not_found"))
	case to.Conversation == "busy" && p.calls == 1:
		return "", RetryAfter(10*time.Millisecond, fmt.Errorf("ratelimited"))
	}
	p.sent = append(p.sent, msg)
	return fmt.Sprintf("ref%d", len(p.sent)), nil
}
func (p *testPlat) Typing(context.Context, string, Address, bool) error { return nil }
func (p *testPlat) Format(md string) string                             { return strings.ToUpper(md) }
func (p *testPlat) Limit() int                                          { return 40 }
func (p *testPlat) Fetch(ctx context.Context, account string, f FileRef) (io.ReadCloser, error) {
	p.mu.Lock()
	p.fetch = f.URL
	p.mu.Unlock()
	return io.NopCloser(strings.NewReader("remote bytes")), nil
}

var thePlat = &testPlat{}

func init() { registerPlatform("testplat", func(Bridge) Platform { return thePlat }) }

// A platform: its secrets first (the page may set only those, in their
// format); fetched attachments; bots dropped; replies formatted, split at its
// limit with files on the last piece; rate limits waited out; permanent
// errors acked as failed at once.
// The page shows the channel's state on the agent: from hello, then as the
// owner claims, switches off or removes it (the outbox stream says so), and
// from verdicts. A removed channel's account says hello again.
func TestChannelState(t *testing.T) {
	a := newFakeAgent(t)
	tile, mux := newTestTile(t, a, nil, "console", nil)
	go tile.run()
	waitUntil(t, "the console account", connectedOn(tile, "console"))
	claimed := func() string {
		var st struct {
			State struct{ Accounts []accountView }
		}
		_ = json.Unmarshal(call(mux, "GET", "/status", nil).Body.Bytes(), &st)
		if len(st.State.Accounts) != 1 {
			return ""
		}
		return st.State.Accounts[0].Claimed
	}
	if c := claimed(); c != "active" {
		t.Fatalf("from hello: %q", c)
	}
	a.rows <- `channel:{"channelId":7,"accountId":"console","state":"disabled"}`
	waitUntil(t, "switched off", func() bool { return claimed() == "disabled" })
	a.rows <- `channel:{"channelId":7,"accountId":"console","state":"removed"}`
	waitUntil(t, "hello again", func() bool { a.mu.Lock(); defer a.mu.Unlock(); return len(a.hellos) == 2 })
	waitUntil(t, "offered anew", func() bool { return claimed() == "active" })
	a.rows <- `channel:{"channelId":7,"accountId":"console","state":"disabled"}`
	waitUntil(t, "switched off", func() bool { return claimed() == "disabled" })
	call(mux, "POST", "/console/send", map[string]any{"as": map[string]string{"id": "u1"}, "conversation": map[string]string{"id": "D1"}, "text": "hi"})
	waitUntil(t, "an accepted message says it is on", func() bool { return claimed() == "active" })
}

func TestPlatformContract(t *testing.T) {
	a := newFakeAgent(t)
	tile, mux := newTestTile(t, a, nil, "testplat", nil)
	go tile.run()
	waitUntil(t, "needs-secrets", func() bool { tile.mu.Lock(); defer tile.mu.Unlock(); return tile.st.Phase == "needs-secrets" })
	var st struct {
		Info struct {
			Title   string `json:"title"`
			Egress  string `json:"egress"`
			Secrets []struct {
				Name, Label, Prefix string
			} `json:"secrets"`
		} `json:"info"`
		SecretsSet map[string]bool `json:"secretsSet"`
	}
	_ = json.Unmarshal(call(mux, "GET", "/status", nil).Body.Bytes(), &st)
	if st.Info.Title != "Test" || st.Info.Egress == "" || len(st.Info.Secrets) != 1 || st.Info.Secrets[0].Label != "Token" || st.SecretsSet["token"] {
		t.Fatalf("the page's view of the platform: %+v", st)
	}
	if w := call(mux, "PUT", "/config/secrets", map[string]string{"other": "x"}); w.Code != 400 {
		t.Fatalf("an undeclared secret: %d", w.Code)
	}
	if w := call(mux, "PUT", "/config/secrets", map[string]string{"token": "nope"}); w.Code != 400 {
		t.Fatalf("a malformed secret: %d", w.Code)
	}
	if w := call(mux, "PUT", "/config/secrets", map[string]string{"token": "tp-123"}); w.Code != 200 {
		t.Fatalf("set: %d %s", w.Code, w.Body)
	}
	waitUntil(t, "the message", func() bool { m, _ := a.snapshot(); return len(m) == 1 })
	m, _ := a.snapshot()
	if m[0].Conversation.Name != "general" || len(m[0].Files) != 1 || string(a.uploads[m[0].Files[0]]) != "remote bytes" {
		t.Fatalf("a fetched attachment: %+v", m[0])
	}
	a.mu.Lock()
	a.files["51/0"] = []byte("IMG")
	a.mu.Unlock()
	long := strings.Repeat("word ", 5) + "\n" + strings.Repeat("more ", 5) + "\n" + strings.Repeat("end ", 5)
	body, _ := json.Marshal(long)
	a.rows <- `{"id":51,"channelId":7,"kind":"answer","address":{"conversation":"busy"},"body":{"text":` + string(body) + `,"files":[{"name":"x.png","mime":"image/png"}]}}`
	a.rows <- `{"id":52,"channelId":7,"kind":"answer","address":{"conversation":"gone"},"body":{"text":"lost"}}`
	waitUntil(t, "two acks", func() bool { _, acks := a.snapshot(); return len(acks) == 2 })
	_, acks := a.snapshot()
	thePlat.mu.Lock()
	sent := append([]Outgoing(nil), thePlat.sent...)
	thePlat.mu.Unlock()
	if len(sent) < 2 || !strings.HasPrefix(sent[0].Text, "WORD") || len(sent[0].Files) != 0 || len(sent[len(sent)-1].Files) != 1 {
		t.Fatalf("split and formatted, files last: %+v", sent)
	}
	for _, s := range sent {
		if len(s.Text) > 40 {
			t.Fatalf("a piece over the limit: %q", s.Text)
		}
	}
	if !acks[0].OK || acks[0].Ref != "ref1" || acks[1].OK || !strings.Contains(acks[1].Error, "channel_not_found") {
		t.Fatalf("acks: %+v", acks)
	}
}

// Events stored before a crash are delivered by the next process.
func TestSpoolReplay(t *testing.T) {
	a := newFakeAgent(t)
	kv := newMem()
	item, _ := json.Marshal(spoolItem{Event: Event{Account: "console", ID: "old", Conversation: Conversation{ID: "D1", Type: "dm"},
		Sender: Person{ID: "u1"}, Text: "from before"}})
	kv.m["spool/00000000000000000001-old"] = item
	tile, _ := newTestTile(t, a, kv, "console", nil)
	tile.replaySpool()
	go tile.run()
	waitUntil(t, "the replayed message", func() bool { m, _ := a.snapshot(); return len(m) == 1 && m[0].Text == "from before" })
	waitUntil(t, "the spool emptied", func() bool { k, _ := kv.List("spool/"); return len(k) == 0 })
}

func TestSplitText(t *testing.T) {
	if p := splitText("short", 100); len(p) != 1 {
		t.Fatalf("short: %v", p)
	}
	var b strings.Builder
	b.WriteString("intro\n```\n")
	for i := 0; i < 40; i++ {
		b.WriteString("line of code number something\n")
	}
	b.WriteString("```\nafter")
	for i, p := range splitText(b.String(), 300) {
		if len(p) > 300 || strings.Count(p, "```")%2 != 0 {
			t.Fatalf("piece %d: %d bytes, fences balanced=%v", i, len(p), strings.Count(p, "```")%2 == 0)
		}
	}
	for _, p := range splitText(strings.Repeat("é", 400), 300) {
		if !strings.HasPrefix(p, "é") {
			t.Fatal("a cut split a rune")
		}
	}
}
