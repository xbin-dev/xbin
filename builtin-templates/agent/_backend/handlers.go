// handlers.go — the run, tree, config, files and admin routes (inputs to a
// run — messages, approvals, stops — are in inbox.go; the live view in
// stream.go). All admin-only: the tile is self (always admin of itself) and
// the owner. See API.md.
package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// publishEvent emits a run lifecycle event on the bus (best-effort,
// fire-and-forget: a gateway round-trip must never sit inside a transaction
// or a turn).
func publishEvent(runID int64, kind string) {
	if agent == nil || agent.noGateway {
		return
	}
	go func() {
		_ = xbin.Publish("res:"+xbin.Self()+"/events", kind, map[string]any{"runId": runID})
	}()
}

// handleListRuns lists the runs the caller may see: the conversations they
// own, joined, or that are shared with the team — and, without roots=1,
// every subagent of those.
func handleListRuns(w http.ResponseWriter, r *http.Request) {
	where, args := aclWhere(callerOf(r))
	var runs []*Run
	var err error
	// an unsent draft (ask.go) is nobody's run yet
	if r.URL.Query().Get("roots") == "1" {
		runs, err = agent.db.queryRuns(`r WHERE r.parent_id=0 AND r.origin<>'held' AND `+where+` ORDER BY r.id DESC`, args...)
	} else {
		runs, err = agent.db.queryRuns(`WHERE root_id IN (SELECT r.id FROM runs r WHERE r.parent_id=0 AND r.origin<>'held' AND `+where+`) ORDER BY id DESC`, args...)
	}
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

// startRun creates a top-level run with its first message and starts it: the
// message is written directly (no run to queue it for yet) and the run poked.
// startRun starts a top-level run the legacy way: no owner, visible to the
// whole team. Kept for forks that call it; new code states who it is for
// with startRunOpts.
func (ag *Agent) startRun(title, kind string, cfg Config, text string, hold bool, note string) (*Run, error) {
	return ag.startRunOpts(runOpts{Title: title, Kind: kind, Cfg: cfg, Text: text, Hold: hold, Note: note})
}

// runOpts is a new top-level run: its first message, and who it is for.
type runOpts struct {
	Title, Kind string
	Cfg         Config
	Text        string
	Hold        bool // create it idle, with no message (attachments come first)
	Note        string
	Stamp       runStamp
	Sender      string  // who sent the first message
	Meta        msgMeta // on the first message: origin/label for automation prompts
}

func (ag *Agent) startRunOpts(o runOpts) (*Run, error) {
	var id int64
	err := ag.db.Tx(func(t *DB) error {
		var err error
		id, err = ag.startRunTx(t, o)
		return err
	})
	if err != nil {
		return nil, err
	}
	if !o.Hold && ag.eng != nil {
		ag.eng.Poke(id)
	}
	return ag.db.getRun(id)
}

// startRunTx creates the run inside the caller's transaction (the caller
// pokes it after the commit when it isn't held).
func (ag *Agent) startRunTx(t *DB, o runOpts) (int64, error) {
	cfgJSON, _ := json.Marshal(o.Cfg)
	status := statusRunning
	if o.Hold {
		status = statusIdle
	}
	id, err := t.createRunStamped(o.Title, string(cfgJSON), 0, status, o.Stamp)
	if err != nil {
		return 0, err
	}
	if o.Kind != "" {
		t.setRunKind(id, o.Kind)
	}
	if _, err := t.addMessage(&Message{RunID: id, Role: "system", Content: o.Cfg.System}); err != nil {
		return 0, err
	}
	if !o.Hold {
		m := &Message{RunID: id, Role: "user", Content: o.Text}
		meta := o.Meta
		meta.Sender = o.Sender
		if meta.Sender != "" || meta.Origin != "" {
			m.Meta, _ = json.Marshal(meta)
		}
		if _, err := t.addMessage(m); err != nil {
			return 0, err
		}
		_, _ = t.q.Exec(`UPDATE runs SET turn_started=? WHERE id=?`, now(), id)
	}
	if o.Note != "" {
		t.journal(id, "note", map[string]string{"text": o.Note})
	}
	if ag.eng != nil {
		ag.eng.emitRun(t, id)
	}
	return id, nil
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
	w0 := callerOf(r)
	st := w0.stamp("chat")
	st.TitleSrc = "user"
	title := body.Title
	if title == "" {
		title, st.TitleSrc = clip(body.Goal, 60), "clip"
	}
	if haltBlocks(w, r, 0) {
		return
	}
	run, err := agent.startRunOpts(runOpts{Title: title, Cfg: cfg, Text: body.Goal,
		Stamp: st, Sender: w0.user})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, run)
}

