package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The demo script (-script) answers before the built-in keywords: a reply
// steps through tool calls by how many results the turn holds, resolves an
// "mcp:<tool>" name to the offered wire name, fills {{placeholders}} from the
// newest JSON tool result, names conversations, and replaces the catch-all
// "ok: …" with its fallback — while the harness keywords still play.
func TestDemoScript(t *testing.T) {
	f := filepath.Join(t.TempDir(), "demo.json")
	if err := os.WriteFile(f, []byte(`{
	  "models": ["assistant"],
	  "fallback": "Let me look into that.",
	  "replies": [{
	    "match": ["brightwell", "renewal"],
	    "title": "Brightwell renewal prep",
	    "steps": [
	      {"thinking": "Pulling the account and its open deals from the CRM first.", "calls": [{"name": "mcp:get_account", "args": {"query": "Brightwell", "summary": "Look up Brightwell"}}]},
	      {"calls": [{"name": "mcp:latest_report", "args": {"summary": "Read the ops report"}}]},
	      {"text": "Brightwell runs {{#1.account.vans}} vans; open: {{#1.deals.0.name}} ({{#1.deals.0.stage}}), {{#1.deals.0.amount|usd}} by {{#1.deals.0.close|date}}; {{missing.path}}; last night {{totals.stops|int}} stops."}
	    ]
	  }]
	}`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := loadDemo(f)
	if err != nil {
		t.Fatal(err)
	}
	demo = d
	defer func() { demo = nil }()

	user := func(s string) turn { return turn{Role: "user", Text: s} }
	tools := []string{"note", "mcp_apps_crm_search_accounts_1a2b3c4d", "mcp_apps_crm_get_account_9f8e7d6c"}

	p := pick([]turn{user("Prep me for the Brightwell renewal call?")}, "", tools)
	if len(p.Calls) != 1 || p.Calls[0].Name != "mcp_apps_crm_get_account_9f8e7d6c" || p.Calls[0].Args["query"] != "Brightwell" {
		t.Fatalf("step 0: %+v", p)
	}
	if strings.Join(p.Thinking, "") != "Pulling the account and its open deals from the CRM first." || len(p.Thinking) < 2 {
		t.Fatalf("thinking streams in phrases: %q", p.Thinking)
	}
	conv := []turn{user("Prep me for the Brightwell renewal call?"), {Role: "assistant"},
		{Role: "tool", Tool: "mcp_apps_crm_get_account_9f8e7d6c", Text: `{"account":{"vans":142},"deals":[{"name":"Fleet expansion","stage":"Negotiation","amount":50400,"close":"2026-10-11"}]}`}}
	report := "mcp_apps_ops-report_latest_report_0badf00d"
	if p = pick(conv, "", append(tools, report)); len(p.Calls) != 1 || p.Calls[0].Name != report {
		t.Fatalf("step 1: %+v", p)
	}
	conv = append(conv, turn{Role: "assistant"}, turn{Role: "tool", Tool: report, Text: "Report:\n{\"totals\":{\"stops\":10925}}"})
	want := "Brightwell runs 142 vans; open: Fleet expansion (Negotiation), $50,400 by Oct 11; —; last night 10,925 stops."
	if p = pick(conv, "", tools); p.Text != want || len(p.Calls) != 0 {
		t.Fatalf("step 2: %q, want %q", p.Text, want)
	}

	// titles: the script's, else the first words as a person would write them
	if p = pick([]turn{user("Name it.\nFirst message:\nbrightwell renewal, please\n")}, "Name this conversation in 3-7 words", nil); p.Text != "Brightwell renewal prep" {
		t.Fatalf("title: %q", p.Text)
	}
	if p = pick([]turn{user("First message:\nwhat changed in the driver app this week?")}, "Name this conversation", nil); p.Text != "What changed in the driver app" {
		t.Fatalf("plain title: %q", p.Text)
	}
	// unmatched: the fallback
	if p = pick([]turn{user("tell me a joke")}, "", nil); p.Text != "Let me look into that." || len(p.Calls) != 0 {
		t.Fatalf("fallback: %+v", p)
	}
}

// With a demo script loaded, the harness's test keywords never answer: a live
// take that types "hello", "quick", a steer, "delegate", "make a file", "use
// a tool", "new sandbox" or "restart me" gets the script's fallback at once —
// never "Hello from the fake model.", a test tool call or a 30 s hang — and
// so does a subagent; a compaction gets a plain summary, not "SUMMARY: …".
func TestDemoScriptOnly(t *testing.T) {
	demo = &demoScript{Fallback: "Ask me about a customer.", Replies: []demoReply{{Match: []string{"pipeline"}, Steps: []demoStep{{Text: "Pipeline: fine."}}}}}
	defer func() { demo = nil }()
	user := func(s string) turn { return turn{Role: "user", Text: s} }
	for _, s := range []string{"hello there", "a quick one", "steer: use tabs", "please delegate this", "make a file for me",
		"use a tool", "start a new sandbox", "restart me", "huge context", "long 3", "paras 2", "harness spawn", "sandbox pwd"} {
		start := time.Now()
		p := pick([]turn{user(s)}, "You are Merrow.", []string{"note", "file_write", "subagent_spawn", "sandbox_create"})
		if p.Text != "Ask me about a customer." || len(p.Calls) != 0 || p.Delay != 0 || p.Prompt > 1_000_000 {
			t.Errorf("%q: %+v, want the fallback", s, p)
		}
		if time.Since(start) > time.Second {
			t.Errorf("%q took %v", s, time.Since(start))
		}
	}
	// a tool result the script didn't ask for: still the fallback, not "done with …"
	if p := pick([]turn{user("hello"), {Role: "assistant"}, {Role: "tool", Tool: "note", Text: "ok"}}, "", nil); p.Text != "Ask me about a customer." {
		t.Errorf("after a tool: %+v", p)
	}
	// a matched reply still answers
	if p := pick([]turn{user("how is the pipeline?")}, "", nil); p.Text != "Pipeline: fine." {
		t.Errorf("a reply: %+v", p)
	}
	// a subagent: the fallback, never "subagent: <task>" or "one, two, three"
	if p := pick([]turn{user("count to three")}, "You are a subagent of an agent.", nil); p.Text != "Ask me about a customer." {
		t.Errorf("subagent: %+v", p)
	}
	// a compaction: a summary of the transcript in words
	tr := "Prior summary:\n(none)\n\nNew transcript to fold in:\n#3 user (priya): [a request — pinned verbatim in the task; do not restate it]\n" +
		"#4 assistant: \n  → mcp_apps_crm_get_account_9f8e7d6c {}\n#5 tool mcp_apps_crm_get_account_9f8e7d6c: {\"account\":{}}\n" +
		"#6 tool mcp_apps_ops-report_latest_report_0badf00d: {}\n#7 assistant: **Brightwell** renews on Nov 12.\nThen more."
	p := pick([]turn{user(tr)}, "You compact an AI agent's working context.", nil)
	if strings.Contains(p.Text, "SUMMARY") || !strings.Contains(p.Text, "#3–#7") || !strings.Contains(p.Text, "get_account, latest_report") ||
		!strings.Contains(p.Text, "**Brightwell** renews on Nov 12.") || strings.Contains(p.Text, "Then more") {
		t.Errorf("compaction: %q", p.Text)
	}
	if p := pick([]turn{user("nothing here")}, "You compact an AI agent's working context.", nil); p.Text == "" || strings.Contains(p.Text, "SUMMARY") {
		t.Errorf("an empty compaction: %q", p.Text)
	}
	// without a fallback of its own: a plain one
	demo.Fallback = ""
	if p := pick([]turn{user("hello")}, "", nil); p.Text != defaultFallback {
		t.Errorf("no fallback: %+v", p)
	}
}

// Day placeholders count working days from the demo's day: -day when given,
// else today on a weekday from 08:00, else the last working day before — a
// set seeded on a weekend or at night is dated that afternoon
// (hack/demo/clock.py agrees).
func TestDemoDays(t *testing.T) {
	defer func() { clock, setDay = time.Now, time.Time{} }()
	at := func(s string) func() time.Time {
		return func() time.Time { d, _ := time.ParseInLocation("2006-01-02 15:04", s, time.Local); return d }
	}
	for _, c := range []struct{ now, in, want string }{
		{"2026-10-03 00:40", "{{weekday:0}} {{date:0}}", "Friday Oct 2"},                     // a Saturday: the Friday before
		{"2026-10-04 15:00", "{{weekday:0}}, {{longdate:+4}}", "Friday, October 8"},          // a Sunday, and forward over the weekend
		{"2026-10-05 07:30", "{{weekday:0}} {{date:0}}", "Friday Oct 2"},                     // a Monday before 08:00: Friday's afternoon
		{"2026-10-05 21:00", "{{weekday:0}} {{date:0}}", "Monday Oct 5"},                     // a weekday's evening: that day
		{"2026-10-07 09:00", "{{weekday:+2}}, then {{ weekday:+3 }}", "Friday, then Monday"}, // forward over it
		{"2026-10-07 09:00", "the {{nth:-2}}, {{nth:+4}}, {{iso:+10}}", "the 5th, 13th, 2026-10-21"},
		{"2026-10-19 09:00", "the {{nth:+2}}, {{nth:+3}}, {{nth:+4}}, {{nth:+8}}", "the 21st, 22nd, 23rd, 29th"},
		{"2026-10-19 09:00", "{{weekday:+1}} {{#1.x}}", "Tuesday —"}, // results still fill after
	} {
		clock = at(c.now)
		if got := fill(c.in, nil); got != c.want {
			t.Errorf("at %s: fill(%q) = %q, want %q", c.now, c.in, got, c.want)
		}
	}
	// -day pins it, whatever the clock says
	clock = at("2026-10-10 03:00")
	setDay = time.Date(2026, 10, 2, 12, 0, 0, 0, time.Local)
	if got := fill("{{weekday:0}} {{date:+1}}", nil); got != "Friday Oct 5" {
		t.Errorf("-day: %q", got)
	}
}
