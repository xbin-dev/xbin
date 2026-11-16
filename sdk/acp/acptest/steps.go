package acptest

import (
	"fmt"
	"strings"

	"github.com/xbin-dev/xbin/sdk/acp"
)

// stepsMark starts the steps script: the rest of that line is shell
// commands separated by " ||| " (no shell writes that; a `ws:` script of hack/fakeopenai, which splits on " ;; ", can carry it).
const stepsMark = "fakeacp-steps:"

// stepsOf are the commands of a prompt's steps script (nil: it has none).
// The mark may stand anywhere in the prompt — a brief that carries it in
// one of its lines plays it.
func stepsOf(text string) []string {
	_, rest, ok := strings.Cut(text, stepsMark)
	if !ok {
		return nil
	}
	line, _, _ := strings.Cut(rest, "\n")
	var out []string
	for _, s := range strings.Split(line, " ||| ") {
		if s = strings.TrimSpace(s); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// steps plays a steps script the way a coding agent in an asking mode
// works: for each command an execute tool_call and a
// session/request_permission (once / always / no), then the command in a
// terminal of the client's, its output on the call. "Allow always" stops
// the asking for the rest of the turn; "no" fails that call and ends the
// turn ("denied step N"); modes auto and yolo ask nothing. It ends with
// "steps done: <the last command's output>". False: the turn was
// cancelled.
func (f *fake) steps(mode string, cmds []string) bool {
	sid := f.session()
	always := mode == "yolo" || mode == "auto"
	last := ""
	for i, cmd := range cmds {
		id := fmt.Sprintf("step%d", i+1)
		title := cmd
		if len(title) > 80 {
			title = title[:80] + "…"
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCall, "toolCallId": id, "title": title, "kind": "execute", "status": "pending",
			"rawInput": map[string]string{"command": cmd}})
		if !always {
			var res acp.RequestPermissionResult
			err := f.conn.Call(acp.MRequestPermission, acp.RequestPermissionParams{SessionID: sid,
				ToolCall: acp.ToolCallUpdate{ToolCallID: id, Title: strp(title), Kind: strp("execute")},
				Options: []acp.PermissionOption{{OptionID: "once", Name: "Allow once", Kind: "allow_once"},
					{OptionID: "always", Name: "Allow always", Kind: "allow_always"}, {OptionID: "no", Name: "Reject", Kind: "reject_once"}}}, &res)
			if err != nil || res.Outcome.Outcome != "selected" {
				f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": id, "status": "failed"})
				f.end("cancelled")
				return false
			}
			switch res.Outcome.OptionID {
			case "no":
				f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": id, "status": "failed"})
				f.say(fmt.Sprintf("denied step %d", i+1))
				return true
			case "always":
				always = true
			}
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": id, "status": "in_progress"})
		var cr acp.TermCreateResult
		if err := f.conn.Call(acp.MTermCreate, acp.TermCreateParams{SessionID: sid, Command: "sh", Args: []string{"-c", cmd + " 2>&1"}}, &cr); err != nil {
			f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": id, "status": "failed"})
			f.say(fmt.Sprintf("step %d error: %v", i+1, err))
			return true
		}
		var st acp.ExitStatus
		_ = f.conn.Call(acp.MTermWait, acp.TermIDParams{SessionID: sid, TerminalID: cr.TerminalID}, &st)
		var out acp.TermOutputResult
		_ = f.conn.Call(acp.MTermOutput, acp.TermIDParams{SessionID: sid, TerminalID: cr.TerminalID}, &out)
		_ = f.conn.Call(acp.MTermRelease, acp.TermIDParams{SessionID: sid, TerminalID: cr.TerminalID}, nil)
		code, status := 0, "completed"
		if st.ExitCode != nil && *st.ExitCode != 0 {
			code, status = *st.ExitCode, "failed"
		}
		f.update(map[string]any{"sessionUpdate": acp.UpToolCallUpdate, "toolCallId": id, "status": status,
			"content": []map[string]any{{"type": "terminal", "terminalId": id}},
			"_meta":   map[string]any{"terminal_output": map[string]any{"terminal_id": id, "data": out.Output}, "terminal_exit": map[string]any{"terminal_id": id, "exit_code": code}}})
		last = strings.Join(strings.Fields(out.Output), " ")
		if code != 0 {
			f.say(fmt.Sprintf("step %d failed (exit %d): %s", i+1, code, last))
			return true
		}
	}
	f.say("steps done: " + last)
	return true
}
