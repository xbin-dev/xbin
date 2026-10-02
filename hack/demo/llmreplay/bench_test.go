package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// longSession is a coding agent's session the size a real one reaches: a
// 20 KB system prompt and every call resending the whole conversation,
// which grows by a tool call and a ~2 KB tool result per step.
func longSession(calls int, stamp string) [][]byte {
	system := strings.Repeat("You are Claude Code, Anthropic's official CLI. Follow the repository's conventions closely. ", 220)
	var msgs []any
	msgs = append(msgs, obj{"role": "user", "content": "Add a CSV export to the invoices page, with tests"})
	var out [][]byte
	for i := range calls {
		b, _ := json.Marshal(obj{"model": "claude-sonnet-4-5", "stream": true, "system": system, "messages": msgs,
			"metadata": obj{"user_id": "session_" + stamp}})
		out = append(out, b)
		id := fmt.Sprintf("toolu_01%016d", i)
		msgs = append(msgs, obj{"role": "assistant", "content": []any{obj{"type": "tool_use", "id": id, "name": "Bash", "input": obj{"command": fmt.Sprintf("go test ./invoices/... -run Step%d", i)}}}},
			obj{"role": "user", "content": []any{obj{"type": "tool_result", "tool_use_id": id,
				"content": fmt.Sprintf("ran at %s\n", stamp) + strings.Repeat(fmt.Sprintf("ok  \tnorthwind/invoices/step%d\t0.%03ds\n", i, i%1000), 40)}}})
	}
	return out
}

// BenchmarkLongSession: loading a 150-call session's cassette, then
// matching a whole retake of it (by thread once the stamps differ; the
// first call carries none).
func BenchmarkLongSession(b *testing.B) {
	rec, live := longSession(150, "2026-10-02T09:00:00Z"), longSession(150, "2026-10-03T17:30:00Z")
	c := &cassette{Header: header{LLMReplay: 1}}
	for i, body := range rec {
		c.Exchanges = append(c.Exchanges, &Exchange{Lane: "claude", Seq: i, Method: "POST", Path: "/v1/messages", ReqBody: body, Status: 200})
	}
	b.Logf("cassette: %d calls, %.1f MB of requests", len(rec), float64(totalLen(rec))/1e6)
	for b.Loop() {
		normMemo.Lock()
		normMemo.m, normMemo.bytes = map[string]string{}, 0
		normMemo.Unlock()
		p := newPlayer(c, replayOpts())
		for i, body := range live {
			m, why := p.next("claude", describe("POST", "/v1/messages", "", body))
			if m == nil || m.e.ex.Seq != i || m.how != map[bool]string{true: "exact", false: "thread"}[i == 0] || m.drift != "" {
				b.Fatalf("call %d: %+v %s", i, m, why)
			}
		}
	}
}

func totalLen(bs [][]byte) (n int) {
	for _, b := range bs {
		n += len(b)
	}
	return n
}
