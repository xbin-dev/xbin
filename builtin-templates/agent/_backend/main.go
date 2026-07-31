// agent backend — a durable, debuggable agentic loop persisted in in-component
// sqlite, driven against the llm-gw component, with tools, subagents, MCP,
// compaction, and self-scheduling via a cron heartbeat. This is a TEMPLATE:
// instantiate it and build it up. See plans/agent.md and API.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strconv"
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
	watcherRounds map[int64]*watcherRound // active watcher rounds (for rollback)
	drafts        map[int64]string        // live streaming assistant text per run
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
	agent = &Agent{db: db, driving: map[int64]bool{}, stop: map[int64]bool{}, watcherRounds: map[int64]*watcherRound{}, drafts: map[int64]string{}}

	// Seed the default config once.
	if db.getSetting("config") == "" {
		b, _ := json.Marshal(defaultConfig())
		_ = db.putSetting("config", string(b))
	}

	// Resume anything left sleeping/mid-drive by a restart, and turn the wake
	// heartbeat on only if something needs it (it's not always-on — a workspace
	// session or the agent itself schedules work; see plans/agent-v2.md).
	go agent.startupResume()

	mux := http.NewServeMux()
	// Everything is admin-only: the tile is self (always admin of itself) and
	// the owner. The heartbeat arrives as self via cron. No public surface.
	mux.Handle("GET /runs", xbin.RoleFunc("admin", handleListRuns))
	mux.Handle("POST /runs", xbin.RoleFunc("admin", handleNewRun))
	mux.Handle("GET /runs/{id}", xbin.RoleFunc("admin", handleGetRun))
	mux.Handle("DELETE /runs/{id}", xbin.RoleFunc("admin", handleDeleteRun))
	mux.Handle("POST /runs/{id}/message", xbin.RoleFunc("admin", handleMessage))
	mux.Handle("POST /runs/{id}/answer", xbin.RoleFunc("admin", handleMessage))
	mux.Handle("POST /runs/{id}/approve", xbin.RoleFunc("admin", handleApprove))
	mux.Handle("POST /runs/{id}/interrupt", xbin.RoleFunc("admin", handleInterrupt))
	mux.Handle("POST /runs/{id}/resume", xbin.RoleFunc("admin", handleResume))
	mux.Handle("POST /runs/{id}/compact", xbin.RoleFunc("admin", handleCompact))
	mux.Handle("PUT /runs/{id}/memory", xbin.RoleFunc("admin", handleMemoryPut))
	mux.Handle("DELETE /runs/{id}/memory", xbin.RoleFunc("admin", handleMemoryDelete))
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
	ag.reRegisterSchedules() // re-assert cron-agents (idempotent)
	ids, _ := ag.db.dueRuns()
	for _, id := range ids {
		ag.driveAsync(id)
	}
	ag.reconcileBeat()
}

// handleModels proxies llm-gw's aggregated model list so the tile can populate
// the model-tier dropdowns (the agent holds the apps/llm-gw grant).
func handleModels(w http.ResponseWriter, r *http.Request) {
	req, _ := http.NewRequestWithContext(r.Context(), http.MethodGet, "http://xbin/api/apps/"+gwPath()+"/v1/models", nil)
	resp, err := xbin.Client().Do(req)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
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
	writeJSON(w, http.StatusOK, map[string]any{"keys": featureKeys, "features": state})
}

// reconcileBeat keeps the wake heartbeat registered ONLY while runs need waking
// (sleeping, or mid-drive/stalled) — so cron isn't always-on. Cheap: it only
// hits the gateway when the desired state actually flips.
func (ag *Agent) reconcileBeat() {
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
		req, _ := http.NewRequest(http.MethodPut, "http://xbin/api/xbin/cron/jobs", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := xbin.Client().Do(req)
		if err != nil {
			continue
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return true
		}
	}
	log.Printf("agent: could not register wake heartbeat (self-scheduling degraded)")
	return false
}

// stopBeat removes the wake heartbeat when nothing is pending.
func (ag *Agent) stopBeat() bool {
	req, _ := http.NewRequest(http.MethodDelete, "http://xbin/api/xbin/cron/jobs/heartbeat", nil)
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNotFound
}

