// ws_done.go — "Workspace agent finished" reaching the person's phone
// (xbin D241; docs/workspace-agent.md §When the app is not open).
//
// One push per finished request, none per turn: a workspace session's
// top-level turn that ends answered or finished is looked at again after a
// short grace, and the person is told only when the conversation is idle
// (it doesn't wait on them), none of its tile agents is starting or
// running, none of its requests or proposals waits, and the person wrote
// to it since the last time they were told about it. The push goes through
// POST /api/xbin/notify like every need of this agent's (kind "done", the
// conversation as `#c=<run>`); xbind writes its words — a workspace agent's
// title and body never reach a screen.
package main

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// wsDoneTold: run → the person's last message id they were told "finished"
// after (in memory: after a restart, the next finished request tells again).
var wsDoneTold = struct {
	sync.Mutex
	last map[int64]int64
}{last: map[int64]int64{}}

// wsDoneAt: a turn's end that can be a "finished" moment — the workspace
// agent's own top-level run, answered or finished (the actor asks; the
// origin is named here, in the profile's own file).
func wsDoneAt(run *Run, why string) bool {
	return wsMode() && run.ParentID == 0 && run.Origin == originWorkspace && (why == endAnswered || why == endFinished)
}

// wsDoneMoment: a workspace session's turn ended; look again after the
// grace (the turn may start the next one at once — a wake for a tile
// agent's news).
func (ag *Agent) wsDoneMoment(runID int64) {
	if ag == nil || ag.needs == nil || !wsMode() {
		return
	}
	grace := ag.needs.grace
	if grace <= 0 {
		go ag.wsDoneCheck(runID)
		return
	}
	time.AfterFunc(grace, func() { ag.wsDoneCheck(runID) })
}

// wsDoneCheck sends "done" when the conversation is finished for its
// person, once per message of theirs.
func (ag *Agent) wsDoneCheck(runID int64) {
	run, err := ag.db.getRun(runID)
	if err != nil || run.ParentID != 0 || run.Origin != originWorkspace {
		return
	}
	if run.Status != statusIdle && run.Status != statusDone {
		return // working again, or waiting on its person (a question pushes itself)
	}
	if ag.db.wsOne(`SELECT count(*) FROM ws_agents WHERE run_id=? AND status IN ('starting','running')`, runID) > 0 ||
		ag.db.wsOne(`SELECT count(*) FROM ws_requests WHERE run_id=? AND state IN ('pending','proposed')`, runID) > 0 {
		return // work of this request is still open: its news wakes the turn that finishes it
	}
	last := ag.db.wsOne(`SELECT max(id) FROM messages WHERE run_id=? AND role='user' AND `+metaStr("meta", "origin")+` != ?`, runID, originWorkspace)
	if last == 0 {
		return
	}
	wsDoneTold.Lock()
	if wsDoneTold.last[runID] >= last {
		wsDoneTold.Unlock()
		return
	}
	wsDoneTold.last[runID] = last
	wsDoneTold.Unlock()
	acl, err := ag.db.loadACL(runID)
	if err != nil || !person(acl.owner) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	n := xbin.UserNotification{User: acl.owner, Title: "Workspace agent finished", Body: "The report is ready.",
		Link: fmt.Sprintf("#c=%d", runID), Kind: "done", CollapseID: fmt.Sprintf("needs:%d", runID)}
	if err := ag.needs.send(ctx, n); err != nil {
		log.Printf("ws done push: run %d to %s: %v", runID, acl.owner, err)
	}
}
