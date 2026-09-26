// stream_deltas.go — GET /stream?deltas=1: draft text as appended pieces.
//
// A draft event carries the WHOLE text so far (events.go), which is O(n²)
// bytes over a fast stream. A connection that asks for deltas gets, instead,
// `text.delta` / `thinking.delta` with only what was appended since the last
// draft event of that run and kind ON THIS CONNECTION — and `tool.delta` for
// a tool call's arguments as the model writes them (a file_write's can run to
// 64 KiB), per call index:
//
//	{"type":"text.delta","run":7,…,"data":{"delta":"lo wor","at":3}}
//	{"type":"tool.delta","run":7,…,"data":{"index":0,"delta":"\"path\":","at":1}}
//
// `at` is where the piece goes: the length, in UTF-16 code units (a JS
// string's .length), of the text (or arguments) the client should already
// hold. The hub
// stays as it is — events are still published (and coalesced per subscriber)
// with the full text; the connection turns them into deltas as it writes
// them, against what it has written. So coalescing, replay and resync keep
// today's semantics:
//
//   - the first draft event of a run on a connection, and any that does not
//     simply extend what was written (a new model call, a provider that
//     rewrote its text, a new model/start time), goes out as the ordinary full
//     `text` / `thinking` event — which a client always applies by replacing;
//   - `draft.end` and `reset` forget what was written, so what follows them
//     is full again; a new connection starts with every live draft in full.
//
// A client that finds its text's length differs from `at` is out of step (it
// replaced the draft from a /view in between, say) and reconnects the stream:
// a fresh connection sends every live draft in full.
package main

import (
	"fmt"
	"strings"
)

// deltaWritten is what one connection has written of one draft kind.
type deltaWritten struct {
	text    string
	units   int // UTF-16 length of text
	model   any // a tool call's: its id
	started any // a tool call's: its name
}

// deltaState is one deltas=1 connection's memory: "text:<run>",
// "thinking:<run>" and "tool:<run>:<index>" → what it wrote last.
type deltaState map[string]*deltaWritten

// rewrite returns the event to write in place of ev: a delta when ev extends
// what this connection wrote of the same draft, else ev itself.
func (s deltaState) rewrite(ev *Event) *Event {
	switch ev.Type {
	case evReset:
		clear(s)
		return ev
	case evDraftEnd:
		delete(s, evText+":"+itoa(ev.Run))
		delete(s, evThinking+":"+itoa(ev.Run))
		tools := evToolArgs + ":" + itoa(ev.Run) + ":"
		for k := range s {
			if strings.HasPrefix(k, tools) {
				delete(s, k)
			}
		}
		return ev
	case evToolArgs:
		return s.rewriteTool(ev)
	case evText, evThinking:
	default:
		return ev
	}
	d, ok := ev.Data.(map[string]any)
	if !ok {
		return ev
	}
	text, _ := d["text"].(string)
	key := ev.Type + ":" + itoa(ev.Run)
	if w := s[key]; w != nil && len(text) > len(w.text) && strings.HasPrefix(text, w.text) &&
		w.model == d["model"] && w.started == d["started"] {
		piece := text[len(w.text):]
		at := w.units
		w.text, w.units = text, w.units+utf16Len(piece)
		dup := *ev
		dup.Type = ev.Type + ".delta"
		dup.Data = map[string]any{"delta": piece, "at": at}
		return &dup
	}
	s[key] = &deltaWritten{text: text, units: utf16Len(text), model: d["model"], started: d["started"]}
	return ev
}

// rewriteTool is rewrite for a tool call's arguments: a `tool.delta`
// {index, delta, at} when the arguments extend what this connection wrote of
// the same call (same index, id and name), else the full `tool` event.
func (s deltaState) rewriteTool(ev *Event) *Event {
	d, ok := ev.Data.(map[string]any)
	if !ok {
		return ev
	}
	args, _ := d["args"].(string)
	key := fmt.Sprintf("%s:%d:%v", evToolArgs, ev.Run, d["index"])
	if w := s[key]; w != nil && len(args) > len(w.text) && strings.HasPrefix(args, w.text) &&
		w.model == d["id"] && w.started == d["name"] {
		piece := args[len(w.text):]
		at := w.units
		w.text, w.units = args, w.units+utf16Len(piece)
		dup := *ev
		dup.Type = evToolArgs + ".delta"
		dup.Data = map[string]any{"index": d["index"], "delta": piece, "at": at}
		return &dup
	}
	// model/started hold the call's id and name: a new id or name is a new call
	s[key] = &deltaWritten{text: args, units: utf16Len(args), model: d["id"], started: d["name"]}
	return ev
}

// utf16Len is len() of s as a JS string. Invalid bytes count one unit each,
// as encoding/json writes each as U+FFFD.
func utf16Len(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}
