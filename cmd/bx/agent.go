package main

// bx agent — drive an AGENT SESSION (docs/bx.md, docs/protocol.md; D74): a
// coding agent running in a tile's sandbox, prompted over the API, its
// events streamed here. `run` creates + prompts + attaches; `send` prompts
// a running one; `permit` answers a permission request; `attach` follows
// the stream (from the start, or a cursor); `ls`/`stop` manage them. The
// stream is the session's log replayed by cursor, so a client that was
// killed mid-turn attaches again and sees everything.

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// cmdExtra is main's default arm: the commands that did not fit the
// switch (main.go is at its size budget).
func cmdExtra(cmd string, args []string) error {
	switch cmd {
	case "agent":
		return cmdAgent(args)
	case "__agent-host":
		return cmdAgentHost()
	}
	usage()
	return nil
}

func cmdAgent(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bx agent run|send|permit|attach|ls|stop|set|history|resume … (docs/bx.md)")
	}
	switch args[0] {
	case "history":
		return cmdAgentHistory(args[1:])
	case "resume":
		return cmdAgentResume(args[1:])
	case "run":
		return cmdAgentRun(args[1:])
	case "send":
		return cmdAgentSend(args[1:])
	case "permit":
		return cmdAgentPermit(args[1:])
	case "attach":
		return cmdAgentAttach(args[1:])
	case "ls":
		return cmdAgentLs(args[1:])
	case "stop":
		return cmdAgentStop(args[1:])
	case "set":
		return cmdAgentSet(args[1:])
	}
	return fmt.Errorf("bx agent: unknown subcommand %q (run|send|permit|attach|ls|stop|set|history|resume)", args[0])
}

// cmdAgentHistory lists the caller's past agent sessions — the transcripts
// kept when a session ended (--tile narrows). resumable = the agent can
// reopen it (bx agent resume <id>).
func cmdAgentHistory(args []string) error {
	tile := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--tile" {
			v, err := nextArg(args, &i)
			if err != nil {
				return err
			}
			tile = v
		} else {
			return unknownFlag("agent history", args[i], false)
		}
	}
	var rows []struct {
		ID, Cwd, Provider, Mode, Name, Ended, Preview string
		Turns                                         int
		Loadable                                      bool
	}
	if err := apiJSON("GET", "/api/xbin/agent/history?cwd="+tile, nil, &rows); err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("no past agent sessions (a session is kept once it took a prompt and ended)")
		return nil
	}
	for _, r := range rows {
		how := "read-only"
		if r.Loadable {
			how = "resumable"
		}
		title := r.Name
		if title == "" {
			title = r.Preview
		}
		fmt.Printf("%-10s %-24s %-9s %-20s %2d turn(s)  %-9s  %s\n", r.ID, r.Cwd, r.Provider, r.Ended, r.Turns, how, orDash(title))
	}
	return nil
}

// cmdAgentResume reopens a past session (its provider, mode and name carry
// over; the agent replays the earlier turns, then continues). With a prompt,
// sends it and follows the turn; without, prints the live session id.
func cmdAgentResume(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: bx agent resume <past-session-id> [\"<prompt>\"]   (bx agent history lists them)")
	}
	id, text := args[0], strings.Join(args[1:], " ")
	var past []struct{ ID, Cwd string }
	if err := apiJSON("GET", "/api/xbin/agent/history", nil, &past); err != nil {
		return err
	}
	cwd := ""
	for _, p := range past {
		if p.ID == id {
			cwd = p.Cwd
		}
	}
	if cwd == "" {
		return fmt.Errorf("no past session %q (bx agent history lists them)", id)
	}
	var info struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Mode     string `json:"mode"`
		Cwd      string `json:"cwd"`
	}
	if err := apiJSON("POST", "/api/xbin/term/sessions", map[string]any{"cwd": cwd, "kind": "agent", "resume": id}, &info); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "session %s: %s on %s (mode %s) — resumed from %s\n", info.ID, info.Provider, info.Cwd, orDash(info.Mode), id)
	if text == "" {
		fmt.Printf("%s\n", info.ID)
		return nil
	}
	if err := agentPrompt(info.ID, text); err != nil {
		return err
	}
	return agentFollow(info.ID, 0, true)
}

