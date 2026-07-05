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
	"strings"
	"sync"
	"time"

	xbin "github.com/magik6k/xbin/sdk"
)

type Agent struct {
	db *DB

	mu      sync.Mutex
	driving map[int64]bool // runs being driven right now (coalesce)
	stop    map[int64]bool // interrupt requests
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
	agent = &Agent{db: db, driving: map[int64]bool{}, stop: map[int64]bool{}}

	// Seed the default config once.
	if db.getSetting("config") == "" {
		b, _ := json.Marshal(defaultConfig())
		_ = db.putSetting("config", string(b))
	}

	// Register the heartbeat that re-drives sleeping/stalled runs. Best-effort
	// and idempotent; retried since the gateway may not be up at the first tick.
	go registerHeartbeat()

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
	mux.Handle("POST /tick", xbin.RoleFunc("admin", handleTick))

	xbin.Serve(mux)
}

func registerHeartbeat() {
	job := map[string]any{
		"name":     "heartbeat",
		"resource": "res:" + xbin.Self() + "/beat",
		"schedule": "@every 1m",
		"path":     "/tick",
		"role":     "admin",
	}
	body, _ := json.Marshal(job)
	var last string
	for i := 0; i < 5; i++ {
		time.Sleep(time.Duration(i) * 2 * time.Second)
		req, _ := http.NewRequest(http.MethodPut, "http://xbin/api/xbin/cron/jobs", bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		resp, err := xbin.Client().Do(req)
		if err != nil {
			last = err.Error()
			continue
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			return
		}
		last = resp.Status + ": " + strings.TrimSpace(string(b))
	}
	log.Printf("agent: could not register heartbeat cron job (self-scheduling degraded): %s", last)
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
	writeJSON(w, 200, map[string]any{"run": run, "messages": msgs, "steps": steps, "memory": mem, "config": cfg})
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
