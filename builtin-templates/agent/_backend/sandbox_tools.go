// sandbox_tools.go — the coding toolset (D115): what a conversation does in
// the sandbox bound to it (sandbox_bind.go).
//
//	bash, bash_output, bash_kill   commands, and the jobs they become (sandbox_jobs.go)
//	read, write, edit, ls, glob, grep   files (sandbox_fs.go)
//	sandbox_upload, sandbox_download   session files ↔ the sandbox (sandbox_move.go)
//	sandbox_copy, sandbox_info         between attached sandboxes; what is attached
//
// subagent_spawn {sandbox, cwd} puts a subagent on another attached sandbox
// (spawnSandbox).
//
// The tools exist only when the conversation's class has the `sandbox`
// toolset AND a sandbox is bound; every call re-runs sandboxUse (the class,
// the manager, the sandbox, the binder's right). Paths resolve against the
// binding's cwd, `~` against the sandbox user's home. Nothing here touches
// the session files (file_*), which live in this tile's own store.
package main

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"
)

// toolCallKey carries a plain tool call's id to the tool (runOneTool): bash
// names its exec after it, so a call repeated after a restart finds the same
// command instead of starting another.
type toolCallKey struct{}

func withToolCall(ctx context.Context, id string) context.Context {
	return context.WithValue(ctx, toolCallKey{}, id)
}

func toolCallOf(ctx context.Context) string {
	s, _ := ctx.Value(toolCallKey{}).(string)
	return s
}

// sandboxToolsOn: the class has the sandbox toolset and a sandbox is bound.
func sandboxToolsOn(cfg Config) bool {
	return cfg.Sandbox != nil && classOf(cfg).has("sandbox")
}

var sandboxToolNames = map[string]bool{
	"bash": true, "bash_output": true, "bash_kill": true,
	"read": true, "write": true, "edit": true, "ls": true, "glob": true, "grep": true,
	"sandbox_upload": true, "sandbox_download": true, "sandbox_copy": true, "sandbox_info": true,
}

// sandboxChanges are the tools that change a sandbox: side effects (Approve
// mode parks them) only when the sandbox can reach out — one with no egress
// is private scratch.
var sandboxChanges = map[string]bool{
	"bash": true, "write": true, "edit": true, "sandbox_upload": true,
}

// sandboxSideEffect: name changes the bound sandbox, and it has egress.
func sandboxSideEffect(name string, cfg Config) bool {
	if name == "sandbox_copy" {
		return copyTouches(cfg)
	}
	if !sandboxChanges[name] || cfg.Sandbox == nil {
		return false
	}
	return cfg.Sandbox.Egress != "none"
}

