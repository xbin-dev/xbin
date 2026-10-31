package main

import (
	"strings"
	"testing"
	"time"
)

// project_goal: the coordinator's own tool, kept per coordinator and shown
// by the overview (by: coordinator), its `now` gone at the turn's end; it,
// the turn's end and a status move send the `project` event's
// "coordinator" change to the project's viewers; it goes with its run.
func TestProjectGoal(t *testing.T) {
	fx := newCoordFix(t)
	d := fx.ag.db
	old := projStreamDelay.Load()
	projStreamDelay.Store(int64(5 * time.Millisecond))
	t.Cleanup(func() { projStreamDelay.Store(old) })
	hub := fx.ag.eng.hub
	alice, _, _ := hub.subscribe(0, hub.now(), who{kind: whoUser, user: "alice", level: "read"})
	defer hub.unsubscribe(alice)
	coordEvents := func() int {
		alice.mu.Lock()
		defer alice.mu.Unlock()
		n := 0
		for _, ev := range alice.q {
			if m, ok := ev.Data.(map[string]any); ok && ev.Type == evProject && m["change"] == "coordinator" && m["id"] == fx.p.ID {
				n++
			}
		}
		return n
	}
	coord := fx.coordinator(t, asAlice, fx.p.ID)
	cfg, _ := d.runConfig(coord.ID)
	if !toolNamesIn(runToolSpecs(cfg, coord, nil))["project_goal"] {
		t.Fatalf("project_goal isn't offered to the coordinator")
	}
	if !strings.Contains(coordPrompt(fx.p, coord), "project_goal") {
		t.Errorf("the coordinator's prompt doesn't say when to set its goal")
	}
	// no goal yet, nothing said: none
	if g := d.coordGoal(coord); g != nil {
		t.Fatalf("a goal from nothing: %+v", g)
	}
	if _, err := fx.tool(t, coord, "project_goal", map[string]any{"text": "  "}); err == nil {
		t.Errorf("an empty goal was taken")
	}
	before := coordEvents()
	out, err := fx.tool(t, coord, "project_goal", map[string]any{"text": "Keep main green; fix #9 first.", "now": "reading CI on #4"})
	if err != nil || !strings.Contains(out, "Goal set") {
		t.Fatalf("project_goal: %q %v", out, err)
	}
	waitFor(t, "the coordinator change", func() bool { return coordEvents() > before })
	g := d.coordGoal(coord)
	if g == nil || g.By != "coordinator" || g.Text != "Keep main green; fix #9 first." || g.Now != "reading CI on #4" || g.AtMs == 0 {
		t.Fatalf("the goal kept: %+v", g)
	}
	if _, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, ""); ov.Coordinator.Goal == nil || ov.Coordinator.Goal.Text != g.Text || ov.Coordinator.Goal.By != "coordinator" {
		t.Errorf("the overview's goal: %+v", ov.Coordinator.Goal)
	}
	// clipped and redacted as kept
	long := strings.Repeat("x", 2*coordGoalMax) + " ghp_" + strings.Repeat("a", 36)
	if _, err := fx.tool(t, coord, "project_goal", map[string]any{"text": "ship it with ghp_" + strings.Repeat("a", 36), "now": long}); err != nil {
		t.Fatal(err)
	}
	if g := d.coordGoal(coord); strings.Contains(g.Text, "ghp_aaaa") || len([]rune(g.Now)) > coordNowMax {
		t.Errorf("kept as said: %+v", g)
	}
	// the turn's end: now goes, the text stays, the page hears
	before = coordEvents()
	if err := d.Tx(func(tx *DB) error { runTurnEnd(tx, coord, turnAnswered, outcomeAnswered, ""); return nil }); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the turn end's coordinator change", func() bool { return coordEvents() > before })
	if g := d.coordGoal(coord); g.Now != "" || !strings.HasPrefix(g.Text, "ship it with") {
		t.Errorf("after the turn: %+v", g)
	}
	// a status move (waiting for its person) is heard too; a task's is not a coordinator change
	before = coordEvents()
	if err := d.setStatusOnly(coord.ID, statusWaiting); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the status move's coordinator change", func() bool { return coordEvents() > before })
	if _, ov := overviewOf(t, fx.mux, asAlice, fx.p.ID, ""); ov.Coordinator.State != "waiting" {
		t.Errorf("waiting coordinator's state: %q", ov.Coordinator.State)
	}
	_, task := fx.addTask(t, fx.p, 1, "alice")
	time.Sleep(30 * time.Millisecond)
	before = coordEvents()
	if err := d.setStatusOnly(task.ID, statusWaiting); err != nil {
		t.Fatal(err)
	}
	time.Sleep(30 * time.Millisecond)
	if n := coordEvents(); n != before {
		t.Errorf("a task's status move made %d coordinator changes", n-before)
	}
	// a task's conversation has no project_goal
	if _, err := fx.tool(t, task, "project_goal", map[string]any{"text": "x"}); err == nil {
		t.Errorf("a task set a goal")
	}
	// deleted with its run
	if err := d.deleteRun(coord.ID); err != nil {
		t.Fatal(err)
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM coord_goals WHERE run_id=?`, coord.ID).Scan(&n)
	if n != 0 {
		t.Errorf("a deleted coordinator's goal stayed")
	}
}
