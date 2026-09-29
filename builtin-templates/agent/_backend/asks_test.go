package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// D133 — the task ledger, compaction in two stages, recall and notes.

func asksOf(t *testing.T, db *DB, runID int64) []*Ask {
	t.Helper()
	a, err := db.asks(runID)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func askLine(a *Ask) string { return a.Source + "|" + a.Who + "|" + a.Text }

// Every way a request reaches a run lands in the ledger, in the transaction
// that delivers it: POST /runs, a person's message, /learn, a schedule (a new
// run, and into a conversation), a trigger, a channel message, a subagent's
// task and its parent's messages. A watcher's "check now" is no request.
func TestEveryDeliveryFillsTheLedger(t *testing.T) {
	ag, mux := chanFixture(t)
	f := fakeOf(ag)
	db := ag.db

	w := callAs(t, mux, asAlice, "POST", "/runs", map[string]any{"goal": "plan the offsite"})
	var run Run
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &run) != nil {
		t.Fatalf("POST /runs: %d %s", w.Code, w.Body)
	}
	waitStatus(t, db, run.ID, statusIdle)
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/message", run.ID), map[string]any{"text": "and book rooms"}); w.Code != 200 {
		t.Fatalf("message: %d %s", w.Code, w.Body)
	}
	waitStatus(t, db, run.ID, statusIdle)
	if w := callAs(t, mux, asAlice, "POST", fmt.Sprintf("/runs/%d/learn", run.ID), nil); w.Code != 200 {
		t.Fatalf("learn: %d %s", w.Code, w.Body)
	}
	waitFor(t, "the learn prompt", func() bool { return len(asksOf(t, db, run.ID)) == 3 })
	got := asksOf(t, db, run.ID)
	if askLine(got[0]) != "human|alice|plan the offsite" || askLine(got[1]) != "human|alice|and book rooms" ||
		got[2].Source != "learn" || got[2].Seq <= got[1].Seq {
		t.Fatalf("a person's asks: %v %v %v", askLine(got[0]), askLine(got[1]), askLine(got[2]))
	}
	waitQuiet(t, ag)

	// Schedules: a run of its own, and a message into the conversation.
	iso := mkSchedule(t, ag, &Schedule{Name: "digest", Cron: "@daily", Goal: "digest the inbox", Owner: "alice", Visibility: visPrivate})
	ag.fireSchedule(iso)
	iso, _ = ag.db.getSchedule(iso.ID)
	if a := asksOf(t, db, iso.LastRunID); len(a) != 1 || askLine(a[0]) != "schedule|digest|digest the inbox" {
		t.Fatalf("a scheduled run: %+v", a)
	}
	into := mkSchedule(t, ag, &Schedule{Name: "nudge", Cron: "@daily", Goal: "remind me of the rooms", Owner: "alice",
		Visibility: visPrivate, Mode: modeConversation, TargetRun: run.ID})
	ag.fireSchedule(into)
	waitFor(t, "the schedule's message", func() bool { a := asksOf(t, db, run.ID); return len(a) == 4 })
	if a := asksOf(t, db, run.ID)[3]; askLine(a) != "schedule|nudge|remind me of the rooms" {
		t.Fatalf("a schedule into a conversation: %s", askLine(a))
	}

	// A trigger's run carries its prompt.
	f.on(nil, say("looked into it"))
	mkTrigger(t, mux, asAlice, map[string]any{"name": "deploys", "source": "push", "sourceRef": "apps/webhooks", "match": "deploy",
		"goal": "Check the {{topic}} deploy", "toolset": "web", "dataClass": "public"})
	_, res := pushEvent(t, mux, "apps/webhooks", map[string]any{"topic": "deploy/site", "eventId": "e1", "dataClass": "public"})
	if len(res) != 1 || res[0].RunID == 0 {
		t.Fatalf("push: %+v", res)
	}
	waitFor(t, "the trigger's request", func() bool { return len(asksOf(t, db, res[0].RunID)) == 1 })
	if a := asksOf(t, db, res[0].RunID)[0]; a.Source != "trigger" || a.Who != "deploys" || !strings.Contains(a.Text, "Check the deploy/site deploy") {
		t.Fatalf("a trigger: %s", askLine(a))
	}

	// A chat channel's message.
	chanRun, _, err := ag.deliverInbound(inbound{Mode: "session", Key: "chan:1:dm:U1", Stamp: runStamp{Owner: "alice", Origin: "channel"},
		Title: "DM", Cfg: defaultConfig(), Source: "channel", Sender: "U1", Text: "what's on today?"})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the channel message", func() bool { return len(asksOf(t, db, chanRun)) == 1 })
	if a := asksOf(t, db, chanRun)[0]; askLine(a) != "channel|U1|what's on today?" {
		t.Fatalf("a channel: %s", askLine(a))
	}

	// A watcher's rounds are not requests.
	wsch := mkSchedule(t, ag, &Schedule{Name: "w", Cron: "@every 1h", Goal: "watch the thing", Watcher: true})
	f.on(lastUser("Check now"), say("no change"))
	ag.fireSchedule(wsch)
	wsch, _ = ag.db.getSchedule(wsch.ID)
	waitFor(t, "the watcher round", func() bool { return len(f.callsFor(wsch.RunID)) > 0 })
	waitQuiet(t, ag)
	if a := asksOf(t, db, wsch.RunID); len(a) != 0 {
		t.Fatalf("a watcher's check is not a request: %+v", a)
	}
}

