// agent backend — a durable, debuggable agentic loop persisted in in-component
// sqlite, driven against the llm-gw component, with tools, subagents, MCP,
// compaction, and self-scheduling via a cron heartbeat. This is a TEMPLATE:
// instantiate it and build it up. See API.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

type Agent struct {
	db *DB

	mu            sync.Mutex
	driving       map[int64]bool          // runs being driven right now (coalesce)
	stop          map[int64]bool          // interrupt requests
	beatOn        bool                    // wake heartbeat currently registered
	beatBusy      bool                    // a reconcile is in flight (single-flight)
	watcherRounds map[int64]*watcherRound // active watcher rounds (for rollback)
	drafts        map[int64]string        // live streaming assistant text per run

	// repl holds the live goja VMs, one per run. Pure cache: everything that
	// must survive a swap is in sqlite, and a dropped session is rebuilt from
	// its replay log on next use (repl.go).
	repl *replRegistry

	// gen labels this process for run leases; kickCh asks the dispatcher for a
	// pass. Both are RAM-only by design — losing them costs latency, never
	// correctness, because /tick runs the same query from cold (workflow.go).
	gen    string
	kickCh chan struct{}

	// The drive ceiling. In RAM and not a count over sqlite: it bounds THIS
	// process's goroutines, so resetting to zero on restart is the correct
	// reconciliation — a durable count would wedge permanently on phantom
	// 'running' rows left by a crash.
	slotMu sync.Mutex
	active int
	limit  int
	// toolSem bounds tool goroutines across ALL drives, not just within one
	// batch — the per-batch cap compounds once several runs execute at once.
	toolSem chan struct{}

	// cancels holds a live drive's cancel func, so cancelling a parent aborts
	// its descendants' in-flight LLM calls at once rather than at their next
	// iteration. RAM-only: the durable half is runs.cancel_req.
	cancels   map[int64]cancelReg
	cancelSeq uint64
}

// cancelReg is a live drive's cancel func plus a token identifying which
// registration it is, so a later one cannot be removed by an earlier owner.
type cancelReg struct {
	fn    context.CancelFunc
	token uint64
}

// registerCancel records a live drive's cancel func and returns its token.
func (ag *Agent) registerCancel(id int64, fn context.CancelFunc) uint64 {
	ag.mu.Lock()
	ag.cancelSeq++
	tok := ag.cancelSeq
	ag.cancels[id] = cancelReg{fn: fn, token: tok}
	ag.mu.Unlock()
	return tok
}

// unregisterCancel removes a registration only if it is still OURS, identified
// by the token registerCancel handed out. An unconditional delete let a losing
// duplicate dispatch wipe the live drive's entry, leaving abort() a no-op for
// the rest of that drive. (Comparing the funcs themselves does not work —
// reflect gives the code pointer, which is identical for every closure from
// the same call site.)
func (ag *Agent) unregisterCancel(id int64, token uint64) {
	ag.mu.Lock()
	if cur, ok := ag.cancels[id]; ok && cur.token == token {
		delete(ag.cancels, id)
	}
	ag.mu.Unlock()
}

// abort cancels a run's in-flight work if it is running in THIS process.
func (ag *Agent) abort(id int64) {
	ag.mu.Lock()
	reg, ok := ag.cancels[id]
	ag.mu.Unlock()
	if ok && reg.fn != nil {
		reg.fn()
	}
}

func (ag *Agent) setDraft(id int64, text string) {
	ag.mu.Lock()
	ag.drafts[id] = text
	ag.mu.Unlock()
}

func (ag *Agent) clearDraft(id int64) {
	ag.mu.Lock()
	delete(ag.drafts, id)
	ag.mu.Unlock()
}

func (ag *Agent) getDraft(id int64) string {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return ag.drafts[id]
}

func (ag *Agent) claim(id int64) bool {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	if ag.driving[id] {
		return false
	}
	ag.driving[id] = true
	return true
}

func (ag *Agent) release(id int64) {
	ag.mu.Lock()
	delete(ag.driving, id)
	ag.mu.Unlock()
}

