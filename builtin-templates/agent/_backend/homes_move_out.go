// homes_move_out.go — the global instance reads a moving conversation out
// and lets it go (homes_move.go's steps 2 and 3): GET /moves/{id}/export,
// POST /moves/{id}/done. A move carries what a copy carries (homes_bundle.go)
// and what is the conversation's alone, which a copy leaves: its notes
// (memory), its owner's schedules that report into it (retargeted to the new
// id), its owner's pin and archive. What doesn't travel — subagents' own
// transcripts, its sandboxes, its granted capabilities — is named in the
// export (`behind`), and the partition's note on the moved conversation
// says so.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// moveExport is GET /moves/{id}/export's answer: the conversation's bundle
// and what only a move carries.
type moveExport struct {
	convBundle
	Ticket     string            `json:"ticket"`               // POST /moves/{id}/done names it
	Memory     map[string]string `json:"memory,omitempty"`     // its notes (memory_set)
	Schedules  []*Schedule       `json:"schedules,omitempty"`  // its owner's schedules reporting into it
	PinnedAt   int64             `json:"pinnedAt,omitempty"`   // its owner's pin
	ArchivedAt int64             `json:"archivedAt,omitempty"` // and archive
	Behind     []string          `json:"behind,omitempty"`     // what stays behind, in words
}

