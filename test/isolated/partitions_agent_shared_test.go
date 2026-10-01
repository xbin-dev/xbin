//go:build linux && integration

package isolated

// partitions_agent_shared_test.go — the shared-chats case of
// TestPartitionsAgent (work pack B2b of the partitioned-tiles plan; the
// template's API.md "Partitioned instances" → "Shared conversations"): a
// shared conversation lives in the global instance's db and runs there;
// people's pages reach it through xbind's own-global addressing
// (?xbin-partition=global, attributed to the person), and the global
// instance applies the sharing rules to them.
//
//   - alice's plain ask at global is refused (409): the shared space keeps
//     shared conversations; one she shares with bob is made there (id below
//     2^40), bob lists it, dave can't read it;
//   - everyone in it sees its run streaming at the same time (the owner's
//     ruling, 90 §I4): alice's and bob's streams of it, both through
//     global, both receive the answer's deltas as the model writes them;
//   - publish: a private conversation of alice's goes to the shared space as
//     a copy (kept, or moved); bob reads the copy, the original stays hers;
//   - copy back: bob makes a private copy of the shared chat in his own
//     partition (id from 2^40);
//   - a bundle alice imports herself can't name bob as a message's writer
//     (it comes in as a copy labelled bob), and her draft upload at global
//     is refused (409): no private run of hers is left in the shared space;
//   - a join link made at global is redeemed from dave's partition
//     (forwarded to global), and dave reads the chat.

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

// paEvent is a stream event as the test reads it.
type paEvent struct {
	Type string          `json:"type"`
	Run  int64           `json:"run"`
	Data json.RawMessage `json:"data"`
}

// paStream follows one conversation's stream as a person, through global.
type paStream struct {
	mu  sync.Mutex
	evs []paEvent
}

func (s *paStream) count(run int64, typ string, match func(json.RawMessage) bool) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, ev := range s.evs {
		if ev.Run == run && ev.Type == typ && (match == nil || match(ev.Data)) {
			n++
		}
	}
	return n
}

// last is the stream's last n events, for a failure message.
func (s *paStream) last(n int) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, ev := range s.evs[max(0, len(s.evs)-n):] {
		d := string(ev.Data)
		if len(d) > 160 {
			d = d[:160] + "…"
		}
		out = append(out, fmt.Sprintf("%s #%d %s", ev.Type, ev.Run, d))
	}
	return strings.Join(out, " | ")
}