// clearStop consumes the interrupt flag. Separate from release() because
// release is now also called at admission, before any drive exists — clearing
// the flag there discarded interrupts that arrived in that window.
func (ag *Agent) clearStop(id int64) {
	ag.mu.Lock()
	delete(ag.stop, id)
	ag.mu.Unlock()
}

func (ag *Agent) requestStop(id int64) {
	ag.mu.Lock()
	ag.stop[id] = true
	ag.mu.Unlock()
}

func (ag *Agent) stopped(id int64) bool {
	ag.mu.Lock()
	defer ag.mu.Unlock()
	return ag.stop[id]
}

var agent *Agent

func main() {
	dbPath := xbin.Resource("db")
	if dbPath == "" {
		log.Fatal("no db resource (grant res:<self>/db writer) — see scope.json")
	}
	db, err := openDB(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	agent = &Agent{db: db, driving: map[int64]bool{}, stop: map[int64]bool{}, watcherRounds: map[int64]*watcherRound{}, drafts: map[int64]string{}, repl: newReplRegistry(),
		gen: generationID(), kickCh: make(chan struct{}, 1), toolSem: make(chan struct{}, maxToolsGlobal), cancels: map[int64]cancelReg{}}

	// Seed the default config once.
	if db.getSetting("config") == "" {
		b, _ := json.Marshal(defaultConfig())
		_ = db.putSetting("config", string(b))
	}
	// The drive ceiling bounds the process, so it is not snapshotted per run —
	// which meant it silently reverted to the default on every restart, since
	// setLimit was only reachable from a config write.
	agent.setLimit(parseConfig(db.getSetting("config")).maxActiveRuns())

	// Resume anything left sleeping/mid-drive by a restart, and turn the wake
	// heartbeat on only if something needs it (it's not always-on — a workspace
	// session or the agent itself schedules work; see API.md).
	go agent.startupResume()
	go agent.replJanitor()
	go agent.kickLoop()
	go agent.beatKeeper()

	mux := http.NewServeMux()
	// Everything is admin-only: the tile is self (always admin of itself) and
	// the owner. The heartbeat arrives as self via cron. No public surface.
	mux.Handle("GET /runs", xbin.RoleFunc("admin", handleListRuns))
	mux.Handle("POST /runs", xbin.RoleFunc("admin", handleNewRun))
	mux.Handle("POST /ask", xbin.RoleFunc("admin", handleAsk))
	mux.Handle("GET /runs/{id}", xbin.RoleFunc("admin", handleGetRun))
	mux.Handle("DELETE /runs/{id}", xbin.RoleFunc("admin", handleDeleteRun))
	mux.Handle("POST /runs/{id}/message", xbin.RoleFunc("admin", handleMessage))
	mux.Handle("POST /runs/{id}/answer", xbin.RoleFunc("admin", handleMessage))
	mux.Handle("POST /runs/{id}/approve", xbin.RoleFunc("admin", handleApprove))
	mux.Handle("POST /runs/{id}/interrupt", xbin.RoleFunc("admin", handleInterrupt))
	mux.Handle("POST /runs/{id}/cancel", xbin.RoleFunc("admin", handleCancel))
	mux.Handle("GET /runs/{id}/tree", xbin.RoleFunc("admin", handleRunTree))
	mux.Handle("GET /halt", xbin.RoleFunc("admin", handleHaltGet))
	mux.Handle("PUT /halt", xbin.RoleFunc("admin", handleHaltPut))
	mux.Handle("POST /runs/{id}/resume", xbin.RoleFunc("admin", handleResume))
	mux.Handle("POST /runs/{id}/compact", xbin.RoleFunc("admin", handleCompact))
	mux.Handle("PUT /runs/{id}/memory", xbin.RoleFunc("admin", handleMemoryPut))
	mux.Handle("DELETE /runs/{id}/memory", xbin.RoleFunc("admin", handleMemoryDelete))
	// Session files. Metadata and content are separate routes so the tile's
	// 1.5s poll never drags file bodies with it.
	mux.Handle("GET /runs/{id}/files", xbin.RoleFunc("admin", handleFilesList))
	mux.Handle("GET /runs/{id}/file", xbin.RoleFunc("admin", handleFileGet))
	mux.Handle("PUT /runs/{id}/file", xbin.RoleFunc("admin", handleFilePut))
	mux.Handle("DELETE /runs/{id}/file", xbin.RoleFunc("admin", handleFileDelete))
	mux.Handle("GET /config", xbin.RoleFunc("admin", handleGetConfig))
	mux.Handle("PUT /config", xbin.RoleFunc("admin", handlePutConfig))
	mux.Handle("GET /features", xbin.RoleFunc("admin", handleFeatures))
	mux.Handle("GET /models", xbin.RoleFunc("admin", handleModels))
	mux.Handle("GET /schedules", xbin.RoleFunc("admin", handleListSchedules))
	mux.Handle("POST /schedules", xbin.RoleFunc("admin", handleNewSchedule))
	mux.Handle("PUT /schedules/{id}", xbin.RoleFunc("admin", handleUpdateSchedule))
	mux.Handle("DELETE /schedules/{id}", xbin.RoleFunc("admin", handleDeleteSchedule))
	mux.Handle("POST /schedules/{id}/fire", xbin.RoleFunc("admin", handleFireSchedule))
	mux.Handle("POST /schedules/{id}/trigger", xbin.RoleFunc("admin", handleFireSchedule))
	mux.Handle("GET /skills", xbin.RoleFunc("admin", handleListSkills))
	mux.Handle("PUT /skills", xbin.RoleFunc("admin", handleSaveSkill))
	mux.Handle("DELETE /skills/{name}", xbin.RoleFunc("admin", handleDeleteSkill))
	mux.Handle("POST /runs/{id}/learn", xbin.RoleFunc("admin", handleLearn))
	mux.Handle("POST /tick", xbin.RoleFunc("admin", handleTick))

	xbin.Serve(mux)
}

// startupResume re-drives runs a restart left sleeping/stalled and syncs the
// wake heartbeat to whatever is pending now.
func (ag *Agent) startupResume() {
	ag.reRegisterSchedules()
	ag.recoverStrandedRuns()
	// One dispatcher, one predicate (workflow.go). A crash leaves runs in
	// exactly the states readyRuns already selects for, so recovery is not a
	// special path — it is the ordinary pass, run once at boot.
	ag.dispatchOnce()
	ag.reconcileBeat()
}

// handleModels proxies llm-gw's aggregated model list so the tile can populate
// the model-tier dropdowns (the agent holds the apps/llm-gw grant).
func handleModels(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://xbin/api/apps/"+gwPath()+"/v1/models", nil)
	resp, err := xbin.Client().Do(req)
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, err.Error())
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// handleFeatures reports the toggleable capabilities and their current state
// (for the tile's Features menu). Toggle by PUT /config with a features map.
func handleFeatures(w http.ResponseWriter, r *http.Request) {
	cfg := parseConfig(agent.db.getSetting("config"))
	state := map[string]bool{}
	for _, k := range featureKeys {
		state[k] = cfg.feature(k)
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"keys": featureKeys, "features": state})
}

