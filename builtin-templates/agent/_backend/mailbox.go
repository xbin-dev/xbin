// mailbox.go — partition mail (docs/partitions.md §Partition mail,
// "partitionMail" in xbin.json): the xbind-owned drop box between a
// partitioned agent's global instance and one person's partition. Global
// mails a person's partition (a linked DM, a private trigger's event…); a
// person's partition mails only global (a reply for the outbox…); xbind
// stamps who sent it. When items wait, xbind rings the doorbell — POST
// /mailbox, as xbin/mail — and the handler pulls the inbox page by page
// (while xbind says more wait: a page is cut at its limit or ~8 MiB), hands
// each item to the handler of its topic and acks the page in one call.
// Every start pulls too (a doorbell may have rung while nothing ran).
//
// Delivery is at least once: a handler's effect and the item's id (mail_seen)
// commit in one transaction, so an item delivered again after a crash between
// the commit and the ack is only acked. An item a handler fails on stays for
// the next pull. An item whose topic this code has no handler for is left
// while it is young (mailUnknownGrace: mid-deploy, the newer code that knows
// it may be the one that reads it next) and acked — dropped, logged — after:
// left for its whole ttl, it would start a stopped partition at every
// doorbell step until it expires.
//
// No topic has a handler yet (channels, triggers and handoffs add theirs to
// mailHandlers). An unpartitioned instance has no mailbox.
package main

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	xbin "github.com/xbin-dev/xbin/sdk"
)

// mailItem is one inbox item: From is stamped by xbind ("global" or
// "user:<id>"), At is when it was sent.
type mailItem struct {
	ID    string          `json:"id"`
	From  string          `json:"from"`
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
	At    time.Time       `json:"at"`
}

// mailSource is this process's drop box: xbind's partition mail through the
// SDK (sdkMail), a fake in tests.
type mailSource interface {
	// Page is one page of the inbox after the id after, oldest first; more
	// says items wait past it (a short page isn't the end).
	Page(ctx context.Context, after string, limit int) (items []mailItem, more bool, err error)
	Ack(ctx context.Context, ids ...string) error
}

// sdkMail reads and acks this instance's own inbox (xbin.InboxPage/Ack; the
// SDK's calls carry their own deadline, so ctx is only checked between them).
type sdkMail struct{}

func (sdkMail) Page(ctx context.Context, after string, limit int) ([]mailItem, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	pg, err := xbin.InboxPage(after, limit)
	if err != nil {
		return nil, false, err
	}
	out := make([]mailItem, 0, len(pg.Items))
	for _, it := range pg.Items {
		out = append(out, mailItem{ID: it.ID, From: it.From, Topic: it.Topic, Data: it.Data, At: it.At})
	}
	return out, pg.More, nil
}

func (sdkMail) Ack(ctx context.Context, ids ...string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return xbin.Ack(ids...)
}

var partitionMail mailSource = sdkMail{}

// mailHandler applies one item inside t (the item's id is recorded in the
// same transaction); an error leaves the item unacked for the next pull.
type mailHandler func(ctx context.Context, t *DB, it mailItem) error

// mailHandlers are the topics this code handles (none yet).
var mailHandlers = map[string]mailHandler{}

const (
	// mailPage is how many items one inbox read asks for.
	mailPage = 50
	// mailUnknownGrace is how long an item of a topic no handler knows stays
	// in the inbox before it is acked (dropped): past the doorbell's first
	// steps (at once, 1 min, 5 min), long enough for a deploy to bring the
	// code that knows it.
	mailUnknownGrace = 30 * time.Minute
)

var (
	mailMu      sync.Mutex // one pull at a time
	mailUnknown = map[string]time.Time{}
)

func (d *DB) ensureMailSeen() error {
	_, err := d.q.Exec(`CREATE TABLE IF NOT EXISTS mail_seen (id TEXT PRIMARY KEY, topic TEXT NOT NULL, at INTEGER NOT NULL)`)
	return err
}

