// homes_move_user.go — a person's partition's side of homes_move.go: a
// shared conversation of theirs that stopped being shared moves in here.
//
// The global instance's `conv/move {run}` mail is recorded (moves_in,
// "asked") in the transaction that takes it; the rest runs after, off the
// mailbox (it calls the global instance), one move at a time, retried with
// backoff while it can't go on:
//
//   - asked: leftovers of an attempt a crash cut short are deleted; the
//     conversation is read (GET /moves/{id}/export; its files past the
//     bundle's cap one by one, GET /runs/{id}/raw) and imported as the
//     person's, private and hidden — origin "held", like an unsent draft,
//     listed nowhere — then "arrived" (its new id);
//   - arrived: POST /moves/{id}/done {to} — the global instance deletes its
//     copy — then the conversation shows here ("done"). Given up at global
//     meanwhile (409): the hidden copy goes ("dropped").
//
// A partition stopping with a move under way asks to be started again
// (userWake, resume_mode.go), and takes it up at its next start.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"
)

func init() {
	mailHandlers[topicMove] = handleMoveMail
}

// moveKey is a moving conversation's hidden copy's session key.
func moveKey(from int64) string { return "move:" + strconv.FormatInt(from, 10) }

// handleMoveMail records a move the global instance asks for. A move asked
// again after one was dropped (the person un-shared it once more) starts
// afresh; one done already is only acknowledged.
func handleMoveMail(_ context.Context, t *DB, it mailItem) error {
	if !userMode() || it.From != "global" {
		logf("conv/move %s from %q: only the global instance moves a conversation here — refused", it.ID, it.From)
		return nil
	}
	var m moveItem
	if err := json.Unmarshal(it.Data, &m); err != nil || m.Run <= 0 || m.Run >= partitionIDBase {
		logf("conv/move %s: malformed — dropped", it.ID)
		return nil
	}
	if _, err := t.q.Exec(`INSERT INTO moves_in (from_id, created) VALUES (?, ?)
		ON CONFLICT(from_id) DO UPDATE SET state='asked', to_id=0, tries=0, next_try=0, error='' WHERE state='dropped'`, m.Run, now()); err != nil {
		return err
	}
	t.AfterCommit(kickMoves)
	return nil
}