// A subagent's task, and every message its parent sends it, are its asks.
func TestSubagentAsksAreItsTaskAndItsParentsMessages(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(taskIs("draft it"), callTools(tc("n", "note", `{"text":"drafting"}`))).block("child").once()
	f.on(lastUser("[message from your parent"), say("REVISED")).once()
	f.on(lastUser("go"), callTools(tc("s", "subagent_spawn", `{"task":"draft it","wait":false}`))).once()
	var child atomic.Int64
	f.on(lastIs("tool", "background"), func(r LLMRequest) (LLMReply, error) {
		child.Store(children(db, r.Run)[0].ID)
		return callTools(tc("m", "subagent_message", fmt.Sprintf(`{"id":%d,"text":"make it shorter"}`, child.Load())))(r)
	}).once()
	f.on(lastIs("tool", "next step"), say("sent"))
	id := newRun(t, ag, Config{Subagents: true}, "go")
	waitFor(t, "the message to be sent", func() bool { return strings.Contains(transcript(db, id), "A:sent") })
	f.release("child")
	waitFor(t, "the child to read it", func() bool { return len(asksOf(t, db, child.Load())) == 2 })
	a := asksOf(t, db, child.Load())
	parent := fmt.Sprintf("#%d", id)
	if askLine(a[0]) != "parent|"+parent+"|draft it" || a[1].Source != "parent" || a[1].Who != parent ||
		!strings.Contains(a[1].Text, "make it shorter") {
		t.Fatalf("the child's asks: %s / %s", askLine(a[0]), askLine(a[1]))
	}
	waitQuiet(t, ag)
}

// promptTokensOf makes a fake reply report the request's size, the way a
// provider does: that is what triggers compaction.
func promptTokensOf(r LLMRequest) int {
	n := 0
	for _, m := range r.Msgs {
		n += estimateTokens(asText(m.Content))
		for _, c := range m.ToolCalls {
			n += estimateTokens(c.Function.Arguments)
		}
	}
	return n
}

func sized(reply func(LLMRequest) (LLMReply, error)) func(LLMRequest) (LLMReply, error) {
	return func(r LLMRequest) (LLMReply, error) {
		rep, err := reply(r)
		rep.Usage.PromptTokens = promptTokensOf(r)
		return rep, err
	}
}

func sysOf(r LLMRequest) string { return asText(r.Msgs[0].Content) }

func lastText(r LLMRequest) string { return asText(r.Msgs[len(r.Msgs)-1].Content) }