// beatCallTimeout bounds a heartbeat registration. The SDK client has no
// Timeout of its own, so without this a gateway that accepts the connection and
// then goes quiet hangs the caller indefinitely. A var so tests can shorten it.
var beatCallTimeout = 10 * time.Second

// reconcileBeat keeps the wake heartbeat registered ONLY while runs need waking
// (sleeping, or mid-drive/stalled) — so cron isn't always-on. Cheap: it only
// hits the gateway when the desired state actually flips. Single-flighted: it
// makes a retrying gateway call and is reached from several concurrent paths
// (every completing drive, schedule changes, the keeper), so overlapping
// attempts would pile up round-trips.
func (ag *Agent) reconcileBeat() {
	ag.mu.Lock()
	if ag.beatBusy {
		ag.mu.Unlock()
		return
	}
	ag.beatBusy = true
	ag.mu.Unlock()
	defer func() {
		ag.mu.Lock()
		ag.beatBusy = false
		ag.mu.Unlock()
	}()

	want := ag.db.hasPending()
	ag.mu.Lock()
	have := ag.beatOn
	ag.mu.Unlock()
	if want == have {
		return
	}
	ok := false
	if want {
		ok = ag.ensureBeat()
	} else {
		ok = ag.stopBeat()
	}
	if ok {
		ag.mu.Lock()
		ag.beatOn = want
		ag.mu.Unlock()
	}
}