// agentEvent is one entry of the session's log as the API renders it.
type agentEvent struct {
	Seq  uint64          `json:"seq"`
	TS   int64           `json:"ts"`
	Type string          `json:"type"`
	Data json.RawMessage `json:"data"`
}

type agentRunOpts struct {
	tile, provider, mode, net, name string
	options                         map[string]string // --model, --option k=v
	rest                            []string
}

// parseAgentRun reads `run`'s flags: --tile (default: this terminal's
// tile), --provider (default: $XBIN_AGENT_PROVIDER or claude), --mode,
// --model (a model the agent offers), --option k=v (any setting the agent
// advertises: effort, fast, …; repeatable), --net, --name; the rest is the
// prompt.
func parseAgentRun(args []string) (agentRunOpts, error) {
	o := agentRunOpts{tile: os.Getenv("XBIN_COMPONENT"), provider: os.Getenv("XBIN_AGENT_PROVIDER"), options: map[string]string{}}
	if o.provider == "" {
		o.provider = "claude"
	}
	for i := 0; i < len(args); i++ {
		a := args[i]
		if !isFlag(a) {
			o.rest = append(o.rest, a)
			continue
		}
		var err error
		switch a {
		case "--tile":
			o.tile, err = nextArg(args, &i)
		case "--provider", "-p":
			o.provider, err = nextArg(args, &i)
		case "--mode", "-m":
			o.mode, err = nextArg(args, &i)
		case "--model":
			o.options["model"], err = nextArg(args, &i)
		case "--option", "-o":
			var kv string
			if kv, err = nextArg(args, &i); err == nil {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || k == "" {
					err = fmt.Errorf("--option wants id=value (got %q)", kv)
				} else {
					o.options[k] = v
				}
			}
		case "--net":
			o.net, err = nextArg(args, &i)
		case "--name":
			o.name, err = nextArg(args, &i)
		default:
			err = unknownFlag("agent run", a, false)
		}
		if err != nil {
			return o, err
		}
	}
	if o.tile == "" {
		return o, errors.New("which tile? pass --tile <path> (inside a tile's terminal it defaults to that tile)")
	}
	return o, nil
}

// cmdAgentRun creates a session, sends the prompt, and follows the stream
// until the turn ends. Exit: 0 end_turn, 3 refusal/error, 130 cancelled.
func cmdAgentRun(args []string) error {
	o, err := parseAgentRun(args)
	if err != nil {
		return err
	}
	if len(o.rest) == 0 {
		return errors.New("usage: bx agent run [--tile <path>] [--provider p] [--mode m] [--model m] [--option id=v] [--net scope] \"<prompt>\"")
	}
	var info struct {
		ID       string `json:"id"`
		Provider string `json:"provider"`
		Mode     string `json:"mode"`
		Cwd      string `json:"cwd"`
	}
	body := map[string]any{"cwd": o.tile, "kind": "agent", "provider": o.provider, "mode": o.mode, "net": o.net, "name": o.name}
	if len(o.options) > 0 {
		body["options"] = o.options
	}
	if err := apiJSON("POST", "/api/xbin/term/sessions", body, &info); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "session %s: %s on %s (mode %s)\n", info.ID, info.Provider, info.Cwd, orDash(info.Mode))
	if err := agentPrompt(info.ID, strings.Join(o.rest, " ")); err != nil {
		return err
	}
	return agentFollow(info.ID, 0, true)
}

// cmdAgentSend prompts a running session and follows the turn.
func cmdAgentSend(args []string) error {
	if len(args) < 2 {
		return errors.New("usage: bx agent send <session> \"<text>\"")
	}
	var cur struct {
		Next uint64 `json:"next"`
	}
	_ = apiJSON("GET", "/api/xbin/term/sessions/"+args[0]+"/events?since=0", nil, &cur)
	if err := agentPrompt(args[0], strings.Join(args[1:], " ")); err != nil {
		return err
	}
	return agentFollow(args[0], cur.Next, true)
}

