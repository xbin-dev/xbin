// homes_move.go — un-sharing a shared conversation in a partitioned agent
// (the partitioned-tiles plan, 90 §I10; API.md "Partitioned instances" → "Shared
// conversations"): the global instance's side. The shared space keeps shared
// conversations only, so a person's chat that stops being shared — the act
// that made it private with nobody left in it (PATCH visibility private), or
// removed its last member — moves to its owner's own partition:
//
//  1. here, in the transaction that un-shared it: a conv_moves row (state
//     "asked") and a `conv/move {run, key}` mail to `user:<owner>`, queued
//     like a handoff (handoff_send.go: per person, retried, given up after 7
//     days). The key is what the partition's backend drives the move with:
//     xbind stamps a person's page and terminals as it stamps their
//     partition's backend, and only the backend reads the mail;
//     its join links are revoked; nothing may change it any more (409) but
//     stopping or answering what it works on — it stays readable where it is;
//  2. the owner's partition (homes_move_user.go) reads it through
//     GET /moves/{id}/export — refused while anything in it is under way
//     (a turn, a wait, input not taken yet, a sandbox command), so nothing
//     it does is left behind — and imports it hidden. The export hands out a
//     ticket and notes how the conversation looked;
//  3. POST /moves/{id}/done {to, ticket}: if it still looks as it was read,
//     it is deleted here (a `run` event with `deleted` and `movedTo` tells
//     its page where it went) and the row becomes a tombstone ("moved", its
//     new id; GET /moves/{id}) for 30 days; if it changed, the partition
//     reads it again (412);
//  4. the partition shows its copy.
//
// So a moved conversation is never listed in two homes: here until step 3,
// there from step 4 (the page follows the tombstone meanwhile). Each step is
// idempotent and resumable: a crash anywhere repeats the step that didn't
// finish. A move the partition can't make (too large, not importable there)
// is given up (POST /moves/{id}/abandon), as is one whose mail is refused
// for good or that waited moveAskTTL: the conversation then stays here,
// private, as before this version.
//
// Only a person's chat moves: an automation's thread (a channel's, a
// schedule's, a trigger's), a draft never sent, the owner token's or an
// element's conversation stays. Unpartitioned, and in a person's partition,
// none of this exists.
package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const moveSchemaSQL = `
CREATE TABLE IF NOT EXISTS conv_moves (
  root INTEGER PRIMARY KEY, owner TEXT NOT NULL, state TEXT NOT NULL DEFAULT 'asked',
  to_id INTEGER NOT NULL DEFAULT 0, created INTEGER NOT NULL, done INTEGER NOT NULL DEFAULT 0,
  ticket TEXT NOT NULL DEFAULT '', mark TEXT NOT NULL DEFAULT '', key TEXT NOT NULL DEFAULT '');
CREATE TABLE IF NOT EXISTS moves_in (
  from_id INTEGER PRIMARY KEY, to_id INTEGER NOT NULL DEFAULT 0, state TEXT NOT NULL DEFAULT 'asked',
  tries INTEGER NOT NULL DEFAULT 0, next_try INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, ticket TEXT NOT NULL DEFAULT '', extra TEXT NOT NULL DEFAULT '', key TEXT NOT NULL DEFAULT '');
`

