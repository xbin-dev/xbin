// schedule.go — cron-agents: user- or agent-created schedules that start (or
// re-drive) a run on a cadence. Each enabled schedule registers its OWN cron
// job (so cron isn't a single always-on poller); firing it creates a fresh run
// (or, for a watcher, re-drives one persistent run). See plans/agent-v2.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// watcherRound tracks one firing of a watcher schedule: the message seq before
// the round (for rollback) and whether the model reported a change.
type watcherRound struct {
	mark    int
	changed bool
}

type Schedule struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Cron    string `json:"cron"`    // 5-field cron or "@every 30m" (robfig)
	Goal    string `json:"goal"`    // the task each firing works on
	System  string `json:"system"`  // optional system prompt override
	Watcher bool   `json:"watcher"` // re-drive one persistent run; discard no-change rounds
	Enabled bool   `json:"enabled"`
	RunID   int64  `json:"runId"` // watcher's persistent run (0 = none yet)
	LastRun int64  `json:"lastRun"`
	Created int64  `json:"created"`
}

// --- storage -------------------------------------------------------------

func (d *DB) createSchedule(s *Schedule) (int64, error) {
	res, err := d.sql.Exec(
		`INSERT INTO schedules (name, cron, goal, system, watcher, enabled, created)
		 VALUES (?, ?, ?, ?, ?, 1, ?)`,
		s.Name, s.Cron, s.Goal, s.System, b2i(s.Watcher), now())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanSchedule(scan func(dest ...any) error) (*Schedule, error) {
	s := &Schedule{}
	var watcher, enabled int
	if err := scan(&s.ID, &s.Name, &s.Cron, &s.Goal, &s.System, &watcher, &enabled, &s.RunID, &s.LastRun, &s.Created); err != nil {
		return nil, err
	}
	s.Watcher, s.Enabled = watcher != 0, enabled != 0
	return s, nil
}

const scheduleCols = `id, name, cron, goal, system, watcher, enabled, run_id, last_run, created`

func (d *DB) getSchedule(id int64) (*Schedule, error) {
	return scanSchedule(d.sql.QueryRow(`SELECT `+scheduleCols+` FROM schedules WHERE id=?`, id).Scan)
}

func (d *DB) listSchedules() ([]*Schedule, error) {
	rows, err := d.sql.Query(`SELECT ` + scheduleCols + ` FROM schedules ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		s, err := scanSchedule(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func (d *DB) updateSchedule(s *Schedule) error {
	_, err := d.sql.Exec(
		`UPDATE schedules SET name=?, cron=?, goal=?, system=?, watcher=?, enabled=? WHERE id=?`,
		s.Name, s.Cron, s.Goal, s.System, b2i(s.Watcher), b2i(s.Enabled), s.ID)
	return err
}

func (d *DB) setScheduleRun(id, runID int64) {
	_, _ = d.sql.Exec(`UPDATE schedules SET run_id=? WHERE id=?`, runID, id)
}

func (d *DB) touchScheduleRun(id int64) {
	_, _ = d.sql.Exec(`UPDATE schedules SET last_run=? WHERE id=?`, now(), id)
}

func (d *DB) deleteSchedule(id int64) error {
	_, err := d.sql.Exec(`DELETE FROM schedules WHERE id=?`, id)
	return err
}

// --- cron wiring ---------------------------------------------------------

func scheduleCronName(id int64) string { return "sched-" + strconv.FormatInt(id, 10) }

// registerScheduleCron creates/updates the cron job that fires a schedule.
// Returns the gateway's error text (e.g. a bad cron expression) if any.
func (ag *Agent) registerScheduleCron(s *Schedule) error {
	job := map[string]any{
		"name":     scheduleCronName(s.ID),
		"resource": "res:" + xbin.Self() + "/beat",
		"schedule": s.Cron,
		"path":     fmt.Sprintf("/schedules/%d/fire", s.ID),
		"role":     "admin",
	}
	body, _ := json.Marshal(job)
	req, _ := http.NewRequest(http.MethodPut, "http://xbin/api/xbin/cron/jobs", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := xbin.Client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := readAllLimited(resp.Body)
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(b))
	}
	return nil
}

func (ag *Agent) unregisterScheduleCron(id int64) {
	req, _ := http.NewRequest(http.MethodDelete, "http://xbin/api/xbin/cron/jobs/"+scheduleCronName(id), nil)
	if resp, err := xbin.Client().Do(req); err == nil {
		resp.Body.Close()
	}
}

// reRegisterSchedules re-asserts every enabled schedule's cron job at startup
// (idempotent) so edits/restarts converge.
func (ag *Agent) reRegisterSchedules() {
	list, err := ag.db.listSchedules()
	if err != nil {
		return
	}
	for _, s := range list {
		if s.Enabled {
			_ = ag.registerScheduleCron(s)
		} else {
			ag.unregisterScheduleCron(s.ID)
		}
	}
}

// fireSchedule runs a schedule once: a watcher re-drives its persistent run
// (with a fresh "check now" nudge), a normal schedule starts a new run.
func (ag *Agent) fireSchedule(s *Schedule) {
	ag.db.touchScheduleRun(s.ID)
	if s.Watcher {
		ag.fireWatcher(s)
		return
	}
	cfg := parseConfig(ag.db.getSetting("config"))
	if s.System != "" {
		cfg.System = s.System
	}
	cfgJSON, _ := json.Marshal(cfg)
	title := s.Name
	if title == "" {
		title = clip(s.Goal, 60)
	}
	id, err := ag.db.createRun("⏱ "+title, string(cfgJSON), 0)
	if err != nil {
		return
	}
	_, _ = ag.db.addMessage(&Message{RunID: id, Role: "system", Content: cfg.System})
	_, _ = ag.db.addMessage(&Message{RunID: id, Role: "user", Content: s.Goal})
	ag.db.journal(id, "note", map[string]string{"text": fmt.Sprintf("started by schedule #%d (%s)", s.ID, s.Name)})
	ag.driveAsync(id)
}

// --- watcher mode --------------------------------------------------------

func watcherSystem(goal string) string {
	return "You are a WATCHER. Your job: " + goal + "\n\nEach time you are asked to " +
		"'check now', inspect the above using your tools. If something CHANGED since the " +
		"last check, call state_changed(summary). If nothing changed, reply briefly with " +
		"'no change' and stop. Do not call finish — you run repeatedly on a schedule."
}

// fireWatcher re-drives a watcher schedule's ONE persistent run with a fresh
// "check now" nudge. If the round reports no change (no state_changed call), its
// messages are rolled back so history keeps only the rounds that mattered.
func (ag *Agent) fireWatcher(s *Schedule) {
	runID := s.RunID
	if runID == 0 {
		cfg := parseConfig(ag.db.getSetting("config"))
		sys := watcherSystem(s.Goal)
		if s.System != "" {
			sys = s.System + "\n\n" + sys
		}
		cfg.System = sys
		cfgJSON, _ := json.Marshal(cfg)
		title := s.Name
		if title == "" {
			title = clip(s.Goal, 60)
		}
		var err error
		runID, err = ag.db.createRun("👁 "+title, string(cfgJSON), 0)
		if err != nil {
			return
		}
		_, _ = ag.db.addMessage(&Message{RunID: runID, Role: "system", Content: cfg.System})
		ag.db.setScheduleRun(s.ID, runID)
	}

	mark := ag.db.maxMessageSeq(runID)
	ag.mu.Lock()
	if ag.watcherRounds == nil {
		ag.watcherRounds = map[int64]*watcherRound{}
	}
	ag.watcherRounds[runID] = &watcherRound{mark: mark}
	ag.mu.Unlock()

	_, _ = ag.db.addMessage(&Message{RunID: runID, Role: "user",
		Content: "Check now. If anything changed since your last check, call state_changed with a short summary; otherwise reply 'no change'."})
	_ = ag.db.setStatus(runID, statusIdle, 0, "", "")

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		ag.drive(ctx, runID)
		ag.finishWatcherRound(runID)
		ag.reconcileBeat()
	}()
}

// finishWatcherRound discards a round that reported no change (keeping the
// transcript compact); a changed round stays.
func (ag *Agent) finishWatcherRound(runID int64) {
	ag.mu.Lock()
	wr := ag.watcherRounds[runID]
	delete(ag.watcherRounds, runID)
	ag.mu.Unlock()
	if wr == nil || wr.changed {
		return
	}
	ag.db.deleteMessagesAfter(runID, wr.mark)
	ag.db.journal(runID, "note", map[string]string{"text": "watcher: no change — round discarded"})
	_ = ag.db.setStatus(runID, statusIdle, 0, "", "")
}

// markWatcherChanged is called by the state_changed tool to keep the round.
func (ag *Agent) markWatcherChanged(runID int64) {
	ag.mu.Lock()
	if wr := ag.watcherRounds[runID]; wr != nil {
		wr.changed = true
	}
	ag.mu.Unlock()
}

// --- handlers ------------------------------------------------------------

func handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := agent.db.listSchedules()
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if list == nil {
		list = []*Schedule{}
	}
	writeJSON(w, 200, list)
}

func handleNewSchedule(w http.ResponseWriter, r *http.Request) {
	var s Schedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		writeErr(w, 400, "need JSON body: {name, cron, goal, watcher?, system?}")
		return
	}
	s.Cron, s.Goal = strings.TrimSpace(s.Cron), strings.TrimSpace(s.Goal)
	if s.Cron == "" || s.Goal == "" {
		writeErr(w, 400, "need {cron, goal}")
		return
	}
	if s.Watcher && !parseConfig(agent.db.getSetting("config")).feature("watcher") {
		writeErr(w, 400, "watcher mode is disabled in the agent's Features")
		return
	}
	id, err := agent.db.createSchedule(&s)
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	s.ID, s.Enabled = id, true
	if err := agent.registerScheduleCron(&s); err != nil {
		// Roll back a schedule the gateway rejected (e.g. bad cron expr).
		_ = agent.db.deleteSchedule(id)
		writeErr(w, 400, "bad schedule: "+err.Error())
		return
	}
	writeJSON(w, 200, s)
}

func handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	cur, err := agent.db.getSchedule(id)
	if err != nil {
		writeErr(w, 404, "no such schedule")
		return
	}
	// Decode onto the current record so omitted fields keep their value.
	if err := json.NewDecoder(r.Body).Decode(cur); err != nil {
		writeErr(w, 400, "bad body")
		return
	}
	cur.ID = id
	if err := agent.db.updateSchedule(cur); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	if cur.Enabled {
		if err := agent.registerScheduleCron(cur); err != nil {
			writeErr(w, 400, "bad schedule: "+err.Error())
			return
		}
	} else {
		agent.unregisterScheduleCron(id)
	}
	writeJSON(w, 200, cur)
}

func handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	agent.unregisterScheduleCron(id)
	if err := agent.db.deleteSchedule(id); err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

// handleFireSchedule is the cron target; handleTriggerSchedule is a manual run.
func handleFireSchedule(w http.ResponseWriter, r *http.Request) {
	s, err := agent.db.getSchedule(pathID(r))
	if err != nil {
		writeErr(w, 404, "no such schedule")
		return
	}
	if s.Enabled {
		agent.fireSchedule(s)
	}
	writeJSON(w, 200, map[string]string{"ok": "true"})
}

func readAllLimited(r interface{ Read([]byte) (int, error) }) (string, error) {
	var b strings.Builder
	buf := make([]byte, 512)
	for b.Len() < 4096 {
		n, err := r.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return b.String(), nil
}
