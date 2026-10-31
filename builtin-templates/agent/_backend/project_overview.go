// project_overview.go — GET /projects/{pid}/overview?range=day|week (D214,
// API.md §Projects and tasks, "The overview"): one read for a project
// page's status strip and its board cards — the caller's coordinator, the
// tasks' worker agents and their subagents, tokens by class of run
// (usage_hourly.go), the repos at the scm provider and the sandbox
// (project_overview_ext.go).
//
// Classes of runs: the *coordinator* is the caller's own coordinator run;
// *workers* are each task's conversation and the coding agents it drives
// (its harness children); *subagents* are every other descendant of a
// task's conversation or of the coordinator. Counts are over the project's
// tasks, which every viewer of the project sees (as GET
// /projects/{pid}/tasks shows them). A coordinator is a person's own: a
// component, someone viewing as another person, and a team project's
// definition have none (state "none", no coordinator class). A field the
// agent can't tell is absent, never guessed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// Overview is GET /projects/{pid}/overview's answer.
type Overview struct {
	At          int64             `json:"at"`
	Range       string            `json:"range"`
	Coordinator OvCoordinator     `json:"coordinator"`
	Workers     OvWorkers         `json:"workers"`
	Subagents   OvSubagents       `json:"subagents"`
	Tokens      OvTokens          `json:"tokens"`
	SCM         OvSCM             `json:"scm"`
	Sandbox     *OvSandbox        `json:"sandbox"`
	Tasks       map[string]OvTask `json:"tasks"`
}

type OvCoordinator struct {
	Run         *OvRun     `json:"run"`
	State       string     `json:"state"` // working | waiting | idle | failed | none
	Goal        *CoordGoal `json:"goal"`
	WakesOn     []OvWake   `json:"wakesOn"`
	LastEvent   *OvEvent   `json:"lastEvent"`
	TurnsToday  int        `json:"turnsToday"`
	TasksMade7d int        `json:"tasksMade7d"`
	Model       string     `json:"model,omitempty"`
	TokensToday int64      `json:"tokensToday"`
}

type OvRun struct {
	ID            int64  `json:"id"`
	Status        string `json:"status"`
	TurnStartedMs int64  `json:"turnStartedMs,omitempty"`
}

// OvWake is one thing that would wake the coordinator now.
type OvWake struct {
	Kind  string `json:"kind"` // ci | review | issues | you
	N     int64  `json:"n,omitempty"`
	PR    int    `json:"pr,omitempty"`
	Label string `json:"label"`
}

type OvEvent struct {
	Kind  string `json:"kind"`
	N     int64  `json:"n,omitempty"`
	Label string `json:"label"`
	AtMs  int64  `json:"atMs"`
}

type OvWorkers struct {
	Total    int          `json:"total"`
	Working  int          `json:"working"`
	NeedsYou int          `json:"needsYou"`
	Idle     int          `json:"idle"`
	Queued   int          `json:"queued"`
	Done     int          `json:"done"`
	Slots    ProjectSlots `json:"slots"`
	ByEngine []OvEngine   `json:"byEngine"`
}

type OvEngine struct {
	Engine string `json:"engine"`
	Title  string `json:"title"`
	Count  int    `json:"count"`
}

type OvSubagents struct {
	Running     int       `json:"running"`
	Waiting     int       `json:"waiting"`
	Finished24h int       `json:"finished24h"`
	Failed24h   int       `json:"failed24h"`
	By          []OvSubBy `json:"by"`
}

// OvSubBy is how many subagents run for task n, or for the coordinator.
type OvSubBy struct {
	N           int64 `json:"n,omitempty"`
	Coordinator bool  `json:"coordinator,omitempty"`
	Count       int   `json:"count"`
}

type OvTokens struct {
	Buckets []OvBucket `json:"buckets"` // oldest first
	Total   OvClasses  `json:"total"`
	Cost    *OvCost    `json:"cost"`
}

type OvClasses struct {
	Workers     int64 `json:"workers"`
	Coordinator int64 `json:"coordinator"`
	Subagents   int64 `json:"subagents"`
}

type OvBucket struct {
	T int64 `json:"t"` // the bucket's start, unix ms
	OvClasses
}

type OvCost struct {
	Amount   float64 `json:"amount"`
	Currency string  `json:"currency"`
}

