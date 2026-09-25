// inbox.go — every input to a run is a row first.
//
// A human message, an approval, an interrupt, a cancel, a compaction request,
// a watcher's "check now", a parent's message to its subagent: each is
// INSERTed here and the run poked. The engine consumes a row exactly once, in
// a transaction that also writes what the row caused (the user message, the
// status change), so a crash either did both or neither. A message sent while
// a turn runs simply waits in the inbox until the next step boundary — that is
// the steer — and until then it can be taken back (DELETE).
package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

const (
	inboxUser      = "user"      // a message for the model (human, parent, learn, schedule)
	inboxApprove   = "approve"   // a verdict on a parked approval
	inboxWake      = "wake"      // continue now (the Resume button)
	inboxInterrupt = "interrupt" // stop the turn, keep the run
	inboxCancel    = "cancel"    // stop the run (and, via fan-out, its subtree)
	inboxCompact   = "compact"   // compact the context now
	inboxWatch     = "watch"     // a watcher round's "check now"
)

type inboxBody struct {
	Text   string   `json:"text,omitempty"`
	Files  []string `json:"files,omitempty"`
	Source string   `json:"source,omitempty"` // human | parent | learn | schedule | watch
	From   int64    `json:"from,omitempty"`   // the parent run, for source=parent
	// Sender is the person who wrote it (source=human, D83); OriginID and
	// Label name the automation that delivered it (source=schedule/…).
	Sender   string `json:"sender,omitempty"`
	OriginID int64  `json:"originId,omitempty"`
	Label    string `json:"label,omitempty"`
	// Addr is where a reply to this message goes (source=channel: the
	// adapter's channelAddr, JSON; D86).
	Addr    string `json:"addr,omitempty"`
	Approve bool   `json:"approve,omitempty"`
	Reason  string `json:"reason,omitempty"`
	// Watcher rounds: the transcript mark before the round, whether it is the
	// open round, and whether state_changed was called in it.
	Mark    int  `json:"mark,omitempty"`
	Open    bool `json:"open,omitempty"`
	Changed bool `json:"changed,omitempty"`
}

type InboxRow struct {
	ID          int64     `json:"id"`
	RunID       int64     `json:"runId"`
	Kind        string    `json:"kind"`
	Body        inboxBody `json:"body"`
	ClientID    string    `json:"clientId,omitempty"`
	Created     int64     `json:"created"`
	DeliveredAt int64     `json:"deliveredAt"`
	MsgID       int64     `json:"msgId"`
}

// enqueue inserts a row. A repeated client id (a retried POST) returns the
// row that is already there, with dup=true.
func (d *DB) enqueue(runID int64, kind string, body inboxBody, clientID string) (id int64, dup bool, err error) {
	raw, _ := json.Marshal(body)
	err = d.q.QueryRow(`INSERT INTO inbox (run_id, kind, body, client_id, created) VALUES (?, ?, ?, ?, ?)
		ON CONFLICT DO NOTHING RETURNING id`, runID, kind, string(raw), clientID, now()).Scan(&id)
	if err == sql.ErrNoRows && clientID != "" {
		err = d.q.QueryRow(`SELECT id FROM inbox WHERE run_id=? AND client_id=?`, runID, clientID).Scan(&id)
		return id, true, err
	}
	return id, false, err
}

const inboxCols = `id, run_id, kind, body, client_id, created, delivered_at, msg_id`

func scanInbox(scan func(dest ...any) error) (*InboxRow, error) {
	r := &InboxRow{}
	var body string
	if err := scan(&r.ID, &r.RunID, &r.Kind, &body, &r.ClientID, &r.Created, &r.DeliveredAt, &r.MsgID); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(body), &r.Body)
	return r, nil
}

