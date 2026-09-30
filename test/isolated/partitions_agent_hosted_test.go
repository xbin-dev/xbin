//go:build linux && integration

package isolated

// partitions_agent_hosted_test.go — the hosted-chats case of
// TestPartitionsAgent (work pack B2d of the partitioned-tiles plan; the
// template's API.md "Non-secure conversations"): a shared conversation that
// uses one person's private resources is hosted by that person's partition.
//
//   - alice hosts a shared conversation of hers with bob, from her own
//     partition (POST /hosting): the global instance moves it into the
//     shared `team` database (an id from 2^39), bob reads it at global and
//     sees who hosts it, dave can't;
//   - bob writes in it at global: the input goes into team, xbind's
//     partition mail rings alice's partition, and alice's partition's engine
//     answers — while it writes, bob's stream at the global instance carries
//     its deltas live (≥ 2 before the answer: the host posts its run to its
//     global instance, which fans it out to the members, 90 §I4);
//   - no join links; a new member pauses it (members' messages 409) until
//     alice confirms from her partition; then it runs again;
//   - "Add a copy of my …": a file of alice's own conversation, copied into
//     another shared conversation, is read there by bob, while the original
//     stays hers alone.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/xbin-dev/xbin/test/xbindtest"
)

const pa2to39 = int64(1) << 39

