package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// f5 is a person's call reaching the global instance from their own
// partition (their frame or backend), attributed by xbind.
func f5(user, level string) map[string]string {
	role := "reader"
	if level != "read" {
		role = "writer"
	}
	return map[string]string{"X-XBin-From": "apps/agent", "X-XBin-Role": role, "X-XBin-User": user,
		"X-XBin-User-Level": level, "X-XBin-Partition": "user:" + user, "X-XBin-Partition-Id": "u-" + strings.Repeat("c", 32)}
}

// ownerToken is the owner token's call (the global-viewer state).
var ownerToken = map[string]string{"X-XBin-From": "owner", "X-XBin-Role": "admin"}

func serveJSON(t *testing.T, h http.Handler, r *http.Request, want int, out any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != want {
		t.Fatalf("%s %s: %d %s (want %d)", r.Method, r.URL, rec.Code, rec.Body, want)
	}
	if out != nil {
		if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
			t.Fatalf("%s %s: %v in %s", r.Method, r.URL, err, rec.Body)
		}
	}
	return rec
}

func globalAgent(t *testing.T) (*Agent, http.Handler) {
	t.Helper()
	setMode(t, modeGlobal, "")
	ag, h := homeAgent(t)
	_ = ag.db.putSetting("config", mustJSON(defaultConfig()))
	return ag, h
}

// homeAgent is partAgent whose background titles end before the test's
// mode is restored (a title reads the mode's LLM slots).
func homeAgent(t *testing.T) (*Agent, http.Handler) {
	t.Helper()
	ag, h := partAgent(t)
	t.Cleanup(func() {
		waitFor(t, "the titles to end", func() bool {
			ag.eng.titleMu.Lock()
			defer ag.eng.titleMu.Unlock()
			return len(ag.eng.titling) == 0
		})
	})
	return ag, h
}

func homeConvIDs(t *testing.T, h http.Handler, hdr map[string]string, scope string) []int64 {
	t.Helper()
	var out struct{ Pinned, Items []struct{ ID int64 } }
	serveJSON(t, h, as("GET", "/conversations?scope="+scope, "", hdr), 200, &out)
	var ids []int64
	for _, it := range append(out.Pinned, out.Items...) {
		ids = append(ids, it.ID)
	}
	return ids
}

