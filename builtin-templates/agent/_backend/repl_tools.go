// repl_tools.go — the model-facing surface of the JavaScript sandbox: js_eval,
// js_run and js_reset over the session VM (repl.go). The session files the VM
// reads and writes are files_store.go's.
//
// None of these is a sideEffect() tool. They mutate only this run's private
// sqlite rows — no world mutation, no egress, no reach into other components —
// so the approval gate in loop.go correctly never fires for them, and they are
// offered in BOTH capability lanes without widening either.
package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dop251/goja"
)

func replToolSpecs() []toolSpec {
	boolProp := func(desc string) map[string]any {
		return map[string]any{"type": "boolean", "description": desc}
	}
	return []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "js_eval",
			Description: "Evaluate JavaScript in this run's private sandbox; returns the last expression's value plus anything console.log'd. " +
				"State persists across calls, so you can build up variables and functions. " +
				"There is NO filesystem, network, require, or timers — pure computation over data you paste in or keep in session files. " +
				"Available beyond standard JS: console.*, files.read/write/list/exists/remove(path), and load(path) to evaluate a session file. " +
				"Use this for arithmetic, parsing, data wrangling, and for generating documents (build a string, files.write it). " +
				"Math.random is seeded per run, so results are reproducible. There is no event loop: a promise not settled by synchronous code never settles.",
			Parameters: obj([]string{"code"}, map[string]any{
				"code": strProp("JavaScript to evaluate. The value of the last expression is returned."),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "js_run",
			Description: "Evaluate a session file in the sandbox's global scope (the same as load(path) inside js_eval), and record it so the session rebuilds from your files after a restart. " +
				"Declare with var or globalThis.x = for anything you may reload — a file whose top level uses let/const cannot be loaded twice.",
			Parameters: obj([]string{"path"}, map[string]any{
				"path": strProp("file key"),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name:        "js_reset",
			Description: "Discard the sandbox session and its replay history, starting from a clean global scope. Session files are kept unless keep_files is false.",
			Parameters: obj(nil, map[string]any{
				"keep_files": boolProp("keep session files (default true)"),
			}),
		}},
	}
}

var replToolNames = map[string]bool{"js_eval": true, "js_run": true, "js_reset": true}

// runReplTool dispatches the sandbox tools, which all need the live VM. It owns
// the session gate, the log bookkeeping, and the result formatting. Called
// from runTool.
func (ag *Agent) runReplTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	s := ag.repl.get(run.ID, ag.db, cfg)

	if name == "js_reset" {
		ag.repl.drop(run.ID)
		if err := ag.db.replClearLog(run.ID); err != nil {
			return "", err
		}
		keep := true
		if v, ok := args["keep_files"].(bool); ok {
			keep = v
		}
		msg := "sandbox reset — the session starts from a clean global scope"
		if !keep {
			if err := ag.db.replClearFiles(run.ID); err != nil {
				return "", err
			}
			msg += ", and session files were deleted"
		} else {
			msg += "; session files kept (js_run them to redefine what you need)"
		}
		ag.db.journal(run.ID, "note", map[string]any{"text": "sandbox session reset"})
		return msg, nil
	}

	if leaks := ag.repl.leaks(); leaks >= maxReplQuarantine {
		return "", fmt.Errorf("the sandbox has %d stuck sessions and is not accepting new work "+
			"until the backend restarts (a previous statement is wedged in an uninterruptible "+
			"operation, most likely a catastrophic regular expression)", leaks)
	}

	if err := s.acquire(ctx); err != nil {
		return "", err
	}
	defer s.release()

	banner, err := s.ensure()
	if err != nil {
		return "", err
	}

	switch name {
	case "js_run":
		path, err := normReplPath(str(args["path"]))
		if err != nil {
			return "", err
		}
		f, ferr := ag.db.replFile(run.ID, path)
		if ferr != nil {
			return "", ferr
		}
		return ag.replExecLogged(ctx, s, run, "load", path, "file:"+path, f.Content, banner)
	default: // js_eval
		code := str(args["code"])
		if strings.TrimSpace(code) == "" {
			return "", fmt.Errorf("code is required")
		}
		return ag.replExecLogged(ctx, s, run, "eval", code, "", code, banner)
	}
}

