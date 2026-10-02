package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestNormalize(t *testing.T) {
	for in, want := range map[string]string{
		"at 2026-10-02T09:14:07.123Z and 2026-10-03":                                "at <date> and <date>",
		"Thursday, Oct 2, 2026 at 9:14 PM":                                          "<date> at <time>",
		"request 3f6a1c2e-0b1d-4e8f-9a7b-6c5d4e3f2a1b done":                         "request <uuid> done",
		"ok  \tnorthwind/billing\t0.412s (12ms)":                                    "ok northwind/billing <dur> (<dur>)",
		"pid 481516 on port 51234, commit 4a9b5b47 from facade":                     "pid <num> on port <num>, commit <hex> from facade",
		"toolu_01AbCdEfGhIjKlMn and call_Ab12Cd34Ef":                                "<id> and <id>",
		"x-anthropic-billing-header: cc_version=2.1; cch=abc;\nYou are Claude Code": "You are Claude Code",
		"Sunset Boulevard, 12 tests":                                                "Sunset Boulevard, 12 tests",
	} {
		if got := normalize(in); got != want {
			t.Errorf("normalize(%q) = %q, want %q", in, got, want)
		}
	}
	long := strings.Repeat("the build took 41.2s at 10:42:07; ", 20)
	if normalize(long) != normalize(strings.ReplaceAll(long, "41.2s at 10:42:07", "38.9s at 11:03:55")) {
		t.Fatal("memoized normalization")
	}
}

// One conversation keeps its thread key from call to call and from run to
// run (dates, ids, sessions aside); a side call and each subagent have
// keys of their own.
func TestThreadKeys(t *testing.T) {
	key := func(v obj) string {
		b, _ := json.Marshal(v)
		return describe("POST", "/v1/chat/completions", "", b).root
	}
	sys := "You are Northwind's operations agent. Current time: 09:12."
	turn1 := key(chat("gpt-5.1", true, sys, user("Prepare the launch checklist")))
	turn2 := key(chat("gpt-5.1", true, sys, user("Prepare the launch checklist"), said("Sure."), user("Add the press kit")))
	otherDay := key(chat("gpt-5.1", true, strings.Replace(sys, "09:12", "17:40", 1), user("Prepare the launch checklist")))
	if turn1 != turn2 || turn1 != otherDay {
		t.Fatalf("one conversation, three keys: %s %s %s", turn1, turn2, otherDay)
	}
	if title := key(chat("gpt-5.1", false, "Name this conversation.", user("Prepare the launch checklist"))); title == turn1 {
		t.Fatal("a title call shares the conversation's key")
	}
	a := key(chat("gpt-5.1", true, "You are a subagent.", user("slow job 1")))
	b := key(chat("gpt-5.1", true, "You are a subagent.", user("slow job 2")))
	if a == b {
		t.Fatal("two subagents share a key")
	}
	cb := func(session, date string) string {
		b, _ := json.Marshal(claude("claude-sonnet-4-5", session, userBlocks("<system-reminder>Today's date is "+date+".</system-reminder>", "Fix the flaky test")))
		return describe("POST", "/v1/messages", "beta=true", b).root
	}
	if cb("11111111-2222-4333-8444-555555555555", "2026-10-02") != cb("99999999-8888-4777-8666-555555555555", "2026-10-09") {
		t.Fatal("Claude Code's next session is another thread")
	}
}

// The exact hash ignores the volatile fields and key order, nothing else.
func TestExactHash(t *testing.T) {
	h := func(s string) string { return exactHash("POST", "/v1/messages", []byte(s)) }
	base := h(`{"model":"m","messages":[{"role":"user","content":"hi"}],"metadata":{"user_id":"session_1"}}`)
	if base != h(`{"metadata":{"user_id":"session_2"},"messages":[{"content":"hi","role":"user"}],"model":"m"}`) {
		t.Fatal("metadata or key order changed the hash")
	}
	if base == h(`{"model":"m","messages":[{"role":"user","content":"hi!"}]}`) {
		t.Fatal("the content didn't change the hash")
	}
	if exactHash("POST", "/v1/messages", []byte("not json")) == exactHash("POST", "/v1/messages", []byte("not json!")) {
		t.Fatal("a non-JSON body")
	}
}

