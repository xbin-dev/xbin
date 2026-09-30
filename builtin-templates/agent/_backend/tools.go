// tools.go — the tool set the agent can call. Opinionated builtins plus a seam
// for MCP-sourced tools. Control-flow tools (finish/ask_user/yield) and the
// subagent tools are advertised here but handled by the engine
// (actor_tools.go, subagent_tools.go), since they change run state; the rest
// execute here. These builtins are the parts
// you extend per instance — especially xbin_call, the point of the agent:
// moving data between xbin components (bounded by grants a human approved).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// obj builds a JSON-Schema object node from (name, schema) pairs.
func obj(required []string, props map[string]any) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		m["required"] = required
	}
	return m
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// finishSpec is finish, worded for who reads its result. A top-level run's
// result is a line under its answer (the ✓ step, plain text): the answer
// itself belongs in the reply, which renders as markdown — models put a
// whole report in result when told it is "the answer". A subagent's result
// IS what its parent receives.
func finishSpec(depth int) toolSpec {
	if depth > 0 {
		return toolSpec{Type: "function", Function: funcDef{
			Name: "finish", Description: "End your run and deliver its result to your parent: result is the answer your parent receives — make it the full, self-contained answer.",
			Parameters: obj([]string{"result"}, map[string]any{"result": strProp("the full answer / outcome, delivered to your parent")}),
		}}
	}
	return toolSpec{Type: "function", Function: funcDef{
		Name: "finish", Description: "End your turn; the conversation then waits for the person. " +
			"result is a SHORT status line (one or two sentences, shown as a ✓ line under your reply) — put the full answer or report in your normal reply BEFORE calling finish, where it renders as markdown, and don't repeat it in result.",
		Parameters: obj([]string{"result"}, map[string]any{"result": strProp("a one- or two-sentence status line — not the answer (that goes in your reply)")}),
	}}
}

// postedFinishSpec is finish for a top-level run whose result is POSTED as
// its reply — a channel conversation's, a trigger's (channelTurnEnd posts
// result; the last reply only when result is empty): there, result is the
// answer, as it always was.
func postedFinishSpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{
		Name: "finish", Description: "End your turn: result is posted to the conversation as your reply (basic Markdown works) — make it the full answer, not a status line.",
		Parameters: obj([]string{"result"}, map[string]any{"result": strProp("your reply, as posted — the full answer")}),
	}}
}

// runToolSpecs is toolSpecs for run: a top-level run whose result is posted
// (a channel's, a trigger's) gets postedFinishSpec's finish.
func runToolSpecs(cfg Config, run *Run, mcp []toolSpec) []toolSpec {
	specs := hostedToolSpecs(run, toolSpecs(cfg, run.Depth, mcp)) // a hosted conversation's lack (hosted_tools.go)
	if run.ParentID == 0 && (run.Origin == "channel" || run.Origin == "trigger") {
		for i := range specs {
			if specs[i].Function.Name == "finish" {
				specs[i] = postedFinishSpec()
			}
		}
	}
	return specs
}

