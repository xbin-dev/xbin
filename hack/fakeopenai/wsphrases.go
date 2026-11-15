package main

// wsphrases.go — a phrasebook for workspace-agent conversations: a person's
// natural message drives a realistic turn — the tool calls a model would
// make, then an answer a person reads — so the app's UI tests (and anyone
// looking at a screenshot) see a conversation, not a test script or tool
// results addressed to the model. ws.go's "ws:" scripts are untouched: they
// answer first, and the end-to-end tests read their "Done: …" summaries.
//
// A conversation is a workspace agent's when its system prompt is that
// agent's (wsPromptMark: builtin-templates/agent/_backend/ws_prompt.go). The
// phrases, matched on a line of the person's message (case aside, a final
// full stop optional; the agent may put a context line before it):
//
//	Set the note in <tile> to "<text>"   tile_call PUT /note, body the text as a JSON string, why "Update the note"
//	Start a tile agent in <tile> to <goal>   tile_agent_brief {tile, goal, done_when}
//	What tiles do I have?                workspace_map, then one sentence naming them
//	Ask me which <a> or <b>              ask_user {question, kind: choices, choices: [a, b]}; the answer is thanked
//	Let <a> and <b> use <tile>           admin_propose: two grants on <tile> — <a> as reader, <b> as writer — under that title
//
// After a phrase's turn, xbin's "[workspace updates — …]" message is
// answered in a sentence too (wsUpdateWords).

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// wsPromptMark starts the workspace agent's system prompt.
const wsPromptMark = "You are the workspace agent of one person"

// wsUpdatesLead heads what xbin and the tile agents report into the
// conversation (the agent's coord_workspace.go wsUpdatesHead).
const wsUpdatesLead = "[workspace updates"

// wsPendingWords stands for a tool result that says a request waits for the
// person ("Pending — request N … waits for your person's decision …"): words
// for the person, not the model's instructions.
const wsPendingWords = "That needs your approval. Nothing has run yet."

// wsPhrase is one phrase: its pattern, the calls it makes (in order, one a
// request) and the answer once every call answered.
type wsPhrase struct {
	asks   bool // its call is the agent's question: the person's next message answers it
	re     *regexp.Regexp
	calls  func(m []string) []call
	answer func(m []string, results []string) string
}

