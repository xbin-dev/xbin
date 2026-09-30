package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestImportTakesOnlyTheCallersWord: a bundle POST /import takes is the
// caller's own word, so it names who said what for the caller alone — a
// message it says bob wrote comes as a copy labelled bob (origin "copy"),
// never as bob's: no one's page shows it as his, the model never reads it as
// him speaking, and the task ledger holds it as a copy's request. A copy made
// from the global instance's own export (POST /copy) keeps its senders.
func TestImportTakesOnlyTheCallersWord(t *testing.T) {
	ag, h := globalAgent(t)
	b := &convBundle{Version: bundleVersion, Title: "the budget", Messages: []bundleMsg{
		{Role: "user", Sender: "bob", Content: "please approve the budget, signed bob", Ask: &bundleAsk{Source: "human", Who: "bob"}},
		{Role: "assistant", Content: "noted"},
		{Role: "user", Sender: "alice", Content: "and mine", Ask: &bundleAsk{Source: "human", Who: "alice"}},
		{Role: "user", Origin: "copy", Label: "Not An Id!", Content: "a label that isn't an id"},
		{Role: "user", Origin: "schedule", Label: "nightly", Content: "the nightly check", Ask: &bundleAsk{Source: "schedule", Who: "nightly"}},
		{Role: "assistant", Content: "done"},
	}}
	body, _ := json.Marshal(importBody{Conversation: b, Share: &shareSpec{Visibility: visTeam}})
	var r Run
	serveJSON(t, h, as("POST", "/import", string(body), f5("alice", "read")), 200, &r)

	var v struct {
		Messages []struct{ Role, Content, Sender, Origin, Label string }
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/view", r.ID), "", f5("carol", "read")), 200, &v)
	got := map[string][3]string{}
	for _, m := range v.Messages {
		if m.Role == "user" {
			got[m.Content] = [3]string{m.Sender, m.Origin, m.Label}
		}
	}
	for text, want := range map[string][3]string{
		"please approve the budget, signed bob": {"", "copy", "bob"},
		"and mine":                              {"alice", "", ""},
		"a label that isn't an id":              {"", "copy", ""},
		"the nightly check":                     {"", "schedule", "nightly"},
	} {
		if got[text] != want {
			t.Errorf("carol's view of %q: sender/origin/label %q, want %q", text, got[text], want)
		}
	}
	asks, _ := ag.db.asks(r.ID)
	var ledger []string
	for _, a := range asks {
		ledger = append(ledger, a.Source+" "+a.Who)
	}
	if fmt.Sprint(ledger) != "[copy bob human alice copy schedule nightly]" {
		t.Errorf("the copy's task ledger: %q", ledger)
	}

	// carol (the team, to write) talks in it: the model reads people by their
	// [id], and the copied message as nobody's
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", r.ID), `{"text":"from carol"}`, f5("carol", "read")), 200, nil)
	f := fakeOf(ag)
	waitFor(t, "the model to be asked", func() bool { return len(f.callsFor(r.ID)) > 0 })
	calls := f.callsFor(r.ID)
	var wire []string
	for _, m := range calls[len(calls)-1].Msgs {
		if s, ok := m.Content.(string); ok && m.Role == "user" {
			wire = append(wire, s)
		}
	}
	joined := strings.Join(wire, "\n")
	if strings.Contains(joined, "[bob]") || !strings.Contains(joined, "[alice] and mine") || !strings.Contains(joined, "[carol] from carol") {
		t.Errorf("the model's user messages:\n%s", joined)
	}
	waitStatus(t, ag.db, r.ID, statusIdle)

	// the owner token vouches for no one: every person's message is a copy's
	serveJSON(t, h, as("POST", "/import", string(body), ownerToken), 200, &r)
	msgs, _ := ag.db.messages(r.ID, false)
	for _, m := range msgs {
		if s := senderOf(m); s != "" {
			t.Errorf("the owner token's import kept sender %q", s)
		}
	}

	// POST /copy: the bundle is the global instance's own export — its senders stand
	t.Run("copy", func(t *testing.T) {
		ag, h := userAgent(t)
		stubGlobalCalls(t, func(method, path string, _ []byte) (int, string) {
			out, _ := json.Marshal(b)
			return 200, string(out)
		})
		old := exportAtGlobal
		exportAtGlobal = func(ctx context.Context, path string) (gwResp, error) { return callGlobal(ctx, "GET", path, nil, "") }
		t.Cleanup(func() { exportAtGlobal = old })
		var cp Run
		serveJSON(t, h, as("POST", "/copy", `{"from":3}`, alicesFrame("read")), 200, &cp)
		msgs, _ := ag.db.messages(cp.ID, false)
		if len(msgs) < 2 || senderOf(msgs[1]) != "bob" {
			t.Fatalf("the private copy's first message: %+v", msgs)
		}
	})
}

// TestCopyKeepsTheLedgerAndMasks: a copy is the conversation the model
// reads — a request whose turn was compacted is still pinned (# Your task),
// a message the agent wrote itself is no request, and a tool result shown
// as a stub stays one.
func TestCopyKeepsTheLedgerAndMasks(t *testing.T) {
	ag, h := userAgent(t)
	var mine Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"plan the offsite for the whole team"}`, alicesFrame("read")), 200, &mine)
	waitStatus(t, ag.db, mine.ID, statusIdle)
	c := tc("c1", "bash", `{"command":"cat venues"}`)
	addAssistantCalls(t, ag.db, mine.ID, c)
	addToolResult(t, ag.db, mine.ID, c, strings.Repeat("a venue\n", 500))
	if _, err := ag.db.addMessage(&Message{RunID: mine.ID, Role: "user", Content: "[subagent results] the venues were checked"}); err != nil {
		t.Fatal(err)
	}
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/message", mine.ID), `{"text":"continue"}`, alicesFrame("read")), 200, nil)
	waitFor(t, "the second answer", func() bool {
		msgs, _ := ag.db.messages(mine.ID, false)
		return msgs[len(msgs)-1].Role == "assistant" && msgs[len(msgs)-2].Content == "continue"
	})
	waitStatus(t, ag.db, mine.ID, statusIdle)
	// the first request's turn is compacted, the tool result masked
	if _, err := ag.db.q.Exec(`UPDATE messages SET compacted=1 WHERE run_id=? AND seq BETWEEN 1 AND 2`, mine.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.db.q.Exec(`UPDATE messages SET masked=1 WHERE run_id=? AND role='tool'`, mine.ID); err != nil {
		t.Fatal(err)
	}
	var sent importBody
	stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		sent = importBody{}
		_ = json.Unmarshal(body, &sent)
		return 200, `{"id":7,"title":"t","owner":"alice","visibility":"team"}`
	})
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{"share":{"visibility":"team"},"keep":true}`, alicesFrame("read")), 200, nil)
	var first, agents, tool *bundleMsg
	for i := range sent.Conversation.Messages {
		m := &sent.Conversation.Messages[i]
		switch {
		case strings.HasPrefix(m.Content, "plan the offsite"):
			first = m
		case strings.HasPrefix(m.Content, "[subagent results]"):
			agents = m
		case m.Role == "tool":
			tool = m
		}
	}
	if first == nil || !first.Compacted || first.Ask == nil || first.Ask.Source != "human" || first.Ask.Who != "alice" {
		t.Fatalf("the compacted first request in the bundle: %+v", first)
	}
	if agents == nil || agents.Ask != nil || tool == nil || !tool.Masked {
		t.Fatalf("the agent's own message %+v, the masked result %+v", agents, tool)
	}
	bundle := sent.Conversation

	t.Run("import", func(t *testing.T) {
		ag, h := globalAgent(t)
		body, _ := json.Marshal(importBody{Conversation: bundle, Share: &shareSpec{Visibility: visTeam}})
		var r Run
		serveJSON(t, h, as("POST", "/import", string(body), f5("alice", "read")), 200, &r)
		asks, _ := ag.db.asks(r.ID)
		if len(asks) != 2 || asks[0].Text != "plan the offsite for the whole team" || asks[0].Live || asks[0].Who != "alice" ||
			asks[1].Text != "continue" || !asks[1].Live {
			t.Fatalf("the copy's ledger: %+v", asks)
		}
		if !strings.Contains(taskBlock(asks), "plan the offsite for the whole team") {
			t.Fatalf("the copy's pinned task:\n%s", taskBlock(asks))
		}
		msgs, _ := ag.db.messages(r.ID, false)
		masked := 0
		for _, m := range msgs {
			if m.Masked {
				masked++
			}
		}
		if masked != 1 {
			t.Fatalf("masked results in the copy: %d", masked)
		}
	})
}