// toolSpecs builds the model's tool list for this run's config, including
// any MCP-sourced tools. The run's class (classes.go, D116) decides which
// toolsets are offered — the core tools (memory, note, finish, yield,
// ask_user, recall, state_changed, attach_to_reply) always are — and the
// Features menu can still switch an optional one off. depth is the caller's
// position in the run graph: the delegation and scheduling tools are ABSENT
// at the limit rather than present-and-erroring, because leaves are the most
// numerous runs in any fan-out and would otherwise pay ~600 prompt tokens per
// call for tools they cannot use.
func toolSpecs(cfg Config, depth int, mcp []toolSpec) []toolSpec {
	cls := classOf(cfg)
	// A subagent has no one to ask: it would park on a human while its parent
	// parks on it, and nothing could answer either. The child contract tells it
	// to finish() with the blocker instead; the loop converts a hallucinated
	// call the same way.
	askUser := depth == 0
	specs := []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "memory_set", Description: "Save a note for this conversation (at most 8000 characters) — always shown to you under # Your notes and kept through compaction; not for the task, which is pinned verbatim under # Your task. For facts learned, decisions and a running plan (don't restate or reinterpret the task); setting a key again replaces it.",
			Parameters: obj([]string{"key", "value"}, map[string]any{"key": strProp("short note name"), "value": strProp("the note")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "memory_get", Description: "Read one of your notes by key (they are all shown under # Your notes already).",
			Parameters: obj([]string{"key"}, map[string]any{"key": strProp("note name")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "memory_delete", Description: "Delete one of your notes by key — for one that is done or no longer true.",
			Parameters: obj([]string{"key"}, map[string]any{"key": strProp("note name")}),
		}},
		{Type: "function", Function: funcDef{
			Name: "message_get", Description: "Read one message of THIS conversation in full by its #number — compacted turns and tool outputs shown as stubs too — 12000 characters per call (offset reads on).",
			Parameters: obj([]string{"seq"}, map[string]any{
				"seq":    map[string]any{"type": "integer", "description": "the message's #number (from a stub, recall, or # Your task)"},
				"offset": map[string]any{"type": "integer", "description": "character offset to start from (default 0)"},
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "note", Description: "Record a short visible note in the run timeline (for the human watching). No effect on control flow.",
			Parameters: obj([]string{"text"}, map[string]any{"text": strProp("the note")}),
		}},
		// --- control-flow tools (handled by the loop) ---
		finishSpec(depth),
		yieldSpec(cfg),
	}
	if askUser {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "ask_user", Description: "Pause and ask the human a question. The run resumes when they answer.",
			Parameters: obj([]string{"question"}, map[string]any{"question": strProp("what you need from the human")}),
		}})
	}
	if cfg.feature("recall") {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "recall", Description: "Search THIS conversation's whole history by words — turns compacted out of your context, tool outputs hidden as stubs, earlier summaries — for at most 20 short excerpts (8 by default), most relevant first. Each hit has its #number, and message_get reads one in full; order:oldest finds the first request.",
			Parameters: obj([]string{"query"}, map[string]any{
				"query": strProp("search words (plain words, stemmed)"),
				"match": map[string]any{"type": "string", "enum": []string{"all", "any"}, "description": "all (default): every word must match; any: at least one"},
				"order": map[string]any{"type": "string", "enum": []string{"relevant", "oldest", "newest"}, "description": "relevant (default), oldest first (e.g. the original request) or newest first"},
				"limit": map[string]any{"type": "integer", "description": "hits to return (default 8, at most 20)"},
			}),
		}})
	}
	// Cron-agents are top-level only. A subagent creating one is the unbounded
	// -spend path: the job outlives the tree that made it and answers to nobody.
	if depth == 0 && cls.has(tsSchedule) {
		specs = append(specs,
			toolSpec{Type: "function", Function: funcDef{
				Name: "schedule", Description: "Schedule future work: create a cron-agent that runs goal on a cadence. cron is a 5-field expression or '@every 30m'. " +
					"deliver says where each firing goes: \"here\" (default) — into this conversation, as a message you answer here (reminders, recurring checks the person wants to see); " +
					"\"new\" — a fresh run each time, listed under Automations (reports); \"thread\" — one ongoing run that builds on the previous firings. " +
					"Set watcher:true for a run that re-checks something and keeps only rounds where it reports a change.",
				Parameters: obj([]string{"cron", "goal"}, map[string]any{
					"cron":    strProp("5-field cron or @every <dur>"),
					"goal":    strProp("what each run should do"),
					"name":    strProp("optional label"),
					"deliver": map[string]any{"type": "string", "enum": []string{"here", "new", "thread"}, "description": "where each firing goes (default here)"},
					"watcher": map[string]any{"type": "boolean", "description": "watcher mode (discard no-change rounds)"},
				}),
			}},
			toolSpec{Type: "function", Function: funcDef{
				Name: "unschedule", Description: "Remove a cron-agent by its schedule id.",
				Parameters: obj([]string{"id"}, map[string]any{"id": map[string]any{"type": "integer", "description": "schedule id"}}),
			}})
	}
	// Looking at its automations and threads (threads_tools.go) — top-level
	// only, like scheduling: a subagent reports to its parent instead.
	if depth == 0 && cls.has(tsThreads) && cfg.feature("threads") {
		specs = append(specs, threadToolSpecs()...)
	}
	if cfg.feature("watcher") {
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "state_changed", Description: "In a watcher run, report that the watched state changed since the last check, with a short summary. Calling this keeps the round in history; not calling it discards the round.",
			Parameters: obj([]string{"summary"}, map[string]any{"summary": strProp("what changed")}),
		}})
	}
	if cls.has(tsSkills) && cfg.feature("skills") {
		specs = append(specs,
			toolSpec{Type: "function", Function: funcDef{
				Name: "skills_list", Description: "List your saved skills (reusable procedures you authored) — names + one-line descriptions.",
				Parameters: obj(nil, map[string]any{}),
			}},
			toolSpec{Type: "function", Function: funcDef{
				Name: "skill_view", Description: "Load a saved skill's full content by name.",
				Parameters: obj([]string{"name"}, map[string]any{"name": strProp("skill name")}),
			}},
			toolSpec{Type: "function", Function: funcDef{
				Name: "skill_manage", Description: "Save or remove a skill. action 'save' upserts {name, description, content}; action 'remove' deletes {name}.",
				Parameters: obj([]string{"action", "name"}, map[string]any{
					"action":      strProp("save | remove"),
					"name":        strProp("skill name"),
					"description": strProp("one-line description (for save)"),
					"content":     strProp("the skill body — steps/knowledge (for save)"),
				}),
			}})
	}
	// Session files + the render pane, and the JS sandbox over them. Safe in
	// any class because they have zero egress and zero internal reach — they
	// widen neither side of the firewall below (repl.go deliberately exposes no
	// host bridge).
	if cls.has(tsFiles) && cfg.feature("files") {
		specs = append(specs, fileToolSpecs(cfg)...)
		// Only offered when the run can actually see images.
		if cfg.feature("vision") {
			specs = append(specs, fileViewSpec(cfg))
		}
	}
	if cls.has(tsRepl) && cfg.feature("repl") {
		specs = append(specs, replToolSpecs()...)
	}
	if cfg.Channel && depth == 0 { // its answers are posted to a chat (channel_files.go)
		specs = append(specs, attachReplySpec())
	}
	// The toolset firewall is the class's (classes.go): a run gets internal
	// reach or egress, never both — otherwise injected/private content in
	// context could be exfiltrated via crafted URLs/queries — unless a manager
	// confirmed a class that mixes them. Enforced again at execution time in
	// runTool.
	if cls.has(tsWeb) {
		specs = append(specs, webToolSpecs()...) // web_search / web_fetch
	}
	if !cls.has(tsInternal) {
		mcp = nil // no internal MCP tools
	} else {
		mcp = classMCP(cls, mcp)
		specs = append(specs, toolSpec{Type: "function", Function: funcDef{
			Name: "xbin_call", Description: "Call another xbin component's API through the gateway (only components this agent has been granted). path like '/api/apps/other/thing'. Returns the response body.",
			Parameters: obj([]string{"method", "path"}, map[string]any{
				"method": strProp("HTTP method, e.g. GET or POST"),
				"path":   strProp("request path, e.g. /api/apps/calendar/events"),
				"body":   strProp("optional JSON request body"),
			}),
		}})
	}
	// The sandbox toolset (D115/D116): only a class with it, only when bound.
	specs = append(specs, sandboxToolSpecs(cfg, depth)...)
	if cls.has(tsSubagents) {
		specs = append(specs, subagentToolSpecs(cfg, depth)...)
	}
	specs = append(specs, mcp...)
	if len(cfg.Deny) == 0 {
		return specs
	}
	kept := specs[:0:0]
	for _, s := range specs {
		if !cfg.denied(s.Function.Name) {
			kept = append(kept, s)
		}
	}
	return kept
}

