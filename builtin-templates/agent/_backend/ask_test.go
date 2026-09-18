package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
)

func TestRunKind(t *testing.T) {
	db := newTestDB(t)
	id, err := db.createRun("q", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	db.setRunKind(id, "quick")
	r, err := db.getRun(id)
	if err != nil || r.Kind != "quick" {
		t.Fatalf("kind round-trip: %+v err=%v", r, err)
	}
	runs, err := db.listRuns()
	if err != nil || len(runs) != 1 || runs[0].Kind != "quick" {
		t.Fatalf("list kind: %+v err=%v", runs, err)
	}
}

// A quick ask answered in plain text (no finish) leaves result empty, so its
// home card would be blank. GET /runs decorates quick asks with their last
// assistant message instead — and only quick asks, since the list is polled.
func TestListRunsCarriesTheLastAnswer(t *testing.T) {
	db := newTestDB(t)
	prev := agent
	agent = &Agent{db: db}
	t.Cleanup(func() { agent = prev })

	quick, _ := db.createRun("q", "", 0)
	db.setRunKind(quick, "quick")
	task, _ := db.createRun("t", "", 0)
	for _, m := range []*Message{
		{RunID: quick, Role: "assistant", Content: "first draft"},
		{RunID: quick, Role: "assistant", Content: "the answer"},
		{RunID: quick, Role: "assistant", ToolCalls: `[]`}, // a tool turn has no text
		{RunID: task, Role: "assistant", Content: "task text"},
	} {
		if _, err := db.addMessage(m); err != nil {
			t.Fatal(err)
		}
	}
	w := httptest.NewRecorder()
	handleListRuns(w, httptest.NewRequest("GET", "/runs", nil))
	var runs []*Run
	if err := json.Unmarshal(w.Body.Bytes(), &runs); err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		switch r.ID {
		case quick:
			if r.Last != "the answer" {
				t.Fatalf("quick ask last = %q, want the latest text answer", r.Last)
			}
		case task:
			if r.Last != "" {
				t.Fatalf("a task should not carry a snippet, got %q", r.Last)
			}
		}
	}
}
