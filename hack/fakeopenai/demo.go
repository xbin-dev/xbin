package main

// The demo script (-script FILE): scripted answers for the demo film set
// (hack/demo), so a workspace dressed as a real company can show its agent
// and chat answering in full sentences without a real model — and without
// the harness scripts' test phrasing. It is consulted before the built-in
// keywords; what it doesn't match falls through to them, except the
// catch-all "ok: <text>" answer, which becomes the script's fallback.
//
//	{
//	  "models": ["assistant"],                 // /v1/models lists these instead
//	  "fallback": "…",                         // the answer to anything unmatched
//	  "replies": [{
//	    "match": ["brightwell", "renewal"],    // every word is in the last user
//	                                           // message (lower-cased); the first
//	                                           // reply that matches answers
//	    "title": "Brightwell renewal prep",    // the conversation's name, when the
//	                                           // agent asks for one (matched on the
//	                                           // first message)
//	    "steps": [                             // step k answers once the turn holds
//	      {"thinking": "…", "calls": [         // k tool results (the last step
//	        {"name": "mcp:get_account",        // repeats)
//	         "args": {"query": "Brightwell", "summary": "Look up Brightwell"}}]},
//	      {"text": "**Brightwell** renews on {{account.renewal}} …"}
//	    ]
//	  }]
//	}
//
// A call named "mcp:<tool>" is the offered tool whose wire name is
// "mcp_<server>_<tool>_<hash>" (the agent's names for MCP tools, which carry
// the server and a hash): the first in the request's tool list whose name
// holds "_<tool>". Every other name is sent as written. A step's text and
// thinking may hold {{path}} placeholders, filled from the turn's newest
// tool result that is JSON (a dotted path into it; a number indexes an
// array) — so an answer quotes what the tool really returned. "{{#2.path}}"
// reads the turn's second JSON result instead (in the order they came), and
// a filter formats a value: "{{deals.0.amount|usd}}" → "$50,400",
// "{{account.renewal|date}}" → "Nov 12" (from an ISO date or unix ms),
// "{{x|int}}" → "1,284".

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

type demoScript struct {
	Models   []string    `json:"models"`
	Fallback string      `json:"fallback"`
	Replies  []demoReply `json:"replies"`
}

type demoReply struct {
	Match []string   `json:"match"`
	Title string     `json:"title"`
	Steps []demoStep `json:"steps"`
}

type demoStep struct {
	Thinking string     `json:"thinking"`
	Text     string     `json:"text"`
	Calls    []demoCall `json:"calls"`
	DelayMs  int        `json:"delayMs"`
}

type demoCall struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

// demo is the loaded -script file (nil: none).
var demo *demoScript

func loadDemo(path string) (*demoScript, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var d demoScript
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	for i, r := range d.Replies {
		if len(r.Match) == 0 || len(r.Steps) == 0 {
			return nil, fmt.Errorf("%s: reply %d needs match words and steps", path, i)
		}
	}
	return &d, nil
}

// find is the first reply whose words are all in s (lower-cased).
func (d *demoScript) find(s string) *demoReply {
	s = strings.ToLower(s)
	for i := range d.Replies {
		r := &d.Replies[i]
		all := true
		for _, w := range r.Match {
			if !strings.Contains(s, strings.ToLower(w)) {
				all = false
				break
			}
		}
		if all {
			return r
		}
	}
	return nil
}

// pick answers one model request: the demo script when it matches, else the
// built-in scripts (with the script's fallback for their catch-all).
func pick(conv []turn, system string, tools []string) plan {
	if demo == nil || len(conv) == 0 {
		return script(conv, system)
	}
	if p, ok := demo.answer(conv, system, tools); ok {
		return p
	}
	p := script(conv, system)
	if demo.Fallback != "" && strings.HasPrefix(p.Text, "ok: ") && len(p.Calls) == 0 {
		p.Text = demo.Fallback
	}
	return p
}

func (d *demoScript) answer(conv []turn, system string, tools []string) (plan, bool) {
	switch purposeOf(system) {
	case "title":
		first := conv[len(conv)-1].Text
		if _, rest, ok := strings.Cut(first, "First message:\n"); ok {
			first = strings.SplitN(rest, "\n", 2)[0]
		}
		if r := d.find(first); r != nil && r.Title != "" {
			return plan{Text: r.Title}, true
		}
		return plan{Text: plainTitle(first)}, true
	case "compact":
		return plan{}, false
	}
	if strings.Contains(system, "You are a subagent") {
		return plan{}, false
	}
	// the last user message, and how many tool results this turn holds
	lastUser, results := "", 0
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" && !strings.HasPrefix(conv[i].Text, "(images you asked to see") {
			lastUser = conv[i].Text
			break
		}
		if conv[i].Role == "tool" {
			results++
		}
	}
	r := d.find(lastUser)
	if r == nil {
		return plan{}, false
	}
	st := r.Steps[min(results, len(r.Steps)-1)]
	data := turnJSON(conv)
	p := plan{Text: fill(st.Text, data), Delay: time.Duration(st.DelayMs) * time.Millisecond}
	if st.Thinking != "" {
		p.Thinking = phrases(fill(st.Thinking, data))
	}
	for _, c := range st.Calls {
		p.Calls = append(p.Calls, call{Name: toolName(c.Name, tools), Args: c.Args})
	}
	// usage the size of what was sent and said (~4 characters a token), so a
	// gateway's counters look like a model's
	in := len(system)
	for _, t := range conv {
		in += len(t.Text)
	}
	p.Prompt, p.Output = in/4+200, (len(p.Text)+len(st.Thinking))/4+30*len(p.Calls)+8
	return p, true
}