func (d *DB) inboxRows(where string, args ...any) []*InboxRow {
	rows, err := d.q.Query(`SELECT `+inboxCols+` FROM inbox `+where, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []*InboxRow
	for rows.Next() {
		if r, err := scanInbox(rows.Scan); err == nil {
			out = append(out, r)
		}
	}
	return out
}

// undelivered is a run's pending input, oldest first.
func (d *DB) undelivered(runID int64) []*InboxRow {
	return d.inboxRows(`WHERE run_id=? AND delivered_at=0 ORDER BY id`, runID)
}

// consume marks a row delivered; false if something else already did.
func (d *DB) consume(id, msgID int64) bool {
	res, err := d.q.Exec(`UPDATE inbox SET delivered_at=?, msg_id=? WHERE id=? AND delivered_at=0`, now(), msgID, id)
	return err == nil && rowsAffected(res) == 1
}

// setInboxBody rewrites a row's body (a watcher round's bookkeeping).
func (d *DB) setInboxBody(id int64, b inboxBody) {
	raw, _ := json.Marshal(b)
	_, _ = d.q.Exec(`UPDATE inbox SET body=? WHERE id=?`, string(raw), id)
}

// removeQueued takes back a message that has not been delivered yet.
func (d *DB) removeQueued(runID, id int64) (removed, exists bool) {
	res, err := d.q.Exec(`DELETE FROM inbox WHERE id=? AND run_id=? AND kind='user' AND delivered_at=0`, id, runID)
	if err == nil && rowsAffected(res) == 1 {
		return true, true
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM inbox WHERE id=? AND run_id=?`, id, runID).Scan(&n)
	return false, n > 0
}

// queuedView is what the tile shows above the composer: messages sent but
// not yet delivered to the model.
func (d *DB) queuedView(runID int64) []map[string]any {
	out := []map[string]any{}
	for _, r := range d.inboxRows(`WHERE run_id=? AND delivered_at=0 AND kind='user' ORDER BY id`, runID) {
		out = append(out, map[string]any{"id": r.ID, "text": r.Body.Text, "files": r.Body.Files,
			"source": r.Body.Source, "sender": r.Body.Sender, "created": r.Created})
	}
	return out
}

// inboxSet sorts a run's pending rows by what they are.
type inboxSet struct {
	user, approve, wake, interrupt, cancel, compact, watch []*InboxRow
}

func sortInbox(rows []*InboxRow) inboxSet {
	var s inboxSet
	for _, r := range rows {
		switch r.Kind {
		case inboxUser:
			s.user = append(s.user, r)
		case inboxApprove:
			s.approve = append(s.approve, r)
		case inboxWake:
			s.wake = append(s.wake, r)
		case inboxInterrupt:
			s.interrupt = append(s.interrupt, r)
		case inboxCancel:
			s.cancel = append(s.cancel, r)
		case inboxCompact:
			s.compact = append(s.compact, r)
		case inboxWatch:
			s.watch = append(s.watch, r)
		}
	}
	return s
}

// active reports statuses a turn or a wait is in progress in.
func active(status string) bool {
	switch status {
	case statusRunning, statusAwait, statusSleep, statusWaiting, statusQueued, statusBlocked:
		return true
	}
	return false
}

// --- handlers --------------------------------------------------------------

// queue inserts a row for a run from a handler, then pokes. Emits the queue
// change so every open tile shows it at once.
func (ag *Agent) queue(runID int64, kind string, body inboxBody, clientID string) (int64, bool, error) {
	var id int64
	var dup bool
	err := ag.db.Tx(func(t *DB) error {
		var err error
		id, dup, err = t.enqueue(runID, kind, body, clientID)
		if err != nil {
			return err
		}
		if kind == inboxUser && ag.eng != nil {
			if r, err := t.getRun(runID); err == nil {
				ag.eng.emitInbox(t, rootOf(r), runID)
			}
		}
		return nil
	})
	if err == nil && ag.eng != nil {
		ag.eng.Poke(runID)
	}
	return id, dup, err
}

func handleMessage(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct {
		Text     string   `json:"text"`
		Files    []string `json:"files"` // session-file paths uploaded for this message
		ClientID string   `json:"clientId"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	body.Text = strings.TrimSpace(body.Text)
	if body.Text == "" && len(body.Files) == 0 {
		xbin.WriteError(w, 400, "need {text} or {files}")
		return
	}
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	// Check attachments BEFORE writing anything, so a bad path fails the
	// request instead of leaving half a message behind.
	if _, err := agent.checkAttachments(id, body.Files); err != nil {
		xbin.WriteError(w, 400, err.Error())
		return
	}
	if haltBlocks(w, r, id) {
		return
	}
	sender := callerOf(r).user
	iid, _, err := agent.queue(id, inboxUser, inboxBody{Text: body.Text, Files: body.Files, Source: "human", Sender: sender}, body.ClientID)
	if err == nil {
		agent.db.bumpActivity(id)
		if run, err := agent.db.getRun(id); err == nil {
			agent.markRead(rootOf(run), sender) // you have seen what you just wrote
		}
	}
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "inboxId": iid, "queued": active(run.Status)})
}

func handleApprove(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Approve bool }
	_ = json.NewDecoder(r.Body).Decode(&body)
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	if parsePending(run.Pending).Kind != "approval" {
		xbin.WriteError(w, 400, "no pending approval")
		return
	}
	if _, _, err := agent.queue(id, inboxApprove, inboxBody{Approve: body.Approve}, ""); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleInterrupt stops the turn in flight and keeps the run: it goes idle,
// its subagents are cancelled, and messages still queued for it come back in
// the response so the tile can put them back in the composer — they are not
// sent to a run the owner just stopped.
func handleInterrupt(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	run, err := agent.db.getRun(id)
	if err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	var returned []map[string]any
	stopped := 0
	c, lv := callerOf(r), levelOf(r)
	// Only your own queued messages come back to you; other people's stay
	// queued and become the next turn (D83).
	mine := func(b inboxBody) bool {
		return c.kind != whoUser || b.Sender == c.user || (b.Sender == "" && lv >= lvOwner)
	}
	err = agent.db.Tx(func(t *DB) error {
		for _, q := range t.undelivered(id) {
			if q.Kind == inboxUser && q.Body.Source == "human" && mine(q.Body) {
				if ok, _ := t.removeQueued(id, q.ID); ok {
					returned = append(returned, map[string]any{"text": q.Body.Text, "files": q.Body.Files})
				}
			}
		}
		if active(run.Status) {
			if _, _, err := t.enqueue(id, inboxInterrupt, inboxBody{Reason: "interrupted by the owner"}, ""); err != nil {
				return err
			}
		}
		stopped = agent.cancelBelow(t, id, "parent interrupted by the owner")
		if agent.eng != nil {
			agent.eng.emitInbox(t, rootOf(run), id)
		}
		return nil
	})
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if agent.eng != nil {
		agent.eng.Signal(id, errInterrupt)
	}
	if returned == nil {
		returned = []map[string]any{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "cancelledDescendants": stopped, "returned": returned})
}