// handleGetRun is the pre-stream run detail, kept for existing tiles.
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
	files, _ := agent.db.replFiles(id)
	if files == nil {
		files = []*ReplFile{}
	}
	active, limit, _ := agent.eng.gate.stats()
	xbin.WriteJSON(w, 200, map[string]any{"run": run, "messages": legacyMessages(msgs), "steps": steps, "memory": mem,
		"config": cfg.forView(), "files": files, "messageFiles": agent.db.messageFiles(id), "draft": agent.eng.getDraft(id),
		"queued": agent.db.queuedView(id), "slots": map[string]int{"active": active, "limit": limit}})
}

func handleDeleteRun(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	// Who could see it, for the list event: once it's gone there is no ACL
	// left to load.
	var acl *rootACL
	if run, err := agent.db.getRun(id); err == nil && run.ParentID == 0 {
		acl, _ = agent.db.loadACL(id)
	}
	// Stop the subtree before removing it: a live turn must not keep
	// spending on rows that are gone.
	_ = agent.db.Tx(func(t *DB) error {
		agent.cancelRuns(t, id, true, "run deleted")
		return nil
	})
	if err := agent.deleteRunTree(id); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	agent.acl.flush(id)
	_, _ = agent.db.q.Exec(`DELETE FROM run_members WHERE run_id=?`, id)
	_, _ = agent.db.q.Exec(`DELETE FROM share_links WHERE run_id=?`, id)
	_, _ = agent.db.q.Exec(`DELETE FROM run_user_state WHERE run_id=?`, id)
	if agent.eng != nil {
		agent.eng.hub.publish(&Event{Type: evRun, Run: id, Root: id, Data: map[string]any{"id": id, "deleted": true}, acl: acl})
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleRunTree is the whole workflow in one request: statuses, links,
// blockers, cost. Metadata only — never transcripts.
func handleRunTree(w http.ResponseWriter, r *http.Request) {
	e := agent.eng
	run, err := e.db.getRun(pathID(r))
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	root := rootOf(run)
	runs, err := e.db.treeRuns(root)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	last := e.db.lastStepByRun(root)
	nodes := make([]map[string]any, 0, len(runs))
	var version int64
	tot := map[string]int{}
	var promptTok, compTok, calls int
	for _, n := range runs {
		if n.Updated > version {
			version = n.Updated
		}
		l := e.db.latestLink(n.ID)
		word := nodeWord(n, l)
		tot[word]++
		promptTok += n.PromptTokens
		compTok += n.CompletionTokens
		calls += n.LLMCalls
		var blockedOn []int64
		if parsePending(n.Pending).Kind == "deps" {
			blockedOn = scanIDs(e.db.q.Query(`SELECT ld.dep_run FROM link_deps ld JOIN links l ON l.id=ld.dep_link
				WHERE ld.child_id=? AND l.state='running'`, n.ID))
		}
		if blockedOn == nil {
			blockedOn = []int64{}
		}
		node := map[string]any{
			"id": n.ID, "parentId": n.ParentID, "depth": n.Depth, "title": n.Title,
			"status": word, "rawStatus": n.Status, "outcome": n.Outcome, "phase": phaseWord(n, l),
			"detached": n.Detached, "blockedOn": blockedOn, "blockReason": blockReason(n, blockedOn),
			"created": n.Created, "updated": n.Updated, "settledAt": n.SettledAt,
			"result": clip(n.Result, 160), "lastStep": last[n.ID],
			"llmCalls": n.LLMCalls, "promptTokens": n.PromptTokens, "completionTokens": n.CompletionTokens,
		}
		if l != nil {
			node["link"] = map[string]any{"id": l.ID, "mode": l.Mode, "state": l.State, "outcome": l.Outcome,
				"toolCallId": l.ToolCallID, "deadline": l.Deadline, "delivered": l.Delivered}
			if l.State != linkRunning {
				node["result"] = clip(l.Result, 160)
			}
		}
		nodes = append(nodes, node)
	}
	active, limit, waiting := e.gate.stats()
	xbin.WriteJSON(w, 200, map[string]any{
		"root": root, "version": version, "halted": e.halted(), "nodes": nodes,
		"totals": map[string]any{
			"nodes": len(nodes), "byStatus": tot, "promptTokens": promptTok, "completionTokens": compTok,
			"llmCalls": calls, "active": active, "limit": limit, "waiting": waiting,
		},
	})
}

// nodeWord maps a run onto the tree view's small vocabulary.
func nodeWord(n *Run, l *Link) string {
	switch n.Status {
	case statusRunning, statusQueued:
		return "running"
	case statusAwait, statusBlocked, statusWaiting:
		return "blocked"
	case statusSleep:
		return "sleeping"
	case statusError:
		return "error"
	case statusCanceled:
		return "cancelled"
	case statusDone:
		return "done"
	}
	if l != nil && l.State != linkRunning {
		switch l.State {
		case linkError:
			return "error"
		case linkCanceled:
			return "cancelled"
		}
		if l.Outcome == outcomeIncomplete {
			return "incomplete"
		}
		return "done"
	}
	return "idle"
}

// blockReason says WHY a node is not moving.
func blockReason(n *Run, blockedOn []int64) string {
	switch {
	case len(blockedOn) > 0:
		return "dep"
	case n.Status == statusAwait:
		return "children"
	case n.Status == statusWaiting:
		return "human"
	case n.Status == statusSleep:
		return "sleeping"
	}
	return ""
}

// lastStepByRun returns each node's most recent journal line.
func (d *DB) lastStepByRun(rootID int64) map[int64]string {
	out := map[int64]string{}
	rows, err := d.q.Query(`
		SELECT s.run_id, s.kind, s.detail FROM steps s
		 WHERE s.id IN (SELECT MAX(id) FROM steps
		                 WHERE run_id IN (SELECT id FROM runs WHERE root_id=?) GROUP BY run_id)`, rootID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var kind, detail string
		if rows.Scan(&id, &kind, &detail) == nil {
			out[id] = kind + " " + clip(detail, 90)
		}
	}
	return out
}

func handleHaltGet(w http.ResponseWriter, r *http.Request) {
	xbin.WriteJSON(w, 200, map[string]any{"on": agent.eng.halted()})
}

// handleHaltPut is the brake: durable (it must survive the restart a runaway
// may cause), cancels every live run, and blocks new spawns and turns until a
// human message or the brake coming off.
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
		ids := scanIDs(agent.db.q.Query(`SELECT id FROM runs WHERE status IN ('running','queued','blocked','awaiting','sleeping','waiting_input')`))
		_ = agent.db.Tx(func(t *DB) error {
			for _, id := range ids {
				stopped += len(agent.cancelRuns(t, id, false, "halted by the owner"))
			}
			return nil
		})
		// Cancel rows are applied even while halted (the pass handles stops
		// before the brake).
	} else if agent.eng != nil {
		go agent.eng.recover()
	}
	xbin.WriteJSON(w, 200, map[string]any{"on": body.On, "cancelled": stopped})
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
	if err := agent.db.memoryDelete(pathID(r), r.URL.Query().Get("key")); err != nil {
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
	// The model-call limit bounds the process, so it is read live here rather
	// than snapshotted into each run's config.
	agent.eng.gate.setLimit(cfg.maxActiveRuns())
	xbin.WriteJSON(w, 200, cfg)
}

// handleFeatures reports the toggleable capabilities and their state.
func handleFeatures(w http.ResponseWriter, r *http.Request) {
	cfg := parseConfig(agent.db.getSetting("config"))
	state := map[string]bool{}
	for _, k := range featureKeys {
		state[k] = cfg.feature(k)
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"keys": featureKeys, "features": state})
}