// OvTask is a board card's agent line.
type OvTask struct {
	Engine        string `json:"engine"`
	Title         string `json:"title"`
	State         string `json:"state"` // working | waiting | failed | idle | queued | done
	TurnStartedMs int64  `json:"turnStartedMs,omitempty"`
	Subagents     int    `json:"subagents"`
	Tokens        int64  `json:"tokens,omitempty"`
}

func handleProjectOverview(w http.ResponseWriter, r *http.Request) {
	p, _ := projectOf(r)
	rng := r.URL.Query().Get("range")
	switch rng {
	case "":
		rng = "day"
	case "day", "week":
	default:
		xbin.WriteError(w, 400, "range: day or week")
		return
	}
	noteStatusRead(p.ID) // its page is open: its repos are fetched meanwhile, as for the status
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	xbin.WriteJSON(w, 200, projectOverview(ctx, projAg().db, p, callerOf(r), rng, time.Now()))
}

// projectOverview builds the answer for c; the scm and sandbox cells are
// read meanwhile.
func projectOverview(ctx context.Context, d *DB, p *Project, c who, rng string, now time.Time) Overview {
	ov := Overview{At: now.UnixMilli(), Range: rng}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); ov.SCM = overviewSCM(ctx, d, p) }()
	go func() { defer wg.Done(); ov.Sandbox = overviewSandbox(ctx, d, p) }()
	b := loadBoard(d, p, c, now)
	ov.Tokens = b.tokens(rng)
	ov.Workers, ov.Subagents, ov.Tasks = b.workers()
	ov.Coordinator = b.coordinator()
	wg.Wait()
	ov.SCM.Pulls = b.pulls
	return ov
}

// --- the board's runs ------------------------------------------------------------------------

// ovRunRow is one run under a task or the coordinator.
type ovRunRow struct {
	ID, Root       int64
	Status, Engine string
	TurnStarted    int64 // unix s
	Updated        int64 // unix s
	Tokens         int64 // prompt + completion, its life long
}

// Classes of runs.
const (
	clsWorkers     = "workers"
	clsCoordinator = "coordinator"
	clsSubagents   = "subagents"
)

type ovBoard struct {
	d      *DB
	p      *Project
	c      who
	now    time.Time
	tasks  []*ProjectTask
	byRoot map[int64]*ProjectTask // a task's run → the task
	coord  *Run
	runs   map[int64]*ovRunRow
	drives map[int64]int64 // a task's run → the latest coding agent under it (engineOf)
	pulls  OvPulls
	// derived per task, by n
	col, state, waiting map[int64]string
	coordToday          int64 // the coordinator's tokens over the last 24 h
}

func loadBoard(d *DB, p *Project, c who, now time.Time) *ovBoard {
	b := &ovBoard{d: d, p: p, c: c, now: now, byRoot: map[int64]*ProjectTask{}, runs: map[int64]*ovRunRow{},
		col: map[int64]string{}, state: map[int64]string{}, waiting: map[int64]string{}}
	b.tasks, _ = d.tasksWhere(`WHERE project_id=? AND phase<>'deleted' ORDER BY n`, p.ID)
	var roots []int64
	for _, k := range b.tasks {
		if k.RunID != 0 {
			b.byRoot[k.RunID] = k
			roots = append(roots, k.RunID)
		}
	}
	if c.kind == whoUser && c.viewedBy == "" && p.Kind != projTeam {
		if id := d.coordRunOf(p.ID, c.user); id != 0 {
			if run, err := d.getRun(id); err == nil {
				b.coord = run
				roots = append(roots, id)
			}
		}
	}
	for i := 0; i < len(roots); i += 400 {
		chunk := roots[i:min(i+400, len(roots))]
		marks := strings.TrimSuffix(strings.Repeat("?,", len(chunk)), ",")
		args := make([]any, 0, 2*len(chunk))
		for _, id := range chunk {
			args = append(args, id)
		}
		args = append(args, args...)
		rows, err := d.q.Query(`SELECT id, root_id, status, engine, turn_started, updated, prompt_tokens + completion_tokens
			FROM runs WHERE root_id IN (`+marks+`) OR id IN (`+marks+`)`, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			r := &ovRunRow{}
			if rows.Scan(&r.ID, &r.Root, &r.Status, &r.Engine, &r.TurnStarted, &r.Updated, &r.Tokens) == nil {
				if r.Root == 0 {
					r.Root = r.ID
				}
				b.runs[r.ID] = r
			}
		}
		rows.Close()
	}
	// each task's place on the board, as GET /projects/{pid}/tasks derives it
	queued := d.queuedInputs(p.ID, 0)
	for _, k := range b.tasks {
		status := ""
		if r := b.runs[k.RunID]; r != nil {
			status = r.Status
		}
		var ci *CISummary
		if k.Phase == phasePR && k.RunID != 0 {
			ci = taskCISummary(k.RunID)
		}
		b.col[k.N], b.state[k.N], b.waiting[k.N] = taskDerived(k, status, ci, queueWaits(queued, k.N))
		for _, pr := range k.taskPRs() {
			if pr.State == "open" {
				b.pulls.Open++
			}
		}
		switch b.state[k.N] {
		case taskCIFailed:
			b.pulls.Failing++
		case taskAwaitingReview:
			b.pulls.Review++
		}
	}
	return b
}

