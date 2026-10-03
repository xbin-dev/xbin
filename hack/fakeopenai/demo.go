package main

// The demo script (-script FILE): scripted answers for the demo film set
// (hack/demo), so a workspace dressed as a real company can show its agent
// and chat answering in full sentences without a real model — and without
// the harness scripts' test phrasing. With a script loaded it is the only
// script: the built-in test keywords never answer ("hello", "quick", "steer"
// typed on camera must not get "Hello from the fake model."). A turn it has
// no reply for — a chat's, a subagent's — gets its fallback; the agent's
// conversation titles are the reply's title or the first words; its
// compaction gets a plain summary of the transcript (demoSummary).
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
// "{{x|int}}" → "1,284". A day can be named relative to the demo's day
// (dayRe): "due {{date:+4}}" → "due Oct 9".

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

// pick answers one model request: from the demo script alone when one is
// loaded (demoScript.answer), else from the built-in test scripts.
func pick(conv []turn, system string, tools []string) plan {
	if demo == nil || len(conv) == 0 {
		return script(conv, system)
	}
	return demo.answer(conv, system, tools)
}

// defaultFallback answers what a script without its own fallback doesn't
// match.
const defaultFallback = "I can't help with that from here."

// fallback is the script's answer to a turn it has no reply for.
func (d *demoScript) fallback() plan {
	if d.Fallback != "" {
		return plan{Text: d.Fallback}
	}
	return plan{Text: defaultFallback}
}

// answer is the script's answer to one request — always one: its reply for
// the turn, a title, a summary, or its fallback.
func (d *demoScript) answer(conv []turn, system string, tools []string) plan {
	switch purposeOf(system) {
	case "title":
		first := conv[len(conv)-1].Text
		if _, rest, ok := strings.Cut(first, "First message:\n"); ok {
			first = strings.SplitN(rest, "\n", 2)[0]
		}
		if r := d.find(first); r != nil && r.Title != "" {
			return plan{Text: r.Title}
		}
		return plan{Text: plainTitle(first)}
	case "compact":
		return plan{Text: demoSummary(conv[len(conv)-1].Text)}
	}
	if strings.Contains(system, "You are a subagent") {
		return d.fallback()
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
		return d.fallback()
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
	return p
}

// transcriptLine is one "#<seq> <role>[ <tool>]: <text>" line of the agent's
// compaction transcript (the agent's compact.go writes them).
var transcriptLine = regexp.MustCompile(`^#(\d+) (user|assistant|tool)(?: ([^ :]+))?(?: \([^)]*\))?: ?(.*)$`)

// demoSummary is a compaction's summary of the transcript it was given,
// in plain words: which messages it covers, the tools consulted, and where
// the last answer left off — what a model's summary would say, briefly.
func demoSummary(in string) string {
	first, last, lastAnswer := 0, 0, ""
	var tools []string
	seen := map[string]bool{}
	for _, line := range strings.Split(in, "\n") {
		m := transcriptLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		if first == 0 {
			first = n
		}
		last = n
		switch m[2] {
		case "tool":
			if t := readableTool(m[3]); t != "" && !seen[t] {
				seen[t] = true
				tools = append(tools, t)
			}
		case "assistant":
			if s := strings.TrimSpace(m[4]); s != "" {
				lastAnswer = s
			}
		}
	}
	if first == 0 {
		return "Nothing to fold in yet; the conversation continues from the pinned task."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Messages #%d–#%d", first, last)
	if len(tools) > 0 {
		fmt.Fprintf(&b, " looked things up with %s", strings.Join(tools, ", "))
	}
	b.WriteString(".")
	if lastAnswer != "" {
		s, _, _ := strings.Cut(lastAnswer, "\n")
		if r := []rune(s); len(r) > 160 {
			s = string(r[:160]) + "…"
		}
		fmt.Fprintf(&b, " The last answer (#%d) began: %s", last, s)
	}
	return b.String()
}

// readableTool is a tool's name as a person reads it: the agent's MCP names
// ("mcp_<server>_<tool>_<hash>") lose their prefix and hash.
func readableTool(name string) string {
	if rest, ok := strings.CutPrefix(name, "mcp_"); ok {
		parts := strings.Split(rest, "_")
		if len(parts) >= 3 {
			// mcp_apps_crm_get_account_9f8e7d6c: the server is "apps_crm", the
			// tool what follows it, less the hash
			tool := parts[2 : len(parts)-1]
			if len(tool) > 0 {
				return strings.Join(tool, "_")
			}
		}
	}
	return name
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

// dayRe names a day relative to the demo's day (demoDay), counted in working
// days: {{weekday:+1}} "Monday", {{date:+4}} "Oct 9", {{longdate:+4}}
// "October 9", {{nth:+4}} "9th", {{iso:-2}} "2026-10-01". hack/demo/seed.sh
// fills the same placeholders in the set's fixtures, so what the model says
// agrees with the calendar and the CRM.
var dayRe = regexp.MustCompile(`\{\{\s*(weekday|date|longdate|nth|iso):([+-]?\d+)\s*\}\}`)

// clock is the demo's now (a test sets it).
var clock = time.Now

// setDay is -day: the set's day (YYYY-MM-DD, hack/demo/clock.py's
// DEMO_DAY), which the scripted answers name days from — pinned, so a set
// filmed past midnight still agrees with its fixtures. Zero: demoDay's rule.
var setDay time.Time

// demoDay is the set's day: -day, else hack/demo/clock.py's rule on the
// clock — today on a weekday from 08:00, else the last working day before
// (a set seeded at night or on a weekend reads like that afternoon).
func demoDay() time.Time {
	if !setDay.IsZero() {
		return setDay
	}
	now := clock()
	day := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	weekend := func(d time.Time) bool { return d.Weekday() == time.Saturday || d.Weekday() == time.Sunday }
	if weekend(day) || now.Hour() < 8 {
		day = day.AddDate(0, 0, -1)
		for weekend(day) {
			day = day.AddDate(0, 0, -1)
		}
	}
	return day
}

// fillDays replaces the day placeholders (dayRe) in s.
func fillDays(s string) string {
	day := demoDay()
	return dayRe.ReplaceAllStringFunc(s, func(m string) string {
		sm := dayRe.FindStringSubmatch(m)
		n, _ := strconv.Atoi(sm[2])
		d, step := day, 1
		if n < 0 {
			n, step = -n, -1
		}
		for ; n > 0; n-- {
			d = d.AddDate(0, 0, step)
			for d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
				d = d.AddDate(0, 0, step)
			}
		}
		switch sm[1] {
		case "weekday":
			return d.Weekday().String()
		case "date":
			return d.Format("Jan 2")
		case "longdate":
			return d.Format("January 2")
		case "nth":
			k, suffix := d.Day(), "th"
			if k < 11 || k > 13 {
				switch k % 10 {
				case 1:
					suffix = "st"
				case 2:
					suffix = "nd"
				case 3:
					suffix = "rd"
				}
			}
			return strconv.Itoa(k) + suffix
		}
		return d.Format("2006-01-02")
	})
}

// fill replaces {{path}} with the value at path in the turn's newest JSON
// result ({{#k.path}}: its k-th), formatted by the filter; "—" when absent.
// Day placeholders (dayRe) are filled first.
func fill(s string, results []any) string {
	if !strings.Contains(s, "{{") {
		return s
	}
	s = fillDays(s)
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