// handleModels proxies llm-gw's aggregated model list (for the tier pickers).
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

// handleTick is the idempotent recovery scan: the resume job left by a
// process that exited with work (owner.go) lands here, and so does the
// pre-engine heartbeat job until takeover deletes it.
func handleTick(w http.ResponseWriter, r *http.Request) {
	if agent.eng != nil {
		agent.eng.recover()
	}
	xbin.WriteJSON(w, 200, map[string]any{"ok": true})
}

// --- session files --------------------------------------------------------------

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

// handleUpload accepts one attached file as the raw request body.
func handleUpload(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	name := r.URL.Query().Get("name")
	if name == "" {
		xbin.WriteError(w, 400, "need ?name=")
		return
	}
	body := http.MaxBytesReader(w, r.Body, maxBinaryFileBytes+1)
	f, err := agent.acceptUpload(r.Context(), id, name, r.Header.Get("Content-Type"), body)
	if err != nil {
		code := 400
		var mbe *http.MaxBytesError
		switch {
		case err == errTooLarge || errors.As(err, &mbe):
			code, err = http.StatusRequestEntityTooLarge, errTooLarge
		case err.Error() == "no such run":
			code = 404
		case strings.Contains(err.Error(), "storing the file failed"):
			code = 502
		}
		xbin.WriteError(w, code, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"path": f.Path, "mime": f.Mime, "bytes": f.Bytes, "binary": f.Binary})
}

// handleRaw returns a file's bytes, for the tile's preview and download.
func handleRaw(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	p, err := normReplPath(r.URL.Query().Get("path"))
	if err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	f, err := agent.db.replFile(id, p)
	if err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	data := []byte(f.Content)
	if f.Binary {
		if data, err = agent.readBlob(r.Context(), f.Blob); err != nil {
			xbin.WriteError(w, 502, err.Error())
			return
		}
	}
	m := f.Mime
	if m == "" {
		m = "text/plain; charset=utf-8"
	}
	w.Header().Set("Content-Type", m)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

// handleFilePut is the human's editor path: an optional version guards a
// change the agent made in between (409).
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
	blob, err := agent.db.replDeleteFile(pathID(r), path)
	if err != nil {
		xbin.WriteError(w, 404, err.Error())
		return
	}
	if blob != "" {
		agent.dropBlobs([]string{blob})
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}