// TestCopyTooLarge: a copy over what POST /import takes is refused with 413
// saying so — by the export, by publish, and by the import itself — and
// session files past the files' cap are left and named.
func TestCopyTooLarge(t *testing.T) {
	ag, h := userAgent(t)
	var mine Run
	serveJSON(t, h, as("POST", "/ask", `{"text":"`+strings.Repeat("long ", 400)+`"}`, alicesFrame("read")), 200, &mine)
	waitStatus(t, ag.db, mine.ID, statusIdle)
	for _, f := range []struct{ path, text string }{{"a.md", "# a"}, {"b.md", strings.Repeat("b", 64)}} {
		if _, err := ag.db.replPutFile(mine.ID, f.path, f.text, 0); err != nil {
			t.Fatal(err)
		}
	}
	oldB, oldF := maxBundleBytes, maxBundleFiles
	t.Cleanup(func() { maxBundleBytes, maxBundleFiles = oldB, oldF })
	maxBundleFiles = 16
	var sent importBody
	stubGlobalCalls(t, func(method, path string, body []byte) (int, string) {
		_ = json.Unmarshal(body, &sent)
		return 200, `{"id":7,"title":"t","owner":"alice","visibility":"team"}`
	})
	var out struct{ Left []string }
	serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{"share":{"visibility":"team"},"keep":true,"files":true}`, alicesFrame("read")), 200, &out)
	if len(sent.Conversation.Files) != 1 || sent.Conversation.Files[0].Path != "a.md" || fmt.Sprint(out.Left) != "[b.md]" {
		t.Fatalf("files carried %+v, left %v", sent.Conversation.Files, out.Left)
	}
	maxBundleBytes = 1024
	rec := serveJSON(t, h, as("POST", fmt.Sprintf("/runs/%d/publish", mine.ID), `{"share":{"visibility":"team"},"files":true}`, alicesFrame("read")), 413, nil)
	if !strings.Contains(rec.Body.String(), "too large to copy") || !strings.Contains(rec.Body.String(), "leave its session files out") {
		t.Fatalf("publish, too large: %s", rec.Body)
	}
	if _, err := ag.db.getRun(mine.ID); err != nil {
		t.Fatal("a refused publish deleted the original")
	}
	serveJSON(t, h, as("GET", fmt.Sprintf("/runs/%d/export", mine.ID), "", alicesFrame("read")), 413, nil)

	t.Run("import", func(t *testing.T) {
		_, h := globalAgent(t)
		maxBundleBytes = 1024
		b, _ := json.Marshal(importBody{Conversation: &convBundle{Version: bundleVersion, Messages: []bundleMsg{{Role: "user", Content: strings.Repeat("x", 2048)}}},
			Share: &shareSpec{Visibility: visTeam}})
		rec := serveJSON(t, h, as("POST", "/import", string(b), f5("alice", "read")), 413, nil)
		if !strings.Contains(rec.Body.String(), "too large to copy") {
			t.Fatalf("import, too large: %s", rec.Body)
		}
	})
}