func agentPrompt(id, text string) error {
	return apiJSON("POST", "/api/xbin/term/sessions/"+id+"/prompt", map[string]string{"text": text}, nil)
}

// cmdAgentPermit answers a permission request: once | always | deny, or
// one of the request's own option ids (a plan approval's choices are modes —
// "clear context and auto", "keep planning" — so they are picked by id).
func cmdAgentPermit(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: bx agent permit <session> <pid> once|always|deny|<option id>")
	}
	body := map[string]string{"optionId": args[2]}
	if decision := map[string]string{"once": "allow_once", "always": "allow_always", "deny": "reject_once", "reject": "reject_once"}[args[2]]; decision != "" {
		body = map[string]string{"decision": decision}
	}
	return apiJSON("POST", "/api/xbin/term/sessions/"+args[0]+"/permissions/"+args[1], body, nil)
}

// cmdAgentAttach follows a session's stream: everything from the start
// (or --since <seq>), then live, until the session ends or Ctrl-C.
func cmdAgentAttach(args []string) error {
	var id string
	var since uint64
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--since":
			v, err := nextArg(args, &i)
			if err != nil {
				return err
			}
			since, _ = strconv.ParseUint(v, 10, 64)
		case isFlag(a):
			return unknownFlag("agent attach", a, false)
		default:
			id = a
		}
	}
	if id == "" {
		return errors.New("usage: bx agent attach <session> [--since <seq>]")
	}
	return agentFollow(id, since, false)
}

// cmdAgentLs lists the caller's agent sessions (--tile narrows).
func cmdAgentLs(args []string) error {
	tile := ""
	for i := 0; i < len(args); i++ {
		if args[i] == "--tile" {
			v, err := nextArg(args, &i)
			if err != nil {
				return err
			}
			tile = v
		} else {
			return unknownFlag("agent ls", args[i], false)
		}
	}
	var rows []struct {
		ID, Cwd, Name, Provider, Mode, Status, Kind, LastActive string
		Pending                                                 int
	}
	if err := apiJSON("GET", "/api/xbin/term/sessions?cwd="+tile, nil, &rows); err != nil {
		return err
	}
	n := 0
	for _, r := range rows {
		if r.Kind != "agent" {
			continue
		}
		n++
		pend := ""
		if r.Pending > 0 {
			pend = fmt.Sprintf("  %d permission(s) waiting", r.Pending)
		}
		fmt.Printf("%-10s %-24s %-9s %-14s %-18s %s%s\n", r.ID, r.Cwd, r.Provider, orDash(r.Mode), r.Status, orDash(r.Name), pend)
	}
	if n == 0 {
		fmt.Println("no agent sessions (bx agent run --tile <path> \"<prompt>\" starts one)")
	}
	return nil
}

func cmdAgentStop(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: bx agent stop <session>")
	}
	return apiJSON("DELETE", "/api/xbin/term/sessions/"+args[0], nil, nil)
}

// cmdAgentSet changes a session setting the agent advertised: model,
// effort, mode, … (the ids and values a session's idle status lists).
func cmdAgentSet(args []string) error {
	if len(args) != 3 {
		return errors.New("usage: bx agent set <session> <option> <value>   (e.g. model sonnet, effort high)")
	}
	return apiJSON("POST", "/api/xbin/term/sessions/"+args[0]+"/options", map[string]string{"id": args[1], "value": args[2]}, nil)
}

// agentFollow streams the log from `since` (NDJSON, ?follow=1) and renders
// it; reconnects from the last cursor if the connection drops while the
// session lives. untilTurnEnd stops at the first turn.end (run/send);
// otherwise it runs until the session ends. Ctrl-C detaches (the session
// keeps running) with exit 130.
func agentFollow(id string, since uint64, untilTurnEnd bool) error {
	base, client := transport()
	client = &http.Client{Timeout: 0, Transport: client.Transport}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	r := newAgentRenderer(os.Stdout, id)
	go agentKeys(ctx, id, r)
	cursor := since
	for {
		code, err := agentStreamOnce(ctx, client, base, id, &cursor, r, untilTurnEnd)
		if ctx.Err() != nil {
			fmt.Fprintf(os.Stderr, "\ndetached; the session keeps running: bx agent attach %s --since %d\n", id, cursor)
			os.Exit(130)
		}
		if err == nil {
			if code != 0 {
				os.Exit(code)
			}
			return nil
		}
		var he *httpErr
		if errors.As(err, &he) {
			return err // 404/403: the session is gone or not ours
		}
		fmt.Fprintf(os.Stderr, "\n[stream dropped: %v — reconnecting from %d]\n", err, cursor)
		time.Sleep(time.Second)
	}
}