func (d *DB) mailSeen(id string) bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM mail_seen WHERE id=?`, id).Scan(&n)
	return n > 0
}

// mailCounts is what one pull did: handled (applied now), left (in the
// inbox for a later pull) and dropped (a topic no handler knows, past its
// grace: acked unhandled).
type mailCounts struct {
	Handled int `json:"handled"`
	Left    int `json:"left"`
	Dropped int `json:"dropped"`
}

// pullMail drains the inbox, page by page while more wait: handled items
// (and ones handled before) and unknown topics past their grace are acked,
// one call a page; items of a failing handler, and unknown topics still
// young, stay. It stops at the first read or ack that fails (the doorbell
// rings again).
func (ag *Agent) pullMail(ctx context.Context) (mailCounts, error) {
	var c mailCounts
	if !partitioned() {
		return c, nil
	}
	mailMu.Lock()
	defer mailMu.Unlock()
	if err := ag.db.ensureMailSeen(); err != nil {
		return c, err
	}
	after := ""
	for {
		items, more, err := partitionMail.Page(ctx, after, mailPage)
		if err != nil {
			return c, err
		}
		var ack []string
		for _, it := range items {
			if it.ID == "" {
				continue
			}
			after = it.ID
			if ag.db.mailSeen(it.ID) {
				ack = append(ack, it.ID) // applied before; its ack was lost
				continue
			}
			h := mailHandlers[it.Topic]
			if h == nil {
				if mailUnknownDue(it) {
					logf("mail %s from %s: no handler for topic %q after %s — acknowledged unhandled", it.ID, it.From, it.Topic, mailUnknownGrace)
					delete(mailUnknown, it.ID)
					c.Dropped++
					ack = append(ack, it.ID)
				} else {
					c.Left++
				}
				continue
			}
			err := ag.db.Tx(func(t *DB) error {
				if err := h(ctx, t, it); err != nil {
					return err
				}
				_, err := t.q.Exec(`INSERT OR IGNORE INTO mail_seen (id, topic, at) VALUES (?, ?, ?)`, it.ID, it.Topic, now())
				return err
			})
			if err != nil {
				logf("mail %s (%s): %v — left for the next pull", it.ID, it.Topic, err)
				c.Left++
				continue
			}
			c.Handled++
			ack = append(ack, it.ID)
		}
		if len(ack) > 0 {
			if err := partitionMail.Ack(ctx, ack...); err != nil {
				return c, err
			}
		}
		if !more || len(items) == 0 {
			break
		}
	}
	_, _ = ag.db.q.Exec(`DELETE FROM mail_seen WHERE at < ?`, now()-31*24*3600) // longer than any item lives
	for id, seen := range mailUnknown {
		if time.Since(seen) > 31*24*time.Hour { // expired unread
			delete(mailUnknown, id)
		}
	}
	return c, nil
}

// mailUnknownDue reports whether an item no handler knows is old enough to
// ack unhandled: sent (At, else first seen here) more than mailUnknownGrace
// ago. The first sight of one is logged.
func mailUnknownDue(it mailItem) bool {
	seen, known := mailUnknown[it.ID]
	if !known {
		seen = time.Now()
		mailUnknown[it.ID] = seen
		logf("mail %s from %s: no handler for topic %q — left in the inbox for now", it.ID, it.From, it.Topic)
	}
	if !it.At.IsZero() && it.At.Before(seen) {
		seen = it.At
	}
	return time.Since(seen) >= mailUnknownGrace
}

// pullMailAtStart reads what waited while nothing ran (main.go).
func (ag *Agent) pullMailAtStart() {
	if !partitioned() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if c, err := ag.pullMail(ctx); err != nil {
		logf("mail at start: %v (the doorbell rings again)", err)
	} else if c != (mailCounts{}) {
		logf("mail at start: %d handled, %d left, %d dropped", c.Handled, c.Left, c.Dropped)
	}
}

// mailboxRoutes mounts the doorbell apart from the route table: xbind rings
// it as xbin/mail with the writer role, which the table's admin gate refuses.
func mailboxRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /mailbox", handleMailbox)
}

// handleMailbox is the doorbell: xbind's xbin/mail principal (or the tile
// itself) says items wait. It answers once the pull is done.
func handleMailbox(w http.ResponseWriter, r *http.Request) {
	if !partitioned() {
		xbin.WriteError(w, http.StatusNotFound, "this agent isn't partitioned: it has no mailbox")
		return
	}
	// principal, not callerOf: a caller nobody could identify is refused here,
	// never taken for the tile itself (callerOf's fallback)
	if from := xbin.Caller(r).From; from != "xbin/mail" && principal(r).kind != whoSystem {
		xbin.WriteError(w, http.StatusForbidden, "only xbind rings the mailbox")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	c, err := agent.pullMail(ctx)
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "reading the mailbox: "+err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, c)
}