// ensureBeat registers the @every 1m heartbeat that fires /tick to re-drive due
// runs. Idempotent; a few retries since the gateway may lag a restart.
func (ag *Agent) ensureBeat() bool {
	job := map[string]any{
		"name": "heartbeat", "resource": "res:" + xbin.Self() + "/beat",
		"schedule": "@every 1m", "path": "/tick", "role": "admin",
	}
	body, _ := json.Marshal(job)
	for i := 0; i < 3; i++ {
		if i > 0 {
			time.Sleep(time.Duration(i) * time.Second)
		}
		if ag.putBeat(body) {
			return true
		}
	}
	log.Printf("agent: could not register wake heartbeat (self-scheduling degraded)")
	return false
}

// putBeat is one registration attempt. xbin.Client() has no Timeout, so
// without the context this can hang forever against a gateway that is up but
// not answering — and reconcileBeat would hold its single-flight slot with it.
func (ag *Agent) putBeat(body []byte) bool {
	ctx, cancel := context.WithTimeout(context.Background(), beatCallTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://xbin/api/xbin/cron/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// stopBeat removes the wake heartbeat when nothing is pending.
func (ag *Agent) stopBeat() bool {
	ctx, cancel := context.WithTimeout(context.Background(), beatCallTimeout)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodDelete, "http://xbin/api/xbin/cron/jobs/heartbeat", nil)
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound
}

// beatKeeper re-attempts the wake-heartbeat registration.
//
// reconcileBeat only acts when want != beatOn, and it sets beatOn only when the
// gateway call SUCCEEDED — so a registration that loses its race against a
// gateway still coming up after a restart is never retried. The next attempt
// would come from a completing drive, and no drive can complete when the only
// pending run is the one waiting for the heartbeat: a sleeping run then never
// wakes. A slow loop holding no state closes that.
func (ag *Agent) beatKeeper() {
	for {
		time.Sleep(time.Minute)
		ag.reconcileBeat()
	}
}

// --- handlers -----------------------------------------------------------

func handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := agent.db.listRuns()
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*Run{}
	}
	last := agent.db.lastAssistantByRun()
	for _, r := range runs {
		if r.Kind == "quick" {
			r.Last = clip(last[r.ID], 400)
		}
	}
	xbin.WriteJSON(w, 200, runs)
}

func handleNewRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title, Goal, System, Toolset string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Goal == "" {
		xbin.WriteError(w, 400, "need {goal}")
		return
	}
	cfg := parseConfig(agent.db.getSetting("config"))
	cfg.Toolset = normalizeToolset(body.Toolset)
	if body.System != "" {
		cfg.System = body.System
	}
	cfgJSON, _ := json.Marshal(cfg)
	title := body.Title
	if title == "" {
		title = clip(body.Goal, 60)
	}
	id, err := agent.db.createRun(title, string(cfgJSON), 0)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System})
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "user", Content: body.Goal})
	agent.db.journal(id, "note", map[string]string{"text": "run created"})
	agent.driveAsync(id)
	run, _ := agent.db.getRun(id)
	xbin.WriteJSON(w, 200, run)
}

func handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	msgs, _ := agent.db.messages(id, false)
	steps, _ := agent.db.steps(id)
	mem, _ := agent.db.memory(id)
	cfg, _ := agent.db.runConfig(id)
	if msgs == nil {
		msgs = []*Message{}
	}
	if steps == nil {
		steps = []*Step{}
	}
	// Session-file METADATA only (paths, sizes, versions) — never content: this
	// payload rides the tile's 1.5s poll.
	files, _ := agent.db.replFiles(id)
	if files == nil {
		files = []*ReplFile{}
	}
	// slots lets the tile say WHY a run is queued: every drive slot busy, or
	// merely admitted and about to start.
	active, limit := agent.activeDrives()
	xbin.WriteJSON(w, 200, map[string]any{"run": run, "messages": msgs, "steps": steps, "memory": mem, "config": cfg, "files": files,
		"draft": agent.getDraft(id), "slots": map[string]int{"active": active, "limit": limit}})
}

func handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	// Stop the subtree before removing it: deleting the rows out from under a
	// live drive would leave it spending on a tree that no longer exists.
	agent.requestCancel(pathID(r), true, "run deleted")
	if err := agent.db.deleteRun(pathID(r)); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// --- session files ------------------------------------------------------

func handleFilesList(w http.ResponseWriter, r *http.Request) {
	files, err := agent.db.replFiles(pathID(r))
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if files == nil {
		files = []*ReplFile{}
	}
	xbin.WriteJSON(w, 200, files)
}