type httpErr struct{ msg string }

func (e *httpErr) Error() string { return e.msg }

// agentStreamOnce is one follow connection; returns the exit code once the
// stream ended for good (nil error), or the error that dropped it.
func agentStreamOnce(ctx context.Context, client *http.Client, base, id string, cursor *uint64, r *agentRenderer, untilTurnEnd bool) (int, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", fmt.Sprintf("%s/api/xbin/term/sessions/%s/events?since=%d&follow=1", base, id, *cursor), nil)
	if err != nil {
		return 0, err
	}
	if tok := ownerToken(); tok != "" {
		req.Header.Set("Authorization", "Bearer "+tok)
	}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		var e struct{ Error string }
		_ = json.Unmarshal(b, &e)
		if e.Error == "" {
			e.Error = strings.TrimSpace(string(b))
		}
		return 0, &httpErr{fmt.Sprintf("%s (%s)", e.Error, resp.Status)}
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var e agentEvent
		if json.Unmarshal(sc.Bytes(), &e) != nil {
			continue
		}
		*cursor = e.Seq
		code, done := r.render(e, untilTurnEnd)
		if done {
			return code, nil
		}
	}
	if err := sc.Err(); err != nil {
		return 0, err
	}
	return r.exitCode, nil // the server closed the stream: the session ended
}

// agentKeys answers the latest permission request from the keyboard when
// stdin is a terminal: a line "a" (allow once), "s" (allow for the
// session), "d" (deny).
func agentKeys(ctx context.Context, id string, r *agentRenderer) {
	if fi, err := os.Stdin.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return
	}
	sc := bufio.NewScanner(os.Stdin)
	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		key := strings.TrimSpace(sc.Text())
		pid := r.lastPending()
		if pid == "" {
			continue
		}
		ans := map[string]string{"a": "once", "s": "always", "d": "deny"}[key]
		if ans == "always" && r.plan[pid] {
			ans = "" // a plan's allow_always options are modes, not "remember": pick one by id
		}
		if ans == "" {
			continue
		}
		if err := cmdAgentPermit([]string{id, pid, ans}); err != nil {
			fmt.Fprintln(os.Stderr, "bx:", err)
		}
	}
}

// agentRenderer turns events into terminal text.
type agentRenderer struct {
	w        io.Writer
	id       string
	pending  []string        // unanswered pids, oldest first
	plan     map[string]bool // pids that are plan approvals (switch_mode)
	inLine   bool            // an agent line is open (deltas print inline)
	role     string          // whose delta the open line is
	ready    bool            // the first idle (with the agent's modes) was shown
	exitCode int
}

func newAgentRenderer(w io.Writer, id string) *agentRenderer {
	return &agentRenderer{w: w, id: id, plan: map[string]bool{}}
}

func (r *agentRenderer) lastPending() string {
	if n := len(r.pending); n > 0 {
		return r.pending[n-1]
	}
	return ""
}

func (r *agentRenderer) br() {
	if r.inLine {
		fmt.Fprintln(r.w)
		r.inLine = false
	}
}