// --- handlers -----------------------------------------------------------

func handleListRuns(w http.ResponseWriter, r *http.Request) {
	runs, err := agent.db.listRuns()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if runs == nil {
		runs = []*Run{}
	}
	writeJSON(w, 200, runs)
}

func handleNewRun(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Title, Goal, System string
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Goal == "" {
		writeErr(w, 400, "need {goal}")
		return
	}
	cfg := parseConfig(agent.db.getSetting("config"))
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
		writeErr(w, 500, err.Error())
		return
	}
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System})
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "user", Content: body.Goal})
	agent.db.journal(id, "note", map[string]string{"text": "run created"})
	agent.driveAsync(id)
	run, _ := agent.db.getRun(id)
	writeJSON(w, 200, run)
}

func handleGetRun(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, err := agent.db.getRun(id)
	if err != nil {
		writeErr(w, 404, "no such run")
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
	writeJSON(w, 200, map[string]any{"run": run, "messages": msgs, "steps": steps, "memory": mem, "config": cfg, "draft": agent.getDraft(id)})
}

func handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	if err := agent.db.deleteRun(pathID(r)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Text string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Text == "" {
		writeErr(w, 400, "need {text}")
		return
	}
	if _, err := agent.db.getRun(id); err != nil {
		writeErr(w, 404, "no such run")
		return
	}
	_, _ = agent.db.addMessage(&Message{RunID: id, Role: "user", Content: body.Text})
	_ = agent.db.setStatus(id, statusIdle, 0, "", "")
	agent.driveAsync(id)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleApprove(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Approve bool }
	_ = json.NewDecoder(r.Body).Decode(&body)
	run, err := agent.db.getRun(id)
	if err != nil {
		writeErr(w, 404, "no such run")
		return
	}
	var pend pending
	if run.Pending == "" || json.Unmarshal([]byte(run.Pending), &pend) != nil || pend.Kind != "approval" {
		writeErr(w, 400, "no pending approval")
		return
	}
	if body.Approve {
		// Keep pending; the drive resume-path executes it.
		_ = agent.db.setStatus(id, statusRunning, 0, "", run.Pending)
	} else {
		for _, tc := range pend.ToolCalls {
			agent.addToolResult(id, tc, "(denied by user)")
		}
		agent.db.journal(id, "note", map[string]string{"text": "tool call(s) denied"})
		_ = agent.db.setStatus(id, statusRunning, 0, "", "")
	}
	agent.driveAsync(id)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleInterrupt(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	agent.requestStop(id)
	_ = agent.db.setStatus(id, statusIdle, 0, "interrupted", "")
	agent.db.journal(id, "note", map[string]string{"text": "interrupted by user"})
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleResume(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	agent.driveAsync(id)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleCompact(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, err := agent.db.getRun(id)
	if err != nil {
		writeErr(w, 404, "no such run")
		return
	}
	cfg, _ := agent.db.runConfig(id)
	cfg.TokenBudget = 0 // force
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	agent.maybeCompact(ctx, run, cfg)
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleMemoryPut(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Key, Value string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if body.Key == "" {
		writeErr(w, 400, "need {key}")
		return
	}
	if err := agent.db.memorySet(id, body.Key, body.Value); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	key := r.URL.Query().Get("key")
	if err := agent.db.memoryDelete(id, key); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func handleGetConfig(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, parseConfig(agent.db.getSetting("config")))
}

func handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var cfg Config
	if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
		writeErr(w, 400, "bad config")
		return
	}
	b, _ := json.Marshal(cfg)
	if err := agent.db.putSetting("config", string(b)); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, cfg)
}

func handleTick(w http.ResponseWriter, r *http.Request) {
	ids, err := agent.db.dueRuns()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	for _, id := range ids {
		agent.driveAsync(id)
	}
	writeJSON(w, 200, map[string]any{"driving": len(ids)})
}

// --- helpers ------------------------------------------------------------

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// publishEvent emits a run lifecycle event on the bus (best-effort).
func publishEvent(runID int64, kind string) error {
	return xbin.Publish("res:"+xbin.Self()+"/events", kind, map[string]any{"runId": runID})
}