func handleFileGet(w http.ResponseWriter, r *http.Request) {
	path, err := normReplPath(r.URL.Query().Get("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	f, err := agent.db.replFile(pathID(r), path)
	if err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, f)
}

// handleFilePut is the human's editor path, so it takes an optional version:
// the tile sends back the version it loaded and gets a 409 rather than
// silently overwriting a change the agent made in between.
func handleFilePut(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct {
		Path    string `json:"path"`
		Content string `json:"content"`
		Version int    `json:"version"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Path == "" {
		xbin.WriteError(w, 400, "need {path}")
		return
	}
	f, err := agent.db.replPutFile(id, body.Path, body.Content, body.Version)
	if err != nil {
		code := 400
		if strings.Contains(err.Error(), "version conflict") {
			code = 409
		}
		xbin.WriteError(w, code, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"path": f.Path, "version": f.Version, "bytes": f.Bytes})
}

func handleFileDelete(w http.ResponseWriter, r *http.Request) {
	path, err := normReplPath(r.URL.Query().Get("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	if err := agent.db.replDeleteFile(pathID(r), path); err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Text string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Text == "" {
		xbin.WriteError(w, 400, "need {text}")
		return
	}
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	// Replying instead of approving DENIES the parked calls. setStatus clears
	// `pending`, so without this the parked tool_calls would be orphaned — an
	// unanswered block shipped to the provider on the very next call.
	agent.denyPending(id)
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "user", Content: body.Text})
	_ = agent.db.setStatus(id, statusIdle, 0, "", "")
	agent.resumeIfHalted(id)
	agent.driveAsync(id)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// denyPending answers any parked approval's tool calls with a refusal, so the
// transcript stays valid when the human responds with words instead of a
// verdict. No-op when nothing is parked.
func (ag *Agent) denyPending(runID int64) {
	run, err := ag.db.getRun(runID)
	if err != nil || run.Pending == "" {
		return
	}
	var pend pending
	if json.Unmarshal([]byte(run.Pending), &pend) != nil || len(pend.ToolCalls) == 0 {
		return
	}
	msg := "(not executed: you replied instead of approving — ask again if you still need it)"
	note := "pending tool call(s) denied: the user replied instead of approving"
	if pend.Kind == "await" {
		// Parked on children, not on approval. The placeholders are already in
		// the transcript, so this must REWRITE them rather than append — and the
		// children keep running, so a real result may still land here later.
		msg = "(interrupted: you sent a message while this was waiting on subagents — they are still running; " +
			"their results will arrive when they finish)"
		note = "await interrupted by a user message"
	}
	for _, tc := range pend.ToolCalls {
		ag.settleToolResult(runID, tc, msg)
	}
	ag.db.journal(runID, "note", map[string]string{"text": note})
}

func handleApprove(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Approve bool }
	_ = json.NewDecoder(r.Body).Decode(&body)
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	var pend pending
	if run.Pending == "" || json.Unmarshal([]byte(run.Pending), &pend) != nil || pend.Kind != "approval" {
		xbin.WriteError(w, 400, "no pending approval")
		return
	}
	if body.Approve {
		// Keep pending; the drive resume-path executes it.
		_ = agent.db.setStatus(id, statusRunning, 0, "", run.Pending)
	} else {
		agent.denyApproval(id, pend)
		_ = agent.db.setStatus(id, statusRunning, 0, "", "")
	}
	agent.driveAsync(id)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// denyApproval answers every parked call with a refusal, REWRITING the
// awaiting-approval placeholder the gate wrote rather than adding a second row.
func (ag *Agent) denyApproval(id int64, pend pending) {
	for _, tc := range pend.ToolCalls {
		ag.settleToolResult(id, tc, "(denied by user)")
	}
	ag.db.journal(id, "note", map[string]string{"text": "tool call(s) denied"})
}

// handleInterrupt parks a run the human is watching. It keeps the old
// behaviour for the run itself — idle, so "type to continue" still works — but
// now also stops its background descendants, which would otherwise carry on
// spending on behalf of a run that is no longer going anywhere.
func handleInterrupt(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	agent.requestStop(id)
	agent.abort(id)
	_ = agent.db.setStatus(id, statusIdle, 0, "interrupted", "")
	agent.db.journal(id, "note", map[string]string{"text": "interrupted by user"})
	stopped := agent.cancelDescendantsCounted(id, "parent interrupted by the user")
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "cancelledDescendants": stopped})
}

// handleRunTree is the whole workflow view in one request: polling each node
// separately would be O(nodes) requests every tick. It carries metadata only —
// statuses, blockers and the denormalized cost — never transcripts, the same
// split the session-file routes established.
func handleRunTree(w http.ResponseWriter, r *http.Request) {
	run, err := agent.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := run.RootID
	if root == 0 {
		root = run.ID
	}
	runs, err := agent.db.treeRuns(root)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	blockers := agent.db.blockersByRun(root)
	last := agent.db.lastStepByRun(root)
	nodes := make([]map[string]any, 0, len(runs))
	var version int64
	tot := map[string]int{}
	var promptTok, compTok, calls int
	for _, n := range runs {
		if n.Updated > version {
			version = n.Updated
		}
		word := nodeWord(n)
		tot[word]++
		promptTok += n.PromptTokens
		compTok += n.CompletionTokens
		calls += n.LLMCalls
		nodes = append(nodes, map[string]any{
			"id": n.ID, "parentId": n.ParentID, "depth": n.Depth, "title": n.Title,
			"status": word, "rawStatus": n.Status, "outcome": n.Outcome,
			"detached": n.Detached, "blockedOn": blockers[n.ID],
			"blockReason": blockReason(n, blockers[n.ID]),
			"created":     n.Created, "updated": n.Updated, "settledAt": n.SettledAt,
			"result":   clip(n.Result, 160),
			"lastStep": last[n.ID],
			"llmCalls": n.LLMCalls, "promptTokens": n.PromptTokens, "completionTokens": n.CompletionTokens,
		})
	}
	active, limit := agent.activeDrives()
	xbin.WriteJSON(w, 200, map[string]any{
		"root": root, "version": version, "halted": agent.halted(),
		"nodes": nodes,
		"totals": map[string]any{
			"nodes": len(nodes), "byStatus": tot,
			"promptTokens": promptTok, "completionTokens": compTok, "llmCalls": calls,
			"active": active, "limit": limit,
		},
	})
}

// blockReason says WHY a node is not moving — the question a human actually has
// when a tree looks stalled. "waiting on a sibling" and "waiting for a slot"
// need different reactions, so they stay distinct.
func blockReason(n *Run, blockedOn []int64) string {
	if n.SettledAt != 0 || terminalStatus(n.Status) {
		return ""
	}
	switch {
	case n.CancelReq != 0:
		return "cancelling"
	case len(blockedOn) > 0:
		return "dep"
	case n.Status == statusQueued:
		return "slot"
	case n.Status == statusWaiting:
		return "human"
	case n.Status == statusSleep:
		return "sleeping"
	}
	return ""
}

func handleHaltGet(w http.ResponseWriter, r *http.Request) {
	xbin.WriteJSON(w, 200, map[string]any{"on": agent.halted()})
}

// handleHaltPut is the brake. Durable rather than a RAM flag, because the swap
// that a runaway can trigger is exactly when you need it to still be set — and
// it blocks NEW spawns too, since cancelling fourteen runs is pointless if the
// fifteenth starts six more.
func handleHaltPut(w http.ResponseWriter, r *http.Request) {
	var body struct{ On bool }
	_ = json.NewDecoder(r.Body).Decode(&body)
	v := ""
	if body.On {
		v = "1"
	}
	if err := agent.db.putSetting("halt", v); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	stopped := 0
	if body.On {
		for _, id := range agent.db.liveRunIDs() {
			stopped += len(agent.requestCancel(id, false, "halted by the owner"))
		}
	} else {
		agent.kick()
	}
	xbin.WriteJSON(w, 200, map[string]any{"on": body.On, "cancelled": stopped})
}

// handleCancel is the durable stop. scope "subtree" (the default) takes the
// whole tree below the run; "node" stops just that one.
func handleCancel(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Scope, Reason string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	stopped := agent.requestCancel(id, body.Scope != "node", body.Reason)
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "cancelled": stopped})
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	agent.resumeIfHalted(id)
	agent.driveAsync(id)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func handleCompact(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	cfg, _ := agent.db.runConfig(id)
	cfg.TokenBudget = 0 // force
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	agent.maybeCompact(ctx, run, cfg)
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Key, Value string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Key == "" {
		xbin.WriteError(w, 400, "need {key}")
		return
	}
	if err := agent.db.memorySet(id, body.Key, body.Value); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	key := r.URL.Query().Get("key")
	if err := agent.db.memoryDelete(id, key); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	xbin.WriteJSON(w, 200, parseConfig(agent.db.getSetting("config")))
}

func handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var cfg Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		xbin.WriteError(w, 400, "bad config")
		return
	}
	b, _ := json.Marshal(cfg)
	if err := agent.db.putSetting("config", string(b)); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	// The drive ceiling bounds the process, not a run, so it is read live here
	// rather than snapshotted into each run's config.
	agent.setLimit(cfg.maxActiveRuns())
	xbin.WriteJSON(w, 200, cfg)
}

// handleTick is the cron heartbeat's entry point — and the backstop for every
// in-process shortcut: if a kick was lost to a swap or an idle reap, this pass
// runs the identical predicate and reaches the identical conclusion.
func handleTick(w http.ResponseWriter, r *http.Request) {
	// Every minute, also un-stick anything no dispatcher can reach. Cheap
	// (two indexed queries) and it is the only thing that recovers a run
	// stranded by a bug rather than by a state machine.
	agent.recoverStrandedRuns()
	ids, err := agent.db.readyRuns(dispatchBatch)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	for _, id := range ids {
		agent.driveAsync(id)
	}
	xbin.WriteJSON(w, 200, map[string]any{"driving": len(ids)})
}

// --- helpers ------------------------------------------------------------

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// publishEvent emits a run lifecycle event on the bus (best-effort).
//
// Fire-and-forget on purpose: xbin.Publish is a gateway round-trip, and this is
// called from inside the drive loop. A fan-out publishing one event per child
// would otherwise serialize a round-trip per child in the middle of a turn.
func publishEvent(runID int64, kind string) error {
	go func() {
		_ = xbin.Publish("res:"+xbin.Self()+"/events", kind, map[string]any{"runId": runID})
	}()
	return nil
}