// The task survives compaction verbatim: three summaries later, every call
// still carries the original request word for word under # Your task, the
// summarizer is shown it but never asked to fold it, and the call after each
// compaction is told so once.
func TestTaskSurvivesThreeCompactions(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	task := "TASK-ORIGINAL: reconcile the Q3 ledger against the bank export, flag every mismatch over 10 EUR, " +
		strings.Repeat("and keep the vendor names exactly as written. ", 30) + "Report by Friday."
	note := strings.Repeat("NOTE-BODY reference material. ", 55) // ~1.6 KB, read back every step
	var summaries atomic.Int32
	f.on(func(r LLMRequest) bool { return r.Purpose == "compact" }, sized(func(r LLMRequest) (LLMReply, error) {
		n := summaries.Add(1)
		return LLMReply{Msg: wireMsg{Role: "assistant", Content: fmt.Sprintf("SUMMARY-%d: read the note, nothing reconciled yet", n)}}, nil
	}))
	f.on(func(r LLMRequest) bool { return r.Purpose == "turn" }, sized(func(r LLMRequest) (LLMReply, error) {
		if summaries.Load() >= 3 {
			return say("done")(r)
		}
		if len(r.Msgs) == 2 {
			return callTools(tc(fmt.Sprintf("set%d", len(r.Msgs)), "memory_set", mustJSON(map[string]string{"key": "ref", "value": note})))(r)
		}
		return callTools(tc(fmt.Sprintf("get%d", time.Now().UnixNano()), "memory_get", `{"key":"ref"}`))(r)
	}))
	id := newRun(t, ag, Config{TokenBudget: 6000, MaxTurnSteps: 300}, task)
	waitStatus(t, db, id, statusIdle)
	if n := summaries.Load(); n < 3 {
		t.Fatalf("only %d compaction(s) ran", n)
	}
	var hist int
	_ = db.q.QueryRow(`SELECT count(*) FROM summaries WHERE run_id=?`, id).Scan(&hist)
	run, _ := db.getRun(id)
	if hist < 3 || !strings.HasPrefix(run.Summary, fmt.Sprintf("SUMMARY-%d", summaries.Load())) {
		t.Fatalf("summary history: %d rows, latest %q", hist, run.Summary)
	}
	var turns, compacts []LLMRequest
	for _, c := range f.callsFor(id) {
		if c.Purpose == "compact" {
			compacts = append(compacts, c)
		} else {
			turns = append(turns, c)
		}
	}
	for i, c := range turns {
		if !strings.Contains(sysOf(c), taskHeading+"\n\n[#1 · human · ") || !strings.Contains(sysOf(c), task) {
			t.Fatalf("call %d lost the task verbatim:\n%s", i, clip(sysOf(c), 600))
		}
	}
	last := turns[len(turns)-1]
	if !strings.Contains(lastText(last), reminderOpen) || !strings.Contains(lastText(last), "TASK-ORIGINAL") {
		t.Fatalf("the last call's reminder: %q", clip(lastText(last), 400))
	}
	// The summarizer sees the task, pinned, and folds the transcript around it.
	for _, c := range compacts {
		body := asText(c.Msgs[1].Content)
		pinned, fold, _ := strings.Cut(body, "New transcript to fold in:")
		if !strings.Contains(pinned, task) || strings.Contains(fold, "TASK-ORIGINAL") || !strings.Contains(fold, "→ memory_") {
			t.Fatalf("summarizer input:\n%s", clip(fold, 3000))
		}
	}
	// The call right after a compaction carries the note, once (so the notes
	// count the compactions — masking ones too); after a summary, it carries
	// the new summary.
	noted, summarized, summary := 0, false, ""
	for i, c := range f.callsFor(id) {
		if c.Purpose == "compact" {
			summarized = true
			continue
		}
		has := strings.Contains(lastText(c), compactedNote)
		if has {
			noted++
		}
		if summarized {
			s := sysOf(c)
			if j := strings.Index(s, "SUMMARY-"); !has || j < 0 || s[j:j+9] == summary {
				t.Fatalf("call %d, right after a summary: note %v, summary %q (had %q)", i, has, s[max(j, 0):max(j, 0)+9], summary)
			} else {
				summary = s[j : j+9]
			}
		}
		summarized = false
	}
	var steps, masks int
	_ = db.q.QueryRow(`SELECT count(*), count(json_extract(detail, '$.masked')) FROM steps WHERE run_id=? AND kind='compaction'`, id).Scan(&steps, &masks)
	t.Logf("%d calls, %d compaction(s), %d of them masking", len(turns), steps, masks)
	if masks == 0 {
		t.Fatal("stage 1 never masked an output")
	}
	if noted != steps {
		t.Fatalf("the note was shown %d time(s) for %d compaction(s)", noted, steps)
	}
	if r, _ := db.getRun(id); r.CompactNote {
		t.Fatal("the note is still pending after it was shown")
	}
	assertTranscriptValid(t, db, id)
}

