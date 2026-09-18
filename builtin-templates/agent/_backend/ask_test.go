package main

import "testing"

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
