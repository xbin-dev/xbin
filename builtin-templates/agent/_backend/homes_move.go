// homes_move.go — un-sharing a shared conversation in a partitioned agent
// (the partitioned-tiles plan, 90 §I10; API.md "Partitioned instances" → "Shared
// conversations"): the global instance's side. The shared space keeps shared
// conversations only, so one that stops being shared — made private with
// nobody left in it (PATCH visibility private), or its last member removed
// or gone — moves to its owner's own partition:
//
//  1. here, in the transaction that un-shared it: a conv_moves row (state
//     "asked") and a `conv/move {run}` mail to `user:<owner>`, queued like a
//     handoff (handoff_send.go: per person, retried, given up after 7 days).
//     Its join links are revoked; nothing may write to it any more (409) —
//     it stays readable where it is;
//  2. the owner's partition (homes_move_user.go) reads it through
//     GET /moves/{id}/export — refused while a run of it is running or
//     queued, so nothing it does is left behind — and imports it hidden;
//  3. POST /moves/{id}/done {to}: here it is deleted (a `run` event with
//     `deleted` and `movedTo` tells its page where it went) and the row
//     becomes a tombstone ("moved", its new id; GET /moves/{id}) for 30 days;
//  4. the partition shows its copy.
//
// So a moved conversation is listed in one home at every moment: here until
// step 3, there from step 4 (the page follows the tombstone meanwhile). Each
// step is idempotent and resumable: a crash anywhere repeats the step that
// didn't finish. A move the partition can't make (too large) is given up
// (POST /moves/{id}/abandon), as is one whose mail is refused for good or
// that waited moveAskTTL: the conversation then stays here, private, as
// before this version.
//
// Unpartitioned, and in a person's partition, none of this exists.
package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const moveSchemaSQL = `
CREATE TABLE IF NOT EXISTS conv_moves (
  root INTEGER PRIMARY KEY, owner TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'asked',
  to_id INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, done INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS moves_in (
  from_id INTEGER PRIMARY KEY, to_id INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'asked',
  tries INTEGER NOT NULL DEFAULT 0, next_try INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL);
`

// addMoveSchema makes the moves' tables in a partitioned instance (global's
// conv_moves, a partition's moves_in). Never unpartitioned.
func (d *DB) addMoveSchema() error {
	if !partitioned() {
		return nil
	}
	_, err := d.q.Exec(moveSchemaSQL)
	return err
}

const (
	topicMove = "conv/move" // global → a person: a conversation of theirs leaves the shared space
	moveKind  = "move"      // its row in the handoffs queue
	// moveAskTTL: a move nobody took this long is given up — its mail (7
	// days in the inbox) is gone by then.
	moveAskTTL = 8 * 86400
	// moveKept: how long a moved conversation's tombstone answers where it went.
	moveKept    = 30 * 86400
	movingWords = "this conversation is moving to its owner's own space (it is no longer shared): it can't be changed here any more"
)

// moveItem is a conv/move mail's data.
type moveItem struct {
	Run int64 `json:"run"`
}

// moveIfUnshared (global, in the transaction that changed who shares root):
// a person's conversation nobody else shares any more starts moving to their
// own partition. Anything else — shared still, not a person's (the owner
// token's, an element's, an unowned one), moving already — is left.
func (d *DB) moveIfUnshared(root int64) error {
	if !globalMode() {
		return nil
	}
	var owner, vis string
	var parent int64
	if err := d.q.QueryRow(`SELECT owner, visibility, parent_id FROM runs WHERE id=?`, root).Scan(&owner, &vis, &parent); err != nil ||
		parent != 0 || vis != visPrivate || !userIDRe.MatchString(owner) {
		return nil
	}
	var members int
	_ = d.q.QueryRow(`SELECT count(*) FROM run_members WHERE run_id=?`, root).Scan(&members)
	if members > 0 {
		return nil
	}
	_, _ = d.q.Exec(`DELETE FROM conv_moves WHERE (state='asked' AND created<?) OR (state='moved' AND done<?)`, now()-moveAskTTL, now()-moveKept)
	res, err := d.q.Exec(`INSERT INTO conv_moves (root, owner, created) VALUES (?, ?, ?) ON CONFLICT(root) DO NOTHING`, root, owner, now())
	if err != nil {
		return err
	}
	if rowsAffected(res) == 0 {
		return nil // moving already
	}
	if _, err := d.q.Exec(`UPDATE share_links SET revoked=1 WHERE run_id=?`, root); err != nil {
		return err
	}
	if err := d.queueHandoff(moveKind, owner, newHandoffID(), moveItem{Run: root}, func(*handoffRow) {}); err != nil {
		return err
	}
	d.AfterCommit(kickHandoffs)
	return nil
}