// classOf is a run's class (root: its tree's root; "" — not one of the
// caller's view: another person's coordinator, say).
func (b *ovBoard) classOf(run, root int64) string {
	r := b.runs[run]
	switch {
	case b.byRoot[root] != nil:
		if run == root || (r != nil && r.Engine == engineHarness) {
			return clsWorkers
		}
		return clsSubagents
	case b.coord != nil && root == b.coord.ID:
		if run == root || (r != nil && r.Engine == engineHarness) {
			return clsCoordinator
		}
		return clsSubagents
	}
	return ""
}

func (o *OvClasses) add(cls string, n int64) {
	switch cls {
	case clsWorkers:
		o.Workers += n
	case clsCoordinator:
		o.Coordinator += n
	case clsSubagents:
		o.Subagents += n
	}
}

// --- tokens ----------------------------------------------------------------------------------

// tokens charts the range: day — the 24 hours up to the current one's end;
// week — the 7 days of 24 hours up to it. Each bucket starts at t.
func (b *ovBoard) tokens(rng string) OvTokens {
	end := hourOf(b.now) + 3600
	n, step := 24, int64(3600)
	if rng == "week" {
		n, step = 7, 86400
	}
	start := end - int64(n)*step
	out := OvTokens{Buckets: make([]OvBucket, n)}
	for i := range out.Buckets {
		out.Buckets[i].T = (start + int64(i)*step) * 1000
	}
	var cost int64
	for _, u := range b.d.projectUsage(b.p.ID, start) {
		cls := b.classOf(u.Run, u.Root)
		i := int((u.Hour - start) / step)
		if cls == "" || i < 0 || i >= n {
			continue
		}
		out.Buckets[i].add(cls, u.Tokens)
		out.Total.add(cls, u.Tokens)
		cost += u.CostMicros
		if cls == clsCoordinator && u.Hour >= end-86400 {
			b.coordToday += u.Tokens
		}
	}
	if cost > 0 {
		out.Cost = &OvCost{Amount: float64(cost) / 1e6, Currency: "USD"}
	}
	return out
}

// --- workers, subagents, cards ---------------------------------------------------------------

// ovState is a worker's state from its task's board column and state.
func ovState(col, state string) string {
	switch col {
	case colWorking:
		return "working"
	case colNeedsYou:
		if state == taskFailed {
			return "failed"
		}
		return "waiting"
	case colPR:
		return "idle" // its turn is over: CI or a review is next
	case colQueued:
		return "queued"
	}
	return "done"
}

// engineOf is who works on task k: its coding agent (the task's own, else
// the latest it drives) or the agent itself.
func (b *ovBoard) engineOf(k *ProjectTask) string {
	harness := func(id int64) string {
		if cfg, err := b.d.runConfig(id); err == nil && cfg.Harness != nil && cfg.Harness.Provider != "" {
			return cfg.Harness.Provider
		}
		return ""
	}
	if r := b.runs[k.RunID]; r != nil && r.Engine == engineHarness {
		if h := harness(r.ID); h != "" {
			return h
		}
	}
	if b.drives == nil { // each task's latest coding agent, once
		b.drives = map[int64]int64{}
		for id, r := range b.runs {
			if id != r.Root && r.Engine == engineHarness && id > b.drives[r.Root] {
				b.drives[r.Root] = id
			}
		}
	}
	if last := b.drives[k.RunID]; last != 0 {
		if h := harness(last); h != "" {
			return h
		}
	}
	return "agent"
}

// engineTitle is an engine's name for people.
func engineTitle(engine string) string {
	if engine == "agent" {
		return "agent"
	}
	return orStr(harnessProvider(engine, nil).Name, engine)
}