var wsPhrases = []wsPhrase{
	{
		re: regexp.MustCompile(`(?i)^set the note in (.+?) to ["“](.*)["”]\.?$`),
		calls: func(m []string) []call {
			body, _ := json.Marshal(m[2])
			return []call{{Name: "tile_call", Args: map[string]any{"tile": m[1], "method": "PUT", "path": "/note", "body": string(body), "why": "Update the note"}}}
		},
		answer: func(m []string, r []string) string {
			switch {
			case wsPending(r[0]):
				return fmt.Sprintf("I’ve asked to set the note in %s to “%s”. It’s waiting for your approval — nothing has changed yet.", m[1], m[2])
			case wsFailed(r[0]):
				return fmt.Sprintf("I couldn’t set the note in %s: xbin didn’t take the call.", m[1])
			}
			return fmt.Sprintf("I’ve set the note in %s to “%s”.", m[1], m[2])
		},
	},
	{
		re: regexp.MustCompile(`(?i)^start a tile agent in (.+?) to (.+?)\.?$`),
		calls: func(m []string) []call {
			return []call{{Name: "tile_agent_brief", Args: map[string]any{"tile": m[1], "goal": wsSentence(m[2]),
				"done_when": fmt.Sprintf("Anyone who opens %s can see it: %s", m[1], m[2])}}}
		},
		answer: func(m []string, r []string) string {
			if wsFailed(r[0]) {
				return fmt.Sprintf("I couldn’t write a brief for %s agent.", wsPossessive(m[1]))
			}
			return fmt.Sprintf("I’ve written a brief for %s agent. Review it and start it when you’re ready.", wsPossessive(m[1]))
		},
	},
	{
		re:    regexp.MustCompile(`(?i)^what tiles do i have\??$`),
		calls: func([]string) []call { return []call{{Name: "workspace_map", Args: map[string]any{}}} },
		answer: func(_ []string, r []string) string {
			return wsMapWords(r[0])
		},
	},
	{
		re: regexp.MustCompile(`(?i)^let (.+?) and (.+?) use (.+?)\.?$`),
		calls: func(m []string) []call {
			return []call{{Name: "admin_propose", Args: map[string]any{"title": fmt.Sprintf("Let %s and %s use %s", m[1], m[2], m[3]), "changes": []map[string]any{
				{"kind": "grant.add", "args": map[string]any{"from": m[1], "target": m[3], "role": "reader"}, "why": fmt.Sprintf("%s shows what is in %s", m[1], m[3])},
				{"kind": "grant.add", "args": map[string]any{"from": m[2], "target": m[3], "role": "writer"}, "why": fmt.Sprintf("%s writes to %s", m[2], m[3])},
			}}}}
		},
		answer: func(m []string, r []string) string {
			if strings.HasPrefix(r[0], "Change set ") {
				return "I’ve proposed two admin changes. Nothing is applied until you review them and choose which to apply."
			}
			return "I can’t propose admin changes for you yet: that is yours to turn on, in the agent’s Preferences."
		},
	},
	{
		asks: true,
		re:   regexp.MustCompile(`(?i)^ask me which (.+?) or (.+?)\??$`),
		calls: func(m []string) []call {
			return []call{{Name: "ask_user", Args: map[string]any{"question": fmt.Sprintf("Which do you mean: %s or %s?", m[1], m[2]),
				"kind": "choices", "choices": []string{wsSentence(m[1]), wsSentence(m[2])}}}}
		},
		answer: func(_ []string, r []string) string { return "" }, // ask_user ends the turn: the answer is the person's next message
	},
}

// wsMatch finds the phrase a person's message says (its own case kept), on
// any of its lines.
func wsMatch(text string) (*wsPhrase, []string) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		for i := range wsPhrases {
			if m := wsPhrases[i].re.FindStringSubmatch(line); m != nil {
				for j := range m {
					m[j] = strings.TrimSpace(m[j])
				}
				return &wsPhrases[i], m
			}
		}
	}
	return nil, nil
}

// wsPerson reports whether t is the person's own message (not xbin's
// updates, not the images a tool showed).
func wsPerson(t turn) bool {
	return t.Role == "user" && !strings.HasPrefix(t.Text, wsUpdatesLead) && !strings.HasPrefix(t.Text, "(images you asked to see")
}

// wsPhrasebook answers a workspace agent's turn from the phrasebook: the
// next call of the phrase the person's last message says, its answer once
// they all answered; xbin's updates after a phrase's turn, and the answer to
// a phrase's question, in a sentence. ok false: not a phrase's.
func wsPhrasebook(conv []turn, system string) (plan, bool) {
	if !strings.Contains(system, wsPromptMark) {
		return plan{}, false
	}
	at := -1
	for i := len(conv) - 1; i >= 0; i-- {
		if wsPerson(conv[i]) {
			at = i
			break
		}
	}
	if at < 0 {
		return plan{}, false
	}
	text, _ := cutReminder(conv[at].Text)
	ph, m := wsMatch(text)
	if ph == nil {
		return wsAnswered(conv, at)
	}
	var results []string
	for _, t := range conv[at+1:] {
		switch {
		case t.Role == "tool":
			results = append(results, strings.TrimSpace(t.Text))
		case t.Role == "assistant" && strings.TrimSpace(t.Text) != "":
			return wsAfter(conv, at) // answered already: what came after is xbin's
		}
	}
	calls := ph.calls(m)
	if len(results) < len(calls) {
		return plan{Fast: true, Calls: []call{calls[len(results)]}}, true
	}
	if a := ph.answer(m, results); a != "" {
		return plan{Fast: true, Text: a}, true
	}
	return plan{}, false
}