// convMove is a conv_moves row.
type convMove struct {
	Root    int64  `json:"run"`
	Owner   string `json:"-"`
	State   string `json:"state"` // asked | moved
	To      int64  `json:"to,omitempty"`
	Created int64  `json:"-"`
}

// moveOf is root's move, if one stands (an "asked" one past moveAskTTL is
// given up: none).
func (d *DB) moveOf(root int64) (*convMove, bool) {
	m := &convMove{Root: root}
	if err := d.q.QueryRow(`SELECT owner, state, to_id, created FROM conv_moves WHERE root=?`, root).
		Scan(&m.Owner, &m.State, &m.To, &m.Created); err != nil {
		return nil, false
	}
	if m.State == "asked" && m.Created < now()-moveAskTTL {
		return nil, false
	}
	return m, true
}

// moving: root is on its way out (writes to it are refused).
func (d *DB) moving(root int64) bool {
	m, ok := d.moveOf(root)
	return ok && m.State == "asked"
}

// refuseWhileMoving is h at the global instance: a change to a conversation
// that is moving out — anything but a read, or marking it read — answers
// 409 (globalRoute, homes.go).
func refuseWhileMoving(pattern string, h http.HandlerFunc) http.HandlerFunc {
	method, path, _ := strings.Cut(pattern, " ")
	if method == http.MethodGet || !strings.HasPrefix(path, "/runs/{id}") || pattern == "POST /runs/{id}/read" {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if run, err := agent.db.getRun(pathID(r)); err == nil && agent.db.moving(rootOf(run)) {
			xbin.WriteError(w, http.StatusConflict, movingWords)
			return
		}
		h(w, r)
	}
}

// moveMailRefused: the conv/move mail was refused for good or never went
// (failHandoff) — the move is given up; the conversation stays here.
func (ag *Agent) moveMailRefused(q queuedHandoff) {
	if q.kind != moveKind {
		return
	}
	var m moveItem
	if json.Unmarshal([]byte(q.payload), &m) == nil && m.Run > 0 {
		_, _ = ag.db.q.Exec(`DELETE FROM conv_moves WHERE root=? AND state='asked'`, m.Run)
	}
}

// --- the routes (the global instance only) ----------------------------------------

// moveRoutes mounts the moves' routes at a partitioned agent's global
// instance: the owner's partition drives a move with them, the owner's page
// asks where a conversation went.
func moveRoutes(mux *http.ServeMux) {
	if !globalMode() {
		return
	}
	for _, rt := range []routeDef{
		{"GET /moves/{id}", needUser, handleMoveGet},
		{"GET /moves/{id}/export", needUser, handleMoveExport},
		{"POST /moves/{id}/done", needUser, handleMoveDone},
		{"POST /moves/{id}/abandon", needUser, handleMoveAbandon},
	} {
		mux.Handle(rt.pattern, agentRole(guard(rt.need, rt.h)))
	}
}

// ownMove is the route's move when the caller is its owner (404 otherwise:
// someone else's moves are nobody's business).
func ownMove(w http.ResponseWriter, r *http.Request) (*convMove, bool) {
	m, ok := agent.db.moveOf(pathID(r))
	if !ok || m.Owner != callerOf(r).user {
		xbin.WriteError(w, http.StatusNotFound, "no such move")
		return nil, false
	}
	return m, true
}

// handleMoveGet: GET /moves/{id} — where the caller's conversation went
// ({run, state, to}), or that it is on its way.
func handleMoveGet(w http.ResponseWriter, r *http.Request) {
	if m, ok := ownMove(w, r); ok {
		xbin.WriteJSON(w, http.StatusOK, m)
	}
}

