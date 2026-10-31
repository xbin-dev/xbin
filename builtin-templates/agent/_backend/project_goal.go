// project_goal.go — a coordinator's goal (D214, API.md §The coordinator):
// the project_goal tool sets {text, now?}, kept per coordinator
// (coord_goals; `now` is the turn's and goes when the turn ends). A
// project's overview shows it — or, before the coordinator set one, its
// person's latest message to it. Setting it, and the coordinator's turn
// starting, ending or its status moving, sends the `project` stream event
// with change "coordinator", so an open overview reads it again.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func init() {
	schemaAdds = append(schemaAdds, (*DB).addCoordGoalSchema)
	runDeletedHooks = append(runDeletedHooks, coordGoalRunDeleted)
	runStatusHooks = append(runStatusHooks, coordStatusMoved)
	turnEndHooks = append(turnEndHooks, coordTurnEnded)
}

// The goal's bounds (runes).
const (
	coordGoalMax = 500
	coordNowMax  = 300
)

const coordGoalSchema = `
CREATE TABLE IF NOT EXISTS coord_goals (
  run_id     INTEGER PRIMARY KEY,
  project_id INTEGER NOT NULL,
  text       TEXT    NOT NULL DEFAULT '',
  now        TEXT    NOT NULL DEFAULT '',
  at_ms      INTEGER NOT NULL DEFAULT 0
);`

// addCoordGoalSchema makes coord_goals (schemaAdds: never on team).
func (d *DB) addCoordGoalSchema() error {
	_, err := d.q.Exec(coordGoalSchema)
	return err
}

func coordGoalRunDeleted(t *DB, runID int64) error {
	_, _ = t.q.Exec(`DELETE FROM coord_goals WHERE run_id=?`, runID)
	return nil
}

// CoordGoal is the overview's coordinator.goal: by "coordinator" (project_goal)
// or "you" (its person's latest message, before it set one).
type CoordGoal struct {
	Text string `json:"text"`
	Now  string `json:"now,omitempty"`
	By   string `json:"by"`
	AtMs int64  `json:"atMs"`
}

// runeClip clips s to n runes, on one line's worth of trimming.
func runeClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n-1])) + "…"
	}
	return s
}

// setCoordGoal keeps run's goal (text "" leaves the text as it was: now only).
func (d *DB) setCoordGoal(pid, runID int64, text, now string) error {
	text, now = runeClip(projRedact(text), coordGoalMax), runeClip(projRedact(now), coordNowMax)
	return d.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`INSERT INTO coord_goals (run_id, project_id, text, now, at_ms) VALUES (?, ?, ?, ?, ?)
			ON CONFLICT(run_id) DO UPDATE SET text=CASE WHEN excluded.text<>'' THEN excluded.text ELSE text END,
			  now=excluded.now, at_ms=excluded.at_ms`, runID, pid, text, now, nowMs()); err != nil {
			return err
		}
		emitProject(t, pid, "coordinator", 0)
		return nil
	})
}

// coordGoal is coordinator run's goal: the one it set, else its person's
// latest message to it (nil: neither).
func (d *DB) coordGoal(run *Run) *CoordGoal {
	g := &CoordGoal{By: "coordinator"}
	if d.q.QueryRow(`SELECT text, now, at_ms FROM coord_goals WHERE run_id=?`, run.ID).Scan(&g.Text, &g.Now, &g.AtMs) == nil && g.Text != "" {
		return g
	}
	rows, err := d.q.Query(`SELECT content, meta, created FROM messages WHERE run_id=? AND role='user' ORDER BY seq DESC LIMIT 20`, run.ID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	for rows.Next() {
		var content, meta string
		var created int64
		if rows.Scan(&content, &meta, &created) != nil || strings.TrimSpace(content) == "" {
			continue
		}
		var m msgMeta
		if meta != "" && json.Unmarshal([]byte(meta), &m) != nil {
			continue
		}
		if m.Origin != "" || (m.Sender != "" && m.Sender != run.Owner) {
			continue // project updates, an automation: not its person's words
		}
		return &CoordGoal{Text: runeClip(content, coordGoalMax), By: "you", AtMs: created * 1000}
	}
	return nil
}

// --- the tool -------------------------------------------------------------------------------

func projectGoalSpec() toolSpec {
	return toolSpec{Type: "function", Function: funcDef{Name: "project_goal",
		Description: "Say what you are working towards in this project — a line your person sees at the top of the project's page. " +
			"Set it when your aim changes (a new instruction from your person, an aim reached), and now for what you are doing this turn.",
		Parameters: obj([]string{"text"}, map[string]any{
			"text": strProp(fmt.Sprintf("the goal in a sentence or two (at most %d characters)", coordGoalMax)),
			"now":  strProp(fmt.Sprintf("what you are doing this turn (at most %d characters; it clears when the turn ends)", coordNowMax)),
		})}}
}

func (ag *Agent) coordGoalTool(run *Run, p *Project, args map[string]any) (string, error) {
	var a struct {
		Text string `json:"text"`
		Now  string `json:"now"`
	}
	if err := coordArgs(args, &a); err != nil {
		return "", err
	}
	if strings.TrimSpace(a.Text) == "" {
		return "", errors.New("text: say the goal")
	}
	if err := ag.db.setCoordGoal(p.ID, run.ID, a.Text, a.Now); err != nil {
		return "", err
	}
	return "Goal set; your person sees it on the project's page.", nil
}

// --- the `coordinator` change ------------------------------------------------------------------

// coordPID is the project run coordinates (0: it is no coordinator) — from
// its row alone, no query.
func coordPID(run *Run) int64 {
	if run == nil || run.ParentID != 0 || run.Origin != originProject {
		return 0
	}
	pid, user, ok := coordKeyOf(run.SessionKey)
	if !ok || user != run.Owner || pid != run.OriginID {
		return 0
	}
	return pid
}

// coordTurnMoved (the engine's turn start): a coordinator's turn began.
func coordTurnMoved(t *DB, run *Run) {
	if run == nil || !hooksOn(t, run.ID) {
		return
	}
	if pid := coordPID(run); pid != 0 {
		emitProject(t, pid, "coordinator", 0)
	}
}

// coordTurnEnded (turnEndHooks): its turn's `now` goes, and the page hears.
func coordTurnEnded(t *DB, run *Run, _, _, _ string) {
	pid := coordPID(run)
	if pid == 0 {
		return
	}
	_, _ = t.q.Exec(`UPDATE coord_goals SET now='' WHERE run_id=? AND now<>''`, run.ID)
	emitProject(t, pid, "coordinator", 0)
}

// coordStatusMoved (runStatusHooks): a coordinator's status was written
// (waiting for its person, a wake, a stop) — one indexed lookup.
func coordStatusMoved(t *DB, runID int64, _ string) {
	r := &Run{ID: runID}
	if t.q.QueryRow(`SELECT origin, origin_id, parent_id, session_key, owner FROM runs WHERE id=?`, runID).
		Scan(&r.Origin, &r.OriginID, &r.ParentID, &r.SessionKey, &r.Owner) != nil {
		return
	}
	if pid := coordPID(r); pid != 0 {
		emitProject(t, pid, "coordinator", 0)
	}
}