// Stage 1 hides old tool outputs behind stubs that say how to get them back
// — and they come back whole, from message_get and from recall; the job an
// output belongs to is named too. Nothing is summarised when that was enough.
func TestMaskedOutputsAreRestorable(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	run := askRun(t, ag, "look through the logs")
	outputs := map[string]string{}
	for k := 1; k <= 7; k++ {
		c := tc(fmt.Sprintf("c%d", k), "bash", fmt.Sprintf(`{"command":"cat log%d"}`, k))
		addAssistantCalls(t, db, run.ID, c)
		body := fmt.Sprintf("OUTPUT-%d zebrafish ", k) + strings.Repeat(fmt.Sprintf("line %d of the log\n", k), 400)
		outputs[c.ID] = body
		addToolResult(t, db, run.ID, c, body)
	}
	if _, err := db.q.Exec(`INSERT INTO sandbox_jobs (root_id, job, run_id, tool_call_id, ref, command, created_ms) VALUES (?, 3, ?, 'c1', 'x', 'cat log1', 1)`,
		run.ID, run.ID); err != nil {
		t.Fatal(err)
	}
	cfg := Config{System: "test agent", TokenBudget: 5000}
	db.setPromptTokens(run.ID, 5001)
	run, _ = db.getRun(run.ID)
	ag.eng.maybeCompact(context.Background(), &turnState{run: run, cfg: cfg, root: run.ID}, false)

	msgs, _ := db.messages(run.ID, false)
	var masked []*Message
	for _, m := range msgs {
		if m.Masked {
			masked = append(masked, m)
		}
		if m.Compacted {
			t.Fatalf("masking was enough — nothing should be compacted (#%d)", m.Seq)
		}
	}
	if len(masked) != 2 || masked[0].ToolCallID != "c1" || masked[1].ToolCallID != "c2" {
		t.Fatalf("masked %d: want the results of the two oldest steps", len(masked))
	}
	run, _ = db.getRun(run.ID)
	out, err := ag.assembleContext(context.Background(), run, cfg)
	if err != nil {
		t.Fatal(err)
	}
	assertWireValid(t, out)
	var stubs []string
	for _, m := range out {
		if m.Role == "tool" && strings.Contains(asText(m.Content), "hidden to save context") {
			stubs = append(stubs, asText(m.Content))
		}
	}
	want := fmt.Sprintf(`message_get {"seq": %d}`, masked[0].Seq)
	if len(stubs) != 2 || !strings.Contains(stubs[0], want) || !strings.Contains(stubs[0], `bash_output {"job": 3, "offset": 0}`) ||
		!strings.Contains(stubs[0], "OUTPUT-1") || len(stubs[0]) > 700 {
		t.Fatalf("stubs: %q", stubs)
	}
	if !strings.Contains(lastText(LLMRequest{Msgs: out}), compactedNote) {
		t.Fatal("the next call should be told the context was compacted")
	}
	// message_get pages the whole output back.
	var got strings.Builder
	for off := 0; ; {
		res, err := ag.runTool(context.Background(), run, cfg, "message_get", map[string]any{"seq": masked[0].Seq, "offset": off})
		if err != nil {
			t.Fatal(err)
		}
		head, body, _ := strings.Cut(res, "]\n")
		if !strings.Contains(head, "hidden as a stub") {
			t.Fatalf("message_get header: %q", head)
		}
		more := ""
		if i := strings.LastIndex(body, "\n…["); i >= 0 {
			body, more = body[:i], body[i:]
		}
		got.WriteString(body)
		if more == "" {
			break
		}
		off = got.Len()
	}
	if got.String() != outputs["c1"] {
		t.Fatalf("message_get restored %d of %d bytes", got.Len(), len(outputs["c1"]))
	}
	// recall still finds what is behind a stub.
	res, err := ag.runTool(context.Background(), run, Config{}, "recall", map[string]any{"query": "OUTPUT-2 zebrafish"})
	if err != nil || !strings.Contains(res, fmt.Sprintf("[#%d tool bash", masked[1].Seq)) {
		t.Fatalf("recall behind a stub: %q %v", res, err)
	}
}