func TestDigestWires(t *testing.T) {
	resp := []byte(`{"model":"gpt-5.1","stream":true,"instructions":"Be brief.","input":[
		{"role":"user","content":[{"type":"input_text","text":"Ship it?"}]},
		{"type":"reasoning","encrypted_content":"gAAAA"},
		{"type":"function_call","call_id":"call_1","name":"bash","arguments":"{\"command\":\"make test\"}"},
		{"type":"function_call_output","call_id":"call_1","output":"PASS"}],
		"tools":[{"type":"function","name":"bash"},{"type":"web_search"}]}`)
	d := describe("POST", "/v1/responses", "", resp)
	if d.api != apiResponses || d.model != "gpt-5.1" || !d.stream || d.system != "Be brief." || len(d.msgs) != 3 || d.tail != 2 ||
		d.msgs[2] != (msg{"tool", "PASS"}) || strings.Join(d.tools, ",") != "bash,web_search" || !d.sequenced() {
		t.Fatalf("responses digest: %+v", d)
	}
	ant := []byte(`{"model":"claude-haiku-4-5","system":"Summarize.","messages":[
		{"role":"user","content":"Read it"},
		{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"sig"},{"type":"tool_use","id":"toolu_01","name":"Read","input":{"file":"a.go"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_01","content":[{"type":"text","text":"package a"}]},{"type":"image","source":{}}]}]}`)
	d = describe("POST", "/v1/messages", "", ant)
	if d.api != apiMessages || d.system != "Summarize." || len(d.msgs) != 3 || d.tail != 2 ||
		!strings.Contains(d.msgs[1].text, `[tool_use Read {"file":"a.go"}]`) || !strings.Contains(d.msgs[2].text, "[tool_result package a") ||
		!strings.Contains(d.msgs[2].text, "[image]") {
		t.Fatalf("messages digest: %+v", d)
	}
	if d := describe("GET", "/v1/models", "", nil); d.sequenced() || d.api != apiOther {
		t.Fatalf("models: %+v", d)
	}
	if d := describe("POST", "/v1/messages/count_tokens", "", ant); d.sequenced() {
		t.Fatal("count_tokens is a lookup")
	}
}

func TestSimilarity(t *testing.T) {
	same := shingleSet("run the billing test suite")
	if jaccard(same, shingleSet("run the billing test suite")) != 1 || jaccard(shingles{}, shingles{}) != 1 {
		t.Fatal("identical texts")
	}
	near := jaccard(same, shingleSet("run the billing test suite now"))
	far := jaccard(same, shingleSet("FAIL TestRoundTotals want 10.00"))
	if !(near > 0.6 && far < 0.1) {
		t.Fatalf("near %.2f far %.2f", near, far)
	}
	rec := describe("POST", "/v1/chat/completions", "", mustJSON(chat("m", true, "s", user("Run the suite"), said("ok"), user("ok northwind/billing 0.4s"))))
	live := describe("POST", "/v1/chat/completions", "", mustJSON(chat("m", true, "s", user("Run the suite"), said("ok"), user("FAIL northwind/billing"))))
	if n := diffNote(rec, live); !strings.Contains(n, `message 3 (user) differs: recorded "ok northwind/billing <dur>", live "FAIL northwind/billing"`) {
		t.Fatalf("diffNote: %s", n)
	}
}

func mustJSON(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

// Events come out whole and verbatim: comments, event names, CRLF lines,
// stray blank lines, an unterminated end.
func TestEventReader(t *testing.T) {
	in := ": keepalive\n\nevent: message_start\ndata: {\"a\":1}\n\n\n\ndata: x\r\n\r\ndata: [DONE]"
	er := &eventReader{br: bufio.NewReader(strings.NewReader(in))}
	var got []string
	for {
		ev, err := er.next()
		if len(ev) > 0 {
			got = append(got, string(ev))
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				t.Fatal(err)
			}
			break
		}
	}
	want := []string{": keepalive\n\n", "event: message_start\ndata: {\"a\":1}\n\n", "\n\ndata: x\r\n\r\n", "data: [DONE]"}
	if strings.Join(got, "|") != strings.Join(want, "|") || strings.Join(got, "") != in {
		t.Fatalf("events %q", got)
	}
}