// moveBusy: something in root's tree is under way — a turn or a wait
// (active: running, sleeping, waiting for an answer or an approval, for its
// subagents, blocked), input not taken yet, a command still running in a
// sandbox. A move waits for it to rest: its export and its done answer
// "later".
func (d *DB) moveBusy(root int64) bool {
	for _, s := range scanStrings(d.q.Query(`SELECT status FROM runs WHERE id=?1 OR root_id=?1`, root)) {
		if active(s) {
			return true
		}
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM inbox WHERE delivered_at=0 AND run_id IN (SELECT id FROM runs WHERE id=?1 OR root_id=?1)`, root).Scan(&n)
	if n > 0 {
		return true
	}
	_ = d.q.QueryRow(`SELECT count(*) FROM sandbox_jobs WHERE root_id=? AND state IN ('starting','running')`, root).Scan(&n)
	return n > 0
}

// moveMark is how conversation root looks — its runs, messages, input,
// files, notes and the schedules reporting into it. The export notes it;
// done lets the conversation go only if it still looks so.
func (d *DB) moveMark(root int64) string {
	const tree = `(SELECT id FROM runs WHERE id=?1 OR root_id=?1)`
	var mark string
	_ = d.q.QueryRow(`SELECT
	  (SELECT count(*) || '.' || COALESCE(max(updated), 0) FROM runs WHERE id IN `+tree+`) || '/' ||
	  (SELECT count(*) || '.' || COALESCE(max(id), 0) FROM messages WHERE run_id IN `+tree+`) || '/' ||
	  (SELECT COALESCE(max(id), 0) FROM inbox WHERE run_id IN `+tree+`) || '/' ||
	  (SELECT count(*) || '.' || COALESCE(max(updated), 0) FROM repl_files WHERE run_id IN `+tree+`) || '/' ||
	  (SELECT count(*) || '.' || COALESCE(sum(length(value)), 0) FROM memory WHERE run_id=?1) || '/' ||
	  (SELECT count(*) || '.' || COALESCE(max(id), 0) || '.' || COALESCE(sum(enabled), 0) FROM schedules WHERE target_run=?1)`, root).Scan(&mark)
	return mark
}

// movingSchedules are the schedules that move with root: its owner's, that
// report into it (mode conversation) — what the agent scheduled there for
// them. Anyone else's (a legacy team one) stays, and fires a new run each
// time once its conversation is gone, as ever.
func (d *DB) movingSchedules(root int64, owner string) []*Schedule {
	rows, err := d.q.Query(`SELECT `+scheduleCols+` FROM schedules WHERE target_run=? AND mode=? AND owner=? AND watcher=0 ORDER BY id`,
		root, modeConversation, owner)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*Schedule
	for rows.Next() {
		if s, err := scanSchedule(rows.Scan); err == nil {
			out = append(out, s)
		}
	}
	return out
}

// leftBehind names what a move of root doesn't carry.
func (d *DB) leftBehind(root int64) []string {
	var out []string
	var n int
	switch _ = d.q.QueryRow(`SELECT count(*) FROM runs WHERE root_id=? AND id<>?`, root, root).Scan(&n); {
	case n == 1:
		out = append(out, "its subagent's own transcript (what it found is in the conversation)")
	case n > 1:
		out = append(out, fmt.Sprintf("its %d subagents' own transcripts (what they found is in the conversation)", n))
	}
	if cfg, err := d.runConfig(root); err == nil && (cfg.Sandbox != nil || len(cfg.Attached) > 0) {
		out = append(out, "its sandboxes (they are the shared space's)")
	}
	if _ = d.q.QueryRow(`SELECT count(*) FROM run_grants WHERE root_id=?`, root).Scan(&n); n > 0 {
		out = append(out, "the capabilities granted to it")
	}
	return out
}

// handleMoveExport: GET /moves/{id}/export — the moving conversation
// (moveExport) with its session files (up to maxBundleFiles; the rest named
// in `left`, read one by one with GET /runs/{id}/raw), and a ticket for its
// done. 409 while anything in it is under way: it is read once it rests.
func handleMoveExport(w http.ResponseWriter, r *http.Request) {
	m, ok := ownMove(w, r)
	if !ok || !keyed(w, m, r.URL.Query().Get("key")) {
		return
	}
	if m.State != "asked" {
		xbin.WriteError(w, http.StatusNotFound, "no such move: it moved already")
		return
	}
	if _, err := agent.db.getRun(m.Root); err != nil { // it left another way (hosted elsewhere, say): nothing to move
		_, _ = agent.db.q.Exec(`DELETE FROM conv_moves WHERE root=? AND state='asked'`, m.Root)
		xbin.WriteError(w, http.StatusNotFound, "no such move: the conversation left the shared space another way")
		return
	}
	if agent.db.moveBusy(m.Root) {
		xbin.WriteError(w, http.StatusConflict, "busy: it is working — it moves once it rests")
		return
	}
	mark := agent.db.moveMark(m.Root) // before it is read: a change after this one makes done ask for it again
	b, err := agent.exportConv(r.Context(), m.Root, true)
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	x := moveExport{convBundle: *b, Ticket: newHandoffID(), Schedules: agent.db.movingSchedules(m.Root, m.Owner), Behind: agent.db.leftBehind(m.Root)}
	if mem, err := agent.db.memory(m.Root); err == nil && len(mem) > 0 {
		x.Memory = mem
	}
	if s, ok := agent.db.userStates(m.Owner, []int64{m.Root})[m.Root]; ok {
		x.PinnedAt, x.ArchivedAt = s.PinnedAt, s.ArchivedAt
	}
	out, err := bundleJSON(x, true)
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	if _, err := agent.db.q.Exec(`UPDATE conv_moves SET ticket=?, mark=? WHERE root=? AND state='asked'`, x.Ticket, mark, m.Root); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// handleMoveDone: POST /moves/{id}/done {to, ticket} — the owner's partition
// holds the conversation now (as run `to`, read with the export that handed
// out ticket): it goes from here, and its tombstone says where. Answers:
//
//   - 200 {state: "moved", to}: gone from here (again with the same `to`:
//     the same answer);
//   - 412: it changed since that export, or is at work again — read it
//     again (the partition drops its copy and makes it anew);
//   - 409: the move was given up (the conversation stays here), or the
//     conversation left another way — the partition drops its copy;
//   - 200 {state: "gone"}: no move and no conversation of the caller's by
//     that id (a tombstone past its 30 days: the partition's copy is the
//     only one) — and whatever the id, to anyone else.
func handleMoveDone(w http.ResponseWriter, r *http.Request) {
	var body struct {
		To          int64
		Ticket, Key string
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	if body.To < partitionIDBase {
		xbin.WriteError(w, http.StatusBadRequest, "to: the conversation's id in your own space (from 2^40)")
		return
	}
	id, me := pathID(r), callerOf(r).user
	m, ok := agent.db.moveOf(id)
	if !ok || m.Owner != me {
		if run, err := agent.db.getRun(id); !ok && err == nil && run.Owner == me {
			xbin.WriteError(w, http.StatusConflict, "no such move: it was given up, and the conversation is still in the shared space")
			return
		}
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"run": id, "state": "gone"}) // the same for every id not the caller's
		return
	}
	if !keyed(w, m, body.Key) {
		return
	}
	switch m.State {
	case "moved", "leaving":
		if m.To != body.To {
			xbin.WriteError(w, http.StatusConflict, "it moved to "+strconv.FormatInt(m.To, 10)+" already")
			return
		}
		if m.State == "moved" {
			xbin.WriteJSON(w, http.StatusOK, m)
			return
		}
	default:
		_, err := agent.db.getRun(id)
		switch {
		case err != nil: // not here any more, and not by its move (hosted elsewhere, say): the partition's copy isn't needed
			_, _ = agent.db.q.Exec(`DELETE FROM conv_moves WHERE root=? AND state='asked'`, id)
			xbin.WriteError(w, http.StatusConflict, "no such move: the conversation left the shared space another way")
			return
		case body.Ticket == "" || body.Ticket != m.Ticket:
			xbin.WriteError(w, http.StatusPreconditionFailed, "read it again: that isn't its latest export")
			return
		case agent.db.moveBusy(id) || agent.db.moveMark(id) != m.Mark:
			xbin.WriteError(w, http.StatusPreconditionFailed, "read it again: it changed since it was read")
			return
		}
		res, err := agent.db.q.Exec(`UPDATE conv_moves SET state='leaving', to_id=? WHERE root=? AND state='asked'`, body.To, id)
		if err != nil || rowsAffected(res) != 1 {
			xbin.WriteError(w, http.StatusConflict, "no such move: it was given up meanwhile")
			return
		}
	}
	if err := agent.moveOut(id, body.To, m.Owner); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	if _, err := agent.db.q.Exec(`UPDATE conv_moves SET state='moved', done=? WHERE root=? AND state='leaving'`, now(), id); err != nil {
		xbin.WriteError(w, http.StatusInternalServerError, err.Error())
		return
	}
	m.State, m.To = "moved", body.To
	xbin.WriteJSON(w, http.StatusOK, m)
}

// moveOut deletes a moved conversation here as DELETE /runs/{id} does, and
// the schedules that went with it; its event tells its page where it went.
// Gone already (a crash after this, before its tombstone was written): only
// what is left of it goes.
func (ag *Agent) moveOut(root, to int64, owner string) error {
	for _, s := range ag.db.movingSchedules(root, owner) {
		ag.unregisterScheduleCron(s.ID)
		if err := ag.db.deleteSchedule(s.ID); err != nil {
			return err
		}
	}
	if _, err := ag.db.getRun(root); err != nil {
		return nil
	}
	acl, _ := ag.db.loadACL(root)
	_ = ag.db.Tx(func(t *DB) error {
		ag.cancelRuns(t, root, true, "moved to its owner's own space")
		return nil
	})
	if err := ag.deleteRunTree(root); err != nil {
		return err
	}
	ag.acl.flush(root)
	for _, q := range []string{`DELETE FROM run_members WHERE run_id=?`, `DELETE FROM share_links WHERE run_id=?`,
		`DELETE FROM run_user_state WHERE run_id=?`, `DELETE FROM run_grants WHERE root_id=?`} {
		_, _ = ag.db.q.Exec(q, root)
	}
	if ag.eng != nil {
		// to everyone who could see it — private, with nobody in it: its
		// owner, and the owner token's page, which doesn't follow it
		ag.eng.hub.publish(&Event{Type: evRun, Run: root, Root: root, Data: map[string]any{"id": root, "deleted": true, "movedTo": to}, acl: acl})
	}
	return nil
}