// render prints one event; done reports the stream should stop (a turn's
// end when untilTurnEnd, or the session's end), code the exit code.
func (r *agentRenderer) render(e agentEvent, untilTurnEnd bool) (code int, done bool) {
	d := map[string]any{}
	_ = json.Unmarshal(e.Data, &d)
	str := func(k string) string { s, _ := d[k].(string); return s }
	switch e.Type {
	case "message.delta":
		role := str("role")
		if role != r.role || !r.inLine {
			r.br()
			if role == "user" {
				fmt.Fprint(r.w, "> ")
			}
			r.role = role
		}
		fmt.Fprint(r.w, str("text"))
		r.inLine = true
	case "thought.delta":
		if r.role != "thought" || !r.inLine {
			r.br()
			fmt.Fprint(r.w, "\x1b[2m(thinking) ")
			r.role = "thought"
		}
		fmt.Fprint(r.w, strings.ReplaceAll(str("text"), "\n", " "))
		fmt.Fprint(r.w, "\x1b[0m")
		r.inLine = true
	case "tool.call":
		r.br()
		fmt.Fprintf(r.w, "⚙ %s %s [%s]\n", str("id"), orDash(toolHeadline(d)), str("kind"))
	case "tool.update":
		r.br()
		if st := str("status"); st != "" {
			fmt.Fprintf(r.w, "  %s %s%s\n", str("id"), st, diffStats(d["content"]))
		}
	case "files.changed":
		r.br()
		who := "turn " + num(d["turn"])
		if id := str("toolCallId"); id != "" {
			who = id
		}
		fmt.Fprintf(r.w, "  %s changed %s\n", who, filesLine(d["changes"]))
	case "plan":
		r.br()
		if entries, ok := d["entries"].([]any); ok {
			fmt.Fprintln(r.w, "plan:")
			for _, en := range entries {
				m, _ := en.(map[string]any)
				fmt.Fprintf(r.w, "  [%s] %s\n", orDash(fmt.Sprint(m["status"])), fmt.Sprint(m["content"]))
			}
		}
	case "permission.request":
		r.br()
		pid := str("pid")
		r.pending = append(r.pending, pid)
		tc, _ := d["toolCall"].(map[string]any)
		if plan := planOf(tc); plan != "" {
			r.plan[pid] = true
			meta, _ := d["meta"].(map[string]any)
			heading, _ := meta["title"].(string)
			if heading == "" {
				heading, _ = tc["title"].(string)
			}
			fmt.Fprintf(r.w, "⚠ plan %s: %s\n", pid, orDash(heading))
			for _, ln := range strings.Split(strings.TrimRight(plan, "\n"), "\n") {
				fmt.Fprintf(r.w, "  │ %s\n", ln)
			}
		} else {
			title, _ := tc["title"].(string)
			fmt.Fprintf(r.w, "⚠ permission %s: %s\n", pid, orDash(title))
		}
		if opts, ok := d["options"].([]any); ok {
			for _, o := range opts {
				m, _ := o.(map[string]any)
				fmt.Fprintf(r.w, "    %-14s %s (%s)\n", m["optionId"], m["name"], m["kind"])
			}
		}
		if r.plan[pid] {
			fmt.Fprintf(r.w, "  answer: bx agent permit %s %s <option id>   (or type a / d here)\n", r.id, pid)
		} else {
			fmt.Fprintf(r.w, "  answer: bx agent permit %s %s once|always|deny   (or type a / s / d here)\n", r.id, pid)
		}
	case "permission.resolved":
		r.br()
		pid := str("pid")
		for i, p := range r.pending {
			if p == pid {
				r.pending = append(r.pending[:i], r.pending[i+1:]...)
				break
			}
		}
		fmt.Fprintf(r.w, "  → %s: %s by %s\n", pid, orDash(str("optionId")), str("by"))
	case "turn.end":
		r.br()
		reason := str("stopReason")
		usage := ""
		if u, ok := d["usage"].(map[string]any); ok {
			usage = "  usage " + num(u["used"]) + "/" + num(u["size"])
		}
		fmt.Fprintf(r.w, "— turn %s ended (%s)%s\n", num(d["turn"]), reason, usage)
		switch reason {
		case "end_turn", "max_tokens", "max_turn_requests":
			code = 0
		case "cancelled":
			code = 130
		default:
			code = 3
		}
		r.exitCode = code
		if untilTurnEnd {
			return code, true
		}
	case "status":
		st := str("status")
		switch st {
		case "error", "exited":
			r.br()
			fmt.Fprintf(r.w, "[%s] %s\n", st, str("detail"))
			if st == "error" {
				r.exitCode = 3
			}
			return r.exitCode, true
		case "idle":
			if opts, ok := d["options"].([]any); ok && !r.ready {
				r.ready = true
				r.br()
				fmt.Fprintf(r.w, "[ready]%s%s\n", modeOf(str("currentMode")), optionsLine(opts))
			} else if mode := str("currentMode"); mode != "" && d["modes"] != nil && !r.ready {
				r.ready = true
				r.br()
				fmt.Fprintf(r.w, "[ready] mode %s\n", mode)
			}
		}
	}
	return 0, false
}