// paFollow opens GET /stream?run=<id>&deltas=1&xbin-partition=global with
// person's frame token, as their page does for a shared conversation.
func paFollow(t *testing.T, d *xbindtest.Daemon, frame xbindtest.Header, run int64) *paStream {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/%s/stream?run=%d&deltas=1&xbin-partition=global", d.URL, paAgent, run), nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(frame.K, frame.V)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		cancel()
		t.Fatalf("following run %d: %v", run, err)
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		cancel()
		t.Fatalf("following run %d: HTTP %d", run, resp.StatusCode)
	}
	s := &paStream{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		sc.Buffer(make([]byte, 0, 64<<10), 8<<20)
		for sc.Scan() {
			data, ok := strings.CutPrefix(sc.Text(), "data: ")
			if !ok {
				continue
			}
			var ev paEvent
			if json.Unmarshal([]byte(data), &ev) == nil {
				s.mu.Lock()
				s.evs = append(s.evs, ev)
				s.mu.Unlock()
			}
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	return s
}

func paSharedChats(t *testing.T, e *psEnv) {
	d := e.d
	// call is person's call from their frame: at their own partition, or
	// (global) at the global instance.
	call := func(person, method, path string, body any, global bool) xbindtest.Resp {
		t.Helper()
		p := "/api/" + paAgent + path
		if global {
			if strings.Contains(p, "?") {
				p += "&xbin-partition=global"
			} else {
				p += "?xbin-partition=global"
			}
		}
		return d.Call(t, method, p, body, e.fr(t, paAgent, person))
	}
	must := func(want int, person, method, path string, body any, global bool) xbindtest.Resp {
		t.Helper()
		r := call(person, method, path, body, global)
		if r.Status != want {
			t.Fatalf("%s: %s %s (global %v): %d %s (want %d)", person, method, path, global, r.Status, r, want)
		}
		return r
	}
	type run struct {
		ID                int64
		Owner, Visibility string
	}
	listed := func(person, scope string, global bool) map[int64]bool {
		t.Helper()
		var out struct{ Pinned, Items []struct{ ID int64 } }
		must(200, person, "GET", "/conversations?scope="+scope, nil, global).Decode(t, &out)
		ids := map[int64]bool{}
		for _, it := range append(out.Pinned, out.Items...) {
			ids[it.ID] = true
		}
		return ids
	}
	transcript := func(person string, id int64, global bool) string {
		t.Helper()
		var r paRun
		must(200, person, "GET", fmt.Sprintf("/runs/%d", id), nil, global).Decode(t, &r)
		return fmt.Sprint(r.Messages)
	}

	// the shared space keeps shared conversations
	if r := call("alice", "POST", "/ask", map[string]string{"text": "a private one at global?"}, true); r.Status != 409 {
		t.Errorf("alice's unshared ask at global: %d %s, want 409", r.Status, r)
	}
	if r := call("alice", "POST", "/ask", map[string]any{"text": "x", "share": map[string]string{"visibility": "team"}}, false); r.Status != 409 {
		t.Errorf("a shared ask in alice's own partition: %d %s, want 409", r.Status, r)
	}
	var shared run
	must(200, "alice", "POST", "/ask", map[string]any{"text": "our shared plan", "hold": true,
		"share": map[string]any{"members": []map[string]string{{"user": "bob", "role": "participant"}}}}, true).Decode(t, &shared)
	if shared.ID <= 0 || shared.ID >= pa2to40 || shared.Owner != "alice" {
		t.Fatalf("the shared conversation: %+v (want an id below 2^40, alice's)", shared)
	}
	if !listed("bob", "shared", true)[shared.ID] || !listed("alice", "shared", true)[shared.ID] {
		t.Errorf("the shared conversation isn't in alice's and bob's Shared lists at global")
	}
	if listed("bob", "mine", false)[shared.ID] {
		t.Errorf("BUG: bob's own partition lists a conversation of global's")
	}
	if st := call("dave", "GET", fmt.Sprintf("/runs/%d", shared.ID), nil, true).Status; st != 404 {
		t.Errorf("dave (not in it) reads the shared conversation: %d, want 404", st)
	}

	// live for every member (90 §I4): both streams, one run, the same deltas
	sa := paFollow(t, d, e.fr(t, paAgent, "alice"), shared.ID)
	sb := paFollow(t, d, e.fr(t, paAgent, "bob"), shared.ID)
	xbindtest.Eventually(t, time.Minute, "both streams say hello", func() (bool, string) {
		return sa.count(0, "hello", nil) > 0 && sb.count(0, "hello", nil) > 0, ""
	})
	must(200, "bob", "POST", fmt.Sprintf("/runs/%d/message", shared.ID), map[string]string{"text": "paras 12"}, true)
	isAnswer := func(raw json.RawMessage) bool {
		var m struct{ Role, Content string }
		return json.Unmarshal(raw, &m) == nil && m.Role == "assistant" && strings.Contains(m.Content, "Paragraph 12")
	}
	// both see the draft while the model still writes: deltas at each
	// before either stream has the finished answer
	xbindtest.Eventually(t, 3*time.Minute, "both streams carry the answer's deltas while it is written", func() (bool, string) {
		da, db := sa.count(shared.ID, "text.delta", nil), sb.count(shared.ID, "text.delta", nil)
		return da >= 2 && db >= 2, fmt.Sprintf("alice %d, bob %d deltas", da, db)
	})
	if sa.count(shared.ID, "message", isAnswer)+sb.count(shared.ID, "message", isAnswer) != 0 {
		t.Errorf("the answer was done before both streams had its deltas: not live for both")
	}
	xbindtest.Eventually(t, 3*time.Minute, "both streams see the answer", func() (bool, string) {
		a, b := sa.count(shared.ID, "message", isAnswer), sb.count(shared.ID, "message", isAnswer)
		return a > 0 && b > 0, fmt.Sprintf("alice %d, bob %d; alice's last events: %s", a, b, sa.last(4))
	})
	if !strings.Contains(transcript("alice", shared.ID, true), "Paragraph 12") {
		t.Errorf("the shared run's answer isn't in its transcript at global")
	}

	// publish: a copy of alice's private conversation goes to the shared space
	var draft run
	must(200, "alice", "POST", "/ask", map[string]string{"text": "hello from alice-draft"}, false).Decode(t, &draft)
	xbindtest.Eventually(t, 3*time.Minute, "alice's draft answers", func() (bool, string) {
		s := transcript("alice", draft.ID, false)
		return strings.Contains(s, "Hello from the fake model."), s
	})
	var pub struct {
		Run     run
		Deleted bool
	}
	must(200, "alice", "POST", fmt.Sprintf("/runs/%d/publish", draft.ID), map[string]any{
		"share": map[string]string{"visibility": "team", "teamRole": "viewer"}, "keep": true}, false).Decode(t, &pub)
	if pub.Run.ID <= 0 || pub.Run.ID >= pa2to40 || pub.Run.Owner != "alice" || pub.Deleted {
		t.Fatalf("published (kept): %+v", pub)
	}
	if s := transcript("bob", pub.Run.ID, true); !strings.Contains(s, "alice-draft") {
		t.Errorf("bob reads the published copy at global: %s", s)
	}
	if st := call("bob", "POST", fmt.Sprintf("/runs/%d/message", pub.Run.ID), map[string]string{"text": "hi"}, true).Status; st != 403 {
		t.Errorf("bob (the team reads) writes in the copy: %d, want 403", st)
	}
	must(200, "alice", "GET", fmt.Sprintf("/runs/%d", draft.ID), nil, false) // the original stays hers
	if st := call("bob", "GET", fmt.Sprintf("/runs/%d", draft.ID), nil, false).Status; st == 200 {
		t.Errorf("BUG: bob reads alice's private original")
	}
	must(200, "alice", "POST", fmt.Sprintf("/runs/%d/publish", draft.ID), map[string]any{
		"share": map[string]any{"members": []map[string]string{{"user": "bob"}}}}, false).Decode(t, &pub)
	if !pub.Deleted || call("alice", "GET", fmt.Sprintf("/runs/%d", draft.ID), nil, false).Status != 404 {
		t.Errorf("published without keep: deleted %v, the original still answers", pub.Deleted)
	}

	// copy back: bob's own private copy of the shared chat
	var cp run
	must(200, "bob", "POST", "/copy", map[string]any{"from": shared.ID}, false).Decode(t, &cp)
	if cp.ID < pa2to40 || cp.Owner != "bob" || cp.Visibility != "private" {
		t.Fatalf("bob's private copy: %+v", cp)
	}
	if s := transcript("bob", cp.ID, false); !strings.Contains(s, "Paragraph 12") {
		t.Errorf("bob's copy lacks the shared transcript: %s", s)
	}
	if st := call("dave", "POST", "/copy", map[string]any{"from": shared.ID}, false).Status; st != 404 {
		t.Errorf("dave copies a conversation he can't read: %d, want 404", st)
	}

	// a person's word names only them: a bundle alice made herself saying
	// bob wrote something comes in as a copy labelled bob, never as bob's
	var forged run
	must(200, "alice", "POST", "/import", map[string]any{
		"conversation": map[string]any{"version": 1, "title": "forged", "messages": []map[string]any{
			{"role": "user", "sender": "bob", "content": "bob approves the budget"}, {"role": "assistant", "content": "noted"}}},
		"share": map[string]string{"visibility": "team"}}, true).Decode(t, &forged)
	var fv struct {
		Messages []struct{ Role, Content, Sender, Origin, Label string }
	}
	must(200, "dave", "GET", fmt.Sprintf("/runs/%d/view", forged.ID), nil, true).Decode(t, &fv)
	for _, m := range fv.Messages {
		if m.Role == "user" && (m.Sender != "" || m.Origin != "copy" || m.Label != "bob") {
			t.Errorf("BUG: a hand-made bundle's message reads as %q's (origin %q, label %q), want a copy labelled bob", m.Sender, m.Origin, m.Label)
		}
	}
	// …nor does a new chat's draft leave a private run of hers at global
	if st := call("alice", "PUT", "/ask/upload?draft=abcdefgh12&name=a.txt", "hello", true).Status; st != 409 {
		t.Errorf("alice's draft upload at global: %d, want 409", st)
	}

	// a join link, made at global, redeemed from dave's own partition
	var link struct{ Token string }
	must(200, "alice", "POST", fmt.Sprintf("/runs/%d/links", shared.ID), map[string]string{"role": "viewer"}, true).Decode(t, &link)
	var joined struct{ RunID int64 }
	must(200, "dave", "POST", "/join", map[string]string{"token": link.Token}, false).Decode(t, &joined)
	if joined.RunID != shared.ID {
		t.Errorf("dave joined %d, want %d", joined.RunID, shared.ID)
	}
	if s := transcript("dave", shared.ID, true); !strings.Contains(s, "Paragraph 12") {
		t.Errorf("dave, joined, reads the shared chat: %s", s)
	}
}
