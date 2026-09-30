// mailbox.go — partition mail (docs/partitions.md, "partitionMail" in
// xbin.json): the xbind-owned drop box between a partitioned agent's global
// instance and one person's partition. Global mails a person's partition
// (a linked DM, a private trigger's event…); a person's partition mails only
// global (a reply for the outbox…); xbind stamps who sent it. When items wait,
// xbind rings the doorbell — POST /mailbox, as xbin/mail — and the handler
// pulls the inbox, hands each item to the handler of its topic and acks it.
// Every start pulls too (a doorbell may have rung while nothing ran).
//
// Delivery is at least once: a handler's effect and the item's id (mail_seen)
// commit in one transaction, so an item delivered again after a crash between
// the commit and the ack is only acked. An item whose topic no handler knows
// stays in the inbox (a newer version of this code, mid-deploy, may be the
// sender) until it expires.
//
// This is the skeleton: no topic has a handler yet (channels, triggers and
// schedules through mail add theirs to mailHandlers), and until xbind's mail
// API is wired in (partitionMail below) the inbox is always empty. An
// unpartitioned instance has no mailbox.
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
// "user:<id>").
type mailItem struct {
	ID    string          `json:"id"`
	From  string          `json:"from"`
	Topic string          `json:"topic"`
	Data  json.RawMessage `json:"data"`
}

// mailSource is this process's drop box. The seam for xbind's partition
// mail: wire the SDK's xbin.Inbox / xbin.Ack here once they exist (an
// adapter mapping xbin.MailItem to mailItem). noMail — the default — holds
// nothing, so a doorbell pulls nothing and nothing is lost: mail waits in
// xbind until code that reads it runs.
type mailSource interface {
	Inbox(ctx context.Context, after string, limit int) ([]mailItem, error)
	Ack(ctx context.Context, ids ...string) error
}

type noMail struct{}

func (noMail) Inbox(context.Context, string, int) ([]mailItem, error) { return nil, nil }
func (noMail) Ack(context.Context, ...string) error                   { return nil }

var partitionMail mailSource = noMail{}

// mailHandler applies one item inside t (the item's id is recorded in the
// same transaction); an error leaves the item unacked for the next pull.
type mailHandler func(ctx context.Context, t *DB, it mailItem) error

// mailHandlers are the topics this code handles (none yet).
var mailHandlers = map[string]mailHandler{}

// mailPage is how many items one inbox read asks for.
const mailPage = 50

var (
	mailMu      sync.Mutex // one pull at a time
	mailUnknown = map[string]bool{}
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

// pullMail drains the inbox: handled items (and ones handled before) are
// acked; items of unknown topics, and ones whose handler failed, stay.
func (ag *Agent) pullMail(ctx context.Context) (handled, left int, err error) {
	if !partitioned() {
		return 0, 0, nil
	}
	mailMu.Lock()
	defer mailMu.Unlock()
	if err := ag.db.ensureMailSeen(); err != nil {
		return 0, 0, err
	}
	after := ""
	for {
		items, err := partitionMail.Inbox(ctx, after, mailPage)
		if err != nil {
			return handled, left, err
		}
		var ack []string
		for _, it := range items {
			after = it.ID
			if it.ID == "" {
				continue
			}
			if ag.db.mailSeen(it.ID) {
				ack = append(ack, it.ID) // applied before; its ack was lost
				continue
			}
			h := mailHandlers[it.Topic]
			if h == nil {
				if !mailUnknown[it.ID] {
					mailUnknown[it.ID] = true
					logf("mail %s from %s: no handler for topic %q — left in the inbox", it.ID, it.From, it.Topic)
				}
				left++
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
				left++
				continue
			}
			handled++
			ack = append(ack, it.ID)
		}
		if len(ack) > 0 {
			if err := partitionMail.Ack(ctx, ack...); err != nil {
				return handled, left, err
			}
		}
		if len(items) < mailPage {
			break
		}
	}
	_, _ = ag.db.q.Exec(`DELETE FROM mail_seen WHERE at < ?`, now()-31*24*3600) // longer than any item lives
	return handled, left, nil
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
	if from := xbin.Caller(r).From; from != "xbin/mail" && callerOf(r).kind != whoSystem {
		xbin.WriteError(w, http.StatusForbidden, "only xbind rings the mailbox")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	handled, left, err := agent.pullMail(ctx)
	if err != nil {
		xbin.WriteError(w, http.StatusBadGateway, "reading the mailbox: "+err.Error())
		return
	}
	xbin.WriteJSON(w, http.StatusOK, map[string]int{"handled": handled, "left": left})
}
