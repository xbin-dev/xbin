// schedule.go — cron-agents: user- or agent-created schedules that start (or
// re-drive) a run on a cadence. Each enabled schedule registers its OWN cron
// job (so cron isn't a single always-on poller); firing it creates a fresh run
// (or, for a watcher, re-drives one persistent run). See API.md.
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

type Schedule struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Cron    string `json:"cron"`    // 5-field cron or "@every 30m" (robfig)
	Goal    string `json:"goal"`    // the task each firing works on
	System  string `json:"system"`  // optional system prompt override
	Watcher bool   `json:"watcher"` // re-drive one persistent run; discard no-change rounds
	Toolset string `json:"toolset"` // capability lane for fired runs: private (default) | web
	Enabled bool   `json:"enabled"`
	RunID   int64  `json:"runId"` // watcher's persistent run (0 = none yet)
	LastRun int64  `json:"lastRun"`
	Created int64  `json:"created"`
	// Whose automation this is, and where its firings go (D83). Owner "" and
	// visibility "team" is a schedule from before: everyone sees its runs.
	Owner        string `json:"owner"`
	Visibility   string `json:"visibility"`
	Mode         string `json:"mode"`         // "" = isolated (a new run per fire) | persistent | conversation
	TargetRun    int64  `json:"targetRun"`    // mode conversation: the chat it reports to
	CreatedByRun int64  `json:"createdByRun"` // the run whose agent created it (0 = a person)
	LastRunID    int64  `json:"lastRunId"`    // the latest run it fired
	LastStatus   string `json:"lastStatus"`   // how that run's last turn ended
}

// access is what a caller may do with this automation (D83): its owner runs
// and edits it; a legacy one (no owner) belongs to the tile's managers; a
// team one is visible to everyone. Managers also OVERSEE every automation —
// see it listed, switch it off, delete it (managerOversees) — without
// opening its private runs.
func (s *Schedule) access(w who) level {
	switch {
	case w.kind == whoSystem || w.kind == whoCron:
		return lvSystem
	case w.kind == whoUser && w.viewedBy == "" && s.Owner != "" && s.Owner == w.user:
		return lvOwner
	case w.kind == whoElement && s.Owner == "el:"+w.el:
		return lvOwner
	case s.Owner == "" && w.manager() && w.viewedBy == "":
		return lvOwner
	case s.Visibility == visTeam:
		return lvViewer
	}
	return lvNone
}

// scheduleFor resolves {id} and the caller's access; it writes the error.
func scheduleFor(w http.ResponseWriter, r *http.Request) (*Schedule, who, level, bool) {
	c := callerOf(r)
	s, err := agent.db.getSchedule(pathID(r))
	if err != nil || (s.access(c) == lvNone && !c.manager()) {
		xbin.WriteError(w, 404, "no such schedule")
		return nil, c, lvNone, false
	}
	return s, c, s.access(c), true
}

// stamp is who a run this schedule fires belongs to.
func (s *Schedule) stamp() runStamp {
	st := runStamp{Owner: s.Owner, Visibility: s.Visibility, TeamRole: roleViewer, Origin: "schedule",
		OriginID: s.ID, SessionKey: "sched:" + strconv.FormatInt(s.ID, 10), TitleSrc: "origin"}
	if s.Watcher {
		st.Origin, st.SessionKey = "watcher", "watch:"+strconv.FormatInt(s.ID, 10)
	}
	if st.Owner == "" { // legacy: shared with everyone, as it always was
		st.Visibility, st.TeamRole = visTeam, roleParticipant
	}
	return st
}

// --- storage -------------------------------------------------------------