// TestSharedAskAtGlobal: at the global instance a person's new conversation
// is a shared one — POST /ask must say who shares it (the team or people);
// POST /runs is refused; the owner token (the global-viewer state) asks as
// ever; the people it names see it, others don't. In a person's partition a
// new conversation can't be shared; unpartitioned, share is simply applied.
func TestSharedAskAtGlobal(t *testing.T) {
	ag, h := globalAgent(t)
	for _, c := range []struct {
		body string
		want int
	}{
		{`{"text":"hi"}`, 409},
		{`{"text":"hi","share":{"visibility":"private"}}`, 409},
		{`{"text":"hi","share":{"visibility":"everyone"}}`, 400},
		{`{"text":"hi","share":{"members":[{"user":"alice"}]}}`, 400}, // the owner
		{`{"text":"hi","share":{"members":[{"user":"Bob!"}]}}`, 400},
		{`{"text":"hi","share":{"visibility":"team"},"draft":"abcdefgh12"}`, 400},
	} {
		serveJSON(t, h, as("POST", "/ask", c.body, f5("alice", "read")), c.want, nil)
	}
	serveJSON(t, h, as("POST", "/runs", `{"goal":"g"}`, f5("alice", "read")), 409, nil)

	var team, people, own Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"for the team","share":{"visibility":"team","teamRole":"viewer"}}`, f5("alice", "read")), 200, &team)
	serveJSON(t, h, as("POST", "/ask", `{"text":"for bob","share":{"members":[{"user":"bob","role":"participant"}]}}`, f5("alice", "read")), 200, &people)
	serveJSON(t, h, as("POST", "/ask", `{"text":"the owner token's"}`, ownerToken), 200, &own)
	for _, r := range []Run{team, people, own} {
		if r.ID <= 0 || r.ID >= partitionIDBase {
			t.Fatalf("a global conversation's id %d", r.ID)
		}
	}
	if a, _ := ag.aclOf(team.ID); a.visibility != visTeam || a.teamRole != roleViewer || a.owner != "alice" {
		t.Fatalf("the team conversation: %+v", a)
	}
	if a, _ := ag.aclOf(people.ID); a.visibility != visPrivate || a.members["bob"] != roleParticipant || a.owner != "alice" {
		t.Fatalf("the people conversation: %+v", a)
	}
	waitStatus(t, ag.db, people.ID, statusIdle)
	// bob: both in his Shared view; the one he is a member of in his own
	if got := fmt.Sprint(homeConvIDs(t, h, f5("bob", "read"), "shared")); got != fmt.Sprint([]int64{people.ID, team.ID}) {
		t.Fatalf("bob's shared view: %s", got)
	}
	if got := fmt.Sprint(homeConvIDs(t, h, f5("bob", "read"), "mine")); !strings.Contains(got, fmt.Sprint(people.ID)) {
		t.Fatalf("bob's own list: %s", got)
	}
	// bob talks where he is a participant, reads where he is a viewer
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", people.ID), `{"text":"from bob"}`, f5("bob", "read")), 200, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", team.ID), `{"text":"from bob"}`, f5("bob", "read")), 403, nil)
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", team.ID), "", f5("bob", "read")), 200, nil)
	// dave, a stranger: neither the people one nor the owner token's
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", people.ID), "", f5("dave", "read")), 404, nil)
	// alice shares on at global as its owner: a member, a link
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/members", team.ID), `{"user":"dave","role":"viewer"}`, f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/links", people.ID), `{"role":"viewer"}`, f5("alice", "read")), 200, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/members", people.ID), `{"user":"dave"}`, f5("bob", "read")), 403, nil)

	// a person's partition: a new conversation there is theirs alone
	t.Run("partition", func(t *testing.T) {
		setMode(t, modeUser, "alice")
		kv := newMemKV()
		confIn = newConfReader(kv, nil)
		putConf(kv, "", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
		_, h := homeAgent(t)
		serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"visibility":"team"}}`, alicesFrame("read")), 409, nil)
		var r Run
		serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"visibility":"private"}}`, alicesFrame("read")), 200, &r)
		if r.ID < partitionIDBase || r.Visibility != visPrivate {
			t.Fatalf("her own conversation: %+v", r)
		}
	})
	// unpartitioned: share is applied (the same as sharing it right after)
	t.Run("legacy", func(t *testing.T) {
		setMode(t, modeLegacy, "")
		ag, h := homeAgent(t)
		_ = ag.db.putSetting("config", mustJSON(defaultConfig()))
		var r Run
		serveJSON(t, h, as("POST", "/ask", `{"text":"x","share":{"visibility":"team"}}`, alicesFrame("read")), 200, &r)
		if a, _ := ag.aclOf(r.ID); a.visibility != visTeam || a.owner != "alice" {
			t.Fatalf("shared at once, unpartitioned: %+v", a)
		}
		serveJSON(t, h, as("POST", "/ask", `{"text":"y"}`, alicesFrame("read")), 200, &r)
		if a, _ := ag.aclOf(r.ID); a.visibility != visPrivate {
			t.Fatalf("a plain ask, unpartitioned: %+v", a)
		}
	})
}

// sseReader follows one stream through the route table as a caller.
type sseReader struct {
	mu     sync.Mutex
	evs    []Event
	cancel context.CancelFunc
	done   chan struct{}
}

func followAs(t *testing.T, h http.Handler, target string, hdr map[string]string) *sseReader {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	r := as("GET", target, "", hdr).WithContext(ctx)
	pr, pw := ioPipe()
	w := &streamRecorder{ResponseRecorder: httptest.NewRecorder(), pw: pw}
	s := &sseReader{cancel: cancel, done: make(chan struct{})}
	go func() { h.ServeHTTP(w, r); pw.Close() }()
	go func() {
		defer close(s.done)
		sc := bufio.NewScanner(pr)
		sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var ev Event
			if json.Unmarshal([]byte(data), &ev) == nil {
				s.mu.Lock()
				s.evs = append(s.evs, ev)
				s.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-s.done })
	return s
}

// text is the draft text the stream's deltas built for run so far, and
// whether it saw the finished assistant message.
func (s *sseReader) text(t *testing.T, run int64) (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	cur := map[string]string{}
	done := false
	for i := range s.evs {
		ev := s.evs[i]
		switch ev.Type {
		case evText, evText + ".delta":
			if ev.Run == run {
				applyDraft(t, cur, &ev)
			}
		case evMessage:
			if m, ok := ev.Data.(map[string]any); ok && ev.Run == run && m["role"] == "assistant" {
				done = true
			}
		}
	}
	return cur["text:"+itoa(run)], done
}

// TestSharedRunStreamsToEveryMember (90 §I4): everyone in a shared
// conversation sees its run streaming at the same time — each member's page
// follows the global instance's stream, so two people's streams of one
// shared run both receive the deltas as the model writes them. A stranger's
// stream of it is refused.
func TestSharedRunStreamsToEveryMember(t *testing.T) {
	ag, h := globalAgent(t)
	f := fakeOf(ag)
	f.on(lastUser("tell us"), say("Once upon a time, together")).
		emit(LLMEvent{Kind: "text", Text: "Once"}, LLMEvent{Kind: "text", Text: "Once upon a time"}).
		block("g")
	var run Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"hold on","hold":true,"share":{"members":[{"user":"bob","role":"viewer"}]}}`, f5("alice", "read")), 200, &run)
	alice := followAs(t, h, fmt.Sprintf("/stream?run=%d&deltas=1", run.ID), f5("alice", "read"))
	bob := followAs(t, h, fmt.Sprintf("/stream?run=%d&deltas=1", run.ID), f5("bob", "read"))
	if rec := httptest.NewRecorder(); true {
		h.ServeHTTP(rec, as("GET", fmt.Sprintf("/stream?run=%d", run.ID), "", f5("dave", "read")))
		if rec.Code != 404 {
			t.Fatalf("a stranger follows the shared run: %d", rec.Code)
		}
	}
	waitFor(t, "both streams to say hello", func() bool {
		a, _ := alice.text(t, run.ID)
		b, _ := bob.text(t, run.ID)
		alice.mu.Lock()
		bob.mu.Lock()
		ok := len(alice.evs) > 0 && len(bob.evs) > 0
		bob.mu.Unlock()
		alice.mu.Unlock()
		return ok && a == "" && b == ""
	})
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", run.ID), `{"text":"tell us a story"}`, f5("alice", "read")), 200, nil)
	waitFor(t, "both streams to carry the draft while the model is still writing", func() bool {
		a, _ := alice.text(t, run.ID)
		b, _ := bob.text(t, run.ID)
		return a == "Once upon a time" && b == "Once upon a time"
	})
	if f.inFlight() != 1 {
		t.Fatal("the model call ended before both had seen the draft")
	}
	// the model writes on (one more piece of the same draft): a delta each
	ag.eng.mu.Lock()
	d := ag.eng.drafts[run.ID]
	started, model := d.Started, d.Model
	ag.eng.mu.Unlock()
	ev := textEv(run.ID, "Once upon a time, together", started)
	ev.Data.(map[string]any)["model"] = model
	ag.eng.hub.publish(ev)
	waitFor(t, "both streams to carry the next piece", func() bool {
		a, _ := alice.text(t, run.ID)
		b, _ := bob.text(t, run.ID)
		return a == "Once upon a time, together" && b == a
	})
	f.release("g")
	waitFor(t, "both streams to see the answer", func() bool {
		_, a := alice.text(t, run.ID)
		_, b := bob.text(t, run.ID)
		return a && b
	})
	for who, s := range map[string]*sseReader{"alice": alice, "bob": bob} {
		s.mu.Lock()
		deltas := 0
		for _, ev := range s.evs {
			if ev.Type == evText+".delta" && ev.Run == run.ID {
				deltas++
			}
		}
		s.mu.Unlock()
		if deltas == 0 {
			var types []string
			s.mu.Lock()
			for _, ev := range s.evs {
				b, _ := json.Marshal(ev.Data)
				types = append(types, ev.Type+" "+clip(string(b), 80))
			}
			s.mu.Unlock()
			t.Errorf("%s's stream carried the draft without a delta: %s", who, strings.Join(types, "\n"))
		}
	}
}

