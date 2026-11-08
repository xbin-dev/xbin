package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// needsOf is GET path's items as c.
func needsOf(t *testing.T, fx *coordFix, c caller, path string) []map[string]any {
	t.Helper()
	w := callAs(t, fx.mux, c, "GET", path, nil)
	var out struct {
		Items []map[string]any `json:"items"`
	}
	if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &out) != nil {
		t.Fatalf("GET %s: %d %s", path, w.Code, w.Body)
	}
	return out.Items
}

// needFor is the item of run id among items (nil: none).
func needFor(items []map[string]any, id int64) map[string]any {
	for _, it := range items {
		if r, _ := it["run"].(map[string]any); r != nil && fmt.Sprint(r["id"]) == fmt.Sprint(id) {
			return it
		}
	}
	return nil
}

// GET /needs items say what waits — a coding agent's question (its
// questions, not its stock message nor its earlier answer), the built-in
// engine's approval — and since when (the park's own time).
func TestNeedsItemsCarryText(t *testing.T) {
	fx := newCoordFix(t)
	_, q := fx.addTask(t, fx.p, 1, "alice")
	asked := time.Now().Add(-90 * time.Minute).UnixMilli()
	parkQuestion(t, fx.ag.db, q.ID, asked)
	_, a := fx.addTask(t, fx.p, 2, "alice")
	ps := pendingState{Kind: "approval", ToolCalls: []toolCall{tc("c1", "bash", `{"command":"make deploy"}`)}, Park: newPark(), Since: asked + 1000}
	raw, _ := json.Marshal(ps)
	if err := fx.ag.db.Tx(func(t *DB) error {
		return t.setStatus(a.ID, statusWaiting, 0, "approve the pending tool call(s)", string(raw))
	}); err != nil {
		t.Fatal(err)
	}
	items := needsOf(t, fx, asAlice, "/needs")
	it := needFor(items, q.ID)
	if it == nil || it["reason"] != "question" || it["text"] != "Deploy the fix to staging now?" || int64(it["since"].(float64)) != asked {
		t.Fatalf("the question's item: %v", it)
	}
	it = needFor(items, a.ID)
	if it == nil || it["text"] != "Wants to run bash — approve or deny." || int64(it["since"].(float64)) != asked+1000 {
		t.Fatalf("the approval's item: %v", it)
	}
	// a park stored before since existed: its age is the run's last write
	if _, err := fx.ag.db.q.Exec(`UPDATE runs SET pending=? WHERE id=?`, strings.Replace(string(raw), fmt.Sprintf(`,"since":%d`, asked+1000), "", 1), a.ID); err != nil {
		t.Fatal(err)
	}
	r := mustRun(t, fx.ag, a.ID)
	if it := needFor(needsOf(t, fx, asAlice, "/needs"), a.ID); it == nil || int64(it["since"].(float64)) != r.Updated*1000 {
		t.Fatalf("an older park's since: %v (updated %d)", it, r.Updated)
	}
}

// GET /projects/{pid}/needs lists the coordinator's own ask_user — the
// person's coordinator, project n 0, its question as text — beside the
// tasks' needs; another participant sees the tasks', never it.
func TestProjectNeedsHasCoordinatorAsk(t *testing.T) {
	fx := newCoordFix(t)
	coord := fx.coordinator(t, asAlice, fx.p.ID)
	fakeOf(fx.ag).on(forRun(coord.ID, lastUser("plan the work")), callTools(tc("q1", "ask_user", `{"question":"Which repo first: web or api?"}`))).once()
	send(t, fx.ag, coord.ID, "plan the work")
	waitFor(t, "the coordinator's question", func() bool { return statusOf(fx.ag.db, coord.ID) == statusWaiting })
	_, task := fx.addTask(t, fx.p, 1, "alice")
	parkQuestion(t, fx.ag.db, task.ID, time.Now().UnixMilli())
	path := fmt.Sprintf("/projects/%d/needs", fx.p.ID)
	items := needsOf(t, fx, asAlice, path)
	it := needFor(items, coord.ID)
	if it == nil || it["reason"] != "question" || it["text"] != "Which repo first: web or api?" || it["since"] == nil {
		t.Fatalf("the coordinator's ask: %v (all %v)", it, items)
	}
	if pj, _ := it["project"].(map[string]any); pj == nil || fmt.Sprint(pj["n"]) != "0" || fmt.Sprint(pj["id"]) != fmt.Sprint(fx.p.ID) {
		t.Fatalf("its project: %v", it["project"])
	}
	if since := int64(it["since"].(float64)); time.Since(time.UnixMilli(since)) > time.Minute {
		t.Fatalf("its since: %d", since)
	}
	if needFor(items, task.ID) == nil {
		t.Fatalf("the task's question isn't listed: %v", items)
	}
	// bob, a participant: the task's question, never alice's coordinator
	if w := callAs(t, fx.mux, asAlice, "POST", fmt.Sprintf("/projects/%d/members", fx.p.ID), map[string]any{"user": "bob", "role": "participant"}); w.Code/100 != 2 {
		t.Fatalf("add bob: %d %s", w.Code, w.Body)
	}
	if b := needsOf(t, fx, asBob, path); needFor(b, coord.ID) != nil || needFor(b, task.ID) == nil {
		t.Fatalf("bob's needs: %v", b)
	}
}

// The feed's event when a task starts waiting says what it asks — never
// run.result, a coding agent's earlier answer.
func TestFeedShowsQuestionText(t *testing.T) {
	fx := newCoordFix(t)
	_, task := fx.addTask(t, fx.p, 1, "alice")
	parkQuestion(t, fx.ag.db, task.ID, time.Now().UnixMilli())
	var text string
	for _, ev := range fx.ag.db.projectEvents(fx.p.ID, 0, 100) {
		var b struct {
			Text string `json:"text"`
		}
		_ = json.Unmarshal(ev.Body, &b)
		if ev.Kind == pevTaskState && ev.N == 1 {
			text = b.Text
		}
	}
	if text != "waits for a person to answer: Deploy the fix to staging now?" {
		t.Fatalf("the feed's event: %q", text)
	}
}
