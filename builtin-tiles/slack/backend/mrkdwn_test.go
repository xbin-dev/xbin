package main

import (
	"strings"
	"testing"
)

func TestToMrkdwn(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"**bold** and *it* and ~~gone~~", "*bold* and _it_ and ~gone~"},
		{"# Title\n- one\n* two", "*Title*\n• one\n• two"},
		{"see [the docs](https://x.dev/a?b=1&c=2)", "see <https://x.dev/a?b=1&amp;c=2|the docs>"},
		{"a <@U123> & b > c", "a &lt;@U123&gt; &amp; b &gt; c"},
		{"`**not bold**` but **bold**", "`**not bold**` but *bold*"},
		{"```go\nx := a < b\n```", "```\nx := a &lt; b\n```"},
		{"> quoted **text**", "> quoted *text*"},
		{"plain 2*3*4 math", "plain 2*3*4 math"},
	} {
		if got := toMrkdwn(tc.in); got != tc.want {
			t.Errorf("toMrkdwn(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFromSlack(t *testing.T) {
	names := func(id string) string { return map[string]string{"U2": "bo"}[id] }
	for _, tc := range []struct{ in, want string }{
		{"<@UBOT> deploy it", "deploy it"},
		{"ask <@U2> and <@U3|cy>", "ask @bo and @cy"},
		{"in <#C1|general>, see <https://x.dev|the site> or <https://y.dev>", "in #general, see the site (https://x.dev) or https://y.dev"},
		{"<!here> a &lt;b&gt; &amp; c", "@here a <b> & c"},
	} {
		if got := fromSlack(tc.in, "UBOT", names); got != tc.want {
			t.Errorf("fromSlack(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSplitMessage(t *testing.T) {
	if p := splitMessage("short", 100); len(p) != 1 {
		t.Fatalf("short: %v", p)
	}
	var b strings.Builder
	b.WriteString("intro\n```\n")
	for i := 0; i < 40; i++ {
		b.WriteString("line of code number something\n")
	}
	b.WriteString("```\nafter")
	parts := splitMessage(b.String(), 300)
	if len(parts) < 3 {
		t.Fatalf("parts: %d", len(parts))
	}
	for i, p := range parts {
		if len(p) > 300 {
			t.Errorf("part %d is %d bytes", i, len(p))
		}
		if strings.Count(p, "```")%2 != 0 {
			t.Errorf("part %d leaves a code block open: %q", i, p)
		}
	}
	long := strings.Repeat("é", 400)
	for _, p := range splitMessage(long, 300) {
		if !strings.HasPrefix(p, "é") {
			t.Fatalf("a cut split a rune: %q", p[:4])
		}
	}
}
