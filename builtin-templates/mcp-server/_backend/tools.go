// tools.go — the tools this server offers. To add one:
//
//  1. write a function that returns a Tool — its name, what it does in a
//     sentence a model can act on, the JSON Schema of its arguments, and
//     Run (dice.go is the example);
//  2. add one line to the list below.
//
// That is all: tools/list names it at once and tools/call runs it. A Run
// that returns an error is the tool's failure, which the calling model reads
// (isError: true) — a bad argument, a thing that isn't there — never a
// broken connection.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
)

// tools are offered in this order.
var tools = []Tool{
	rollDice(), // dice.go
}

// Tool is one tool.
type Tool struct {
	Name        string // a–z, 0–9, _ and -, at most 64: how the model calls it
	Title       string // for people ("Roll dice")
	Description string // for the model: what it does, what it returns
	// Input is the JSON Schema of the arguments — an object (Object and
	// the Prop helpers below build one).
	Input map[string]any
	// Output, when set, is the JSON Schema of Result.Data, which a client
	// gets as structuredContent.
	Output map[string]any
	// Annotations are hints for clients (MCP's ToolAnnotations):
	// readOnlyHint, destructiveHint, idempotentHint, openWorldHint.
	Annotations map[string]any
	// Run does it. args is the call's arguments object as sent ({} when
	// none); Args decodes it.
	Run func(ctx context.Context, args json.RawMessage) (Result, error)
}

// Result is what a call returns.
type Result struct {
	Text string // what the model reads; "" = Data as JSON
	Data any    // the result as data (structuredContent), matching Output
}

var toolName = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

func toolNamed(name string) (Tool, bool) {
	for _, t := range tools {
		if t.Name == name {
			return t, true
		}
	}
	return Tool{}, false
}

// describe is the tool as tools/list answers it.
func (t Tool) describe() map[string]any {
	d := map[string]any{"name": t.Name, "description": t.Description, "inputSchema": t.Input}
	if t.Title != "" {
		d["title"] = t.Title
	}
	if t.Output != nil {
		d["outputSchema"] = t.Output
	}
	if len(t.Annotations) > 0 {
		d["annotations"] = t.Annotations
	}
	return d
}

// call runs the tool and shapes its answer as tools/call returns it.
func (t Tool) call(ctx context.Context, args json.RawMessage) (out map[string]any) {
	defer func() {
		if p := recover(); p != nil {
			out = failed(fmt.Sprintf("%s failed: %v", t.Name, p))
		}
	}()
	if len(bytes.TrimSpace(args)) == 0 || string(bytes.TrimSpace(args)) == "null" {
		args = json.RawMessage("{}")
	}
	res, err := t.Run(ctx, args)
	if err != nil {
		return failed(err.Error())
	}
	text := res.Text
	if text == "" && res.Data != nil {
		b, _ := json.Marshal(res.Data)
		text = string(b)
	}
	out = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": false}
	if res.Data != nil {
		out["structuredContent"] = res.Data
	}
	return out
}

func failed(msg string) map[string]any {
	return map[string]any{"content": []map[string]any{{"type": "text", "text": msg}}, "isError": true}
}

// Args decodes a call's arguments into T — an argument the schema doesn't
// name is an error the model reads, so a misspelt one isn't silently dropped.
func Args[T any](raw json.RawMessage) (T, error) {
	var v T
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return v, fmt.Errorf("bad arguments: %v", err)
	}
	return v, nil
}

// Object is a JSON Schema object of props; required names the ones a call
// must give.
func Object(props map[string]any, required ...string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

// IntProp is an integer argument between lo and hi, def when left out.
func IntProp(desc string, lo, hi, def int) map[string]any {
	return map[string]any{"type": "integer", "description": desc, "minimum": lo, "maximum": hi, "default": def}
}

// StringProp is a string argument.
func StringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}