func paHostedChats(t *testing.T, e *psEnv) {
	d := e.d
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
	transcript := func(person string, id int64, global bool) string {
		t.Helper()
		var r paRun
		must(200, person, "GET", fmt.Sprintf("/runs/%d", id), nil, global).Decode(t, &r)
		return fmt.Sprint(r.Messages)
	}
	type hosting struct {
		Host, State, PendingKey string
	}
	hostingOf := func(person string, id int64) hosting {
		t.Helper()
		var v struct{ Hosted hosting }
		must(200, person, "GET", fmt.Sprintf("/runs/%d/view", id), nil, true).Decode(t, &v)
		return v.Hosted
	}

	// a shared conversation of alice's with bob, at the global instance
	var shared struct{ ID int64 }
	must(200, "alice", "POST", "/ask", map[string]any{"text": "our hosted plan",
		"share": map[string]any{"members": []map[string]string{{"user": "bob", "role": "participant"}}}}, true).Decode(t, &shared)
	xbindtest.Eventually(t, 3*time.Minute, "the shared conversation answers", func() (bool, string) {
		s := transcript("alice", shared.ID, true)
		return strings.Contains(s, "{assistant "), s
	})

	// alice hosts it from her own partition: it moves into team
	if st := call("bob", "POST", "/hosting", map[string]any{"conversation": shared.ID}, true).Status; st != 404 {
		t.Errorf("hosting asked of the global instance: %d, want 404 (a person's own partition hosts)", st)
	}
	var hosted struct {
		Conversation int64
		State        string
	}
	must(200, "alice", "POST", "/hosting", map[string]any{"conversation": shared.ID,
		"seen": map[string]any{"owner": "alice", "visibility": "private", "teamRole": "viewer", "members": map[string]string{"bob": "participant"}}},
		false).Decode(t, &hosted)
	id := hosted.Conversation
	if id < pa2to39 || id >= pa2to40 || hosted.State != "active" {
		t.Fatalf("hosted: %+v (want an id in [2^39, 2^40), active)", hosted)
	}
	if st := call("alice", "GET", fmt.Sprintf("/runs/%d", shared.ID), nil, true).Status; st != 404 {
		t.Errorf("the moved conversation still answers at its old id: %d", st)
	}
	if h := hostingOf("bob", id); h.Host != "alice" || h.State != "active" {
		t.Errorf("bob's view of its hosting: %+v", h)
	}
	if !strings.Contains(transcript("bob", id, true), "our hosted plan") {
		t.Errorf("the hosted conversation's transcript at global lacks what was said")
	}
	if st := call("dave", "GET", fmt.Sprintf("/runs/%d/view", id), nil, true).Status; st != 404 {
		t.Errorf("dave reads the hosted conversation: %d, want 404", st)
	}
	if st := call("alice", "POST", fmt.Sprintf("/runs/%d/links", id), map[string]string{"role": "viewer"}, true).Status; st != 409 {
		t.Errorf("a join link on a hosted conversation: %d, want 409", st)
	}

	// live for every member (90 §I4): alice's partition runs it, bob follows at global
	sb := paFollow(t, d, e.fr(t, paAgent, "bob"), id)
	sa := paFollow(t, d, e.fr(t, paAgent, "alice"), id)
	xbindtest.Eventually(t, time.Minute, "both streams say hello", func() (bool, string) {
		return sa.count(0, "hello", nil) > 0 && sb.count(0, "hello", nil) > 0, ""
	})
	must(200, "bob", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "paras 12"}, true)
	isAnswer := func(raw json.RawMessage) bool {
		var m struct{ Role, Content string }
		return json.Unmarshal(raw, &m) == nil && m.Role == "assistant" && strings.Contains(m.Content, "Paragraph 12")
	}
	xbindtest.Eventually(t, 3*time.Minute, "bob's stream carries alice's run's deltas while it is written", func() (bool, string) {
		n := sb.count(id, "text.delta", nil)
		return n >= 2, fmt.Sprintf("bob %d deltas (alice %d); bob's last events: %s", n, sa.count(id, "text.delta", nil), sb.last(4))
	})
	if sb.count(id, "message", isAnswer) != 0 {
		t.Errorf("the answer was done before bob's stream had its deltas: not live")
	}
	xbindtest.Eventually(t, 3*time.Minute, "both streams see the answer", func() (bool, string) {
		a, b := sa.count(id, "message", isAnswer), sb.count(id, "message", isAnswer)
		return a > 0 && b > 0, fmt.Sprintf("alice %d, bob %d; bob's last events: %s", a, b, sb.last(4))
	})
	if !strings.Contains(transcript("bob", id, true), "Paragraph 12") {
		t.Errorf("the hosted run's answer isn't in its transcript")
	}

	// a wider audience pauses it until alice confirms
	must(200, "alice", "POST", fmt.Sprintf("/runs/%d/members", id), map[string]string{"user": "dave", "role": "viewer"}, true)
	var h hosting
	xbindtest.Eventually(t, time.Minute, "alice's partition pauses it", func() (bool, string) {
		h = hostingOf("bob", id)
		return h.State == "paused" && h.PendingKey != "", fmt.Sprintf("%+v", h)
	})
	if st := call("bob", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "still there?"}, true).Status; st != 409 {
		t.Errorf("bob writes while it is paused: %d, want 409", st)
	}
	if st := call("bob", "POST", fmt.Sprintf("/hosting/%d/confirm", id), map[string]string{"seen": h.PendingKey}, false).Status; st != 404 {
		t.Errorf("bob confirms alice's hosting from his partition: %d, want 404", st)
	}
	must(200, "alice", "POST", fmt.Sprintf("/hosting/%d/confirm", id), map[string]string{"seen": h.PendingKey}, false)
	if h := hostingOf("dave", id); h.State != "active" {
		t.Errorf("after alice confirmed: %+v", h)
	}
	answers := strings.Count(transcript("dave", id, true), "{assistant ")
	must(200, "bob", "POST", fmt.Sprintf("/runs/%d/message", id), map[string]string{"text": "and again"}, true)
	xbindtest.Eventually(t, 3*time.Minute, "the confirmed conversation answers again", func() (bool, string) {
		s := transcript("dave", id, true)
		return strings.Count(s, "{assistant ") > answers && strings.Contains(s, "and again"), s
	})

	// "Add a copy of my …": alice's own file into another shared conversation
	var mine, other struct{ ID int64 }
	must(200, "alice", "POST", "/ask", map[string]string{"text": "my own plan"}, false).Decode(t, &mine)
	must(200, "alice", "PUT", fmt.Sprintf("/runs/%d/file", mine.ID), map[string]string{"path": "plan.md", "content": "alice-plan-copied"}, false)
	must(200, "alice", "POST", "/ask", map[string]any{"text": "another shared one",
		"share": map[string]any{"members": []map[string]string{{"user": "bob", "role": "viewer"}}}}, true).Decode(t, &other)
	must(200, "alice", "POST", "/copyin", map[string]any{"conversation": other.ID,
		"files": []map[string]any{{"run": mine.ID, "path": "plan.md"}}}, false)
	r := must(200, "bob", "GET", fmt.Sprintf("/runs/%d/file?path=from-alice/plan.md", other.ID), nil, true)
	if !strings.Contains(string(r.Body), "alice-plan-copied") {
		t.Errorf("bob reads the copy: %s", r)
	}
	if r := call("alice", "GET", fmt.Sprintf("/runs/%d/file?path=plan.md", mine.ID), nil, false); r.Status != 200 || !strings.Contains(string(r.Body), "alice-plan-copied") {
		t.Errorf("alice's original: %d %s", r.Status, r)
	}
	for _, global := range []bool{false, true} {
		if r := call("bob", "GET", fmt.Sprintf("/runs/%d/file?path=plan.md", mine.ID), nil, global); strings.Contains(string(r.Body), "alice-plan-copied") {
			t.Errorf("BUG: bob reads alice's original (global %v): %d %s", global, r.Status, r)
		}
	}
	if st := call("bob", "POST", "/copyin", map[string]any{"conversation": other.ID,
		"files": []map[string]any{{"run": mine.ID, "path": "plan.md"}}}, false).Status; st != 404 {
		t.Errorf("bob copies alice's file from his partition: %d, want 404", st)
	}
}
