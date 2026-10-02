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
	// unmatched: the fallback instead of "ok: …"; the built-in keywords still answer
	if p = pick([]turn{user("tell me a joke")}, "", nil); p.Text != "Let me look into that." {
		t.Fatalf("fallback: %q", p.Text)
	}
	if p = pick([]turn{user("hello there")}, "", nil); p.Text != "Hello from the fake model." {
		t.Fatalf("a built-in keyword: %q", p.Text)
	}
}

// Day placeholders count working days from the demo's day: today on a
// weekday, the coming Monday on a weekend (hack/demo/seed.sh agrees).
func TestDemoDays(t *testing.T) {
	defer func() { clock = time.Now }()
	at := func(s string) func() time.Time {
		return func() time.Time { d, _ := time.ParseInLocation("2006-01-02 15:04", s, time.Local); return d }
	}
	for _, c := range []struct{ now, in, want string }{
		{"2026-10-03 00:40", "{{weekday:0}} {{date:0}}", "Monday Oct 5"},                     // a Saturday: the coming Monday
		{"2026-10-03 00:40", "{{weekday:-1}}, {{longdate:+4}}", "Friday, October 9"},         // back over the weekend
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
}