func askRun(t *testing.T, ag *Agent, ask string) *Run {
	t.Helper()
	r, err := ag.startRunOpts(runOpts{Title: "t", Cfg: Config{System: "test agent"}, Hold: true})
	if err != nil {
		t.Fatal(err)
	}
	m := &Message{RunID: r.ID, Role: "user", Content: ask}
	if _, err := ag.db.addMessage(m); err != nil {
		t.Fatal(err)
	}
	if err := ag.db.recordAsk(m, "human", "alice"); err != nil {
		t.Fatal(err)
	}
	r, _ = ag.db.getRun(r.ID)
	return r
}

// recall ranks by bm25 by default — a short old message that is about the
// words beats newer ones that mention one in passing — finds the original
// request with order:oldest, and never returns its own earlier results.
func TestRecallRanksAndOrders(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	run := askRun(t, ag, "Build the quarterly revenue forecast for the board.")
	for k := 0; k < 12; k++ {
		_, _ = db.addMessage(&Message{RunID: run.ID, Role: "assistant",
			Content: fmt.Sprintf("Step %d: loaded more rows; revenue column checked. ", k) + strings.Repeat("unrelated filler words here. ", 40)})
	}
	c := tc("r1", "recall", `{"query":"forecast"}`)
	addAssistantCalls(t, db, run.ID, c)
	addToolResult(t, db, run.ID, c, "[#1 user] Build the quarterly revenue forecast for the board. forecast forecast")
	cfg := Config{}
	rel, err := ag.runTool(context.Background(), run, cfg, "recall", map[string]any{"query": "revenue forecast", "match": "any"})
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(rel, "\n")
	if !strings.HasPrefix(lines[1], "[#1 user] Build the quarterly revenue forecast") {
		t.Fatalf("bm25 should put the request first:\n%s", rel)
	}
	if strings.Contains(rel, "tool recall") {
		t.Fatalf("recall returned its own earlier result:\n%s", rel)
	}
	newest, _ := ag.runTool(context.Background(), run, cfg, "recall", map[string]any{"query": "revenue forecast", "match": "any", "order": "newest"})
	if strings.Contains(newest, "[#1 user]") {
		t.Fatalf("newest-first (the old behaviour) buries the request under 8 newer hits:\n%s", newest)
	}
	oldest, _ := ag.runTool(context.Background(), run, cfg, "recall", map[string]any{"query": "revenue", "order": "oldest", "limit": 1})
	if !strings.Contains(oldest, "[#1 user] Build the quarterly revenue forecast") {
		t.Fatalf("order:oldest: %s", oldest)
	}
	if all, _ := ag.runTool(context.Background(), run, cfg, "recall", map[string]any{"query": "revenue zeppelin"}); !strings.Contains(all, `match:"any"`) {
		t.Fatalf("every word, no hit: %q", all)
	}
	// Summaries are searched too.
	_ = db.addSummary(run.ID, "Decided to use the zeppelin exchange rates.", 5, 4)
	if s, _ := ag.runTool(context.Background(), run, cfg, "recall", map[string]any{"query": "zeppelin"}); !strings.Contains(s, "[summary ") {
		t.Fatalf("summary history: %q", s)
	}
}