// handleMoveExport: GET /moves/{id}/export — the moving conversation as a
// bundle with its session files (up to maxBundleFiles; the rest named in
// `left`, read one by one with GET /runs/{id}/raw). 409 while a run of it
// is running or queued: it is read once it rests.
func handleMoveExport(w http.ResponseWriter, r *http.Request) {
	m, ok := ownMove(w, r)
	if !ok {
		return
	}
	if m.State != "asked" {
		xbin.WriteError(w, http.StatusNotFound, "no such move: it moved already")
		return
	}
	var busy int
	_ = agent.db.q.QueryRow(`SELECT count(*) FROM runs WHERE (id=? OR root_id=?) AND status IN ('running','queued')`, m.Root, m.Root).Scan(&busy)
	if busy > 0 {
		xbin.WriteError(w, http.StatusConflict, "busy: it is working — it moves once it rests")
		return
	}
	b, err := agent.exportConv(r.Context(), m.Root, true)
	var out []byte
	if err == nil {
		out, err = bundleJSON(b, true)
	}
	if err != nil {
		writeBundleErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// handleMoveDone: POST /moves/{id}/done {to} — the owner's partition holds
// the conversation now (as run `to`): it goes from here, and its tombstone
// says where. Idempotent: done again with the same `to` answers the same. A
// move given up meanwhile (the conversation still here, no move) is 409 —
// the partition drops its copy; a conversation gone with no move on record
// (a tombstone past its 30 days) is 200: the partition's copy is the only
// one.
func handleMoveDone(w http.ResponseWriter, r *http.Request) {
	var body struct{ To int64 }
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	if body.To < partitionIDBase {
		xbin.WriteError(w, http.StatusBadRequest, "to: the conversation's id in your own space (from 2^40)")
		return
	}
	id := pathID(r)
	m, ok := agent.db.moveOf(id)
	if !ok {
		if run, err := agent.db.getRun(id); err == nil {
			if run.Owner != callerOf(r).user {
				xbin.WriteError(w, http.StatusNotFound, "no such move") // someone else's conversation is none of their business
				return
			}
			xbin.WriteError(w, http.StatusConflict, "no such move: it was given up, and the conversation is still in the shared space")
			return
		}
		xbin.WriteJSON(w, http.StatusOK, map[string]any{"run": id, "state": "gone"})
		return
	}
	if m.Owner != callerOf(r).user {
		xbin.WriteError(w, http.StatusNotFound, "no such move")
		return
	}
	switch {
	case m.State == "moved" && m.To == body.To:
	case m.State == "moved":
		xbin.WriteError(w, http.StatusConflict, "it moved to "+strconv.FormatInt(m.To, 10)+" already")
		return
	default:
		if err := agent.moveOut(id, body.To); err != nil {
			xbin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if _, err := agent.db.q.Exec(`UPDATE conv_moves SET state='moved', to_id=?, done=? WHERE root=? AND state='asked'`, body.To, now(), id); err != nil {
			xbin.WriteError(w, http.StatusInternalServerError, err.Error())
			return
		}
		m.State, m.To = "moved", body.To
	}
	xbin.WriteJSON(w, http.StatusOK, m)
}

// moveOut deletes a moved conversation here as DELETE /runs/{id} does, its
// event saying where it went. Gone already (a crash after this, before its
// tombstone was written): nothing to do.
func (ag *Agent) moveOut(root, to int64) error {
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
	for _, q := range []string{`DELETE FROM run_members WHERE run_id=?`, `DELETE FROM share_links WHERE run_id=?`, `DELETE FROM run_user_state WHERE run_id=?`} {
		_, _ = ag.db.q.Exec(q, root)
	}
	if ag.eng != nil {
		ag.eng.hub.publish(&Event{Type: evRun, Run: root, Root: root, Data: map[string]any{"id": root, "deleted": true, "movedTo": to}, acl: acl})
	}
	return nil
}

// handleMoveAbandon: POST /moves/{id}/abandon {why} — the owner's partition
// can't take it (too large to copy): the move is given up, and the
// conversation stays here, private, writable again.
func handleMoveAbandon(w http.ResponseWriter, r *http.Request) {
	m, ok := ownMove(w, r)
	if !ok {
		return
	}
	if m.State != "asked" {
		xbin.WriteError(w, http.StatusConflict, "it moved already")
		return
	}
	var body struct{ Why string }
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	logf("the move of conversation %d to %s's own space was given up: %s", m.Root, m.Owner, clip(body.Why, 300))
	_, _ = agent.db.q.Exec(`DELETE FROM conv_moves WHERE root=? AND state='asked'`, m.Root)
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"run": m.Root, "state": "abandoned"})
}
