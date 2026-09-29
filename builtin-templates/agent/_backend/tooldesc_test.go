package main

import (
	"strings"
	"testing"
)

// firstSentence is a description up to its first sentence's end.
func firstSentence(d string) string {
	for from := 0; ; {
		i := strings.Index(d[from:], ". ")
		if i < 0 {
			return d
		}
		i += from
		if strings.HasSuffix(d[:i], "e.g") || strings.HasSuffix(d[:i], "i.e") { // an abbreviation, not a sentence's end
			from = i + 2
			continue
		}
		return d[:i+1]
	}
}

// The first sentence of a tool's description is what a model weighs most
// when it picks a tool: it gives the purpose, the scope (run, sandbox,
// session) and the key limitation (D134, after "MCP Tool Descriptions Are
// Smelly!" and ToolBeHonest). These are pinned so a change to one is a
// reviewed change — update the text here with it, deliberately.
func TestToolDescriptionFirstSentences(t *testing.T) {
	want := map[string]string{
		"bash": "Run a shell command in this conversation's coding sandbox — a separate machine, not the session files (the file_* tools hold those) — with no terminal (TTY) and no stdin.",
		"bash_output": "Read a sandbox job's output since you last read it (or from byte offset), waiting up to wait_s for it to finish " +
			"(at most 600, and a tool call's time limit, about 2 minutes, cuts it).",
		"bash_kill": "Stop a sandbox job by its number — the way to stop one (never pkill -f or killall): the signal reaches its whole process group.",
		"jobs": "List this conversation's sandbox jobs (every bash command is one), newest first: the running ones as their sandbox reports them now, " +
			"then the latest that ended — number, state, how long, exit code or signal, and the command.",
		"yield": "Sleep for a while, then resume automatically (durable: it survives restarts) — early when a sandbox job you started ends, " +
			"or, with until_job, when that job ends.",
		"render_html": "Shows the human a STATIC snapshot of an HTML file — not a browser: scripts never run and external loads are blocked, " +
			"so it cannot verify JavaScript; use browser_check for that, or preview_port for a live page.",
		"read": "Read a text file in the coding sandbox — not a session file (file_read reads those) — as numbered lines (cat -n style), " +
			"the first 2000 unless offset/limit say otherwise (about 14 KB at most).",
		"write":        "Create or replace a whole file in the coding sandbox — not a session file (file_write writes those).",
		"edit":         "Replace an exact string in a text file (up to 4 MB) in the coding sandbox — not a session file (file_edit edits those).",
		"ls":           "List a directory in the coding sandbox (default: the working directory), at most 500 entries: subdirectories end in /, files show their size.",
		"glob":         "Find files by name in the coding sandbox, at most 200 paths (relative to the working directory).",
		"grep":         "Search file contents in the coding sandbox with a regular expression (ripgrep syntax where the sandbox has rg, else grep -E), at most 100 matches.",
		"preview_port": "Show the human a LIVE page served by a program in your sandbox (e.g. python3 -m http.server 8000) — scripts run, in an isolated frame.",
	}
	cfg := defaultConfig()
	cfg.Class = "coding"
	b := SandboxBinding{Ref: "apps/cs|sb-1", Name: "box", Egress: "none"}
	cfg.Sandbox, cfg.Attached = &b, []SandboxBinding{b}
	got := map[string]string{}
	for _, s := range toolSpecs(cfg, 0, nil) {
		got[s.Function.Name] = s.Function.Description
	}
	for name, w := range want {
		d, ok := got[name]
		if !ok {
			t.Errorf("%s: not offered to a coding conversation with a sandbox", name)
			continue
		}
		if f := firstSentence(d); f != w {
			t.Errorf("%s's first sentence changed:\n got: %s\nwant: %s", name, f, w)
		}
	}
	// without a sandbox, yield is the plain timer
	if f := firstSentence(yieldSpec(defaultConfig()).Function.Description); f != "Sleep for a while, then resume automatically (durable: it survives restarts)." {
		t.Errorf("yield without a sandbox: %s", f)
	}
}