// The notes render sorted — the same bytes every call, so the prompt prefix
// (and the provider's cache) holds — under a heading that puts the task
// first; memory_delete removes one; a note has a size cap.
func TestNotesAreStableAndOutrankedByTheTask(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	run := askRun(t, ag, "the task")
	for _, k := range []string{"zeta", "alpha", "mid", "beta", "omega", "kappa"} {
		if _, err := ag.runTool(context.Background(), run, Config{}, "memory_set", map[string]any{"key": k, "value": "v-" + k}); err != nil {
			t.Fatal(err)
		}
	}
	first, _ := ag.assembleContext(context.Background(), run, Config{System: "s"})
	for i := 0; i < 5; i++ {
		again, _ := ag.assembleContext(context.Background(), run, Config{System: "s"})
		if sysOf(LLMRequest{Msgs: again}) != sysOf(LLMRequest{Msgs: first}) {
			t.Fatal("the system prompt changed between two calls with nothing new")
		}
	}
	sys := sysOf(LLMRequest{Msgs: first})
	ti, ni := strings.Index(sys, taskHeading), strings.Index(sys, "# Your notes (you wrote these; the task above outranks them)")
	if ti < 0 || ni < ti || !strings.Contains(sys, "- alpha: v-alpha\n- beta: v-beta\n- kappa: v-kappa\n- mid: v-mid\n- omega: v-omega\n- zeta: v-zeta\n") {
		t.Fatalf("system prompt:\n%s", sys)
	}
	if _, err := ag.runTool(context.Background(), run, Config{}, "memory_delete", map[string]any{"key": "mid"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ag.runTool(context.Background(), run, Config{}, "memory_delete", map[string]any{"key": "mid"}); err == nil {
		t.Fatal("deleting a missing note should say so")
	}
	if mem, _ := db.memory(run.ID); len(mem) != 5 {
		t.Fatalf("notes after delete: %v", mem)
	}
	if _, err := ag.runTool(context.Background(), run, Config{}, "memory_set", map[string]any{"key": "big", "value": strings.Repeat("x", noteMax+1)}); err == nil {
		t.Fatal("an oversized note should be refused")
	}
}

// The reminder rides the last message and nothing else: the first call (the
// request is the last message) has none, a later one names the task and the
// latest request, and everything before the last message is byte-identical
// to what the previous call sent — the cacheable prefix.
func TestReminderKeepsThePrefixStable(t *testing.T) {
	db := newTestDB(t)
	ag := newTestAgent(t, db)
	f := fakeOf(ag)
	f.on(lastUser("sort the invoices"), callTools(tc("n1", "note", `{"text":"one"}`))).once()
	f.on(lastIs("tool", "noted"), callTools(tc("n2", "note", `{"text":"two"}`))).once()
	id := newRun(t, ag, Config{}, "sort the invoices")
	waitStatus(t, db, id, statusIdle)
	calls := f.callsFor(id)
	if len(calls) < 3 {
		t.Fatalf("%d calls", len(calls))
	}
	if strings.Contains(lastText(calls[0]), reminderOpen) {
		t.Fatalf("the first call's last message is the request itself: %q", lastText(calls[0]))
	}
	r := lastText(calls[1])
	if !strings.Contains(r, reminderOpen) || !strings.Contains(r, `Latest request (#1, human): "sort the invoices"`) || strings.Contains(r, compactedNote) {
		t.Fatalf("the reminder: %q", r)
	}
	for i := 1; i < len(calls); i++ {
		prev, cur := calls[i-1].Msgs, calls[i].Msgs
		for j := 0; j < len(prev)-1; j++ {
			a, _ := json.Marshal(prev[j])
			b, _ := json.Marshal(cur[j])
			if string(a) != string(b) {
				t.Fatalf("call %d changed message %d of the prefix:\n%s\n%s", i, j, a, b)
			}
		}
		if got := stripReminder(asText(cur[len(prev)-1].Content)); got != stripReminder(asText(prev[len(prev)-1].Content)) {
			t.Fatalf("call %d: the previous last message changed beyond its reminder", i)
		}
	}
}

// The budget: explicit wins, 12000 (the old stored default) reads as unset,
// else 60% of the window with a floor; a provider's model list says the
// window in any of the usual fields.
func TestBudgetFromTheContextWindow(t *testing.T) {
	for w, want := range map[int]int{128000: 76800, 200000: 120000, 40000: 32000, 32768: 26214, 8192: 6553} {
		if got := budgetForWindow(w); got != want {
			t.Errorf("window %d: budget %d, want %d", w, got, want)
		}
	}
	e := &Engine{ag: &Agent{noGateway: true}}
	for cfg, want := range map[int]int{0: budgetFloor, legacyTokenBudget: budgetFloor, 9000: 9000} {
		if got, _ := e.tokenBudget(context.Background(), Config{TokenBudget: cfg}); got != want {
			t.Errorf("tokenBudget %d: %d, want %d", cfg, got, want)
		}
	}
	var list struct{ Data []catalogModel }
	_ = json.Unmarshal([]byte(`{"data":[
		{"id":"a","context_length":131072},
		{"id":"b","max_input_tokens":200000,"context_window":"x"},
		{"id":"c","top_provider":{"context_length":65536}},
		{"id":"d","max_model_len":32768.0,"provider":{"weird":true}},
		{"id":"e"}]}`), &list)
	got := map[string]int{}
	for _, m := range list.Data {
		got[m.ID] = m.ContextWindow
	}
	if fmt.Sprint(got) != "map[a:131072 b:200000 c:65536 d:32768 e:0]" {
		t.Fatalf("context windows: %v", got)
	}
	if c := parseConfig(`{"tokenBudget":12000}`); c.TokenBudget != 12000 {
		t.Fatalf("a stored budget is kept as written: %d", c.TokenBudget)
	}
	if c := parseConfig(""); c.TokenBudget != 0 {
		t.Fatalf("the default budget is unset: %d", c.TokenBudget)
	}
}

// An instance's database from before the ledger: the migration adds the
// tables and columns, and each run's first request is pinned — a subagent's
// as its parent's, never a watcher's "check now".
func TestLedgerMigratesAnOldDatabase(t *testing.T) {
	path := filepath.Join(t.TempDir(), "agent.db")
	db, err := openDB(path)
	if err != nil {
		t.Fatal(err)
	}
	x := func(q string, args ...any) int64 {
		res, err := db.sql.Exec(q, args...)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	top := x(`INSERT INTO runs (title, status, config, created, updated) VALUES ('old', 'idle', '{"tokenBudget":12000}', 1, 1)`)
	x(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, 0, 'system', 'sys', 1)`, top)
	x(`INSERT INTO messages (run_id, seq, role, content, meta, created) VALUES (?, 1, 'user', 'the original ask', '{"sender":"alice"}', 2)`, top)
	x(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, 2, 'assistant', 'on it', 3)`, top)
	x(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, 3, 'user', 'a follow-up', 4)`, top)
	kid := x(`INSERT INTO runs (title, status, config, parent_id, created, updated) VALUES ('kid', 'idle', '{}', ?, 1, 1)`, top)
	x(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, 0, 'user', 'the kid task', 1)`, kid)
	watch := x(`INSERT INTO runs (title, status, config, created, updated) VALUES ('w', 'idle', '{}', 1, 1)`)
	x(`INSERT INTO messages (run_id, seq, role, content, meta, created) VALUES (?, 0, 'user', 'Check now.', '{"origin":"watch"}', 1)`, watch)
	legacyMeta := x(`INSERT INTO runs (title, status, config, created, updated) VALUES ('m', 'idle', '{}', 1, 1)`)
	x(`INSERT INTO messages (run_id, seq, role, content, meta, created) VALUES (?, 0, 'user', 'not json meta', 'garbage', 1)`, legacyMeta)
	// back to the pre-D133 shape
	for _, q := range []string{`DROP TABLE asks`, `DROP TABLE summaries`, `DROP TABLE summaries_fts`,
		`ALTER TABLE messages DROP COLUMN masked`, `ALTER TABLE runs DROP COLUMN compact_note`,
		`DELETE FROM settings WHERE k='asks_backfill'`} {
		x(q)
	}
	_ = db.sql.Close()

	db, err = openDB(path)
	if err != nil {
		t.Fatalf("migrating: %v", err)
	}
	defer db.sql.Close()
	lines := func(id int64) []string {
		var out []string
		for _, a := range asksOf(t, db, id) {
			out = append(out, fmt.Sprintf("%d:%s", a.Seq, askLine(a)))
		}
		return out
	}
	if got := fmt.Sprint(lines(top)); got != "[1:human|alice|the original ask]" {
		t.Fatalf("top-level: %s", got)
	}
	if got := fmt.Sprint(lines(kid)); got != fmt.Sprintf("[0:parent|#%d|the kid task]", top) {
		t.Fatalf("subagent: %s", got)
	}
	if got := lines(watch); len(got) != 0 {
		t.Fatalf("watcher: %v", got)
	}
	if got := fmt.Sprint(lines(legacyMeta)); got != "[0:human||not json meta]" {
		t.Fatalf("unparseable meta: %s", got)
	}
	var col int
	if err := db.q.QueryRow(`SELECT count(*) FROM pragma_table_info('messages') WHERE name='masked'`).Scan(&col); err != nil || col != 1 {
		t.Fatalf("messages.masked: %d %v", col, err)
	}
	// A run the old binary makes after the migration is pinned when read.
	late := x(`INSERT INTO runs (title, status, config, created, updated) VALUES ('late', 'idle', '{}', 1, 1)`)
	x(`INSERT INTO messages (run_id, seq, role, content, created) VALUES (?, 0, 'user', 'made by the old binary', 1)`, late)
	if got := fmt.Sprint(lines(late)); got != "[0:human||made by the old binary]" {
		t.Fatalf("a late run: %s", got)
	}
}

// The reminder rides both wires where the provider takes it: in a tool
// result's text (Chat's tool message, the Responses function_call_output),
// and in a person's message — as text, or as one more part of a parts array.
func TestReminderOnBothWires(t *testing.T) {
	rem := reminderText("t", &Ask{Seq: 1, Source: "human", Text: "do the thing"}, true, true)
	call := tc("c1", "note", `{}`)
	toolLast := withReminder([]wireMsg{{Role: "system", Content: "s"}, {Role: "user", Content: "do the thing"},
		{Role: "assistant", ToolCalls: []toolCall{call}}, {Role: "tool", ToolCallID: "c1", Name: "note", Content: "noted"}}, rem)
	parts := partsOf(nil, "look", []json.RawMessage{json.RawMessage(`{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}`)})
	userLast := withReminder([]wireMsg{{Role: "system", Content: "s"}, {Role: "user", Content: parts}}, rem)
	if err := validateWire(toolLast); err != nil {
		t.Fatal(err)
	}
	chatLast := func(msgs []wireMsg) (role string, content any) {
		b, err := chatCodec{}.encode(LLMRequest{Model: "m", Msgs: msgs})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(b, &body); err != nil {
			t.Fatal(err)
		}
		m := body.Messages[len(body.Messages)-1]
		return m.Role, m.Content
	}
	if role, content := chatLast(toolLast); role != "tool" || !strings.HasPrefix(str(content), "noted\n\n"+reminderOpen) {
		t.Fatalf("chat, tool last: %s %v", role, content)
	}
	role, content := chatLast(userLast)
	ps, _ := content.([]any)
	if role != "user" || len(ps) != 3 || str(ps[1].(map[string]any)["type"]) != "image_url" ||
		!strings.HasPrefix(str(ps[2].(map[string]any)["text"]), reminderOpen) {
		t.Fatalf("chat, parts last: %s %v", role, content)
	}
	b, err := responsesCodec{}.encode(LLMRequest{Model: "gpt-5", Msgs: toolLast})
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Input []map[string]any `json:"input"`
	}
	_ = json.Unmarshal(b, &body)
	last := body.Input[len(body.Input)-1]
	if last["type"] != "function_call_output" || !strings.HasPrefix(str(last["output"]), "noted\n\n"+reminderOpen) {
		t.Fatalf("responses, tool last: %v", last)
	}
	if n := strings.Count(string(b), "task-reminder"); n != 2 { // open and close, once
		t.Fatalf("the reminder appears %d times: %s", n/2, b)
	}
	// Nothing else to append to: a user message of its own.
	after := withReminder([]wireMsg{{Role: "system", Content: "s"}, {Role: "assistant", Content: "hi"}}, rem)
	if len(after) != 3 || after[2].Role != "user" || !strings.HasPrefix(asText(after[2].Content), reminderOpen) {
		t.Fatalf("after an assistant message: %+v", after)
	}
}

// The first sentences of the notes and history tools (D133), pinned like
// the sandbox tools' (tooldesc_test.go): purpose, scope and limit first.
func TestTaskToolsFirstSentences(t *testing.T) {
	want := map[string]string{
		"memory_set":    "Save a note for this conversation (at most 8000 characters) — always shown to you under # Your notes and kept through compaction; not for the task, which is pinned verbatim under # Your task.",
		"memory_get":    "Read one of your notes by key (they are all shown under # Your notes already).",
		"memory_delete": "Delete one of your notes by key — for one that is done or no longer true.",
		"message_get":   "Read one message of THIS conversation in full by its #number — compacted turns and tool outputs shown as stubs too — 12000 characters per call (offset reads on).",
		"recall":        "Search THIS conversation's whole history by words — turns compacted out of your context, tool outputs hidden as stubs, earlier summaries — for at most 20 short excerpts (8 by default), most relevant first.",
	}
	got := map[string]string{}
	for _, s := range toolSpecs(defaultConfig(), 0, nil) {
		got[s.Function.Name] = s.Function.Description
	}
	for name, w := range want {
		if f := firstSentence(got[name]); f != w {
			t.Errorf("%s's first sentence changed:\n got: %s\nwant: %s", name, f, w)
		}
	}
}