func (b *ovBoard) workers() (OvWorkers, OvSubagents, map[string]OvTask) {
	w := OvWorkers{ByEngine: []OvEngine{}}
	w.Slots = ProjectSlots{Used: b.d.slotsUsed(b.p.ID), Max: policyOf(b.p.Policy).MaxTasks}
	s := OvSubagents{By: []OvSubBy{}}
	cards := map[string]OvTask{}
	// subagents: by the task (or the coordinator) they work for
	running := map[int64]int{} // task n → running subagents; -1 the coordinator
	tokens := map[int64]int64{}
	since := b.now.Add(-24 * time.Hour).Unix()
	for id, r := range b.runs {
		k := b.byRoot[r.Root]
		if k != nil {
			tokens[k.N] += r.Tokens
		}
		if b.classOf(id, r.Root) != clsSubagents {
			continue
		}
		key := int64(-1)
		if k != nil {
			key = k.N
		}
		switch {
		case r.Status == statusWaiting:
			s.Waiting++
		case active(r.Status):
			s.Running++
			running[key]++
		case r.Updated >= since && (r.Status == statusError || r.Status == statusCanceled):
			s.Failed24h++
		case r.Updated >= since:
			s.Finished24h++
		}
	}
	for key, n := range running {
		if key < 0 {
			s.By = append(s.By, OvSubBy{Coordinator: true, Count: n})
		} else {
			s.By = append(s.By, OvSubBy{N: key, Count: n})
		}
	}
	sort.Slice(s.By, func(i, j int) bool {
		a, c := s.By[i], s.By[j]
		if a.Count != c.Count {
			return a.Count > c.Count
		}
		if a.Coordinator != c.Coordinator {
			return c.Coordinator
		}
		return a.N < c.N
	})
	engines := map[string]int{}
	for _, k := range b.tasks {
		st := ovState(b.col[k.N], b.state[k.N])
		w.Total++
		switch st {
		case "working":
			w.Working++
		case "waiting", "failed":
			w.NeedsYou++
		case "idle":
			w.Idle++
		case "queued":
			w.Queued++
		default:
			w.Done++
		}
		eng := b.engineOf(k)
		engines[eng]++
		card := OvTask{Engine: eng, Title: engineTitle(eng), State: st, Subagents: running[k.N], Tokens: tokens[k.N]}
		if r := b.runs[k.RunID]; r != nil && st == "working" && r.TurnStarted > 0 {
			card.TurnStartedMs = r.TurnStarted * 1000
		}
		cards[strconv.FormatInt(k.N, 10)] = card
	}
	for eng, n := range engines {
		w.ByEngine = append(w.ByEngine, OvEngine{Engine: eng, Title: engineTitle(eng), Count: n})
	}
	sort.Slice(w.ByEngine, func(i, j int) bool {
		if w.ByEngine[i].Count != w.ByEngine[j].Count {
			return w.ByEngine[i].Count > w.ByEngine[j].Count
		}
		return w.ByEngine[i].Engine < w.ByEngine[j].Engine
	})
	return w, s, cards
}

// --- the coordinator -------------------------------------------------------------------------

// coordinator is the caller's coordinator's cell (state none while they
// have none; empty for whoever can have none).
func (b *ovBoard) coordinator() OvCoordinator {
	out := OvCoordinator{State: "none", WakesOn: []OvWake{}}
	mayHave := b.c.kind == whoUser && b.c.viewedBy == "" && b.p.Kind != projTeam
	if !mayHave {
		return out
	}
	out.WakesOn = b.wakesOn()
	out.LastEvent = b.lastEvent()
	run := b.coord
	if run == nil {
		return out
	}
	out.Run = &OvRun{ID: run.ID, Status: run.Status}
	switch {
	case run.Status == statusWaiting:
		out.State = "waiting"
	case active(run.Status):
		out.State = "working"
		if run.TurnStarted > 0 {
			out.Run.TurnStartedMs = run.TurnStarted * 1000
		}
	case run.Status == statusError:
		out.State = "failed"
	default:
		out.State = "idle"
	}
	out.Goal = b.d.coordGoal(run)
	day := b.now.Add(-24 * time.Hour)
	_ = b.d.q.QueryRow(`SELECT count(*) FROM messages WHERE run_id=? AND role='assistant' AND tool_calls='' AND created>=?`,
		run.ID, day.Unix()).Scan(&out.TurnsToday)
	_ = b.d.q.QueryRow(`SELECT count(*) FROM project_tasks WHERE project_id=? AND from_run=? AND created_ms>=?`,
		b.p.ID, run.ID, b.now.Add(-7*24*time.Hour).UnixMilli()).Scan(&out.TasksMade7d)
	out.Model = b.coordModel(run)
	out.TokensToday = b.coordToday
	return out
}

