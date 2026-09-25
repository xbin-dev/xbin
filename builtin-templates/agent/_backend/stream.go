// stream.go — the tile's live view: a snapshot, then events.
//
//	GET /runs/{id}/view            one run as the chat shows it (+ a cursor)
//	GET /stream?run=<id>&since=c   SSE: run-list changes + that run's tree
//	GET /runs/{id}/stream?since=c  the same, run from the path
//
// The client reads /view, then opens /stream with the view's cursor; nothing
// can fall between the two (the cursor is taken before the database is read,
// and events are upserts by id, so seeing one twice is harmless). A `reset`
// means "re-read /view" (the cursor is from another process, or too old); a
// `bye` means this process is going away — reconnect, and the successor
// answers.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// messageView is a message as the tile gets it: tool calls parsed, meta
// flattened, and nothing the model needs but the browser does not
// (reasoningRaw can be large encrypted blobs).
func messageView(m *Message) map[string]any {
	v := map[string]any{
		"id": m.ID, "runId": m.RunID, "seq": m.Seq, "role": m.Role, "content": clip(m.Content, 64<<10),
		"name": m.Name, "toolCallId": m.ToolCallID, "compacted": m.Compacted, "created": m.Created,
	}
	if m.ToolCalls != "" {
		var calls []toolCall
		if json.Unmarshal([]byte(m.ToolCalls), &calls) == nil {
			v["toolCalls"] = calls
		}
	}
	if len(m.Meta) > 0 {
		var meta msgMeta
		if json.Unmarshal(m.Meta, &meta) == nil {
			if meta.Reasoning != "" {
				v["reasoning"] = clip(meta.Reasoning, 32<<10)
				v["reasoningMs"] = meta.ReasoningMs
			}
			if meta.Model != "" {
				v["model"] = meta.Model
			}
			if meta.Usage != nil {
				v["usage"] = meta.Usage
			}
		}
	}
	return v
}

// legacyMessages strips reasoningRaw from the stored meta for GET /runs/{id}.
func legacyMessages(msgs []*Message) []*Message {
	out := make([]*Message, len(msgs))
	for i, m := range msgs {
		c := *m
		if len(c.Meta) > 0 {
			var meta msgMeta
			if json.Unmarshal(c.Meta, &meta) == nil && len(meta.ReasoningRaw) > 0 {
				meta.ReasoningRaw = nil
				b, _ := json.Marshal(meta)
				c.Meta = b
			}
		}
		out[i] = &c
	}
	return out
}

// runView is the chat's snapshot of one run.
func (e *Engine) runView(id int64) (map[string]any, error) {
	cursor := e.hub.cursor(e.hub.now()) // BEFORE the reads
	run, err := e.db.getRun(id)
	if err != nil {
		return nil, err
	}
	msgs, _ := e.db.messages(id, false)
	mv := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		mv = append(mv, messageView(m))
	}
	steps, _ := e.db.steps(id)
	if steps == nil {
		steps = []*Step{}
	}
	links := []map[string]any{}
	for _, l := range e.db.queryLinks(`WHERE parent_id=? ORDER BY id`, id) {
		links = append(links, e.linkView(l))
	}
	var chain []map[string]any // root … parent, for the breadcrumb
	for p := run; p.ParentID != 0; {
		pr, err := e.db.getRun(p.ParentID)
		if err != nil {
			break
		}
		chain = append([]map[string]any{{"id": pr.ID, "title": pr.Title}}, chain...)
		p = pr
	}
	if chain == nil {
		chain = []map[string]any{}
	}
	var own *Link
	if run.ParentID != 0 {
		own = e.db.latestLink(id)
	}
	files, _ := e.db.replFiles(id)
	if files == nil {
		files = []*ReplFile{}
	}
	mem, _ := e.db.memory(id)
	cfg, _ := e.db.runConfig(id)
	active, limit, waiting := e.gate.stats()
	sum := runSummary(run)
	sum["pendingState"] = parsePending(run.Pending)
	sum["summary"] = run.Summary
	v := map[string]any{
		"cursor": cursor, "run": sum, "messages": mv, "steps": steps, "links": links,
		"queued": e.db.queuedView(id), "drafts": e.draftsOf(rootOf(run)), "chain": chain,
		"files": files, "messageFiles": e.db.messageFiles(id), "memory": mem, "config": cfg.forView(),
		"halted": e.halted(), "slots": map[string]int{"active": active, "limit": limit, "waiting": waiting},
	}
	if own != nil {
		v["ownLink"] = e.linkView(own)
	}
	return v, nil
}

