package main

import (
	"context"
	"strings"
	"testing"
)

func TestToolsetFirewall(t *testing.T) {
	names := func(cfg Config, mcp []toolSpec) map[string]bool {
		out := map[string]bool{}
		for _, s := range toolSpecs(cfg, mcp) {
			out[s.Function.Name] = true
		}
		return out
	}
	mcp := []toolSpec{{Type: "function", Function: funcDef{Name: "mcp:comm:get_thread"}}}

	priv := names(Config{Subagents: true}, mcp)
	if priv["web_search"] || priv["web_fetch"] {
		t.Fatal("private toolset must not expose web tools")
	}
	if !priv["xbin_call"] || !priv["mcp:comm:get_thread"] {
		t.Fatal("private toolset must expose internal reach")
	}

	web := names(Config{Subagents: true, Toolset: "web"}, mcp)
	if !web["web_search"] || !web["web_fetch"] {
		t.Fatal("web toolset must expose web tools")
	}
	if web["xbin_call"] || web["mcp:comm:get_thread"] {
		t.Fatal("web toolset must not expose internal reach")
	}
	// Control/local tools present in both lanes.
	for _, n := range []string{"finish", "ask_user", "yield", "memory_set", "spawn_subagent"} {
		if !priv[n] || !web[n] {
			t.Fatalf("control tool %s missing from a lane", n)
		}
	}
}

func TestNormalizeToolset(t *testing.T) {
	for in, want := range map[string]string{"web": "web", " WEB ": "web", "": "private", "junk": "private", "private": "private"} {
		if got := normalizeToolset(in); got != want {
			t.Fatalf("normalizeToolset(%q)=%q want %q", in, got, want)
		}
	}
}

func TestScheduleToolsetRoundTrip(t *testing.T) {
	db := newTestDB(t)
	id, err := db.createSchedule(&Schedule{Name: "n", Cron: "@every 1h", Goal: "g", Toolset: "web"})
	if err != nil {
		t.Fatal(err)
	}
	s, err := db.getSchedule(id)
	if err != nil || s.Toolset != "web" {
		t.Fatalf("toolset round-trip: %+v err=%v", s, err)
	}
}

// TestToolsetFirewallAtExecution is the second wall: a call the model names
// without being offered it (a hallucination, or a transcript from another
// lane) must still be refused when it runs.
func TestToolsetFirewallAtExecution(t *testing.T) {
	db := newTestDB(t)
	ag := &Agent{db: db}
	id, _ := db.createRun("t", "", 0)
	run, _ := db.getRun(id)
	ctx := context.Background()
	web := Config{Toolset: "web"}
	for _, name := range []string{"xbin_call", "mcp:comm:get_thread"} {
		if _, err := ag.runTool(ctx, run, web, name, map[string]any{"method": "GET", "path": "/api/x"}); err == nil ||
			!strings.Contains(err.Error(), "web toolset") {
			t.Fatalf("%s ran in the web lane (err=%v)", name, err)
		}
	}
	for _, name := range []string{"web_search", "web_fetch"} {
		if _, err := ag.runTool(ctx, run, Config{}, name, map[string]any{"query": "x", "url": "https://example.com"}); err == nil ||
			!strings.Contains(err.Error(), "private toolset") {
			t.Fatalf("%s ran in the private lane (err=%v)", name, err)
		}
	}
}

func TestParseDDG(t *testing.T) {
	page := `<div class="result">
	  <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpage&amp;rut=abc">An <b>Example</b> Title</a>
	  <a class="result__snippet" href="#">Some &amp; snippet <i>text</i> here.</a>
	</div>`
	rs := parseDDG(page, 8)
	if len(rs) != 1 {
		t.Fatalf("want 1 result, got %d", len(rs))
	}
	if rs[0].url != "https://example.com/page" {
		t.Fatalf("uddg not decoded: %q", rs[0].url)
	}
	if rs[0].title != "An Example Title" {
		t.Fatalf("title: %q", rs[0].title)
	}
	if !strings.Contains(rs[0].snippet, "Some & snippet text") {
		t.Fatalf("snippet: %q", rs[0].snippet)
	}
}

func TestHTMLToText(t *testing.T) {
	in := `<html><head><style>body{color:red}</style><script>var x=1;</script></head>
	<body><h1>Hi</h1><p>One &amp; two.</p></body></html>`
	out := htmlToText(in)
	if strings.Contains(out, "color:red") || strings.Contains(out, "var x") {
		t.Fatalf("script/style leaked: %q", out)
	}
	if !strings.Contains(out, "Hi") || !strings.Contains(out, "One & two.") {
		t.Fatalf("text lost: %q", out)
	}
}