// coordModel is the model of its latest answer, else the one its config
// names (the provider's prefix off: "apps/llm|big" → "big").
func (b *ovBoard) coordModel(run *Run) string {
	var raw string
	m := ""
	if b.d.q.QueryRow(`SELECT meta FROM messages WHERE run_id=? AND role='assistant' AND meta<>'' ORDER BY seq DESC LIMIT 1`, run.ID).Scan(&raw) == nil {
		var meta msgMeta
		if json.Unmarshal([]byte(raw), &meta) == nil {
			m = meta.Model
		}
	}
	if m == "" {
		if cfg, err := b.d.runConfig(run.ID); err == nil {
			m = firstNonEmpty(cfg.Pick, classOf(cfg).Model, cfg.Models.General, cfg.Model)
		}
	}
	if i := strings.LastIndex(m, "|"); i >= 0 {
		m = m[i+1:]
	}
	return clip(strings.TrimSpace(m), 80)
}

// ovWakeMax bounds the tasks wakesOn names.
const ovWakeMax = 10

// wakesOn is what would wake the caller's coordinator now: their tasks'
// CI and reviews (a task's events reach its creator's coordinator), new
// issues (the project's own events reach its owner's), and always them.
func (b *ovBoard) wakesOn() []OvWake {
	out := []OvWake{}
	for _, want := range []string{"ci", "review"} {
		for _, k := range b.tasks {
			if len(out) >= ovWakeMax || k.CreatedBy != b.c.user || b.waiting[k.N] != want {
				continue
			}
			wk := OvWake{Kind: want, N: k.N, Label: fmt.Sprintf("CI on #%d", k.N)}
			if want == "review" {
				wk.Label = fmt.Sprintf("review on #%d", k.N)
				for _, pr := range k.taskPRs() {
					if pr.State == "open" {
						wk.PR, wk.Label = pr.Number, fmt.Sprintf("review on #%d", pr.Number)
						break
					}
				}
			}
			out = append(out, wk)
		}
	}
	if b.p.Owner == b.c.user && policyOf(b.p.Policy).AutoLabel != "" {
		out = append(out, OvWake{Kind: "issues", Label: "new issues"})
	}
	return append(out, OvWake{Kind: "you", Label: "your message"})
}

// lastEvent is the latest project event for the caller's coordinator.
func (b *ovBoard) lastEvent() *OvEvent {
	var ev ProjectEvent
	var body string
	if b.d.q.QueryRow(`SELECT n, kind, body, created FROM project_events WHERE project_id=? AND coord_user=? ORDER BY id DESC LIMIT 1`,
		b.p.ID, b.c.user).Scan(&ev.N, &ev.Kind, &body, &ev.Created) != nil {
		return nil
	}
	ev.Body = json.RawMessage(body)
	return &OvEvent{Kind: ev.Kind, N: ev.N, Label: eventLabel(ev), AtMs: ev.Created}
}

// eventLabel says an event in a few words (the feed has its text).
func eventLabel(ev ProjectEvent) string {
	on := ""
	if ev.N > 0 {
		on = fmt.Sprintf(" on #%d", ev.N)
	}
	var body struct {
		Text   string `json:"text"`
		Why    string `json:"why"`
		Status string `json:"status"`
	}
	_ = json.Unmarshal(ev.Body, &body)
	words := map[string]string{pevCIFailed: "CI failed", pevCIStuck: "CI stuck", pevPRReady: "CI green", pevPROpened: "PR opened",
		pevReview: "review", pevComment: "comment", pevMerged: "merged", pevClosed: "closed", pevPush: "push",
		pevTaskCreated: "task created", pevTaskHuman: "message", pevTaskCancel: "cancelled", pevWorkspace: "workspace"}
	switch {
	case ev.Kind == pevIssue:
		return "new issue"
	case ev.Kind == pevTaskState:
		w := "answered"
		switch {
		case body.Status == statusWaiting:
			w = "waits for a person"
		case body.Why == turnError:
			w = "failed"
		}
		if ev.N > 0 {
			return fmt.Sprintf("#%d %s", ev.N, w)
		}
		return w
	case words[ev.Kind] != "":
		return words[ev.Kind] + on
	}
	return clip(firstLine(body.Text), 60)
}
