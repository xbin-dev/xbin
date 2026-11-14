// project_needs.go — what in a project really waits for a person (#56),
// beside the board's Needs you column, which also holds tasks whose turn
// ended with their answer ready: ProjectView.Counts' asks (a question, an
// approval, a sign-in, a cleanup refused), failed, and the caller's own
// coordinator's question (coord) — the conversation list's red count
// (model/project-pins.js); GET /projects/{pid}/needs lists them.
package main

// countNone is askCount's "neither" (dropped from the counts).
const countNone = ""

// askCount is the count a task in column col, state state, its run in
// status adds to beside its column: asks, failed or neither.
func askCount(col, state, status string) string {
	switch {
	case state == taskFailed:
		return countFailed
	case col != colNeedsYou:
		return countNone
	case state == taskNeedsYou && status != statusWaiting: // its turn ended: its answer is ready
		return countNone
	}
	return countAsks
}

// withCoordAsk adds counts.coord for c: 1 while their coordinator of the
// project (or a run below it) waits for them.
func (d *DB) withCoordAsk(v ProjectView, c who) ProjectView {
	if c.kind != whoUser || c.user == "" || v.Counts == nil {
		return v
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM runs x WHERE x.status='waiting_input' AND x.root_id=(SELECT id FROM runs
		WHERE session_key=? AND origin=? AND origin_id=? AND owner=? AND parent_id=0 ORDER BY id LIMIT 1)`,
		coordSessionKey(v.ID, c.user), originProject, v.ID, c.user).Scan(&n)
	v.Counts[countCoord] = min(n, 1)
	return v
}
