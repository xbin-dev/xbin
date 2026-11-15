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

// wsDone starts the summary a finished script is answered with.
const wsDone = "Done: "

// wsScript answers a conversation whose last "ws:" script isn't finished:
// the next step's call, or the summary once every step ran. The script is
// the last one a message carries, and a message without one after it does
// not end it: the agent writes its own "[workspace updates — …]" message
// into the transcript at a step boundary — right after the person's
// message, or between two steps — and a model goes on with what it was
// asked. Once the summary is said the script is over, and what comes after
// is answered as any other message ("ok: <text>").
func wsScript(conv []turn) (plan, bool) {
	at := -1
	var steps []call
	for i := len(conv) - 1; i >= 0 && at < 0; i-- {
		if conv[i].Role != "user" {
			continue
		}
		text, _ := cutReminder(conv[i].Text)
		if s, ok := wsSteps(text); ok {
			at, steps = i, s
		}
	}
	if at < 0 {
		return plan{}, false
	}
	var results []string
	for _, t := range conv[at+1:] {
		switch {
		case t.Role == "tool":
			results = append(results, answerLine(t.Text))
		case t.Role == "assistant" && strings.HasPrefix(t.Text, wsDone):
			return plan{}, false // summarized already: the script is over
		}
	}
	if len(results) < len(steps) {
		return plan{Fast: true, Calls: []call{steps[len(results)]}}, true
	}
	return plan{Fast: true, Text: wsDone + strings.Join(results, " | ")}, true
}