// toolsetOf is the class toolset a tool belongs to ("" = a core tool every
// class has).
func toolsetOf(name string) string {
	switch {
	case name == "xbin_call" || strings.HasPrefix(name, "mcp:"):
		return tsInternal
	case name == "web_search" || name == "web_fetch":
		return tsWeb
	case name == "schedule" || name == "unschedule":
		return tsSchedule
	case threadToolNames[name]:
		return tsThreads
	case name == "skills_list" || name == "skill_view" || name == "skill_manage":
		return tsSkills
	case fileToolNames[name]:
		return tsFiles
	case replToolNames[name]:
		return tsRepl
	case isSubagentTool(name):
		return tsSubagents
	case sandboxToolNames[name]:
		return tsSandbox
	}
	return ""
}

// classAllows refuses a tool outside the class's toolsets (and an MCP server
// the class doesn't name) — in the lanes' words where they apply.
func classAllows(cls agentClass, name string) error {
	ts := toolsetOf(name)
	switch {
	case ts == "":
		return nil
	case cls.has(ts):
		if strings.HasPrefix(name, "mcp:") && !cls.allowsMCP(mcpServerOf(name)) {
			return fmt.Errorf("the MCP server %q is not available in this conversation's class (%s)", mcpServerOf(name), cls.Name)
		}
		return nil
	case ts == tsInternal && cls.lane() == "web":
		return fmt.Errorf("%s is not available in the web toolset (no internal reach from web runs)", name)
	case ts == tsWeb && !cls.egress():
		return fmt.Errorf("%s is not available in the private toolset (no egress from private runs — start a web-toolset run instead)", name)
	}
	return fmt.Errorf("%s is not available in this conversation's class (%s)", name, cls.Name)
}