// userAgent is a person's partition (alice's) with conf known.
func userAgent(t *testing.T) (*Agent, http.Handler) {
	t.Helper()
	setMode(t, modeUser, "alice")
	kv := newMemKV()
	confIn = newConfReader(kv, nil)
	putConf(kv, "", `{"config":`+strconvQuote(mustJSON(defaultConfig()))+`}`)
	return homeAgent(t)
}

// TestPublishAndCopy: a person publishes a copy of their conversation to
// the shared space (the partition sends global its transcript — files only
// when asked — and deletes the original unless kept); global makes it a
// shared conversation of theirs with the transcript, the files and a note;
// and a shared conversation copies back into their own space as a private
// one.
func TestPublishAndCopy(t *testing.T) {
	ag, h := userAgent(t)
	var mine Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"plan the offsite"}`, alicesFrame("read")), 200, &mine)
	waitStatus(t, ag.db, mine.ID, statusIdle)
	if _, err := ag.db.replPutFile(mine.ID, "plan.md", "# offsite", 0); err != nil {
		t.Fatal(err)
	}
	var sent importBody
	g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		if method == "POST" && path == "/import" {
			sent = importBody{}
			_ = json.Unmarshal(body, &sent)
			return 200, `{"id":7,"title":"plan the offsite","owner":"alice","visibility":"team"}`
		}
		return 404, `{"error":"no"}`
	})
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{}`, alicesFrame("read")), 400, nil)
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{"share":{"visibility":"team"}}`, map[string]string{
		"X-XBin-From": "apps/agent", "X-XBin-Role": "admin", "X-XBin-User": "bob", "X-XBin-User-Level": "read", "X-XBin-Partition": "user:alice"}), 404, nil)
	var out struct {
		Run     Run
		Deleted bool
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{"share":{"visibility":"team"},"files":true,"keep":true}`, alicesFrame("read")), 200, &out)
	if out.Run.ID != 7 || out.Deleted {
		t.Fatalf("publish (kept): %+v", out)
	}
	if sent.Share == nil || sent.Share.Visibility != visTeam || sent.Conversation == nil || len(sent.Conversation.Files) != 1 ||
		sent.Conversation.Files[0].Content != "# offsite" || !hasMsg(sent.Conversation, "user", "plan the offsite") || !hasMsg(sent.Conversation, "assistant", "ok") {
		t.Fatalf("what went to global: %+v", sent)
	}
	for _, m := range sent.Conversation.Messages {
		if m.Role == "system" {
			t.Fatal("the system prompt travelled")
		}
	}
	if _, err := ag.db.getRun(mine.ID); err != nil {
		t.Fatal("kept, yet gone")
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{"share":{"members":[{"user":"bob"}]}}`, alicesFrame("read")), 200, &out)
	if !out.Deleted || len(sent.Conversation.Files) != 0 {
		t.Fatalf("publish (moved, no files): %+v, files %d", out, len(sent.Conversation.Files))
	}
	if _, err := ag.db.getRun(mine.ID); err == nil {
		t.Fatal("published without keep, the original stayed")
	}
	if n := len(g.got()); n != 2 {
		t.Fatalf("calls to global: %v", g.got())
	}
	bundle := sent.Conversation

	// global takes it: a shared conversation of alice's, with the transcript
	t.Run("import", func(t *testing.T) {
		ag, h := globalAgent(t)
		b, _ := json.Marshal(importBody{Conversation: bundle})
		serveJSON(t, h, as("POST", "/import", string(b), f5("alice", "read")), 409, nil) // a person's needs share
		b, _ = json.Marshal(importBody{Conversation: bundle, Share: &shareSpec{Members: []shareMember{{User: "bob", Role: roleViewer}}}})
		var r Run
		serveJSON(t, h, as("POST", "/import", string(b), f5("alice", "read")), 200, &r)
		if r.ID >= partitionIDBase || r.Owner != "alice" || r.Status != statusIdle {
			t.Fatalf("the shared copy: %+v", r)
		}
		var v struct {
			Messages []struct{ Role, Content string }
			Steps    []struct{ Kind, Detail string }
			Access   string
		}
		serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", r.ID), "", f5("bob", "read")), 200, &v)
		if v.Access != "viewer" || len(v.Messages) < 3 || v.Messages[1].Content != "plan the offsite" || !strings.Contains(fmt.Sprint(v.Steps), "Published from alice") {
			t.Fatalf("bob's view of the copy: %+v", v)
		}
		// it goes on at global: alice talks in it
		serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", r.ID), `{"text":"and the budget"}`, f5("alice", "read")), 200, nil)
		waitFor(t, "the copy to answer", func() bool {
			msgs, _ := ag.db.messages(r.ID, false)
			return len(msgs) > 0 && msgs[len(msgs)-1].Role == "assistant" && len(msgs) >= 5
		})
		// files come along, binary ones too
		fb := &convBundle{Version: bundleVersion, Title: "f", Messages: []bundleMsg{{Role: "user", Content: "see", Files: []string{"a.png"}}},
			Files: []bundleFile{{Path: "a.png", Mime: "image/png", Data: []byte("\x89PNG\r\n\x1a\nfake"), Binary: true}, {Path: "n.md", Content: "n"}}}
		b, _ = json.Marshal(importBody{Conversation: fb, Share: &shareSpec{Visibility: visTeam}})
		serveJSON(t, h, as("POST", "/import", string(b), f5("alice", "read")), 200, &r)
		fs, _ := ag.db.replFiles(r.ID)
		if len(fs) != 2 || len(ag.db.messageFiles(r.ID)) != 1 {
			t.Fatalf("the files of the copy: %d, carried %v", len(fs), ag.db.messageFiles(r.ID))
		}
		// POST /import is global's: a partition copies with POST /copy
		serveJSON(t, h, as("POST", "/runs/1/publish", `{"share":{"visibility":"team"}}`, f5("alice", "read")), 409, nil)
	})

	// …and a shared one copies back into alice's own space
	g.reply = func(method, path string, body []byte) (int, string) {
		if method == "GET" && path == "/runs/5/export?files=1" {
			b, _ := json.Marshal(bundle)
			return 200, string(b)
		}
		return 404, `{"error":"no such run"}`
	}
	var cp Run
	serveJSON(t, h, as("POST", "/copy", `{"from":5,"files":true}`, alicesFrame("read")), 200, &cp)
	if cp.ID < partitionIDBase || cp.Owner != "alice" || cp.Visibility != visPrivate {
		t.Fatalf("the private copy: %+v", cp)
	}
	if msgs, _ := ag.db.messages(cp.ID, false); len(msgs) < 3 || msgs[1].Content != "plan the offsite" {
		t.Fatalf("the private copy's transcript: %d", len(msgs))
	}
	serveJSON(t, h, as("POST", "/copy", `{"from":6}`, alicesFrame("read")), 404, nil)
	serveJSON(t, h, as("POST", "/copy", fmt.Sprintf(`{"from":%d}`, partitionIDBase+1), alicesFrame("read")), 400, nil)
	serveJSON(t, h, as("POST", "/import", `{}`, alicesFrame("read")), 409, nil)
}

func hasMsg(b *convBundle, role, text string) bool {
	for _, m := range b.Messages {
		if m.Role == role && strings.Contains(m.Content, text) {
			return true
		}
	}
	return false
}

// TestJoinAtTheSharedSpace: a join link is the shared space's — redeemed in
// a person's partition, it is forwarded to the global instance.
func TestJoinAtTheSharedSpace(t *testing.T) {
	_, h := userAgent(t)
	g := stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		return 200, `{"runId":3,"title":"t","role":"viewer"}`
	})
	var out struct{ RunID int64 }
	serveJSON(t, h, as("POST", "/join", `{"token":"abc"}`, alicesFrame("read")), 200, &out)
	if out.RunID != 3 || fmt.Sprint(g.got()) != `[POST /join {"token":"abc"}]` {
		t.Fatalf("join forwarded: %+v %v", out, g.got())
	}
}

// TestNoHomeRoutesUnpartitioned: an unpartitioned agent has none of the
// copies' routes (its surface is today's).
func TestNoHomeRoutesUnpartitioned(t *testing.T) {
	setMode(t, modeLegacy, "")
	ag, h := partAgent(t)
	id, _ := ag.db.createRun("x", "", 0)
	for _, r := range []*http.Request{
		as("GET", fmt.Sprintf("/runs/%d/export", id), "", alicesFrame("write")),
		as("POST", "/import", `{}`, alicesFrame("write")),
		as("POST", fmt.Sprintf("/runs/%d/publish", id), `{}`, alicesFrame("write")),
		as("POST", "/copy", `{}`, alicesFrame("write")),
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, r)
		if rec.Code != 404 && rec.Code != 405 {
			t.Errorf("%s %s unpartitioned: %d %s", r.Method, r.URL.Path, rec.Code, rec.Body)
		}
	}
}
