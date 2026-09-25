// draft.go — the model call in flight, as the tile watches it.
//
// Not durable: a draft exists only while a call streams. It is published as
// coalescable events (events.go) — the accumulated answer text, the
// accumulated thinking, each tool call as its arguments are written (so its
// summary shows before the call even runs) — and included in the /view
// snapshot so a tile that connects mid-call sees it at once.
package main

import (
	"strconv"
	"time"
)

type draftTool struct {
	Index int    `json:"index"`
	ID    string `json:"id,omitempty"`
	Name  string `json:"name,omitempty"`
	Args  string `json:"args,omitempty"`
}

type draft struct {
	Run        int64              `json:"run"`
	Model      string             `json:"model,omitempty"`
	Started    int64              `json:"started"` // ms
	Text       string             `json:"text,omitempty"`
	Thinking   string             `json:"thinking,omitempty"`
	ThinkStart int64              `json:"thinkStart,omitempty"` // ms
	ThinkEnd   int64              `json:"thinkEnd,omitempty"`   // ms
	Tools      map[int]*draftTool `json:"-"`
	ToolList   []*draftTool       `json:"tools,omitempty"`
	root       int64
}

func (e *Engine) draftStart(ts *turnState, model string) {
	d := &draft{Run: ts.run.ID, Model: model, Started: time.Now().UnixMilli(), Tools: map[int]*draftTool{}, root: ts.root}
	e.mu.Lock()
	e.drafts[ts.run.ID] = d
	e.mu.Unlock()
	e.hub.publish(&Event{Type: evText, Run: ts.run.ID, Root: ts.root, key: "text:" + itoa(ts.run.ID),
		Data: map[string]any{"text": "", "model": model, "started": d.Started}})
}

// draftEvent folds one live event into the run's draft and publishes it.
func (e *Engine) draftEvent(ts *turnState, ev LLMEvent) {
	id := ts.run.ID
	nowMs := time.Now().UnixMilli()
	e.mu.Lock()
	d := e.drafts[id]
	if d == nil {
		e.mu.Unlock()
		return
	}
	var out *Event
	switch ev.Kind {
	case "thinking":
		if d.ThinkStart == 0 {
			d.ThinkStart = nowMs
		}
		d.Thinking = ev.Text
		out = &Event{Type: evThinking, key: "thinking:" + itoa(id),
			Data: map[string]any{"text": ev.Text, "started": d.ThinkStart}}
	case "text":
		if d.ThinkStart != 0 && d.ThinkEnd == 0 {
			d.ThinkEnd = nowMs
		}
		d.Text = ev.Text
		out = &Event{Type: evText, key: "text:" + itoa(id),
			Data: map[string]any{"text": ev.Text, "model": d.Model, "started": d.Started}}
	case "tool":
		if d.ThinkStart != 0 && d.ThinkEnd == 0 {
			d.ThinkEnd = nowMs
		}
		t := d.Tools[ev.Index]
		if t == nil {
			t = &draftTool{Index: ev.Index}
			d.Tools[ev.Index] = t
		}
		if ev.ID != "" {
			t.ID = ev.ID
		}
		if ev.Name != "" {
			t.Name = ev.Name
			if n, ok := ts.back[ev.Name]; ok {
				t.Name = n
			}
		}
		t.Args = ev.Args
		out = &Event{Type: evToolArgs, key: "tool:" + itoa(id) + ":" + strconv.Itoa(ev.Index),
			Data: map[string]any{"index": t.Index, "id": t.ID, "name": t.Name, "args": t.Args}}
	}
	e.mu.Unlock()
	if out != nil {
		out.Run, out.Root = id, ts.root
		e.hub.publish(out)
	}
}

func (e *Engine) draftEnd(ts *turnState) {
	e.mu.Lock()
	delete(e.drafts, ts.run.ID)
	e.mu.Unlock()
	e.hub.publish(&Event{Type: evDraftEnd, Run: ts.run.ID, Root: ts.root})
}

// draftsOf returns the live drafts of a tree (for a snapshot).
func (e *Engine) draftsOf(root int64) []*draft {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []*draft
	for _, d := range e.drafts {
		if d.root != root {
			continue
		}
		c := *d
		c.ToolList = nil
		for _, t := range d.Tools {
			tc := *t
			c.ToolList = append(c.ToolList, &tc)
		}
		out = append(out, &c)
	}
	return out
}

// getDraft is the legacy single-string draft (GET /runs/{id}).
func (e *Engine) getDraft(id int64) string {
	e.mu.Lock()
	defer e.mu.Unlock()
	if d := e.drafts[id]; d != nil {
		return d.Text
	}
	return ""
}
