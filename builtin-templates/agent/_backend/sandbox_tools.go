// sandbox_tools.go — the coding toolset (D115): what a conversation does in
// the sandbox bound to it (sandbox_bind.go).
//
//	bash, bash_output, bash_kill, jobs   commands, and the jobs they become (sandbox_jobs.go)
//	read, write, edit, ls, glob, grep   files (sandbox_fs.go)
//	sandbox_upload, sandbox_download   session files ↔ the sandbox (sandbox_move.go)
//	sandbox_copy, sandbox_info         between attached sandboxes; what is attached
//	sandbox_create                     a new sandbox, the owner's grant (sandbox_create.go)
//
// subagent_spawn {sandbox, cwd} puts a subagent on another attached sandbox
// (spawnSandbox).
//
// The tools exist only when the conversation's class has the `sandbox`
// toolset AND a sandbox is bound (sandbox_create alone is offered unbound, to
// a top-level conversation with a manager its class allows); every call
// re-runs sandboxUse (the class, the manager, the sandbox, the binder's
// right). Paths resolve against the binding's cwd, `~` against the sandbox
// user's home. Nothing here touches the session files (file_*), which live
// in this tile's own store.
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
	"bash": true, "bash_output": true, "bash_kill": true, "jobs": true,
	"read": true, "write": true, "edit": true, "ls": true, "glob": true, "grep": true,
	"sandbox_upload": true, "sandbox_download": true, "sandbox_copy": true, "sandbox_info": true,
	"sandbox_create": true,
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

// sandboxToolSpecs: the coding tools where a sandbox is bound (subagents work
// in their root's sandbox too, or the one they were spawned onto), and
// sandbox_create where one may be made — bound or not.
func sandboxToolSpecs(cfg Config, depth int) []toolSpec {
	var specs []toolSpec
	if sandboxToolsOn(cfg) {
		specs = boundSandboxSpecs(cfg)
	}
	if sandboxCreateOffered(cfg, depth) {
		specs = append(specs, sandboxCreateSpec(classOf(cfg)))
	}
	return specs
}

func boundSandboxSpecs(cfg Config) []toolSpec {
	jobProp := intProp("the job number (bash's footer says it; jobs lists them)")
	specs := []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "bash",
			Description: "Run a shell command in this conversation's coding sandbox — a separate machine, not the session files (the file_* tools hold those) — with no terminal (TTY) and no stdin. " +
				"It runs in the sandbox's working directory unless cwd says otherwise: use non-interactive flags. " +
				"With no TTY, programs may block-buffer their output (Python is unbuffered here; for others use stdbuf -oL or the program's unbuffered flag). " +
				"Waits up to timeout_s (default 120; a tool call's time limit, about 2 minutes, cuts it) for it to finish; if it is still running then, it keeps running as a job — bash_output follows it, bash_kill stops it, jobs lists them. " +
				"background:true starts it as a job at once (servers, watchers, long builds). " +
				"Stop jobs with bash_kill {job}, never pkill -f or killall: those match other jobs' command lines too (a command using them isn't run unless force:true). " +
				"The result is the combined stdout and stderr (its head and tail when long, about 12 KB) and a footer like [exit 1 · 14s · job 3].",
			Parameters: obj([]string{"command"}, map[string]any{
				"command":    strProp("the command line, run by the sandbox user's login shell"),
				"cwd":        strProp("where to run it: absolute, relative to the working directory, or ~/…"),
				"timeout_s":  intProp("how long to wait for it before it goes on as a job (default 120; the footer says when the tool call's time limit cut it)"),
				"background": boolProp("start it as a job and return at once"),
				"force":      boolProp("run a command with pkill -f, pkill --full or killall anyway (it is refused without this)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "bash_output",
			Description: "Read a sandbox job's output since you last read it (or from byte offset), waiting up to wait_s for it to finish (at most 600, and a tool call's time limit, about 2 minutes, cuts it). " +
				"Says whether it still runs, and how it ended (exit code, or the signal that killed it).",
			Parameters: obj([]string{"job"}, map[string]any{
				"job":    jobProp,
				"wait_s": intProp("wait up to this many seconds for it to finish (default 0: what is there now)"),
				"offset": intProp("read from this byte of its output instead (0: from the start, as far as the sandbox kept it)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "bash_kill",
			Description: "Stop a sandbox job by its number — the way to stop one (never pkill -f or killall): the signal reaches its whole process group. " +
				"By default TERM, then KILL if it hasn't ended a few seconds later. Returns how it ended (exit code or signal) and the last output it wrote (about 4 KB).",
			Parameters: obj([]string{"job"}, map[string]any{
				"job":    jobProp,
				"signal": strProp("INT, TERM, KILL or HUP (default TERM, then KILL)"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "jobs",
			Description: "List this conversation's sandbox jobs (every bash command is one), newest first: the running ones as their sandbox reports them now, then the latest that ended — " +
				"number, state, how long, exit code or signal, and the command.",
			Parameters: obj(nil, map[string]any{
				"limit": intProp("how many (default 12)"),
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
	ctx = withSbxCall(ctx, sbxCall{run: run.ID, name: name, approve: cfg.Approve})
	switch name {
	case "sandbox_create":
		return ag.toolSandboxCreate(ctx, run, cfg, args)
	case "bash":
		return ag.toolBash(ctx, run, cfg, args)
	case "bash_output":
		return ag.toolBashOutput(ctx, run, cfg, args)
	case "bash_kill":
		return ag.toolBashKill(ctx, run, cfg, args)
	case "jobs":
		return ag.toolJobs(ctx, run, cfg, args)
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
	if len(b.Tools) > 0 {
		fmt.Fprintf(&s, "Its image has %s. ", strings.Join(b.Tools, ", "))
	}
	fmt.Fprintf(&s, "bash runs commands there; read, write, edit, ls, glob and grep work on its files. "+
		"The working directory is %s: relative paths resolve against it, ~ against the sandbox user's home. ", orStr(b.Cwd, "the sandbox's workdir"))
	s.WriteString("Files live in two places that don't see each other: the session files (the file_* tools and render_html; what the human sees and attaches) and this sandbox's own filesystem")
	if cfg.feature("files") {
		s.WriteString(" — sandbox_upload and sandbox_download copy between them")
	}
	s.WriteString(". ")
	s.WriteString("Every bash command is a numbered job: stop one with bash_kill {job}, never pkill -f or killall (they match other jobs too); jobs lists them, and yield {until_job} sleeps until one ends.")
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