// addMoveSchema makes the moves' tables in a partitioned instance (global's
// conv_moves, a partition's moves_in). Never unpartitioned.
func (d *DB) addMoveSchema() error {
	if !partitioned() {
		return nil
	}
	if _, err := d.q.Exec(moveSchemaSQL); err != nil {
		return err
	}
	for _, q := range []string{ // tables this version's first builds made
		`ALTER TABLE conv_moves ADD COLUMN ticket TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE conv_moves ADD COLUMN mark TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE conv_moves ADD COLUMN key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE moves_in ADD COLUMN ticket TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE moves_in ADD COLUMN key TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE moves_in ADD COLUMN extra TEXT NOT NULL DEFAULT ''`,
	} {
		_, _ = d.q.Exec(q) // fails harmlessly when the column is there
	}
	return nil
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

// moveItem is a conv/move mail's data: the conversation, and the key its
// owner's partition drives the move with (export, done, abandon) — which the
// partition's backend takes from its mail, and its page doesn't have.
type moveItem struct {
	Run int64  `json:"run"`
	Key string `json:"key,omitempty"`
}

// sharedAtGlobal: root is shared at a partitioned agent's global instance —
// with the team, or with someone in it. What an act's callers read before
// they change who shares root (moveIfUnshared's was). False anywhere else.
func (d *DB) sharedAtGlobal(root int64) bool {
	if !globalMode() {
		return false
	}
	var vis string
	var members int
	_ = d.q.QueryRow(`SELECT visibility, (SELECT count(*) FROM run_members WHERE run_id=?1) FROM runs WHERE id=?1`, root).Scan(&vis, &members)
	return vis == visTeam || members > 0
}

// isChat: a run of this origin is a conversation (conversations.go
// chatOrigins), not an automation's thread or a draft.
func isChat(origin string) bool {
	return origin == "" || origin == "chat" || origin == "api"
}

// moveIfUnshared (global, in the transaction that changed who shares root;
// was: sharedAtGlobal before the change): a person's chat that this act
// left shared with nobody starts moving to their own partition. Anything
// else — shared still or before, an automation's thread, one nobody ever
// said anything in, not a person's (the owner token's, an element's, an
// unowned one), moving already — is left.
func (d *DB) moveIfUnshared(root int64, was bool) error {
	if !was || !globalMode() || d.sharedAtGlobal(root) {
		return nil
	}
	var owner, origin string
	var parent int64
	var said bool
	if err := d.q.QueryRow(`SELECT owner, origin, parent_id, EXISTS(SELECT 1 FROM messages WHERE run_id=?1 AND role='user')
		OR EXISTS(SELECT 1 FROM inbox WHERE run_id=?1 AND kind=?2) FROM runs WHERE id=?1`, root, inboxUser).Scan(&owner, &origin, &parent, &said); err != nil ||
		parent != 0 || !isChat(origin) || !said || !userIDRe.MatchString(owner) {
		return nil
	}
	_, _ = d.q.Exec(`DELETE FROM conv_moves WHERE (state='asked' AND created<?) OR (state='moved' AND done<?) OR (state='leaving' AND created<?)`,
		now()-moveAskTTL, now()-moveKept, now()-moveKept)
	key := newHandoffID()
	res, err := d.q.Exec(`INSERT INTO conv_moves (root, owner, created, key) VALUES (?, ?, ?, ?) ON CONFLICT(root) DO NOTHING`, root, owner, now(), key)
	if err != nil {
		return err
	}
	if rowsAffected(res) == 0 {
		return nil // moving already
	}
	if _, err := d.q.Exec(`UPDATE share_links SET revoked=1 WHERE run_id=?`, root); err != nil {
		return err
	}
	if err := d.queueHandoff(moveKind, owner, newHandoffID(), moveItem{Run: root, Key: key}, func(*handoffRow) {}); err != nil {
		return err
	}
	d.AfterCommit(kickHandoffs)
	return nil
}

// convMove is a conv_moves row.
type convMove struct {
	Root    int64  `json:"run"`
	Owner   string `json:"-"`
	State   string `json:"state"` // asked | leaving (being deleted here) | moved
	To      int64  `json:"to,omitempty"`
	Created int64  `json:"-"`
	Ticket  string `json:"-"` // the latest export's
	Key     string `json:"-"` // the mailed key (moveItem)
	Mark    string `json:"-"` // how the conversation looked at it (moveMark)
}

// moveOf is root's move, if one stands (an "asked" one past moveAskTTL is
// given up: none).
func (d *DB) moveOf(root int64) (*convMove, bool) {
	m := &convMove{Root: root}
	if err := d.q.QueryRow(`SELECT owner, state, to_id, created, ticket, mark, key FROM conv_moves WHERE root=?`, root).
		Scan(&m.Owner, &m.State, &m.To, &m.Created, &m.Ticket, &m.Mark, &m.Key); err != nil {
		return nil, false
	}
	if m.State == "asked" && m.Created < now()-moveAskTTL {
		return nil, false
	}
	return m, true
}

// moving: root is on its way out (changes to it are refused). Only at a
// partitioned agent's global instance.
func (d *DB) moving(root int64) bool {
	if !globalMode() {
		return false
	}
	m, ok := d.moveOf(root)
	return ok && m.State != "moved"
}

// errConvMoving: an automation's delivery into a conversation moving out
// (deliverInboundTx) — refused, like every other change to it.
var errConvMoving = errors.New(movingWords)

// movingRun: run id belongs to a conversation moving out.
func (d *DB) movingRun(id int64) bool {
	if !globalMode() {
		return false
	}
	r, err := d.getRun(id)
	return err == nil && d.moving(rootOf(r))
}

// whileMoving are the changes a moving conversation still takes: marking it
// read, stopping what it works on, deciding an approval it waits for, taking
// back a message not delivered yet — each lets it come to rest, which its
// move waits for. An answer (POST /runs/{id}/answer) goes only to a run
// waiting for one (refuseWhileMoving).
var whileMoving = map[string]bool{
	"POST /runs/{id}/read":          true,
	"POST /runs/{id}/cancel":        true,
	"POST /runs/{id}/interrupt":     true,
	"POST /runs/{id}/approve":       true,
	"DELETE /runs/{id}/inbox/{iid}": true,
}

// refuseWhileMoving is h at the global instance: any other change to a
// conversation that is moving out answers 409 (globalRoute, homes.go).
func refuseWhileMoving(pattern string, h http.HandlerFunc) http.HandlerFunc {
	method, path, _ := strings.Cut(pattern, " ")
	if method == http.MethodGet || !strings.HasPrefix(path, "/runs/{id}") || whileMoving[pattern] {
		return h
	}
	answer := pattern == "POST /runs/{id}/answer"
	return func(w http.ResponseWriter, r *http.Request) {
		if run, err := agent.db.getRun(pathID(r)); err == nil && !(answer && run.Status == statusWaiting) && movingRefused(w, rootOf(run)) {
			return
		}
		h(w, r)
	}
}

// movingRefused answers 409 when conversation root is moving out: true, the
// caller stops. For a handler mounted outside globalRoute that changes a
// conversation or takes it elsewhere.
func movingRefused(w http.ResponseWriter, root int64) bool {
	if !agent.db.moving(root) {
		return false
	}
	xbin.WriteError(w, http.StatusConflict, movingWords)
	return true
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
// instance: the owner's partition drives a move with them (export, done,
// abandon: its calls only), the owner's page asks where a conversation went.
func moveRoutes(mux *http.ServeMux) {
	if !globalMode() {
		return
	}
	for _, rt := range []routeDef{
		{"GET /moves/{id}", needUser, handleMoveGet},
		{"GET /moves/{id}/export", needUser, fromPartition(handleMoveExport)},
		{"POST /moves/{id}/done", needUser, fromPartition(handleMoveDone)},
		{"POST /moves/{id}/abandon", needUser, fromPartition(handleMoveAbandon)},
	} {
		mux.Handle(rt.pattern, agentRole(guard(rt.need, rt.h)))
	}
}

// fromPartition is h for a person's own partition's calls only — their page
// or terminals get 403 whatever the id, so neither can take a conversation
// out of the shared space, or keep it there, by hand.
func fromPartition(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !personFromPartition(r) {
			xbin.WriteError(w, http.StatusForbidden, "a move is made by the agent in your own space, not by hand")
			return
		}
		h(w, r)
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

// keyed: the call carries the move's mailed key — it is the owner's
// partition's backend, which took the conv/move mail (xbind stamps its
// page and terminals the same way; they don't have the key). 403 otherwise.
func keyed(w http.ResponseWriter, m *convMove, key string) bool {
	if m.Key != "" && key != m.Key {
		xbin.WriteError(w, http.StatusForbidden, "a move is made by the agent in your own space, not by hand")
		return false
	}
	return true
}

// handleMoveGet: GET /moves/{id} — where the caller's conversation went
// ({run, state, to}), or that it is on its way.
func handleMoveGet(w http.ResponseWriter, r *http.Request) {
	if m, ok := ownMove(w, r); ok {
		xbin.WriteJSON(w, http.StatusOK, m)
	}
}

// handleMoveAbandon: POST /moves/{id}/abandon {why} — the owner's partition
// can't take it (too large to copy, not importable there): the move is given
// up, and the conversation stays here, private, writable again.
func handleMoveAbandon(w http.ResponseWriter, r *http.Request) {
	m, ok := ownMove(w, r)
	if !ok {
		return
	}
	var body struct{ Why, Key string }
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&body)
	if !keyed(w, m, body.Key) {
		return
	}
	if m.State != "asked" {
		xbin.WriteError(w, http.StatusConflict, "it moved already")
		return
	}
	logf("the move of conversation %d to %s's own space was given up: %s", m.Root, m.Owner, clip(body.Why, 300))
	_, _ = agent.db.q.Exec(`DELETE FROM conv_moves WHERE root=? AND state='asked'`, m.Root)
	xbin.WriteJSON(w, http.StatusOK, map[string]any{"run": m.Root, "state": "abandoned"})
}