// mcpServerOf is the server an "mcp:<server>:<tool>" name calls.
func mcpServerOf(name string) string {
	server, _, _ := strings.Cut(strings.TrimPrefix(name, "mcp:"), ":")
	return server
}

// classMCP is the MCP tools the class's servers offer.
func classMCP(cls agentClass, mcp []toolSpec) []toolSpec {
	if cls.MCP.All {
		return mcp
	}
	var out []toolSpec
	for _, s := range mcp {
		if cls.allowsMCP(mcpServerOf(s.Function.Name)) {
			out = append(out, s)
		}
	}
	return out
}

// sideEffect reports whether a tool mutates the world (gated by approval mode).
// The file and REPL tools are deliberately NOT here: despite writing to
// sqlite, they touch only this run's private rows — no egress, no other
// component, nothing outside the run — so pausing a turn for approval would be
// pure friction. The coding tools that change a sandbox are, when it has
// egress (sandbox_tools.go).
func sideEffect(name string, cfg Config) bool {
	switch name {
	case "xbin_call":
		return true
	}
	return strings.HasPrefix(name, "mcp:") || sandboxSideEffect(name, cfg)
}

// runTool executes a non-control tool and returns its textual result.
func (ag *Agent) runTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	if cfg.denied(name) {
		return "", fmt.Errorf("%s is not available in this conversation", name)
	}
	// The class's toolsets (classes.go), whatever the model was offered: a
	// hallucinated call, or a transcript from another class, stops here.
	if err := classAllows(classOf(cfg), name); err != nil {
		return "", err
	}
	// Enforced here as well as by hiding the tools from deeper runs: a subagent
	// creating a cron-agent is the unbounded-spend path — the job outlives the
	// tree that made it — and a hallucinated call must not get through just
	// because the spec was absent from its list.
	if (name == "schedule" || name == "unschedule") && run.Depth > 0 {
		return "", fmt.Errorf("only a top-level run can create or remove cron-agents; report the cadence you want and let your parent (or the owner) set it up")
	}
	if err := hostedToolRefused(run, name); err != nil { // hosted_tools.go
		return "", err
	}
	switch name {
	case "memory_set":
		key, _ := args["key"].(string)
		val, _ := args["value"].(string)
		if key == "" {
			return "", fmt.Errorf("memory_set needs a key")
		}
		if len(val) > noteMax {
			return "", fmt.Errorf("a note holds at most %d characters (this one has %d) — keep the gist here and the detail in a file", noteMax, len(val))
		}
		if err := ag.db.memorySet(run.ID, key, val); err != nil {
			return "", err
		}
		return "stored " + key, nil

	case "memory_delete":
		key, _ := args["key"].(string)
		mem, err := ag.db.memory(run.ID)
		if err != nil {
			return "", err
		}
		if _, ok := mem[key]; !ok {
			return "", fmt.Errorf("there is no note %q", key)
		}
		if err := ag.db.memoryDelete(run.ID, key); err != nil {
			return "", err
		}
		return "deleted " + key, nil

	case "message_get":
		return ag.toolMessageGet(run, args)

	case "memory_get":
		key, _ := args["key"].(string)
		mem, err := ag.db.memory(run.ID)
		if err != nil {
			return "", err
		}
		if v, ok := mem[key]; ok {
			return v, nil
		}
		return "(no such memory block)", nil

	case "note":
		// The call itself is the visible note (the chat shows it as a card).
		return "noted", nil

	case "state_changed":
		summary, _ := args["summary"].(string)
		ag.db.markWatchChanged(run.ID)
		ag.db.journal(run.ID, "state_changed", map[string]string{"summary": summary})
		return "change recorded", nil

	case "schedule":
		s := &Schedule{
			Name:    fmt.Sprint(args["name"]),
			Cron:    strings.TrimSpace(fmt.Sprint(args["cron"])),
			Goal:    strings.TrimSpace(fmt.Sprint(args["goal"])),
			Watcher: args["watcher"] == true,
			// Agent-created schedules INHERIT the creating run's class and
			// lane — a private run must not be able to smuggle data into a
			// future web run's goal text (the firewall would leak through
			// time).
			Toolset: cfg.fixedLane(),
			Class:   classOf(cfg).ID,
			// It belongs to whoever owns this conversation (D83), and by
			// default reports back into it.
			CreatedByRun: run.ID,
			Mode:         modeConversation,
			TargetRun:    rootOf(run),
		}
		switch args["deliver"] {
		case "new":
			s.Mode, s.TargetRun = modeIsolated, 0
		case "thread":
			s.Mode, s.TargetRun = modePersistent, 0
		}
		if root, err := ag.db.getRun(rootOf(run)); err == nil {
			s.Owner, s.Visibility = root.Owner, root.Visibility
		}
		if s.Name == "<nil>" {
			s.Name = ""
		}
		if s.Cron == "" || s.Goal == "" {
			return "", fmt.Errorf("schedule needs cron and goal")
		}
		if s.Watcher && !cfg.feature("watcher") {
			return "", fmt.Errorf("watcher mode is disabled in Features")
		}
		id, err := ag.db.createSchedule(s)
		if err != nil {
			return "", err
		}
		s.ID, s.Enabled = id, true
		if err := ag.registerScheduleCron(s); err != nil {
			_ = ag.db.deleteSchedule(id)
			return "", fmt.Errorf("bad schedule: %w", err)
		}
		return fmt.Sprintf("scheduled #%d (%s)", id, s.Cron), nil

	case "unschedule":
		id := int64(toInt(args["id"]))
		// Only this conversation's own automations (D83): one it created, or
		// one belonging to the person whose conversation this is.
		sch, err := ag.db.getSchedule(id)
		if err != nil {
			return "", fmt.Errorf("no such schedule #%d", id)
		}
		owner := ""
		if root, err := ag.db.getRun(rootOf(run)); err == nil {
			owner = root.Owner
		}
		if sch.CreatedByRun != run.ID && sch.Owner != owner {
			return "", fmt.Errorf("schedule #%d belongs to someone else", id)
		}
		ag.unregisterScheduleCron(id)
		if err := ag.db.deleteSchedule(id); err != nil {
			return "", err
		}
		return fmt.Sprintf("removed schedule #%d", id), nil

	case "recall":
		return ag.toolRecall(run, args)

	case "xbin_call":
		return ag.toolXBinCall(ctx, args)

	case "web_search":
		return toolWebSearch(ctx, fmt.Sprint(args["query"]))

	case "web_fetch":
		return toolWebFetch(ctx, fmt.Sprint(args["url"]))

	case "skills_list", "skill_view", "skill_manage":
		return ag.runSkillTool(run, cfg, name, args)

	case "attach_to_reply":
		if !cfg.Channel || run.Depth > 0 {
			return "", fmt.Errorf("attach_to_reply is for conversations that answer into a chat")
		}
		return ag.toolAttachReply(run, args)
	}

	if threadToolNames[name] {
		return ag.runThreadTool(ctx, run, cfg, name, args)
	}
	if sandboxToolNames[name] {
		return ag.runSandboxTool(ctx, run, cfg, name, args)
	}
	if fileToolNames[name] {
		return ag.runFileTool(ctx, run, cfg, name, args)
	}
	if replToolNames[name] {
		return ag.runReplTool(ctx, run, cfg, name, args)
	}
	if strings.HasPrefix(name, "mcp:") {
		return ag.mcpCall(ctx, cfg, name, args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// toolXBinCall lets the agent reach other components through the gateway.
// Attribution + policy are the xbin gateway's job: the call only succeeds for
// components this element holds a grant on — the agent cannot widen its own
// reach (docs/auth.md).
func (ag *Agent) toolXBinCall(ctx context.Context, args map[string]any) (string, error) {
	method, _ := args["method"].(string)
	path, _ := args["path"].(string)
	body, _ := args["body"].(string)
	if method == "" {
		method = http.MethodGet
	}
	if !strings.HasPrefix(path, "/") {
		return "", fmt.Errorf("path must start with /")
	}
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(method), "http://xbin"+path, rdr)
	if err != nil {
		return "", err
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return fmt.Sprintf("HTTP %d\n%s", resp.StatusCode, strings.TrimSpace(string(out))), nil
}

// decodeArgs parses a tool call's JSON argument string into a map.
func decodeArgs(raw string) map[string]any {
	m := map[string]any{}
	if raw != "" {
		_ = json.Unmarshal([]byte(raw), &m)
	}
	return m
}