// movesWait: a move is under way — work that goes on without the person
// (userWake).
func (d *DB) movesWait() bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM moves_in WHERE state IN ('asked','arrived')`).Scan(&n)
	return n > 0
}

// mover runs one pass at a time; timer is the next, when a move waits for a
// retry.
var mover = struct {
	mu      sync.Mutex
	running bool
	again   bool
	timer   *time.Timer
}{}

// kickMoves starts a pass (after the mail that asked for one, at start).
func kickMoves() {
	if !userMode() || agent == nil {
		return
	}
	s := &mover
	s.mu.Lock()
	if s.running {
		s.again = true
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()
	go func() {
		for {
			next := agent.runMoves(context.Background())
			s.mu.Lock()
			if s.again {
				s.again = false
				s.mu.Unlock()
				continue
			}
			s.running = false
			if s.timer != nil {
				s.timer.Stop()
				s.timer = nil
			}
			if next > 0 {
				s.timer = time.AfterFunc(max(time.Until(time.Unix(next, 0)), time.Second), kickMoves)
			}
			s.mu.Unlock()
			return
		}
	}()
}

// errMoveLater: a step that can't go on now (the conversation is working,
// the global instance didn't answer) — tried again with backoff.
var errMoveLater = errors.New("later")

// runMoves takes every due move as far as it goes; it answers when the next
// waiting one is due (unix seconds; 0: none waits).
func (ag *Agent) runMoves(ctx context.Context) int64 {
	type row struct {
		from, to int64
		state    string
		tries    int
	}
	var due []row
	rows, err := ag.db.q.Query(`SELECT from_id, to_id, state, tries FROM moves_in WHERE state IN ('asked','arrived') AND next_try<=? ORDER BY created, from_id`, now())
	if err != nil {
		logf("moves: %v", err)
		return 0
	}
	for rows.Next() {
		var r row
		if rows.Scan(&r.from, &r.to, &r.state, &r.tries) == nil {
			due = append(due, r)
		}
	}
	rows.Close()
	for _, r := range due {
		var err error
		if r.state == "asked" {
			r.to, err = ag.moveIn(ctx, r.from)
		}
		if err == nil {
			err = ag.moveConfirm(ctx, r.from, r.to)
		}
		if err != nil {
			wait := min(int64(1)<<min(r.tries, 9), handoffMaxBackoff)
			logf("moving conversation %d here: %v — tried again in %ds", r.from, err, wait)
			_, _ = ag.db.q.Exec(`UPDATE moves_in SET tries=tries+1, next_try=?, error=? WHERE from_id=?`, now()+wait, clip(err.Error(), 400), r.from)
		}
	}
	_, _ = ag.db.q.Exec(`DELETE FROM moves_in WHERE state IN ('done','dropped') AND created<?`, now()-moveKept)
	var n int
	var next int64
	_ = ag.db.q.QueryRow(`SELECT count(*), COALESCE(min(next_try), 0) FROM moves_in WHERE state IN ('asked','arrived')`).Scan(&n, &next)
	if n == 0 {
		return 0
	}
	return max(next, now()+1)
}

// moveIn reads the moving conversation from the global instance and imports
// it hidden; it answers the copy's id once the move is "arrived". A move
// that can't be made — gone at global, too large to copy — is dropped (and,
// when too large, given up at global) with no error.
func (ag *Agent) moveIn(ctx context.Context, from int64) (int64, error) {
	stale := scanIDs(ag.db.q.Query(`SELECT id FROM runs WHERE parent_id=0 AND origin=? AND session_key=?`, heldOrigin, moveKey(from)))
	for _, id := range stale { // an attempt a crash cut short: never shown, made again
		if err := ag.deleteRunTree(id); err != nil {
			return 0, err
		}
		ag.acl.flush(id)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path := "/moves/" + strconv.FormatInt(from, 10)
	res, err := exportAtGlobal(cctx, path+"/export")
	var tl tooLargeToCopy
	switch {
	case errors.As(err, &tl) || err == nil && res.Status == http.StatusRequestEntityTooLarge:
		why := "too large to copy into the person's own space"
		body, _ := json.Marshal(map[string]string{"why": why})
		if r, err := callGlobal(cctx, http.MethodPost, path+"/abandon", body, "application/json"); err != nil || r.Status/100 != 2 && r.Status != http.StatusNotFound {
			return 0, fmt.Errorf("%w: giving the move up at the global instance: %v (HTTP %d)", errMoveLater, err, r.Status)
		}
		return 0, ag.dropMove(from, 0, why)
	case err != nil:
		return 0, fmt.Errorf("%w: the global instance: %v", errMoveLater, err)
	case res.Status == http.StatusNotFound:
		return 0, ag.dropMove(from, 0, "no move at the global instance (given up, or moved already): "+clip(string(res.Body), 200))
	case res.Status != http.StatusOK:
		return 0, fmt.Errorf("%w: the global instance: HTTP %d %s", errMoveLater, res.Status, clip(string(res.Body), 200))
	}
	var b convBundle
	if err := json.Unmarshal(res.Body, &b); err != nil {
		return 0, fmt.Errorf("%w: the export: %v", errMoveLater, err)
	}
	c := who{kind: whoUser, user: runUser}
	cls, err := importClass(c, &b)
	if err != nil {
		return 0, fmt.Errorf("%w: its class: %v", errMoveLater, err)
	}
	st := c.stamp("chat")
	st.Origin, st.SessionKey = heldOrigin, moveKey(from) // hidden until the global instance let it go
	run, err := ag.importConv(ctx, &b, c, st, cls, nil, "Moved here from the agent's shared space when it stopped being shared: only you can open it now.", false)
	if err != nil {
		return 0, fmt.Errorf("%w: importing it: %v", errMoveLater, err)
	}
	if err := ag.moveLeftFiles(ctx, from, run.ID, &b); err != nil {
		return 0, fmt.Errorf("%w: its files: %v", errMoveLater, err)
	}
	if _, err := ag.db.q.Exec(`UPDATE moves_in SET state='arrived', to_id=?, tries=0, next_try=0, error='' WHERE from_id=? AND state='asked'`, run.ID, from); err != nil {
		return 0, err
	}
	return run.ID, nil
}

// moveLeftFiles copies the session files the bundle had no room for, one at
// a time (GET /runs/{id}/raw as the person), onto the messages that carried
// them.
func (ag *Agent) moveLeftFiles(ctx context.Context, from, to int64, b *convBundle) error {
	if len(b.Left) == 0 {
		return nil
	}
	msgIDs := scanIDs(ag.db.q.Query(`SELECT id FROM messages WHERE run_id=? AND role<>'system' ORDER BY id`, to))
	for _, p := range b.Left {
		res, err := exportAtGlobal(ctx, "/runs/"+strconv.FormatInt(from, 10)+"/raw?path="+url.QueryEscape(p))
		if err != nil {
			return err
		}
		if res.Status == http.StatusNotFound {
			continue // deleted since the export
		}
		if res.Status != http.StatusOK {
			return fmt.Errorf("%s: HTTP %d %s", p, res.Status, clip(string(res.Body), 200))
		}
		f, err := ag.acceptUploadSrc(ctx, to, p, res.Type, bytes.NewReader(res.Body), &fileSource{Kind: "copy"})
		if err != nil {
			return fmt.Errorf("%s: %w", p, err)
		}
		if len(msgIDs) != len(b.Messages) {
			continue
		}
		for i, bm := range b.Messages {
			if slices.Contains(bm.Files, p) {
				if _, err := ag.db.q.Exec(`INSERT OR IGNORE INTO message_files (msg_id, run_id, path) VALUES (?, ?, ?)`, msgIDs[i], to, f.Path); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

// moveConfirm tells the global instance the copy is here (it deletes its
// own), then shows the copy. A move given up there meanwhile drops the copy.
func (ag *Agent) moveConfirm(ctx context.Context, from, to int64) error {
	if to == 0 {
		return nil // dropped by moveIn
	}
	body, _ := json.Marshal(map[string]int64{"to": to})
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := callGlobal(cctx, http.MethodPost, "/moves/"+strconv.FormatInt(from, 10)+"/done", body, "application/json")
	switch {
	case err != nil:
		return fmt.Errorf("%w: the global instance: %v", errMoveLater, err)
	case res.Status == http.StatusConflict:
		return ag.dropMove(from, to, "given up at the global instance: "+clip(string(res.Body), 200))
	case res.Status/100 != 2:
		return fmt.Errorf("%w: the global instance: HTTP %d %s", errMoveLater, res.Status, clip(string(res.Body), 200))
	}
	return ag.db.Tx(func(t *DB) error {
		if _, err := t.q.Exec(`UPDATE runs SET origin='chat', session_key='' WHERE id=? AND origin=? AND session_key=?`, to, heldOrigin, moveKey(from)); err != nil {
			return err
		}
		if _, err := t.q.Exec(`UPDATE moves_in SET state='done', tries=0, next_try=0, error='' WHERE from_id=?`, from); err != nil {
			return err
		}
		if ag.eng != nil {
			ag.eng.emitRun(t, to)
		}
		return nil
	})
}

// dropMove ends a move that won't happen: its hidden copy (if any) goes.
func (ag *Agent) dropMove(from, to int64, why string) error {
	logf("moving conversation %d here: %s — dropped", from, why)
	if to != 0 {
		if err := ag.deleteRunTree(to); err != nil {
			return err
		}
		ag.acl.flush(to)
	}
	_, err := ag.db.q.Exec(`UPDATE moves_in SET state='dropped', to_id=0, error=? WHERE from_id=?`, clip(why, 400), from)
	return err
}
