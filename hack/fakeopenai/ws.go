package main

// ws.go — a script the message itself carries, for the workspace agent's
// end-to-end tests (test/isolated/wsagent_test.go): no keyword of main.go
// knows that agent's tools, and the tests need them in orders of their own.
//
// A person's message whose own line starts with "ws:" is a list of steps,
// separated by " ;; ":
//
//	ws: workspace_map {} ;; tile_call {"tile":"apps/x","method":"GET","path":"/note"}
//
// Each step is a tool's name and its arguments as a JSON object ({} when
// left out). The model makes them one request at a time, in order, and then
// answers "Done: " with the first line of every tool result, " | " between
// them — so a test reads what each tool told the model from the answer.
//
// The line is found anywhere in the message (the agent puts a context line
// before a person's words), in the message's own case (tool arguments are
// case-sensitive, and script() lower-cases what it matches on).

import (
	"encoding/json"
	"strings"
)

// wsSteps parses a "ws:" message; ok false when text carries none.
func wsSteps(text string) (steps []call, ok bool) {
	body := ""
	for _, line := range strings.Split(text, "\n") {
		if rest, found := strings.CutPrefix(strings.TrimSpace(line), "ws:"); found {
			body = rest
			break
		}
	}
	if strings.TrimSpace(body) == "" {
		return nil, false
	}
	for _, part := range strings.Split(body, ";;") {
		name, args, _ := strings.Cut(strings.TrimSpace(part), " ")
		if name == "" {
			continue
		}
		c := call{Name: name, Args: map[string]any{}}
		if a := strings.TrimSpace(args); a != "" {
			if err := json.Unmarshal([]byte(a), &c.Args); err != nil {
				return []call{{Name: "invalid_ws_script", Args: map[string]any{"step": part, "error": err.Error()}}}, true
			}
		}
		steps = append(steps, c)
	}
	return steps, len(steps) > 0
}

// wsScript answers a conversation whose last message from a person is a
// "ws:" script: the next step's call, or the summary once every step ran.
func wsScript(conv []turn) (plan, bool) {
	at := -1
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" {
			at = i
			break
		}
	}
	if at < 0 {
		return plan{}, false
	}
	text, _ := cutReminder(conv[at].Text)
	steps, ok := wsSteps(text)
	if !ok {
		return plan{}, false
	}
	var results []string
	for _, t := range conv[at+1:] {
		if t.Role == "tool" {
			results = append(results, answerLine(t.Text))
		}
	}
	if len(results) < len(steps) {
		return plan{Fast: true, Calls: []call{steps[len(results)]}}, true
	}
	return plan{Fast: true, Text: "Done: " + strings.Join(results, " | ")}, true
}