// toolName resolves "mcp:<tool>" against the request's offered tools: the
// tool itself, else one named "…_<tool>" (the chat tile's "m0_<tool>"), else
// "…_<tool>_…" (the agent's "mcp_<server>_<tool>_<hash>").
func toolName(name string, tools []string) string {
	t, ok := strings.CutPrefix(name, "mcp:")
	if !ok {
		return name
	}
	for _, match := range []func(string) bool{
		func(n string) bool { return n == t },
		func(n string) bool { return strings.HasSuffix(n, "_"+t) },
		func(n string) bool { return strings.Contains(n, "_"+t+"_") },
	} {
		for _, n := range tools {
			if match(n) {
				return n
			}
		}
	}
	return t
}

// turnJSON is this turn's tool results that parse as JSON (or hold JSON
// after a line of prose), in the order they came.
func turnJSON(conv []turn) []any {
	var out []any
	for i := len(conv) - 1; i >= 0; i-- {
		if conv[i].Role == "user" && !strings.HasPrefix(conv[i].Text, "(images you asked to see") {
			break
		}
		if conv[i].Role != "tool" {
			continue
		}
		s := strings.TrimSpace(conv[i].Text)
		var v any
		if json.Unmarshal([]byte(s), &v) == nil {
			out = append([]any{v}, out...)
		} else if j := strings.IndexAny(s, "{["); j > 0 && json.Unmarshal([]byte(s[j:]), &v) == nil {
			out = append([]any{v}, out...)
		}
	}
	return out
}

var placeholderRe = regexp.MustCompile(`\{\{\s*(#\d+\.)?([A-Za-z0-9_.\-]+)\s*(?:\|\s*([a-z]+)\s*)?\}\}`)

// fill replaces {{path}} with the value at path in the turn's newest JSON
// result ({{#k.path}}: its k-th), formatted by the filter; "—" when absent.
func fill(s string, results []any) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	return placeholderRe.ReplaceAllStringFunc(s, func(m string) string {
		sm := placeholderRe.FindStringSubmatch(m)
		var v any
		if len(results) > 0 {
			v = results[len(results)-1]
		}
		if sm[1] != "" {
			k, _ := strconv.Atoi(strings.TrimSuffix(sm[1][1:], "."))
			if k < 1 || k > len(results) {
				return "—"
			}
			v = results[k-1]
		}
		for _, k := range strings.Split(sm[2], ".") {
			switch x := v.(type) {
			case map[string]any:
				v = x[k]
			case []any:
				n, err := strconv.Atoi(k)
				if err != nil || n < 0 || n >= len(x) {
					return "—"
				}
				v = x[n]
			default:
				return "—"
			}
		}
		return format(v, sm[3])
	})
}

// format writes a placeholder's value through its filter: usd, int, date.
func format(v any, filter string) string {
	switch x := v.(type) {
	case nil:
		return "—"
	case string:
		if filter == "date" {
			if t, err := time.Parse("2006-01-02", x[:min(10, len(x))]); err == nil {
				return t.Format("Jan 2")
			}
		}
		return x
	case float64:
		switch filter {
		case "usd":
			return "$" + grouped(int64(x+0.5))
		case "int":
			return grouped(int64(x + 0.5))
		case "date":
			return time.UnixMilli(int64(x)).Format("Jan 2")
		}
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// grouped writes n with thousands separators: 118800 → "118,800".
func grouped(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	if neg {
		return "-" + s
	}
	return s
}

// plainTitle names a conversation the script has no title for: its first
// words, as a person would.
func plainTitle(first string) string {
	ws := strings.FieldsFunc(first, func(r rune) bool { return r == ' ' || r == '\n' || r == '\t' })
	if len(ws) > 6 {
		ws = ws[:6]
	}
	t := []rune(strings.TrimRight(strings.Join(ws, " "), ".,:;!?—-"))
	if len(t) == 0 {
		return "New conversation"
	}
	return strings.ToUpper(string(t[:1])) + string(t[1:])
}

// phrases streams thinking a few words at a time (each piece is a pause).
func phrases(s string) []string {
	ws := words(s)
	var out []string
	for i := 0; i < len(ws); i += 5 {
		out = append(out, strings.Join(ws[i:min(i+5, len(ws))], ""))
	}
	return out
}

// offered reads the tool names a request offers: Chat Completions'
// {type, function: {name}} and the Responses API's {type, name}.
func offered(raw []json.RawMessage) []string {
	var out []string
	for _, r := range raw {
		var t struct {
			Name     string `json:"name"`
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		}
		if json.Unmarshal(r, &t) != nil {
			continue
		}
		if t.Function.Name != "" {
			out = append(out, t.Function.Name)
		} else if t.Name != "" {
			out = append(out, t.Name)
		}
	}
	return out
}