func (d *DB) createSchedule(s *Schedule) (int64, error) {
	res, err := d.q.Exec(
		`INSERT INTO schedules (name, cron, goal, system, watcher, toolset, enabled, created,
		   owner, visibility, mode, target_run, created_by_run)
		 VALUES (?, ?, ?, ?, ?, ?, 1, ?, ?, ?, ?, ?, ?)`,
		s.Name, s.Cron, s.Goal, s.System, b2i(s.Watcher), s.Toolset, now(),
		s.Owner, orStr(s.Visibility, visTeam), s.Mode, s.TargetRun, s.CreatedByRun)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func scanSchedule(scan func(dest ...any) error) (*Schedule, error) {
	s := &Schedule{}
	var watcher, enabled int
	if err := scan(&s.ID, &s.Name, &s.Cron, &s.Goal, &s.System, &watcher, &s.Toolset, &enabled, &s.RunID, &s.LastRun, &s.Created,
		&s.Owner, &s.Visibility, &s.Mode, &s.TargetRun, &s.CreatedByRun, &s.LastRunID, &s.LastStatus); err != nil {
		return nil, err
	}
	s.Watcher, s.Enabled = watcher != 0, enabled != 0
	return s, nil
}

const scheduleCols = `id, name, cron, goal, system, watcher, toolset, enabled, run_id, last_run, created, owner, visibility, mode, target_run, created_by_run, last_run_id, last_status`

func (d *DB) getSchedule(id int64) (*Schedule, error) {
	return scanSchedule(d.q.QueryRow(`SELECT `+scheduleCols+` FROM schedules WHERE id=?`, id).Scan)
}

func (d *DB) listSchedules() ([]*Schedule, error) {
	rows, err := d.q.Query(`SELECT ` + scheduleCols + ` FROM schedules ORDER BY id`)
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
	_, err := d.q.Exec(
		`UPDATE schedules SET name=?, cron=?, goal=?, system=?, watcher=?, enabled=? WHERE id=?`,
		s.Name, s.Cron, s.Goal, s.System, b2i(s.Watcher), b2i(s.Enabled), s.ID)
	return err
}

func (d *DB) setScheduleRun(id, runID int64) {
	_, _ = d.q.Exec(`UPDATE schedules SET run_id=? WHERE id=?`, runID, id)
}

func (d *DB) touchScheduleRun(id int64) {
	_, _ = d.q.Exec(`UPDATE schedules SET last_run=? WHERE id=?`, now(), id)
}

func (d *DB) deleteSchedule(id int64) error {
	_, err := d.q.Exec(`DELETE FROM schedules WHERE id=?`, id)
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
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPut, "http://xbin/api/xbin/cron/jobs", bytes.NewReader(body))
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
	ag.cronDelete(scheduleCronName(id))
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

// fireSchedule runs a schedule once: a watcher gets a "check now" in its
// persistent run, a normal schedule starts a new run. Nothing fires while the
// owner's halt is on.
func (ag *Agent) fireSchedule(s *Schedule) {
	if ag.db.getSetting("halt") == "1" {
		return
	}
	ag.db.touchScheduleRun(s.ID)
	if s.Watcher {
		ag.fireWatcher(s)
		return
	}
	cfg := parseConfig(ag.db.getSetting("config"))
	cfg.Toolset = s.Toolset // fired runs carry the schedule's capability lane
	if s.System != "" {
		cfg.System = s.System
	}
	title := s.Name
	if title == "" {
		title = clip(s.Goal, 60)
	}
	run, err := ag.startRunOpts(runOpts{Title: "⏱ " + title, Cfg: cfg, Text: s.Goal,
		Note: fmt.Sprintf("started by schedule #%d (%s)", s.ID, s.Name), Stamp: s.stamp(),
		Meta: msgMeta{Origin: "schedule", OriginID: s.ID, Label: title}})
	if err == nil {
		_, _ = ag.db.q.Exec(`UPDATE schedules SET last_run_id=? WHERE id=?`, run.ID, s.ID)
	}
}

// --- watcher mode --------------------------------------------------------

func watcherSystem(goal string) string {
	return "You are a WATCHER. Your job: " + goal + "\n\nEach time you are asked to " +
		"'check now', inspect the above using your tools. If something CHANGED since the " +
		"last check, call state_changed(summary). If nothing changed, reply briefly with " +
		"'no change' and stop. Do not call finish — you run repeatedly on a schedule."
}

const watcherCheck = "Check now. If anything changed since your last check, call state_changed with a short summary; otherwise reply 'no change'."

// fireWatcher queues a "check now" round in the watcher's ONE persistent
// run. The round is durable (an inbox row that records its transcript mark
// and whether state_changed was called), so a restart mid-round still rolls a
// no-change round back. A round still open (or queued) makes this firing a
// no-op rather than stacking checks.
func (ag *Agent) fireWatcher(s *Schedule) {
	runID := s.RunID
	if _, err := ag.db.getRun(runID); runID == 0 || err != nil {
		cfg := parseConfig(ag.db.getSetting("config"))
		cfg.Toolset = s.Toolset
		sys := watcherSystem(s.Goal)
		if s.System != "" {
			sys = s.System + "\n\n" + sys
		}
		cfg.System = sys
		title := s.Name
		if title == "" {
			title = clip(s.Goal, 60)
		}
		run, err := ag.startRunOpts(runOpts{Title: "👁 " + title, Cfg: cfg, Hold: true, Stamp: s.stamp()})
		if err != nil {
			return
		}
		runID = run.ID
		ag.db.setScheduleRun(s.ID, runID)
	}
	var open int
	_ = ag.db.q.QueryRow(`SELECT count(*) FROM inbox WHERE run_id=? AND kind='watch'
		AND (delivered_at=0 OR json_extract(body,'$.open')=1)`, runID).Scan(&open)
	if open > 0 {
		return
	}
	_, _, _ = ag.queue(runID, inboxWatch, inboxBody{Text: watcherCheck, Source: "watch"}, "")
}

// --- handlers ------------------------------------------------------------

func handleListSchedules(w http.ResponseWriter, r *http.Request) {
	list, err := agent.db.listSchedules()
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	c := callerOf(r)
	out := []*Schedule{}
	for _, s := range list {
		switch lv := s.access(c); {
		case lv >= lvViewer:
			out = append(out, s)
		case c.manager() && c.viewedBy == "":
			// Oversight: a manager sees it exists and can switch it off, but
			// not what someone else's private automation is about.
			cp := *s
			cp.Goal, cp.System = "", ""
			out = append(out, &cp)
		}
	}
	xbin.WriteJSON(w, 200, out)
}

func handleNewSchedule(w http.ResponseWriter, r *http.Request) {
	var s Schedule
	if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
		xbin.WriteError(w, 400, "need JSON body: {name, cron, goal, watcher?, system?}")
		return
	}
	s.Cron, s.Goal = strings.TrimSpace(s.Cron), strings.TrimSpace(s.Goal)
	if s.Cron == "" || s.Goal == "" {
		xbin.WriteError(w, 400, "need {cron, goal}")
		return
	}
	s.Toolset = normalizeToolset(s.Toolset) // human-created: either lane, validated
	// Whose it is comes from the caller, never the body.
	w0 := callerOf(r)
	st := w0.stamp("schedule")
	s.Owner, s.CreatedByRun, s.LastRunID, s.LastStatus, s.Mode, s.TargetRun = st.Owner, 0, 0, "", "", 0
	if s.Visibility != visTeam {
		s.Visibility = st.Visibility
	}
	if s.Watcher && !parseConfig(agent.db.getSetting("config")).feature("watcher") {
		xbin.WriteError(w, 400, "watcher mode is disabled in the agent's Features")
		return
	}
	id, err := agent.db.createSchedule(&s)
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	s.ID, s.Enabled = id, true
	if err := agent.registerScheduleCron(&s); err != nil {
		// Roll back a schedule the gateway rejected (e.g. bad cron expr).
		_ = agent.db.deleteSchedule(id)
		xbin.WriteError(w, 400, "bad schedule: "+err.Error())
		return
	}
	xbin.WriteJSON(w, 200, s)
}