func sandboxToolSpecs(cfg Config, depth int) []toolSpec {
	if !sandboxToolsOn(cfg) {
		return nil
	}
	_ = depth // subagents work in their root's sandbox too (or the one they were spawned onto)
	jobProp := intProp("the job number (bash's footer says it; sandbox_info lists them)")
	specs := []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "bash",
			Description: "Run a shell command in this conversation's coding sandbox (a separate machine — not the session files, which the file_* tools hold). " +
				"It runs in the sandbox's working directory unless cwd says otherwise, with no terminal and no stdin: use non-interactive flags. " +
				"Waits up to timeout_s (default 120) for it to finish; if it is still running then, it keeps running as a job — bash_output follows it, bash_kill stops it. " +
				"background:true starts it as a job at once (servers, watchers, long builds). " +
				"The result is the combined stdout and stderr (its head and tail when long, about 12 KB) and a footer like [exit 1 · 14s · job 3].",
			Parameters: obj([]string{"command"}, map[string]any{
				"command":    strProp("the command line, run by the sandbox user's login shell"),
				"cwd":        strProp("where to run it: absolute, relative to the working directory, or ~/…"),
				"timeout_s":  intProp("how long to wait for it before it goes on as a job (default 120)"),
				"background": boolProp("start it as a job and return at once"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "bash_output",
			Description: "Read a sandbox job's output since you last read it (or from byte offset), waiting up to wait_s for it to finish. " +
				"Says whether it still runs, and how it ended.",
			Parameters: obj([]string{"job"}, map[string]any{
				"job":    jobProp,
				"wait_s": intProp("wait up to this many seconds for it to finish (default 0: what is there now)"),
				"offset": intProp("read from this byte of its output instead (0: from the start, as far as the sandbox kept it)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "bash_kill",
			Description: "Stop a sandbox job: the signal reaches its whole process group. By default TERM, then KILL if it hasn't ended a few seconds later.",
			Parameters: obj([]string{"job"}, map[string]any{
				"job":    jobProp,
				"signal": strProp("INT, TERM, KILL or HUP (default TERM, then KILL)"),
			}),
		}},
	}
	specs = append(specs, sandboxFileSpecs()...)
	return append(specs, sandboxMoveSpecs(cfg)...)
}

// runSandboxTool dispatches the coding tools. Called from runTool.
func (ag *Agent) runSandboxTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	if !classOf(cfg).has("sandbox") {
		return "", fmt.Errorf("%s is not available: this conversation's class has no sandbox toolset", name)
	}
	switch name {
	case "bash":
		return ag.toolBash(ctx, run, cfg, args)
	case "bash_output":
		return ag.toolBashOutput(ctx, run, cfg, args)
	case "bash_kill":
		return ag.toolBashKill(ctx, run, cfg, args)
	}
	if sandboxFileTools[name] {
		return ag.runSandboxFileTool(ctx, run, cfg, name, args)
	}
	if sandboxMoveTools[name] {
		return ag.runSandboxMoveTool(ctx, run, cfg, name, args)
	}
	return "", fmt.Errorf("unknown sandbox tool %q", name)
}

// path resolves a model's path in the sandbox: absolute as is, ~ against the
// sandbox user's home, anything else against the working directory. "" is
// the working directory.
func (u *sbxUse) path(p string) (string, error) {
	p = strings.TrimSpace(p)
	if strings.ContainsRune(p, 0) || len(p) > 4096 {
		return "", fmt.Errorf("bad path %q", p)
	}
	switch {
	case p == "":
		p = u.cwd()
	case p == "~" || strings.HasPrefix(p, "~/"):
		if u.Box.Home == "" {
			return "", fmt.Errorf("the sandbox names no home directory — use an absolute path")
		}
		p = u.Box.Home + p[1:]
	case !strings.HasPrefix(p, "/"):
		p = path.Join(u.cwd(), p)
	}
	return path.Clean(p), nil
}

func (u *sbxUse) cwd() string {
	if u.Cwd == "" {
		return "/"
	}
	return u.Cwd
}

// rel shows a path relative to the working directory when it is inside it.
func (u *sbxUse) rel(p string) string {
	if c := u.cwd(); c != "/" && strings.HasPrefix(p, c+"/") {
		return p[len(c)+1:]
	}
	return p
}

// toolDeadline is when a waiting tool must answer: before the per-tool
// timeout (runOneTool) cuts it off, so a command outliving it becomes a job
// rather than an error.
func toolDeadline(ctx context.Context, want time.Duration) time.Time {
	until := time.Now().Add(want)
	if d, ok := ctx.Deadline(); ok {
		if lim := d.Add(-toolMargin); lim.Before(until) {
			until = lim
		}
	}
	return until
}

// toolMargin is kept before the per-tool timeout: the manager's answer and
// the result's bookkeeping fit in it.
var toolMargin = 3 * time.Second

// --- the prompt ----------------------------------------------------------------

// sandboxPrompt is the system prompt's # Sandbox section: from the binding
// snapshot only, so it changes on a rebind and nowhere else (the prompt's
// cached prefix stays valid). "" when no sandbox is bound.
func sandboxPrompt(cfg Config) string {
	if !sandboxToolsOn(cfg) {
		return ""
	}
	b := cfg.Sandbox
	var s strings.Builder
	s.WriteString("\n\n# Sandbox\n")
	fmt.Fprintf(&s, "You work in the coding sandbox %q (%s", orStr(b.Name, b.Ref), orStr(b.Manager, "its manager"))
	if b.Image != "" {
		fmt.Fprintf(&s, ", image %s", b.Image)
	}
	fmt.Fprintf(&s, "; %s). ", egressWords(b.Egress))
	fmt.Fprintf(&s, "bash runs commands there; read, write, edit, ls, glob and grep work on its files. "+
		"The working directory is %s: relative paths resolve against it, ~ against the sandbox user's home. ", orStr(b.Cwd, "the sandbox's workdir"))
	s.WriteString("It is a separate machine: the session files (file_*) are not in it")
	if cfg.feature("files") {
		s.WriteString(" — sandbox_upload and sandbox_download move files between the two")
	}
	s.WriteString(".")
	var others []string
	for _, a := range cfg.Attached {
		if a.Ref != b.Ref {
			others = append(others, fmt.Sprintf("%q (%s; %s)", orStr(a.Name, a.Ref), a.Ref, egressWords(a.Egress)))
		}
	}
	if len(others) > 0 {
		fmt.Fprintf(&s, "\nAlso attached: %s.", strings.Join(others, ", "))
	}
	return s.String()
}

func egressWords(e string) string {
	switch e {
	case "none":
		return "no network at all"
	case "internet":
		return "the public internet only"
	case "open":
		return "network as its manager gives it"
	}
	return "network access unknown"
}
