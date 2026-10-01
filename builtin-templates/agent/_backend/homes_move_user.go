// homes_move_user.go — a person's partition's side of homes_move.go: a
// shared conversation of theirs that stopped being shared moves in here.
//
// The global instance's `conv/move {run}` mail is recorded (moves_in,
// "asked") in the transaction that takes it; the rest runs after, off the
// mailbox (it calls the global instance), one move at a time, retried with
// backoff while it can't go on:
//
//   - asked: leftovers of an attempt a crash cut short are deleted; the
//     conversation is read (GET /moves/{id}/export — its bundle, notes,
//     schedules, the owner's pin, and a ticket; its files past the bundle's
//     cap one by one, GET /runs/{id}/raw) and imported as the person's,
//     private and hidden — origin "held", like an unsent draft, listed
//     nowhere — then "arrived" (its new id, the ticket);
//   - arrived: POST /moves/{id}/done {to, ticket} — the global instance
//     deletes its copy — then the conversation shows here ("done"), its
//     schedules made here, reporting into it. Changed at global since it was
//     read (412): the copy goes, and it is read again ("asked"). Given up at
//     global meanwhile (409): the copy goes ("dropped").
//
// What can't be imported here — too large, a bundle this home refuses, past
// its file store's limits — is given up at global (POST /moves/{id}/abandon),
// so the conversation isn't frozen there. A partition stopping with a move
// under way asks to be started again (userWake, resume_mode.go), and takes
// it up at its next start.
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
	"strings"
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
	if _, err := t.q.Exec(`INSERT INTO moves_in (from_id, created, key) VALUES (?, ?, ?)
		ON CONFLICT(from_id) DO UPDATE SET key=excluded.key WHERE state<>'done'`, m.Run, now(), m.Key); err != nil {
		return err
	}
	if _, err := t.q.Exec(`UPDATE moves_in SET state='asked', to_id=0, tries=0, next_try=0, error='', ticket='', extra='' WHERE from_id=? AND state='dropped'`, m.Run); err != nil {
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

// moveInRow is a moves_in row a pass takes up.
type moveInRow struct {
	from, to           int64
	state, ticket, key string
	extra              string
	tries              int
}

// runMoves takes every due move as far as it goes; it answers when the next
// waiting one is due (unix seconds; 0: none waits).
func (ag *Agent) runMoves(ctx context.Context) int64 {
	var due []moveInRow
	rows, err := ag.db.q.Query(`SELECT from_id, to_id, state, tries, ticket, extra, key FROM moves_in WHERE state IN ('asked','arrived') AND next_try<=? ORDER BY created, from_id`, now())
	if err != nil {
		logf("moves: %v", err)
		return 0
	}
	for rows.Next() {
		var r moveInRow
		if rows.Scan(&r.from, &r.to, &r.state, &r.tries, &r.ticket, &r.extra, &r.key) == nil {
			due = append(due, r)
		}
	}
	rows.Close()
	for _, r := range due {
		var err error
		if r.state == "asked" {
			err = ag.moveIn(ctx, &r)
		}
		if err == nil {
			err = ag.moveConfirm(ctx, &r)
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

// moveExtra is what a move carries past the conversation's import, kept
// with the move until it shows here.
type moveExtra struct {
	Schedules  []*Schedule `json:"schedules,omitempty"`
	PinnedAt   int64       `json:"pinnedAt,omitempty"`
	ArchivedAt int64       `json:"archivedAt,omitempty"`
}

// moveNote is the note on a moved conversation.
func moveNote(x *moveExport) string {
	note := "Moved here from the agent's shared space when it stopped being shared: only you can open it now."
	switch n := len(x.Schedules); {
	case n == 1:
		note += " Its schedule reporting into it came along."
	case n > 1:
		note += fmt.Sprintf(" Its %d schedules reporting into it came along.", n)
	}
	if len(x.Behind) > 0 {
		note += " Not moved with it: " + strings.Join(x.Behind, "; ") + "."
	}
	return note
}

// moveIn reads the moving conversation from the global instance and imports
// it hidden; the row is "arrived" (its copy's id, the export's ticket) once
// it did. A move that can't be made — gone at global, too large or not
// importable here — is dropped (and, when it can't be made here, given up at
// global) with no error.
func (ag *Agent) moveIn(ctx context.Context, r *moveInRow) error {
	from := r.from
	stale := scanIDs(ag.db.q.Query(`SELECT id FROM runs WHERE parent_id=0 AND origin=? AND session_key=?`, heldOrigin, moveKey(from)))
	for _, id := range stale { // an attempt a crash cut short, or read again: never shown, made anew
		if err := ag.deleteRunTree(id); err != nil {
			return err
		}
		ag.acl.flush(id)
	}
	cctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path := "/moves/" + strconv.FormatInt(from, 10)
	res, err := exportAtGlobal(cctx, path+"/export?key="+url.QueryEscape(r.key))
	var tl tooLargeToCopy
	switch {
	case errors.As(err, &tl) || err == nil && res.Status == http.StatusRequestEntityTooLarge:
		return ag.abandonMove(cctx, r, 0, "too large to copy into the person's own space")
	case err != nil:
		return fmt.Errorf("%w: the global instance: %v", errMoveLater, err)
	case res.Status == http.StatusNotFound:
		return ag.moveGoneAtGlobal(cctx, from, res)
	case res.Status != http.StatusOK:
		return fmt.Errorf("%w: the global instance: HTTP %d %s", errMoveLater, res.Status, clip(string(res.Body), 200))
	}
	var x moveExport
	if err := json.Unmarshal(res.Body, &x); err != nil {
		return fmt.Errorf("%w: the export: %v", errMoveLater, err)
	}
	c := who{kind: whoUser, user: runUser}
	fail := func(what string, to int64, err error) error {
		if permanentImport(err) {
			return ag.abandonMove(cctx, r, to, what+": "+err.Error())
		}
		return fmt.Errorf("%w: %s: %v", errMoveLater, what, err)
	}
	cls, err := importClass(c, &x.convBundle)
	if err != nil {
		return fail("its class", 0, err)
	}
	st := c.stamp("chat")
	st.Origin, st.SessionKey = heldOrigin, moveKey(from) // hidden until the global instance let it go
	run, err := ag.importConv(ctx, &x.convBundle, c, st, cls, nil, moveNote(&x), false)
	if err != nil {
		return fail("importing it", 0, err)
	}
	if err := ag.moveLeftFiles(ctx, from, run.ID, &x.convBundle); err != nil {
		return fail("its files", run.ID, err)
	}
	for k, v := range x.Memory {
		if err := ag.db.memorySet(run.ID, k, v); err != nil {
			return fail("its notes", run.ID, err)
		}
	}
	extra, _ := json.Marshal(moveExtra{Schedules: x.Schedules, PinnedAt: x.PinnedAt, ArchivedAt: x.ArchivedAt})
	if _, err := ag.db.q.Exec(`UPDATE moves_in SET state='arrived', to_id=?, ticket=?, extra=?, tries=0, next_try=0, error='' WHERE from_id=? AND state='asked'`,
		run.ID, x.Ticket, string(extra), from); err != nil {
		return err
	}
	r.to, r.ticket, r.extra = run.ID, x.Ticket, string(extra)
	return nil
}

// permanentImport: an import error that trying again won't cure — the
// bundle itself (badRequest), a class the person may not use, a file too
// large, a conversation past this home's file-store limits
// (files_store.go's words).
func permanentImport(err error) bool {
	var br badRequest
	var ce *errClass
	if errors.As(err, &br) || errors.As(err, &ce) || errors.Is(err, errTooLarge) {
		return true
	}
	s := err.Error()
	return strings.Contains(s, "too many files") || strings.Contains(s, "store full") || strings.Contains(s, "file too large")
}

// abandonMove gives a move up at the global instance (the conversation stays
// there, private, writable again), then drops it here with its copy, if any.
func (ag *Agent) abandonMove(ctx context.Context, m *moveInRow, to int64, why string) error {
	body, _ := json.Marshal(map[string]string{"why": why, "key": m.key})
	r, err := callGlobal(ctx, http.MethodPost, "/moves/"+strconv.FormatInt(m.from, 10)+"/abandon", body, "application/json")
	if err != nil || r.Status/100 != 2 && r.Status != http.StatusNotFound {
		return fmt.Errorf("%w: giving the move up at the global instance (%s): %v (HTTP %d)", errMoveLater, why, err, r.Status)
	}
	return ag.dropMove(m.from, to, why)
}

// moveGoneAtGlobal: the export answered 404 — the move's own "no such move"
// (given up, moved already, the conversation gone), or a 404 that isn't its
// (a route not there). GET /moves/{id} tells them apart: a move that stands
// is tried again later; none is dropped here.
func (ag *Agent) moveGoneAtGlobal(ctx context.Context, from int64, export gwResp) error {
	g, err := exportAtGlobal(ctx, "/moves/"+strconv.FormatInt(from, 10))
	if err != nil || g.Status != http.StatusOK && g.Status != http.StatusNotFound {
		return fmt.Errorf("%w: the export answered 404, and the move: %v (HTTP %d)", errMoveLater, err, g.Status)
	}
	var m convMove
	if g.Status == http.StatusOK && json.Unmarshal(g.Body, &m) == nil && m.State == "asked" {
		return fmt.Errorf("%w: the export answered 404 while the move stands: %s", errMoveLater, clip(string(export.Body), 200))
	}
	return ag.dropMove(from, 0, "no move at the global instance (given up, or moved already): "+clip(string(export.Body), 200))
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
// own), then shows the copy with what came with it. Changed at global since
// it was read: the move is read again; given up there meanwhile: the copy
// goes.
func (ag *Agent) moveConfirm(ctx context.Context, r *moveInRow) error {
	from, to := r.from, r.to
	if to == 0 {
		return nil // dropped by moveIn
	}
	body, _ := json.Marshal(map[string]any{"to": to, "ticket": r.ticket, "key": r.key})
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	res, err := callGlobal(cctx, http.MethodPost, "/moves/"+strconv.FormatInt(from, 10)+"/done", body, "application/json")
	switch {
	case err != nil:
		return fmt.Errorf("%w: the global instance: %v", errMoveLater, err)
	case res.Status == http.StatusPreconditionFailed: // it changed there, or works again: read it anew (moveIn deletes this copy)
		_, _ = ag.db.q.Exec(`UPDATE moves_in SET state='asked', to_id=0, ticket='', extra='' WHERE from_id=? AND state='arrived'`, from)
		return fmt.Errorf("%w: %s", errMoveLater, clip(string(res.Body), 200))
	case res.Status == http.StatusConflict:
		return ag.dropMove(from, to, "given up at the global instance: "+clip(string(res.Body), 200))
	case res.Status/100 != 2:
		return fmt.Errorf("%w: the global instance: HTTP %d %s", errMoveLater, res.Status, clip(string(res.Body), 200))
	}
	var x moveExtra
	_ = json.Unmarshal([]byte(r.extra), &x)
	var made []*Schedule
	err = ag.db.Tx(func(t *DB) error {
		res, err := t.q.Exec(`UPDATE runs SET origin='chat', session_key='' WHERE id=? AND origin=? AND session_key=?`, to, heldOrigin, moveKey(from))
		if err != nil {
			return err
		}
		if rowsAffected(res) == 1 {
			if made, err = t.adoptSchedules(x.Schedules, from, to); err != nil {
				return err
			}
			if x.PinnedAt != 0 || x.ArchivedAt != 0 {
				t.setUserState(to, runUser, func(s *userState) { s.PinnedAt, s.ArchivedAt = x.PinnedAt, x.ArchivedAt })
			}
		}
		if _, err := t.q.Exec(`UPDATE moves_in SET state='done', tries=0, next_try=0, error='', extra='' WHERE from_id=?`, from); err != nil {
			return err
		}
		if ag.eng != nil {
			ag.eng.emitRun(t, to)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, s := range made {
		if err := ag.registerScheduleCron(s); err != nil { // reRegisterSchedules asserts it again at every start
			logf("moved conversation %d: its schedule #%d: %v", to, s.ID, err)
		}
	}
	return nil
}

// adoptSchedules makes here the schedules that moved with conversation from
// (its owner's, reporting into it): the person's own, private, reporting
// into to. Answers the enabled ones, whose cron jobs are set after the
// commit.
func (d *DB) adoptSchedules(list []*Schedule, from, to int64) ([]*Schedule, error) {
	var made []*Schedule
	for _, s := range list {
		ns := &Schedule{Name: s.Name, Cron: s.Cron, Goal: s.Goal, System: s.System, Toolset: s.Toolset, Class: s.Class,
			Owner: runUser, Visibility: visPrivate, Mode: modeConversation, TargetRun: to}
		if s.CreatedByRun == from {
			ns.CreatedByRun = to
		}
		id, err := d.createSchedule(ns)
		if err != nil {
			return nil, err
		}
		ns.ID, ns.Enabled = id, s.Enabled
		if !s.Enabled {
			_, _ = d.q.Exec(`UPDATE schedules SET enabled=0 WHERE id=?`, id)
			continue
		}
		made = append(made, ns)
	}
	return made, nil
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
	_, err := ag.db.q.Exec(`UPDATE moves_in SET state='dropped', to_id=0, ticket='', extra='', error=? WHERE from_id=?`, clip(why, 400), from)
	return err
}