func handleUpdateSchedule(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	cur, c, lv, ok := scheduleFor(w, r)
	if !ok {
		return
	}
	if lv < lvOwner && !c.manager() {
		xbin.WriteError(w, 403, "only its owner can change this automation")
		return
	}
	// Decode onto the current record so omitted fields keep their value —
	// except the ones no request may set (who owns it, what it fired).
	keep := *cur
	if err := json.NewDecoder(r.Body).Decode(cur); err != nil {
		xbin.WriteError(w, 400, "bad body")
		return
	}
	cur.ID, cur.Owner, cur.CreatedByRun, cur.LastRunID, cur.LastStatus, cur.RunID, cur.Created, cur.LastRun =
		id, keep.Owner, keep.CreatedByRun, keep.LastRunID, keep.LastStatus, keep.RunID, keep.Created, keep.LastRun
	if lv < lvOwner { // a manager overseeing someone else's: on/off only
		enabled := cur.Enabled
		*cur = keep
		cur.Enabled = enabled
	}
	if err := agent.db.updateSchedule(cur); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if cur.Enabled {
		if err := agent.registerScheduleCron(cur); err != nil {
			xbin.WriteError(w, 400, "bad schedule: "+err.Error())
			return
		}
	} else {
		agent.unregisterScheduleCron(id)
	}
	xbin.WriteJSON(w, 200, cur)
}

func handleDeleteSchedule(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, c, lv, ok := scheduleFor(w, r); !ok {
		return
	} else if lv < lvOwner && !c.manager() {
		xbin.WriteError(w, 403, "only its owner can delete this automation")
		return
	}
	agent.unregisterScheduleCron(id)
	if err := agent.db.deleteSchedule(id); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleFireSchedule is the cron target; handleTriggerSchedule is a manual run.
func handleFireSchedule(w http.ResponseWriter, r *http.Request) {
	s, _, lv, ok := scheduleFor(w, r)
	if !ok {
		return
	}
	if lv < lvOwner { // "Run now" is its owner's; cron and the system pass as lvSystem
		xbin.WriteError(w, 403, "only its owner can run this automation")
		return
	}
	if s.Enabled {
		agent.fireSchedule(s)
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
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