// filesLine lists a files.changed event's files: "a.go +3/-1, new.txt (added +2)".
func filesLine(changes any) string {
	cs, _ := changes.([]any)
	var parts []string
	for _, c := range cs {
		m, _ := c.(map[string]any)
		p, _ := m["path"].(string)
		switch st, _ := m["status"].(string); st {
		case "added", "deleted":
			p += " (" + st + ")"
		case "renamed":
			old, _ := m["oldPath"].(string)
			p = old + " → " + p
		}
		if b, _ := m["binary"].(bool); !b {
			p += " +" + num(m["add"]) + "/-" + num(m["del"])
		}
		parts = append(parts, p)
		if len(parts) == 8 && len(cs) > 8 {
			parts = append(parts, fmt.Sprintf("… %d more", len(cs)-8))
			break
		}
	}
	return strings.Join(parts, ", ")
}

// toolHeadline is what a tool call is called: the harness's description
// (label) when it gave one, else the title's first line.
func toolHeadline(d map[string]any) string {
	if s, _ := d["label"].(string); s != "" {
		return s
	}
	title, _ := d["title"].(string)
	if first, _, more := strings.Cut(title, "\n"); more {
		return first + " …"
	}
	return title
}

// planOf is the plan a permission request asks to approve (Claude's
// ExitPlanMode, Codex's plan review): the text content, else rawInput.plan;
// "" when it isn't a plan approval.
func planOf(tc map[string]any) string {
	raw, _ := tc["rawInput"].(map[string]any)
	plan, _ := raw["plan"].(string)
	if kind, _ := tc["kind"].(string); kind != "switch_mode" && plan == "" {
		return ""
	}
	items, _ := tc["content"].([]any)
	for _, it := range items {
		m, _ := it.(map[string]any)
		c, _ := m["content"].(map[string]any)
		if s, _ := c["text"].(string); strings.TrimSpace(s) != "" {
			return s
		}
	}
	if plan == "" {
		return "(no plan text)"
	}
	return plan
}

func modeOf(mode string) string {
	if mode == "" {
		return ""
	}
	return " mode " + mode
}

// optionsLine renders the agent's settings compactly: " · model sonnet · effort high".
func optionsLine(opts []any) string {
	var b strings.Builder
	for _, o := range opts {
		m, _ := o.(map[string]any)
		id, _ := m["id"].(string)
		cur, _ := m["currentValue"].(string)
		if id == "" || id == "mode" {
			continue
		}
		b.WriteString(" · " + id + " " + cur)
	}
	return b.String()
}

// num renders a JSON number as an integer (json decodes to float64).
func num(v any) string {
	if f, ok := v.(float64); ok {
		return strconv.FormatInt(int64(f), 10)
	}
	return fmt.Sprint(v)
}

// diffStats summarizes a tool update's diff content (+added/-removed lines).
func diffStats(content any) string {
	items, ok := content.([]any)
	if !ok {
		return ""
	}
	add, del := 0, 0
	for _, it := range items {
		m, _ := it.(map[string]any)
		if m["type"] != "diff" {
			continue
		}
		oldT, _ := m["oldText"].(string)
		newT, _ := m["newText"].(string)
		add += strings.Count(newT, "\n")
		del += strings.Count(oldT, "\n")
		if p, _ := m["path"].(string); p != "" {
			return fmt.Sprintf("  %s +%d/-%d", p, add, del)
		}
	}
	return ""
}