// replExecLogged runs one statement with full bookkeeping: append the log row
// as `running` FIRST (so a statement that kills the process is identifiable
// after the restart), execute, then settle the row.
func (ag *Agent) replExecLogged(ctx context.Context, s *replSession, run *Run,
	kind, logCode, name, code, banner string) (string, error) {

	seq, created, err := ag.db.replAppend(run.ID, kind, logCode)
	if err != nil {
		return "", err
	}
	if name == "" {
		name = fmt.Sprintf("eval%d.js", seq)
	}
	// Pin the sandbox clock to the log row's timestamp for this statement, the
	// same value a replay will use. Freezing Date within one statement is the
	// price of a session that rebuilds to exactly the values the model last
	// saw — and elapsed-time measurement is meaningless in a sandbox with no
	// I/O anyway. Restored afterwards so an unlogged call still sees real time.
	prevNow := s.nowFn
	s.nowFn = func() time.Time { return time.UnixMilli(created) }
	defer func() { s.nowFn = prevNow }()

	r := s.exec(ctx, name, code, s.timeout)
	s.stmt++

	// Top-level await is something models write by reflex; goja rejects it
	// outside a module. Retry once wrapped, and log the WRAPPED source so a
	// replay reproduces exactly what ran.
	if r.err != nil && isAwaitSyntaxError(r.err) && kind == "eval" {
		wrapped := "(async () => {\n" + code + "\n})()"
		r2 := s.exec(ctx, name, wrapped, s.timeout)
		if r2.err == nil {
			ag.db.replFinish(run.ID, seq, replOK, r2.ms)
			_, _ = ag.db.sql.Exec(`UPDATE repl_log SET code=? WHERE run_id=? AND seq=?`, wrapped, run.ID, seq)
			out := ag.replFormat(s, r2, banner)
			return out + "\n(note: wrapped in an async IIFE because top-level await is not available here — " +
				"var/function declarations inside it did NOT reach global scope; assign to globalThis to persist.)", nil
		}
	}

	switch {
	case r.stuck:
		// The VM ignored the interrupt; it is unusable and cannot be freed.
		leaks := ag.repl.quarantined(run.ID)
		ag.db.replDrop(run.ID, seq)
		ag.db.journal(run.ID, "note", map[string]any{
			"text": fmt.Sprintf("sandbox session abandoned (stuck in an uninterruptible operation; %d total)", leaks)})
		return "", fmt.Errorf("the sandbox did not respond to the time limit and was discarded — " +
			"this is almost always a catastrophic regular expression. The session's variables are " +
			"gone; your session files are intact. Simplify the pattern and js_run your files again")
	case r.interrupt != nil:
		ag.db.replDrop(run.ID, seq) // partial effects are not reproducible
		return "", fmt.Errorf("%s (after %dms)%s", r.interrupt, r.ms, outputTail(r.output))
	case r.err != nil:
		ag.db.replFinish(run.ID, seq, replThrew, r.ms)
	default:
		ag.db.replFinish(run.ID, seq, replOK, r.ms)
	}

	if dropped := ag.db.replPrune(run.ID, maxReplLogEntries, maxReplLogBytes); dropped > 0 {
		banner += fmt.Sprintf("\n[%d older statement(s) dropped from the replay log — move durable "+
			"definitions into files (file_write + js_run); files always survive a rebuild]", dropped)
	}
	out := ag.replFormat(s, r, banner)
	if kind == "load" {
		out += "\n\n" + ag.db.replFileIndex(run.ID)
	}
	return out, nil
}

// replFormat renders a result the way the model reads best: a statement
// header (so parallel calls are distinguishable — runToolBatch returns results
// in call order, but same-run statements execute in whatever order the gate
// admits them), then output, then the value or the error.
func (ag *Agent) replFormat(s *replSession, r replResult, banner string) string {
	var b strings.Builder
	if banner != "" {
		b.WriteString(banner)
		b.WriteString("\n")
	}
	fmt.Fprintf(&b, "[stmt #%d · %dms]\n", s.stmt, r.ms)
	if r.output != "" {
		b.WriteString(r.output)
	}
	if r.err != nil {
		b.WriteString("!! " + replErrorText(s, r))
		return strings.TrimRight(b.String(), "\n")
	}
	val := s.formatValue(r.value)
	switch {
	case val == "undefined" && r.output != "":
		// A definition or a bunch of logs: printing "undefined" is pure noise.
	case val == "undefined":
		b.WriteString("(ok — no value, no output)")
	default:
		b.WriteString("=> " + val)
	}
	for _, rej := range r.rejections {
		b.WriteString("\n[unhandled promise rejection: " + rej + "]")
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatValue renders the completion value. Promises are read from Go because
// JS cannot observe a promise's state synchronously — and since goja drains
// the microtask queue before returning, anything still pending here can never
// settle: there is no event loop to settle it.
func (s *replSession) formatValue(v goja.Value) string {
	if v == nil || goja.IsUndefined(v) {
		return "undefined"
	}
	if p, ok := v.Export().(*goja.Promise); ok {
		switch p.State() {
		case goja.PromiseStateFulfilled:
			return "Promise { <fulfilled> " + s.inspectVal(p.Result()) + " }"
		case goja.PromiseStateRejected:
			return "Promise { <rejected> " + s.inspectVal(p.Result()) + " }"
		default:
			return "Promise { <pending> }\n(note: this sandbox has no event loop — there are no timers " +
				"and no I/O, so a promise not settled by synchronous code and microtasks never will be.)"
		}
	}
	return s.inspectVal(v)
}

func replErrorText(s *replSession, r replResult) string {
	if r.err == nil {
		return ""
	}
	var ex *goja.Exception
	if errors.As(r.err, &ex) {
		return s.inspectVal(ex.Value())
	}
	return r.err.Error()
}

func isAwaitSyntaxError(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "SyntaxError") &&
		(strings.Contains(s, "await") || strings.Contains(s, "Unexpected identifier"))
}

func outputTail(out string) string {
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return "\n--- output before it stopped ---\n" + out
}