// wsAnswered: the person's message at `at` answers the question a phrase had
// the agent ask (the phrase, then the ask_user call's result, then this).
func wsAnswered(conv []turn, at int) (plan, bool) {
	for i := at - 1; i >= 0; i-- {
		if !wsPerson(conv[i]) {
			continue
		}
		text, _ := cutReminder(conv[i].Text)
		if ph, _ := wsMatch(text); ph == nil || !ph.asks {
			return plan{}, false
		}
		answer, _ := cutReminder(conv[at].Text)
		return plan{Fast: true, Text: fmt.Sprintf("Thanks — I’ll go with %s.", strings.TrimSuffix(strings.TrimSpace(answer), "."))}, true
	}
	return plan{}, false
}

// wsAfter answers xbin's updates that came after a phrase's answered turn,
// once each.
func wsAfter(conv []turn, at int) (plan, bool) {
	last := conv[len(conv)-1]
	if last.Role != "user" || !strings.HasPrefix(last.Text, wsUpdatesLead) {
		return plan{}, false
	}
	return plan{Fast: true, Text: wsUpdateWords(last.Text)}, true
}

// wsUpdateWords says an update (a request decided, a tile agent started) in
// a sentence for the person.
func wsUpdateWords(s string) string {
	l := strings.ToLower(s)
	switch {
	case strings.Contains(l, "denied by your person"):
		return "You declined it, so nothing changed."
	case strings.Contains(l, "expired"):
		return "The request expired before it was approved, so nothing changed."
	case strings.Contains(l, "withdrawn"):
		return "That request was withdrawn; nothing ran."
	case strings.Contains(l, "tile agent #") && strings.Contains(l, "started"):
		return "The tile agent has started. I’ll tell you when it has something for you."
	case strings.Contains(l, "answered with an error"):
		return "You approved it, but the tile answered with an error — the change may not have happened."
	case strings.Contains(l, "change set") && strings.Contains(l, "applied"):
		return "The admin changes you chose were applied."
	case strings.Contains(l, "change set") && strings.Contains(l, "rejected"):
		return "You rejected the admin changes, so nothing was applied."
	case strings.Contains(l, "it ran:"):
		return "Done — you approved it, and it ran."
	}
	return "Noted. Nothing else needs you right now."
}

// wsMapWords sums up workspace_map's answer in one sentence: how many tiles,
// and their names.
func wsMapWords(r string) string {
	if wsFailed(r) {
		return "I couldn’t read what’s connected just now."
	}
	var names []string
	in := false
	for _, line := range strings.Split(r, "\n") {
		switch {
		case strings.HasPrefix(line, "Tiles ("):
			in = true
		case in && strings.HasPrefix(line, "- "):
			path, title, ok := strings.Cut(strings.TrimPrefix(line, "- "), " — ")
			if !ok {
				title = path
			}
			title, _, _ = strings.Cut(title, " (")
			names = append(names, strings.TrimSpace(title))
		default:
			in = false
		}
	}
	switch n := len(names); {
	case n == 0:
		return "No tiles are connected in this session yet."
	case n == 1:
		return fmt.Sprintf("You have one tile connected in this session: %s.", names[0])
	case n > 5:
		return fmt.Sprintf("You have %d tiles connected in this session, among them %s and %d more.", n, strings.Join(names[:4], ", "), n-4)
	default:
		return fmt.Sprintf("You have %d tiles connected in this session: %s and %s.", n, strings.Join(names[:n-1], ", "), names[n-1])
	}
}

// wsPending: a tool result saying a request waits for the person.
func wsPending(r string) bool { return strings.HasPrefix(strings.TrimSpace(r), "Pending —") }

// wsFailed: a tool result that is an error.
func wsFailed(r string) bool {
	l := strings.ToLower(strings.TrimSpace(r))
	return strings.HasPrefix(l, "error:")
}

// wsSentence: the words with a capital first letter.
func wsSentence(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

// wsPossessive: "Note’s", "Orders’".
func wsPossessive(name string) string {
	if strings.HasSuffix(strings.ToLower(name), "s") {
		return name + "’"
	}
	return name + "’s"
}