func handleView(w http.ResponseWriter, r *http.Request) {
	v, err := agent.eng.runView(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	// What the caller may do here, and who else is in it (D83).
	v["access"] = levelOf(r).String()
	if run, err := agent.db.getRun(pathID(r)); err == nil {
		if acl, err := agent.aclOf(rootOf(run)); err == nil {
			members := []map[string]string{}
			for u, role := range acl.members {
				members = append(members, map[string]string{"user": u, "role": role})
			}
			v["acl"] = map[string]any{"owner": acl.owner, "visibility": acl.visibility, "teamRole": acl.teamRole, "members": members}
		}
	}
	xbin.WriteJSON(w, 200, v)
}

// handleStream is the SSE feed. It holds the request open (which is also
// what keeps an idle-looking backend from being reaped while a tile is open).
func handleStream(w http.ResponseWriter, r *http.Request) {
	e := agent.eng
	c := callerOf(r)
	id := pathID(r)
	if id == 0 {
		id, _ = strconv.ParseInt(r.URL.Query().Get("run"), 10, 64)
	}
	var root int64
	if id != 0 {
		run, lv, err := agent.runAccess(c, id)
		if err != nil || lv < lvViewer {
			xbin.WriteError(w, 404, "no such run")
			return
		}
		root = rootOf(run)
	}
	since := e.hub.now()
	if c := r.URL.Query().Get("since"); c != "" {
		since = e.hub.parseCursor(c)
	}
	fl, ok := w.(http.Flusher)
	if !ok {
		xbin.WriteError(w, 500, "streaming unsupported")
		return
	}
	sub, missed, fresh := e.hub.subscribe(root, since, c)
	defer e.hub.unsubscribe(sub)
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Accel-Buffering", "no")
	w.WriteHeader(200)
	send := func(ev *Event) {
		if ev.acl != nil { // a run-list row: say what this caller may do with it
			if d, ok := ev.Data.(map[string]any); ok {
				cp := make(map[string]any, len(d)+2)
				for k, v := range d {
					cp[k] = v
				}
				cp["access"], cp["mine"] = ev.acl.level(c).String(), ev.acl.mine(c)
				dup := *ev
				dup.Data = cp
				ev = &dup
			}
		}
		b, _ := json.Marshal(ev)
		fmt.Fprintf(w, "id: %s\ndata: %s\n\n", e.hub.cursor(ev.Seq), b)
	}
	send(&Event{Type: "hello", Seq: e.hub.now(), Data: map[string]any{"cursor": e.hub.cursor(e.hub.now()), "gen": e.hub.gen}})
	if !fresh {
		send(&Event{Type: evReset, Seq: e.hub.now()})
	}
	for _, ev := range missed {
		send(ev)
	}
	// Calls in flight right now: their drafts, so a tile that connects
	// mid-answer sees the text so far.
	if root != 0 {
		for _, d := range e.draftsOf(root) {
			send(&Event{Type: evText, Run: d.Run, Root: root, Seq: e.hub.now(),
				Data: map[string]any{"text": d.Text, "model": d.Model, "started": d.Started}})
			if d.Thinking != "" {
				send(&Event{Type: evThinking, Run: d.Run, Root: root, Seq: e.hub.now(),
					Data: map[string]any{"text": d.Thinking, "started": d.ThinkStart}})
			}
			for _, t := range d.ToolList {
				send(&Event{Type: evToolArgs, Run: d.Run, Root: root, Seq: e.hub.now(),
					Data: map[string]any{"index": t.Index, "id": t.ID, "name": t.Name, "args": t.Args}})
			}
		}
	}
	fl.Flush()
	for {
		select {
		case _, open := <-sub.wake:
			for _, ev := range sub.drain() {
				send(ev)
				if ev.Type == evBye {
					fl.Flush()
					return
				}
			}
			fl.Flush()
			if !open {
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}
