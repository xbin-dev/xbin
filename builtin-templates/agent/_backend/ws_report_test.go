package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The feedback run (2026-11-17): of a Codex tile agent that ran thirty
// commands and changed two files, tile_agent_result said "Files changed:
// none reported. Commands it ran to check: none recorded — nothing here
// shows the work was verified", and the workspace agent told its person
// the work was unverified. Two causes, both in how the report reads the
// session's log: a command's title and kind are on its tool.call and its
// exit code on the tool.update that ends it (the scripted agent sends one
// event with both; Codex two), and only the log's last 400 events were
// read — which an agent that streams its answer fills with the answer.
func TestWsAgentReportReadsTheWholeTurn(t *testing.T) {
	type ev struct {
		Seq  int            `json:"seq"`
		Type string         `json:"type"`
		Data map[string]any `json:"data"`
	}
	var log []ev
	add := func(typ string, data map[string]any) { log = append(log, ev{len(log) + 1, typ, data}) }
	// an earlier turn, whose command and file are not this report's
	add("message.delta", map[string]any{"role": "user", "text": "first brief"})
	add("tool.call", map[string]any{"id": "c0", "title": "rm -rf old", "kind": "execute", "status": "in_progress"})
	add("tool.update", map[string]any{"id": "c0", "status": "completed", "exitCode": 0})
	add("files.changed", map[string]any{"changes": []any{map[string]any{"path": "old.go", "add": 9, "del": 9}}})
	add("turn.end", map[string]any{"turn": 1, "stopReason": "end_turn"})
	// the turn: a prompt, a steered message, commands as Codex logs them, an edit, then a long streamed answer
	add("message.delta", map[string]any{"role": "user", "text": "the brief"})
	add("message.delta", map[string]any{"role": "user", "text": "also the header", "steered": true})
	for i := range 25 {
		id := fmt.Sprintf("c%d", i+1)
		add("tool.call", map[string]any{"id": id, "title": fmt.Sprintf("ls dir%d", i+1), "kind": "execute", "status": "in_progress"})
		add("tool.update", map[string]any{"id": id, "status": "completed", "exitCode": 0})
	}
	add("tool.call", map[string]any{"id": "e1", "title": "Editing files", "kind": "edit", "status": "in_progress"})
	add("tool.update", map[string]any{"id": "e1", "status": "completed"})
	add("files.changed", map[string]any{"toolCallId": "e1", "changes": []any{map[string]any{"path": "index.html", "add": 12, "del": 1}}})
	add("tool.call", map[string]any{"id": "t1", "title": "go test ./...", "kind": "execute", "status": "in_progress"})
	add("tool.update", map[string]any{"id": "t1", "status": "failed", "exitCode": 1})
	add("tool.update", map[string]any{"id": "t1", "status": "failed", "exitCode": 1}) // said twice: listed once
	for i := range 900 {
		add("message.delta", map[string]any{"role": "agent", "text": fmt.Sprintf("w%d ", i)})
	}
	add("turn.end", map[string]any{"turn": 2, "stopReason": "end_turn"})
	reads := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads++
		var before, limit int
		fmt.Sscan(r.URL.Query().Get("before"), &before)
		fmt.Sscan(r.URL.Query().Get("limit"), &limit)
		end := len(log)
		if before > 0 {
			end = before - 1
		}
		start := max(0, end-limit)
		next := 0
		if start < end {
			next = log[start].Seq
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"events": log[start:end], "hasOlder": start > 0, "nextBefore": next})
	}))
	defer srv.Close()
	h := wsHTTP{via: &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "tcp", srv.Listener.Addr().String())
	}}}}
	rep, err := h.AgentReport(context.Background(), "h1", "s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(rep.Files) != 1 || rep.Files[0] != (wsFileStat{Path: "index.html", Add: 12, Del: 1}) {
		t.Errorf("the turn's files: %+v — want index.html alone (the earlier turn's old.go is not this turn's)", rep.Files)
	}
	last := ""
	if n := len(rep.Verified); n > 0 {
		last = rep.Verified[n-1]
	}
	if len(rep.Verified) != wsReportCommands+1 || rep.Verified[0] != "(6 earlier commands not shown)" || last != "go test ./... → exit 1" || strings.Contains(strings.Join(rep.Verified, " "), "rm -rf old") {
		t.Errorf("the turn's commands (%d): first %q, last %q — want the last %d of 26, the failed test run last and once", len(rep.Verified), rep.Verified[0], last, wsReportCommands)
	}
	if rep.Turn != 2 || rep.Status != "end_turn" || !strings.HasPrefix(rep.Answer, "w0 w1 ") || !strings.HasSuffix(rep.Answer, "w899") {
		t.Errorf("the answer: turn %d, %s, %q…", rep.Turn, rep.Status, clip(rep.Answer, 40))
	}
	if reads != 3 {
		t.Errorf("%d reads of the log, want the tail and the two pages before it back to the prompt", reads)
	}
}
