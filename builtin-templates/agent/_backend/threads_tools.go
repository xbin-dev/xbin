// threads_tools.go — the agent looking at its automations and at the
// person's other conversations (D111): schedules_list, schedule_inspect,
// threads_list, thread_inspect.
//
// Two scopes, always decided on the conversation the call is made in:
//
//   - mine (free): the schedules this conversation created or that deliver
//     into it, the threads those schedules ran, and this conversation's own
//     tree.
//   - all: what the conversation's OWNER has — every thread they own or were
//     added to (not other people's team conversations, not held drafts) and
//     every schedule they own. It needs their grant (grants.go): execTools
//     parks the step until they allow it once or for an hour.
//
// "all" is refused outright where no one can answer it or where it would
// breach the lanes: a web-toolset run (a web lane carries public data only —
// it also sees only web-toolset automations in mine), a chat channel's run,
// a conversation no person owns. A thread outside both reads as missing.
//
// A schedule removed since keeps its past runs, but they are no longer
// "mine" (nothing records which conversation made them): scope all finds
// them.
package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

var threadToolNames = map[string]bool{"schedules_list": true, "schedule_inspect": true, "threads_list": true, "thread_inspect": true}

const (
	scopeMine = "mine"
	scopeAll  = "all"
)

func threadToolSpecs() []toolSpec {
	scope := map[string]any{"type": "string", "enum": []string{scopeMine, scopeAll},
		"description": "mine (default): this conversation's own; all: everything the person has — they are asked to allow it"}
	cursor := strProp("from the previous page's \"more:\" line")
	limit := func(def, maxN int) map[string]any {
		return map[string]any{"type": "integer", "description": fmt.Sprintf("page size (default %d, at most %d)", def, maxN)}
	}
	return []toolSpec{
		{Type: "function", Function: funcDef{
			Name: "schedules_list",
			Description: "List scheduled automations (cron-agents and watchers): id, cadence, where firings go, their last run. " +
				"scope mine (default): the ones this conversation created or that report into it. scope all: every schedule the person owns " +
				"(they are asked for permission). Paged — pass the returned cursor for more.",
			Parameters: obj(nil, map[string]any{
				"scope":   scope,
				"enabled": map[string]any{"type": "boolean", "description": "only enabled (true) or only disabled (false) ones"},
				"q":       strProp("words in the name or goal"),
				"cursor":  cursor,
				"limit":   limit(20, 50),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "schedule_inspect",
			Description: "Look at one schedule: its settings and goal, and the runs it fired, newest first with each run's latest answer (paged). " +
				"thread_inspect a run for its transcript.",
			Parameters: obj([]string{"id"}, map[string]any{
				"id":     map[string]any{"type": "integer", "description": "schedule id"},
				"cursor": cursor,
				"limit":  limit(10, 30),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "threads_list",
			Description: "List threads — conversations and automation runs — newest activity first. " +
				"scope mine (default): the threads this conversation's schedules ran. scope all: every conversation the person owns or was added to " +
				"(they are asked for permission). Filter by origin, status, words (title or anything said) and archived. Paged — pass the returned cursor for more.",
			Parameters: obj(nil, map[string]any{
				"scope":    scope,
				"origin":   map[string]any{"type": "string", "enum": []string{"chat", "schedule", "watcher", "channel", "trigger", "api"}, "description": "where the thread came from"},
				"status":   map[string]any{"type": "string", "enum": []string{"idle", "running", "waiting", "awaiting", "sleeping", "done", "error", "canceled"}, "description": "its state now"},
				"q":        strProp("words in the title or anything said in it"),
				"archived": map[string]any{"type": "boolean", "description": "only archived (true) or only not archived (false); both when absent"},
				"cursor":   cursor,
				"limit":    limit(20, 50),
			}),
		}},
		{Type: "function", Function: funcDef{
			Name: "thread_inspect",
			Description: "Read one thread: what it is (origin, owner, state), the summary of its compacted turns, then its messages numbered by seq — " +
				"the latest page; pass before=<seq> for older ones.",
			Parameters: obj([]string{"id"}, map[string]any{
				"id":     map[string]any{"type": "integer", "description": "thread (run) id"},
				"before": map[string]any{"type": "integer", "description": "only messages before this seq"},
				"limit":  limit(30, 100),
			}),
		}},
	}
}

// threadCtx is where a thread tool is called from.
type threadCtx struct {
	root  *Run   // the conversation
	owner string // whose "all" it is
	web   bool   // a web-toolset run: web automations only, no "all"
	why   string // why "all" is refused here ("" = it may be asked for)
}

func (ag *Agent) threadCtxOf(run *Run, cfg Config) (*threadCtx, error) {
	if run.Depth > 0 {
		return nil, fmt.Errorf("only a top-level run can look at threads and schedules; ask your parent")
	}
	root, err := ag.db.getRun(rootOf(run))
	if err != nil {
		return nil, err
	}
	tc := &threadCtx{root: root, owner: root.Owner, web: cfg.toolset() == "web"}
	switch {
	case tc.web:
		tc.why = "the person's other conversations can't be read from a web-toolset run (a web lane carries public data only) — use scope mine, or ask from a private conversation"
	case cfg.Channel:
		tc.why = "the person's other conversations can't be read from a chat channel — no one here can allow it; they can ask in the agent's page"
	case !person(root.Owner):
		tc.why = "no person owns this conversation, so no one can allow reading other conversations — use scope mine"
	}
	return tc, nil
}

// mineSchedules is the SQL for this conversation's schedules (alias s).
func (tc *threadCtx) mineSchedules() (string, []any) {
	q := `(s.created_by_run=? OR s.target_run=?)`
	if tc.web {
		q += ` AND s.toolset='web'`
	}
	return q, []any{tc.root.ID, tc.root.ID}
}

// ownerSees keeps runs (alias r) the conversation's owner may read — a
// mine schedule's thread is still someone's.
func (tc *threadCtx) ownerSees() (string, []any) {
	if !person(tc.owner) {
		return `r.owner=?`, []any{tc.owner}
	}
	return `(r.owner=? OR r.visibility='team' OR EXISTS (SELECT 1 FROM run_members m WHERE m.run_id=r.id AND m.user=?))`, []any{tc.owner, tc.owner}
}

// allThreads is the SQL for the owner's threads (alias r): owned or joined.
func (tc *threadCtx) allThreads() (string, []any) {
	return `r.parent_id=0 AND r.origin<>'held' AND (r.owner=? OR EXISTS (SELECT 1 FROM run_members m WHERE m.run_id=r.id AND m.user=?))`,
		[]any{tc.owner, tc.owner}
}

// mineThreads is the SQL for the threads this conversation's schedules ran.
func (tc *threadCtx) mineThreads() (string, []any) {
	sq, sa := tc.mineSchedules()
	oq, oa := tc.ownerSees()
	return `r.parent_id=0 AND r.origin IN ('schedule','watcher') AND r.origin_id IN (SELECT s.id FROM schedules s WHERE ` + sq + `) AND ` + oq,
		append(sa, oa...)
}

func (ag *Agent) countWhere(table, where string, args ...any) int {
	var n int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM `+table+` WHERE `+where, args...).Scan(&n)
	return n
}

// threadScope is which scope a call reads. An error is a refusal or a miss,
// reported to the model as it is.
func (ag *Agent) threadScope(tc *threadCtx, name string, args map[string]any) (string, error) {
	switch name {
	case "schedules_list", "threads_list":
		switch s, _ := args["scope"].(string); s {
		case "", scopeMine:
			return scopeMine, nil
		case scopeAll:
			if tc.why != "" {
				return "", fmt.Errorf("%s", tc.why)
			}
			return scopeAll, nil
		default:
			return "", fmt.Errorf("scope is mine or all")
		}
	case "schedule_inspect":
		id := int64(toInt(args["id"]))
		sq, sa := tc.mineSchedules()
		if ag.countWhere(`schedules s`, `s.id=? AND `+sq, append([]any{id}, sa...)...) > 0 {
			return scopeMine, nil
		}
		if tc.why != "" {
			return "", fmt.Errorf("schedule #%d isn't this conversation's — %s", id, tc.why)
		}
		if ag.countWhere(`schedules s`, `s.id=? AND s.owner=?`, id, tc.owner) > 0 {
			return scopeAll, nil
		}
		return "", fmt.Errorf("no such schedule #%d", id)
	case "thread_inspect":
		id := int64(toInt(args["id"]))
		run, err := ag.db.getRun(id)
		if err != nil {
			return "", fmt.Errorf("no such thread #%d", id)
		}
		top := rootOf(run)
		if top == tc.root.ID {
			return scopeMine, nil
		}
		mq, ma := tc.mineThreads()
		if ag.countWhere(`runs r`, `r.id=? AND `+mq, append([]any{top}, ma...)...) > 0 {
			return scopeMine, nil
		}
		if tc.why != "" {
			return "", fmt.Errorf("thread #%d isn't this conversation's — %s", id, tc.why)
		}
		aq, aa := tc.allThreads()
		if ag.countWhere(`runs r`, `r.id=? AND `+aq, append([]any{top}, aa...)...) > 0 {
			return scopeAll, nil
		}
		return "", fmt.Errorf("no such thread #%d", id)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

// threadsGrantNeeded (grantDefs): a thread tool reading scope all without a
// live grant.
func threadsGrantNeeded(ag *Agent, run *Run, cfg Config, calls []toolCall, own map[string]bool) bool {
	var tc *threadCtx
	for _, c := range calls {
		name := c.Function.Name
		if !threadToolNames[name] || cfg.denied(name) || !cfg.feature("threads") {
			continue
		}
		if tc == nil {
			var err error
			if tc, err = ag.threadCtxOf(run, cfg); err != nil {
				return false
			}
		}
		args, _ := callArgs(c, own)
		if sc, err := ag.threadScope(tc, name, args); err == nil && sc == scopeAll && !ag.db.liveGrant(tc.root.ID, capThreads) {
			return true
		}
	}
	return false
}

// runThreadTool executes one of the four.
func (ag *Agent) runThreadTool(ctx context.Context, run *Run, cfg Config, name string, args map[string]any) (string, error) {
	if !cfg.feature("threads") {
		return "", fmt.Errorf("the thread and schedule tools are off in Features")
	}
	tc, err := ag.threadCtxOf(run, cfg)
	if err != nil {
		return "", err
	}
	sc, err := ag.threadScope(tc, name, args)
	if err != nil {
		return "", err
	}
	if sc == scopeAll && !ag.db.liveGrant(tc.root.ID, capThreads) && !grantedOnce(ctx, capThreads) {
		// execTools parks for the grant first; this is the backstop
		return "", fmt.Errorf("%s", grantOf(capThreads).Forbid)
	}
	switch name {
	case "schedules_list":
		return ag.toolSchedulesList(tc, sc, args)
	case "schedule_inspect":
		return ag.toolScheduleInspect(tc, args)
	case "threads_list":
		return ag.toolThreadsList(tc, sc, args)
	case "thread_inspect":
		return ag.toolThreadInspect(tc, args)
	}
	return "", fmt.Errorf("unknown tool %q", name)
}

func pageLimit(v any, def, maxN int) int {
	n := toInt(v)
	if n <= 0 {
		return def
	}
	return min(n, maxN)
}

// ago is a moment (unix ms) as "5m ago".
func ago(ms int64) string {
	if ms <= 0 {
		return "never"
	}
	d := time.Since(time.UnixMilli(ms))
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	}
	return fmt.Sprintf("%dd ago", int(d.Hours()/24))
}

// flat is s on one line, clipped to n bytes.
func flat(s string, n int) string {
	return clip(strings.Join(strings.Fields(s), " "), n)
}

func statusWord(s string) string {
	if s == statusWaiting {
		return "waiting"
	}
	return s
}

// delivery says where a schedule's firings go.
func (ag *Agent) delivery(s *Schedule, root int64) string {
	switch {
	case s.Watcher:
		if s.RunID != 0 {
			return fmt.Sprintf("watcher, thread #%d", s.RunID)
		}
		return "watcher"
	case s.Mode == modeConversation && s.TargetRun == root:
		return "into this conversation"
	case s.Mode == modeConversation:
		return fmt.Sprintf("into conversation #%d", s.TargetRun)
	case s.Mode == modePersistent:
		if id, _ := ag.db.sessionRun("sched:" + strconv.FormatInt(s.ID, 10)); id != 0 {
			return fmt.Sprintf("one ongoing thread, #%d", id)
		}
		return "one ongoing thread"
	}
	return "a new thread each firing"
}

func (ag *Agent) toolSchedulesList(tc *threadCtx, sc string, args map[string]any) (string, error) {
	var where string
	var qa []any
	if sc == scopeAll {
		where, qa = `s.owner=?`, []any{tc.owner}
	} else {
		where, qa = tc.mineSchedules()
	}
	if e, ok := args["enabled"].(bool); ok {
		where += ` AND s.enabled=?`
		qa = append(qa, b2i(e))
	}
	if q := strings.TrimSpace(fmt.Sprint(orNil(args["q"]))); q != "" {
		like := "%" + strings.ReplaceAll(q, "%", "") + "%"
		where += ` AND (s.name LIKE ? OR s.goal LIKE ?)`
		qa = append(qa, like, like)
	}
	if cur := fmt.Sprint(orNil(args["cursor"])); cur != "" {
		n, err := strconv.ParseInt(cur, 10, 64)
		if err != nil {
			return "", fmt.Errorf("bad cursor")
		}
		where += ` AND s.id<?`
		qa = append(qa, n)
	}
	limit := pageLimit(args["limit"], 20, 50)
	rows, err := ag.db.q.Query(`SELECT `+prefixed("s.", scheduleCols)+` FROM schedules s WHERE `+where+
		` ORDER BY s.id DESC LIMIT `+strconv.Itoa(limit+1), qa...)
	if err != nil {
		return "", err
	}
	var list []*Schedule
	for rows.Next() {
		if s, err := scanSchedule(rows.Scan); err == nil {
			list = append(list, s)
		}
	}
	rows.Close()
	more := len(list) > limit
	if more {
		list = list[:limit]
	}
	if len(list) == 0 {
		return "(no schedules match)", nil
	}
	var b strings.Builder
	for _, s := range list {
		state := "enabled"
		if !s.Enabled {
			state = "disabled"
		}
		fmt.Fprintf(&b, "#%d %q — %s — %s — %s", s.ID, orStr(s.Name, flat(s.Goal, 40)), s.Cron, ag.delivery(s, tc.root.ID), state)
		if s.LastRunID != 0 {
			fmt.Fprintf(&b, " — last run #%d %s, %s", s.LastRunID, orStr(s.LastStatus, "?"), ago(s.LastRun*1000))
		} else if s.LastRun != 0 {
			fmt.Fprintf(&b, " — last fired %s", ago(s.LastRun*1000))
		}
		if sc == scopeAll && s.CreatedByRun == tc.root.ID {
			b.WriteString(" — made here")
		}
		fmt.Fprintf(&b, "\n    goal: %s\n", flat(s.Goal, 140))
	}
	if more {
		fmt.Fprintf(&b, "more: cursor %q\n", strconv.FormatInt(list[len(list)-1].ID, 10))
	}
	return strings.TrimSpace(b.String()), nil
}

func (ag *Agent) toolScheduleInspect(tc *threadCtx, args map[string]any) (string, error) {
	s, err := ag.db.getSchedule(int64(toInt(args["id"])))
	if err != nil {
		return "", fmt.Errorf("no such schedule")
	}
	var b strings.Builder
	state := "enabled"
	if !s.Enabled {
		state = "disabled"
	}
	fmt.Fprintf(&b, "schedule #%d %q — %s\n", s.ID, orStr(s.Name, flat(s.Goal, 40)), state)
	fmt.Fprintf(&b, "cron: %s · toolset %s · owner %s · %s\n", s.Cron, orStr(s.Toolset, "private"), orStr(s.Owner, "(none)"), orStr(s.Visibility, visTeam))
	fmt.Fprintf(&b, "delivers: %s\n", ag.delivery(s, tc.root.ID))
	made := "by a person"
	switch {
	case s.CreatedByRun == tc.root.ID:
		made = "by this conversation"
	case s.CreatedByRun != 0:
		made = fmt.Sprintf("by conversation #%d", s.CreatedByRun)
	}
	fmt.Fprintf(&b, "created: %s %s\n", ago(s.Created*1000), made)
	fmt.Fprintf(&b, "goal: %s\n", clip(strings.TrimSpace(s.Goal), 1500))
	if s.Mode == modeConversation && !s.Watcher {
		fmt.Fprintf(&b, "each firing is a message in conversation #%d — thread_inspect it\n", s.TargetRun)
	}
	origin := "schedule"
	if s.Watcher {
		origin = "watcher"
	}
	oq, oa := tc.ownerSees()
	where := `r.parent_id=0 AND r.origin=? AND r.origin_id=? AND ` + oq
	qa := append([]any{origin, s.ID}, oa...)
	runs, next, err := ag.threadPage(where, qa, args["cursor"], pageLimit(args["limit"], 10, 30))
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		b.WriteString("runs: none yet")
		return b.String(), nil
	}
	b.WriteString("runs (newest first):\n")
	for _, r := range runs {
		fmt.Fprintf(&b, "#%d %q — %s · %s · %d model call(s)\n", r.ID, orStr(r.Title, "untitled"), statusWord(r.Status), ago(r.ActivityMs), r.LLMCalls)
		if txt := ag.db.lastAssistant(r.ID); txt != "" {
			fmt.Fprintf(&b, "    latest answer: %s\n", flat(txt, 240))
		}
	}
	if next != "" {
		fmt.Fprintf(&b, "more: cursor %q\n", next)
	}
	return strings.TrimSpace(b.String()), nil
}

// threadPage is one page of top-level runs matching where, newest activity
// first, with the cursor for the next.
func (ag *Agent) threadPage(where string, qa []any, cursor any, limit int) ([]*Run, string, error) {
	if cur := fmt.Sprint(orNil(cursor)); cur != "" {
		ms, id, ok := parseConvCursor(cur)
		if !ok {
			return nil, "", fmt.Errorf("bad cursor")
		}
		where += ` AND (r.activity_ms<? OR (r.activity_ms=? AND r.id<?))`
		qa = append(qa, ms, ms, id)
	}
	runs, err := ag.db.queryRuns(`r WHERE `+where+` ORDER BY r.activity_ms DESC, r.id DESC LIMIT `+strconv.Itoa(limit+1), qa...)
	if err != nil {
		return nil, "", err
	}
	next := ""
	if len(runs) > limit {
		runs = runs[:limit]
		last := runs[len(runs)-1]
		next = fmt.Sprintf("%d.%d", last.ActivityMs, last.ID)
	}
	return runs, next, nil
}

// originLabel is where a thread came from, as a list shows it.
func originLabel(r *Run) string {
	switch r.Origin {
	case "", "chat":
		return "chat"
	case "schedule", "watcher", "trigger":
		if r.OriginID != 0 {
			return fmt.Sprintf("%s #%d", r.Origin, r.OriginID)
		}
	}
	return r.Origin
}

func (ag *Agent) toolThreadsList(tc *threadCtx, sc string, args map[string]any) (string, error) {
	var where string
	var qa []any
	if sc == scopeAll {
		where, qa = tc.allThreads()
	} else {
		where, qa = tc.mineThreads()
	}
	switch o, _ := args["origin"].(string); o {
	case "":
	case "chat":
		where += ` AND r.origin IN ('', 'chat')`
	default:
		where += ` AND r.origin=?`
		qa = append(qa, o)
	}
	if st, _ := args["status"].(string); st != "" {
		if st == "waiting" {
			st = statusWaiting
		}
		where += ` AND r.status=?`
		qa = append(qa, st)
	}
	if q := strings.TrimSpace(fmt.Sprint(orNil(args["q"]))); q != "" {
		like := "%" + strings.ReplaceAll(q, "%", "") + "%"
		where += ` AND (r.title LIKE ? OR r.id IN (SELECT CASE WHEN x.root_id<>0 THEN x.root_id ELSE x.id END
			FROM messages_fts f JOIN runs x ON x.id=f.run_id WHERE messages_fts MATCH ?))`
		qa = append(qa, like, ftsQuery(q))
	}
	if a, ok := args["archived"].(bool); ok {
		cond := `EXISTS (SELECT 1 FROM run_user_state us WHERE us.run_id=r.id AND us.user=? AND us.archived_at<>0)`
		if !a {
			cond = `NOT ` + cond
		}
		where += ` AND ` + cond
		qa = append(qa, tc.owner)
	}
	runs, next, err := ag.threadPage(where, qa, args["cursor"], pageLimit(args["limit"], 20, 50))
	if err != nil {
		return "", err
	}
	if len(runs) == 0 {
		return "(no threads match)", nil
	}
	var ids []int64
	for _, r := range runs {
		ids = append(ids, r.ID)
	}
	states := ag.db.userStates(tc.owner, ids)
	var b strings.Builder
	for _, r := range runs {
		fmt.Fprintf(&b, "#%d [%s] %q — %s · %s", r.ID, originLabel(r), orStr(r.Title, "untitled"), statusWord(r.Status), ago(r.ActivityMs))
		if r.ID == tc.root.ID {
			b.WriteString(" · this conversation")
		}
		if r.Owner != tc.owner {
			fmt.Fprintf(&b, " · owner %s", orStr(r.Owner, "(none)"))
		} else if r.Visibility == visTeam {
			b.WriteString(" · shared with the team")
		}
		if states[r.ID].ArchivedAt != 0 {
			b.WriteString(" · archived")
		}
		b.WriteString("\n")
	}
	if next != "" {
		fmt.Fprintf(&b, "more: cursor %q\n", next)
	}
	return strings.TrimSpace(b.String()), nil
}

func (ag *Agent) toolThreadInspect(tc *threadCtx, args map[string]any) (string, error) {
	r, err := ag.db.getRun(int64(toInt(args["id"])))
	if err != nil {
		return "", fmt.Errorf("no such thread")
	}
	var b strings.Builder
	fmt.Fprintf(&b, "thread #%d %q — %s · %s\n", r.ID, orStr(r.Title, "untitled"), originLabel(r), statusWord(r.Status))
	if r.ParentID != 0 {
		fmt.Fprintf(&b, "a subagent of #%d (conversation #%d)\n", r.ParentID, rootOf(r))
	} else {
		vis := orStr(r.Visibility, visTeam)
		if vis == visTeam {
			vis = "shared with the team (" + orStr(r.TeamRole, roleViewer) + "s)"
		}
		fmt.Fprintf(&b, "owner %s · %s\n", orStr(r.Owner, "(none)"), vis)
	}
	fmt.Fprintf(&b, "created %s · last activity %s · %d model call(s)\n", ago(r.Created*1000), ago(r.ActivityMs), r.LLMCalls)
	if r.Status == statusWaiting && r.Result != "" {
		fmt.Fprintf(&b, "waiting on: %s\n", flat(r.Result, 300))
	}
	if r.Summary != "" {
		fmt.Fprintf(&b, "summary of earlier turns: %s\n", clip(strings.TrimSpace(r.Summary), 2000))
	}
	before := toInt(args["before"])
	if before <= 0 {
		before = -1
	}
	msgs, err := ag.db.pageMessages(r.ID, before, pageLimit(args["limit"], 30, 100))
	if err != nil {
		return "", err
	}
	if len(msgs) == 0 {
		b.WriteString("messages: none")
		return b.String(), nil
	}
	b.WriteString("messages:\n")
	for _, m := range msgs {
		switch m.Role {
		case "tool":
			fmt.Fprintf(&b, "[#%d tool %s] %s\n", m.Seq, m.Name, flat(m.Content, 300))
		case "assistant":
			line := flat(m.Content, 800)
			var calls []toolCall
			if m.ToolCalls != "" && decodeCalls(m.ToolCalls, &calls) {
				var hs []string
				for _, c := range calls {
					hs = append(hs, c.Function.Name+": "+flat(callHeadline(c), 80))
				}
				line = strings.TrimSpace(line + " → called " + strings.Join(hs, "; "))
			}
			fmt.Fprintf(&b, "[#%d assistant] %s\n", m.Seq, line)
		default:
			fmt.Fprintf(&b, "[#%d %s] %s\n", m.Seq, m.Role, flat(m.Content, 800))
		}
	}
	if first := msgs[0].Seq; ag.db.hasShownBefore(r.ID, first) {
		fmt.Fprintf(&b, "older: thread_inspect {id: %d, before: %d}\n", r.ID, first)
	}
	return strings.TrimSpace(b.String()), nil
}

// orNil turns a missing argument into "" for fmt.Sprint.
func orNil(v any) any {
	if v == nil {
		return ""
	}
	return v
}
