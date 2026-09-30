// handoff_send.go — the global instance mailing queued handoffs (handoff.go)
// to people's partitions.
//
// Each person's handoffs go in order, oldest first. A failure xbind may
// retry — the tile paused, that person's inbox full, a network error, the
// blob store — holds back only that person's: their oldest waits for its
// next try (handoffs.next_try: 1 s, doubling to 5 min) while everyone
// else's go on, so one person's full inbox never stalls anyone else's DMs
// or events. A refusal (the person is gone or may no longer use the tile, the
// item is too large) is final. A handoff still queued after handoffQueueTTL
// is given up. Either way its content is deleted at once and a DM's chat is
// told. The staged files of a queued DM are kept for as long as it waits
// (stagedHeld: the upload prune leaves them).
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

const (
	// handoffQueueTTL is how long a handoff waits to be mailed: a mail
	// item's default life in xbind's inbox, where a handoff mailed at once
	// would have expired unread as well.
	handoffQueueTTL = 7 * 86400
	// handoffMaxBackoff caps the wait between two tries of one handoff.
	handoffMaxBackoff = 300
)

// errBadHandoff: a queued handoff's payload doesn't read (never retried).
var errBadHandoff = errors.New("a handoff that can't be read")

// handoffSender runs one sender pass at a time; timer is the next pass, at
// the time the earliest held handoff is due.
var handoffSender = struct {
	mu      sync.Mutex
	running bool
	again   bool
	timer   *time.Timer
}{}

// kickHandoffs starts a pass (after a commit that queued one, at start, when
// a held one is due).
func kickHandoffs() {
	if !globalMode() || agent == nil {
		return
	}
	s := &handoffSender
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
			next := agent.mailHandoffs(context.Background())
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
				s.timer = time.AfterFunc(max(time.Until(time.Unix(next, 0)), time.Second), kickHandoffs)
			}
			s.mu.Unlock()
			return
		}
	}()
}

// mailHandoffs gives up what waited too long and mails what is due; it
// answers when the next held handoff is due (unix seconds; 0: none waits).
func (ag *Agent) mailHandoffs(ctx context.Context) int64 {
	ag.expireHandoffs()
	for page := 0; page < 100 && ag.mailHandoffPage(ctx); page++ {
	}
	var n int
	var next int64
	_ = ag.db.q.QueryRow(`SELECT count(*), COALESCE(min(next_try), 0) FROM handoffs WHERE state='queued'`).Scan(&n, &next)
	if n == 0 {
		return 0
	}
	return max(next, now()+1)
}

// queuedHandoff is a handoff the sender reads.
type queuedHandoff struct {
	id, kind, person, source, payload, address string
	channel                                    int64
	tries                                      int
}

// mailHandoffPage mails up to 50 due handoffs — none of a person one of whose
// handoffs waits for a later try — and says whether a full page was read.
func (ag *Agent) mailHandoffPage(ctx context.Context) (more bool) {
	var list []queuedHandoff
	rows, err := ag.db.q.Query(`SELECT id, kind, person, source, payload, address, channel_id, tries FROM handoffs
		WHERE state='queued' AND person NOT IN (SELECT person FROM handoffs WHERE state='queued' AND next_try>?)
		ORDER BY created, rowid LIMIT 50`, now()) // rowid: the order they were queued in
	if err != nil {
		logf("handoffs: %v", err)
		return false
	}
	for rows.Next() {
		var q queuedHandoff
		if rows.Scan(&q.id, &q.kind, &q.person, &q.source, &q.payload, &q.address, &q.channel, &q.tries) == nil {
			list = append(list, q)
		}
	}
	rows.Close()
	held := map[string]bool{} // people whose oldest handoff failed in this page: the rest of theirs wait with it
	for _, q := range list {
		if held[q.person] {
			continue
		}
		topic, data, staged, err := ag.handoffMail(ctx, q.kind, q.payload)
		if err == nil {
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			_, err = sendMail(cctx, "user:"+q.person, topic, data, q.source)
			cancel()
		}
		switch {
		case err == nil:
			_, _ = ag.db.q.Exec(`UPDATE handoffs SET state='mailed', payload='', mailed_at=?, error='' WHERE id=?`, now(), q.id)
			ag.dropStaged(staged)
		case errors.Is(err, errMailRefused), errors.Is(err, errBadHandoff):
			logf("handoff %s (%s) to %s: %v — dropped", q.id, q.kind, q.person, err)
			ag.failHandoff(q, err.Error(), "Your message couldn't be passed on to your own space in this agent (your account there may be gone or no longer allowed to use it). Ask the agent's operator.")
		default:
			held[q.person] = true
			wait := min(int64(1)<<min(q.tries, 9), handoffMaxBackoff)
			logf("handoff %s (%s) to %s: %v — tried again in %ds", q.id, q.kind, q.person, err, wait)
			_, _ = ag.db.q.Exec(`UPDATE handoffs SET tries=tries+1, error=?, next_try=? WHERE id=?`, clip(err.Error(), 400), now()+wait, q.id)
		}
	}
	return len(list) == 50
}