// handleCancel is the durable stop. scope "subtree" (the default) takes the
// whole tree below the run; "node" stops just that one.
func handleCancel(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	var body struct{ Scope, Reason string }
	_ = json.NewDecoder(r.Body).Decode(&body)
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	var stopped []int64
	_ = agent.db.Tx(func(t *DB) error {
		stopped = agent.cancelRuns(t, id, body.Scope != "node", body.Reason)
		return nil
	})
	if stopped == nil {
		stopped = []int64{}
	}
	xbin.WriteJSON(w, 200, map[string]any{"ok": "true", "cancelled": stopped})
}

// handleResume drives the run again now (retry after an error, continue
// after an interrupt).
func handleResume(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	if haltBlocks(w, r, id) {
		return
	}
	if _, _, err := agent.queue(id, inboxWake, inboxBody{}, ""); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleCompact asks for a compaction and waits (bounded) for the engine to
// do it, so the response still means "done".
func handleCompact(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	iid, _, err := agent.queue(id, inboxCompact, inboxBody{}, "")
	if err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	if agent.eng != nil && !agent.eng.waitDelivered(iid, 2*time.Minute) {
		xbin.WriteJSON(w, 202, map[string]string{"ok": "true", "queued": "true"})
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleLearn asks the run to distill itself into a skill (the /learn flow).
func handleLearn(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	if _, err := agent.db.getRun(id); err != nil {
		xbin.WriteError(w, 404, "no such run")
		return
	}
	if _, _, err := agent.queue(id, inboxUser, inboxBody{Text: learnPrompt, Source: "learn"}, ""); err != nil {
		xbin.WriteError(w, 500, err.Error())
		return
	}
	xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
}

// handleRemoveQueued takes back a queued message: 200 when removed, 409 when
// it was already delivered to the model, 404 when there is no such row.
func handleRemoveQueued(w http.ResponseWriter, r *http.Request) {
	id := pathID(r)
	iid, _ := strconv.ParseInt(r.PathValue("iid"), 10, 64)
	var removed, exists bool
	if c, lv := callerOf(r), levelOf(r); lv < lvOwner {
		// A participant takes back only what they sent.
		rows := agent.db.inboxRows(`WHERE id=? AND run_id=?`, iid, id)
		if len(rows) == 1 && rows[0].Body.Sender != c.user {
			xbin.WriteError(w, 403, "only the conversation's owner can take back someone else's message")
			return
		}
	}
	_ = agent.db.Tx(func(t *DB) error {
		removed, exists = t.removeQueued(id, iid)
		if removed && agent.eng != nil {
			if run, err := t.getRun(id); err == nil {
				agent.eng.emitInbox(t, rootOf(run), id)
			}
		}
		return nil
	})
	switch {
	case removed:
		xbin.WriteJSON(w, 200, map[string]string{"ok": "true"})
	case exists:
		xbin.WriteError(w, 409, "already delivered to the agent")
	default:
		xbin.WriteError(w, 404, "no such queued message")
	}
}

// --- cancellation fan-out ------------------------------------------------------

// cancelRuns writes a cancel row for a run (and, with subtree, every live run
// below it) and signals each after the commit. Fanning out from here — not
// run by run down the tree — means a slow actor in the middle cannot keep the
// leaves spending. Returns the runs that were live.
func (ag *Agent) cancelRuns(t *DB, id int64, subtree bool, reason string) []int64 {
	targets := []int64{id}
	if subtree {
		if kids, err := t.descendants(id); err == nil {
			targets = append(targets, kids...)
		}
	}
	msg := strings.TrimSpace(reason)
	if msg == "" {
		msg = "cancelled"
	}
	var stopped []int64
	for _, rid := range targets {
		r, err := t.getRun(rid)
		if err != nil || !active(r.Status) {
			continue
		}
		if _, _, err := t.enqueue(rid, inboxCancel, inboxBody{Reason: msg}, ""); err == nil {
			stopped = append(stopped, rid)
		}
	}
	if ag.eng != nil {
		t.AfterCommit(func() {
			for _, rid := range stopped {
				ag.eng.Signal(rid, errCancel)
			}
		})
	}
	return stopped
}

// cancelBelow cancels a run's live descendants (not the run itself).
func (ag *Agent) cancelBelow(t *DB, id int64, reason string) int {
	kids, err := t.descendants(id)
	if err != nil {
		return 0
	}
	n := 0
	for _, k := range kids {
		n += len(ag.cancelRuns(t, k, false, reason))
	}
	return n
}