// expireHandoffs gives up the handoffs that waited longer than
// handoffQueueTTL.
func (ag *Agent) expireHandoffs() {
	var list []queuedHandoff
	rows, err := ag.db.q.Query(`SELECT id, kind, person, payload, address, channel_id FROM handoffs WHERE state='queued' AND created<?`,
		now()-handoffQueueTTL)
	if err != nil {
		return
	}
	for rows.Next() {
		var q queuedHandoff
		if rows.Scan(&q.id, &q.kind, &q.person, &q.payload, &q.address, &q.channel) == nil {
			list = append(list, q)
		}
	}
	rows.Close()
	for _, q := range list {
		logf("handoff %s (%s) to %s: waited more than %d days — dropped", q.id, q.kind, q.person, handoffQueueTTL/86400)
		ag.failHandoff(q, fmt.Sprintf("not mailed within %d days", handoffQueueTTL/86400),
			fmt.Sprintf("Your message couldn't be passed on to your own space in this agent: it waited %d days without your space taking it. "+
				"Open the agent once (or ask its operator), then send it again.", handoffQueueTTL/86400))
	}
}

// failHandoff ends a queued handoff for good: its content and staged files
// go, and a DM's chat is told (notice).
func (ag *Agent) failHandoff(q queuedHandoff, why, notice string) {
	var h dmHandoff
	if q.kind == "dm" {
		_ = json.Unmarshal([]byte(q.payload), &h)
	}
	ag.moveMailRefused(q) // homes_move.go: a move whose mail never goes is given up
	_ = ag.db.Tx(func(t *DB) error {
		_, err := t.q.Exec(`UPDATE handoffs SET state='failed', payload='', error=? WHERE id=? AND state='queued'`, clip(why, 400), q.id)
		if q.kind == "dm" && q.address != "" {
			t.outboxAdd(q.channel, "", 0, "notice", q.address, notice)
		}
		return err
	})
	ag.dropStaged(h.Staged)
}

// handoffsWait: handoffs wait to be mailed (the global instance) — work that
// moves without anyone, so a stopping global instance leaves its resume job
// (leaveWakeUp).
func (d *DB) handoffsWait() bool {
	if !globalMode() {
		return false
	}
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM handoffs WHERE state='queued'`).Scan(&n)
	return n > 0
}

// stagedHeld is what the upload prune (channel_files.go) leaves at a
// partitioned agent's global instance: the staged files a queued handoff
// will carry, and a person's reply's files (ids "r…"), which go at its ack.
// "" anywhere else.
func stagedHeld() string {
	if !globalMode() {
		return ""
	}
	return ` AND id NOT LIKE 'r%' AND id NOT IN (SELECT j.value FROM handoffs h,
		json_each(CASE WHEN json_valid(h.payload) THEN h.payload ELSE '{}' END, '$.staged') j WHERE h.state='queued')`
}

// handoffMail is the item a queued handoff mails: a DM's staged files are
// read and carried inline (what doesn't fit is named in the text).
func (ag *Agent) handoffMail(ctx context.Context, kind, payload string) (topic string, data any, staged []string, err error) {
	if kind == moveKind { // homes_move.go: a conversation leaving the shared space
		return topicMove, json.RawMessage(payload), nil, nil
	}
	if kind == "event" {
		var e eventHandoff
		if err := json.Unmarshal([]byte(payload), &e); err != nil {
			return "", nil, nil, fmt.Errorf("%w: %v", errBadHandoff, err)
		}
		return topicEvent, e, nil, nil
	}
	var h dmHandoff
	if err := json.Unmarshal([]byte(payload), &h); err != nil {
		return "", nil, nil, fmt.Errorf("%w: %v", errBadHandoff, err)
	}
	staged, h.Staged = h.Staged, nil
	budget := mailFileBudget - len(h.Text) - len(h.System)
	var left []string
	for _, id := range staged {
		var name, mime, content, blob string
		if err := ag.db.q.QueryRow(`SELECT name, mime, content, blob FROM channel_files WHERE id=? AND channel_id=?`, id, h.Channel).
			Scan(&name, &mime, &content, &blob); err != nil {
			continue // gone (its message came too late): the message still goes
		}
		b := []byte(content)
		if blob != "" {
			if b, err = ag.readBlob(ctx, blob); err != nil {
				return "", nil, nil, err // the blob store: later
			}
		}
		if len(b) > budget {
			left = append(left, fmt.Sprintf("%s (%s)", name, humanBytes(len(b))))
			continue
		}
		budget -= len(b)
		h.Files = append(h.Files, hoFile{Name: name, Mime: mime, Data: b})
	}
	if len(left) > 0 {
		h.Text += "\n\n[not passed on — too large for a private handoff: " + strings.Join(left, ", ") + "]"
	}
	return topicDM, h, staged, nil
}

// dropStaged deletes a DM's staged files once they were mailed (or given
// up).
func (ag *Agent) dropStaged(ids []string) {
	var blobs []string
	for _, id := range ids {
		var blob string
		if ag.db.q.QueryRow(`SELECT blob FROM channel_files WHERE id=?`, id).Scan(&blob) == nil {
			_, _ = ag.db.q.Exec(`DELETE FROM channel_files WHERE id=?`, id)
			if blob != "" {
				blobs = append(blobs, blob)
			}
		}
	}
	ag.dropBlobs(blobs)
}
