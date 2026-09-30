// handoff.go — chat channels and event triggers in a partitioned agent
// (API.md "Partitioned instances"; /docs/partitions.md §Partition mail). The
// messaging bridge and the webhooks tile aren't partitioned, so they reach
// the global instance; what one person's partition must run gets there by
// partition mail, and what it answers comes back the same way:
//
//   - A DM from a chat account linked to a person (channel_peers.xbin_user,
//     global's db) is a handoff: global records where it came from — the
//     channel, the peer, the reply address, the person; routing metadata —
//     and mails `user:<person>` a `handoff/dm` with the message (its files
//     inline). Their partition runs the DM conversation in its own db
//     (handoff_user.go) and answers with an `outbox/add` naming the handoff;
//     global takes the destination from its own record, never from the mail,
//     and only when the mail's sender (stamped by xbind) is the handoff's
//     person — so no partition can post into anyone else's chat.
//   - An event for a person's private trigger (a registry row, host
//     "user:<id>": trigger_registry.go) is recorded (the dedupe, the hourly
//     cap, the halt) and mailed as `handoff/event`; the person's partition
//     runs it with the trigger's config from its own db. The mail names its
//     source, which xbind counts in the person's egress ledger.
//
// A handoff is queued in the transaction that decided it and mailed after
// the commit (handoff_send.go) — so a crash never loses one, and nothing
// waits on the network while holding the database. Its content stays in
// global's db only until it is mailed, or given up (at most
// handoffQueueTTL); a person's reply stays in global's outbox only until the
// adapter acknowledged it, and nobody but the adapter reads it there.
// Delivery is at least once: the partition dedupes by the handoff (and the
// event) id.
//
// Unpartitioned, nothing here runs: these tables are made only in a
// partitioned instance (addHandoffSchema, from startMode).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
)

const handoffSchemaSQL = `
CREATE TABLE IF NOT EXISTS handoffs (
  id TEXT PRIMARY KEY, kind TEXT NOT NULL, person TEXT NOT NULL,
  channel_id INTEGER NOT NULL DEFAULT 0, peer_id TEXT NOT NULL DEFAULT '',
  session_key TEXT NOT NULL DEFAULT '', address TEXT NOT NULL DEFAULT '',
  trigger_id INTEGER NOT NULL DEFAULT 0, source TEXT NOT NULL DEFAULT '',
  created INTEGER NOT NULL, state TEXT NOT NULL DEFAULT 'queued',
  tries INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '',
  mailed_at INTEGER NOT NULL DEFAULT 0, payload TEXT NOT NULL DEFAULT '',
  next_try INTEGER NOT NULL DEFAULT 0);
CREATE INDEX IF NOT EXISTS idx_handoffs_state ON handoffs(state, created);
CREATE TABLE IF NOT EXISTS usage_days (
  person TEXT NOT NULL, day TEXT NOT NULL,
  runs INTEGER NOT NULL DEFAULT 0, llm_calls INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0,
  at INTEGER NOT NULL, PRIMARY KEY (person, day));
CREATE TABLE IF NOT EXISTS usage_daily (
  day TEXT PRIMARY KEY, llm_calls INTEGER NOT NULL DEFAULT 0,
  prompt_tokens INTEGER NOT NULL DEFAULT 0, completion_tokens INTEGER NOT NULL DEFAULT 0);
CREATE TABLE IF NOT EXISTS partition_people (
  person TEXT PRIMARY KEY, seen INTEGER NOT NULL DEFAULT 0, noticed INTEGER NOT NULL DEFAULT 0);
`

// addHandoffSchema makes the tables above and the columns a partitioned
// instance adds to today's: outbox.origin (a person's partition's reply,
// "user:<id>/<its key>": the dedupe of its retries) and triggers.host (a
// registry row's partition, trigger_registry.go). Never unpartitioned.
func (d *DB) addHandoffSchema() error {
	if _, err := d.q.Exec(handoffSchemaSQL); err != nil {
		return err
	}
	for _, q := range []string{
		`ALTER TABLE outbox ADD COLUMN origin TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE triggers ADD COLUMN host TEXT NOT NULL DEFAULT ''`,
		`ALTER TABLE handoffs ADD COLUMN next_try INTEGER NOT NULL DEFAULT 0`, // a table this version's first builds made
	} {
		_, _ = d.q.Exec(q) // fails harmlessly when the column is there
	}
	if _, err := d.q.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS idx_outbox_origin ON outbox(origin) WHERE origin<>''`); err != nil {
		return err
	}
	if userMode() {
		return d.seedTriggerIDs() // trigger_registry.go
	}
	return nil
}

// Mail topics.
const (
	topicDM       = "handoff/dm"    // global → a person: a DM from their linked chat account
	topicEvent    = "handoff/event" // global → a person: an event for their private trigger
	topicOutbox   = "outbox/add"    // a person → global: a reply for a chat
	topicUsage    = "usage/day"     // a person → global: their daily usage totals
	handoffMaxAge = 30 * 86400      // a handoff record's life: replies to older ones are refused
	// mailFileBudget is how many bytes of files one mail item carries (base64
	// adds a third; the item limit is 1 MiB). What doesn't fit is named in
	// the text instead.
	mailFileBudget = 640 << 10
)

// hoFile is a file carried inline in a mail item.
type hoFile struct {
	Name string `json:"name"`
	Mime string `json:"mime"`
	Data []byte `json:"data"` // base64 on the wire
}

// dmHandoff is a handoff/dm item: a message a person's linked chat account
// sent, and how the channel's rules say it runs (global decided them: the
// lane and class, the deny list, the channel's system addendum).
type dmHandoff struct {
	Handoff  string   `json:"handoff"`
	Channel  int64    `json:"channel"`
	Platform string   `json:"platform"`
	Session  string   `json:"session"` // the session key (the same everywhere)
	PeerID   string   `json:"peerId"`
	PeerName string   `json:"peerName,omitempty"`
	EventID  string   `json:"eventId,omitempty"`
	Text     string   `json:"text"`
	Command  string   `json:"command,omitempty"` // a session command: new | reset | status | stop | approve | deny
	Arg      string   `json:"arg,omitempty"`
	Lane     string   `json:"lane"`
	Class    string   `json:"class"`
	Deny     []string `json:"deny,omitempty"`
	System   string   `json:"system,omitempty"`
	Reset    string   `json:"reset,omitempty"`
	Trusted  bool     `json:"trusted,omitempty"`
	Files    []hoFile `json:"files,omitempty"`
	Staged   []string `json:"staged,omitempty"` // global only: staged channel files, inlined when mailed
}

// eventHandoff is a handoff/event item: one event for a private trigger.
type eventHandoff struct {
	Handoff   string          `json:"handoff"`
	Trigger   string          `json:"trigger"` // its name (the registry's key)
	Source    string          `json:"source"`
	SourceRef string          `json:"sourceRef"`
	EventID   string          `json:"eventId"`
	Topic     string          `json:"topic,omitempty"`
	Text      string          `json:"text,omitempty"`
	Data      json.RawMessage `json:"data,omitempty"`
	DataClass string          `json:"dataClass"`
}

// outboxAddItem is an outbox/add item: a person's partition's reply to the
// chat a handoff came from.
type outboxAddItem struct {
	Handoff string   `json:"handoff"`
	Key     string   `json:"key"` // unique per reply: retries of it post once
	Kind    string   `json:"kind"`
	Text    string   `json:"text"`
	Files   []hoFile `json:"files,omitempty"`
}

func newHandoffID() string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return "h" + hex.EncodeToString(b[:])
}

// --- sending mail ------------------------------------------------------------------

// errMailRefused is xbind's final "no" to a mail: the addressee isn't a live
// person who can read the tile, the item is too large, the sender may not.
var errMailRefused = errors.New("refused")

// sendMail sends partition mail (the SDK's MailWith wire, with a deadline:
// the SDK client has none). A var so tests can stand in for xbind.
var sendMail = func(ctx context.Context, to, topic string, data any, source string) (string, error) {
	body := map[string]any{"to": to, "topic": topic, "data": data}
	if source != "" {
		body["source"] = source
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	res, err := gwDo(ctx, http.MethodPost, "http://xbin/api/xbin/partitions/mail", raw, "application/json")
	if err != nil {
		return "", err
	}
	if res.Status != http.StatusOK {
		err := gwErr("partition mail", res)
		switch res.Status {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusRequestEntityTooLarge:
			return "", fmt.Errorf("%w: %v", errMailRefused, err)
		}
		return "", err // 409 paused, 507 full, 5xx: later
	}
	var out struct{ ID string }
	_ = json.Unmarshal(res.Body, &out)
	return out.ID, nil
}

// --- global: the linked DM ------------------------------------------------------------

// handDM hands a linked person's DM to their partition (channelMessage, in
// its transaction): true when it did — or answered it here, for /help and
// /link, which are about the chat account, not the conversation. Anything
// but a linked DM at a partitioned agent's global instance: false, and the
// message goes on as today.
func (ag *Agent) handDM(t *DB, ch *Channel, m *adapterMsg, key, addr string, peer *chanPeer, v *msgVerdict, after *[]func()) (bool, error) {
	if !globalMode() || !m.dm() || peer == nil || peer.XbinUser == "" {
		return false, nil
	}
	cmd, arg := commandOf(m)
	if cmd == "help" || cmd == "link" {
		v.Command = cmd
		_, more, err := ag.channelCommand(t, ch, m, key, addr, peer, cmd, arg)
		*after = append(*after, more...)
		return true, err
	}
	lane := laneFor(ch.Policy, m, peer)
	cls := ch.Policy.classFor(lane)
	h := dmHandoff{Handoff: newHandoffID(), Channel: ch.ID, Platform: ch.Platform, Session: key, PeerID: m.Sender.ID,
		PeerName: m.Sender.Name, EventID: m.EventID, Text: m.Text, Command: cmd, Arg: arg, Lane: lane, Class: cls.ID,
		Deny: ch.Policy.deny(), System: channelAddendum(ch, m) + orStr("\n\n"+ch.Policy.System, ""), Reset: ch.Policy.Reset,
		Trusted: peer.trusted(ch.Policy), Staged: m.Files}
	if cmd != "" {
		v.Command = cmd
	}
	if err := t.queueHandoff("dm", peer.XbinUser, h.Handoff, h, func(q *handoffRow) {
		q.channel, q.peer, q.session, q.address = ch.ID, m.Sender.ID, key, addr
	}); err != nil {
		return true, err
	}
	v.SessionKey = key
	state := "working"
	if !t.partitionRan(peer.XbinUser) { // handoff_people.go: it waits in their inbox, unread, until they open the agent
		state = "idle"
	}
	*after = append(*after, func() {
		kickHandoffs()
		outStatus(ch.Adapter, outStatusEv{ChannelID: ch.ID, SessionKey: key, Address: json.RawMessage(addr), State: state})
	})
	return true, nil
}

// handoffRow is a handoff's routing metadata.
type handoffRow struct {
	id, kind, person      string
	channel               int64
	peer, session, source string
	address               string
	trigger               int64
}

// queueHandoff records a handoff with its payload (mailed after the commit
// by handoffSender); old records are pruned here.
func (d *DB) queueHandoff(kind, person, id string, payload any, fill func(*handoffRow)) error {
	q := &handoffRow{id: id, kind: kind, person: person}
	fill(q)
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := d.q.Exec(`INSERT INTO handoffs (id, kind, person, channel_id, peer_id, session_key, address, trigger_id, source, created, payload)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, q.id, q.kind, q.person, q.channel, q.peer, q.session, q.address, q.trigger, q.source,
		now(), string(raw)); err != nil {
		return err
	}
	_, _ = d.q.Exec(`DELETE FROM handoffs WHERE state<>'queued' AND created<?`, now()-handoffMaxAge)
	return nil
}

// --- global: a person's reply ------------------------------------------------------------

func init() {
	mailHandlers[topicOutbox] = handleOutboxAdd
}

// outboxKinds are the rows a person's partition may post.
var outboxKinds = map[string]bool{"answer": true, "question": true, "approval": true, "error": true, "notice": true, "announce": true}

// handleOutboxAdd (global) posts a person's partition's reply to the chat
// its handoff came from — the destination is global's own record; the mail's
// sender (stamped by xbind) must be the handoff's person. A mail that fails
// those checks is refused: logged and acknowledged, never posted.
func handleOutboxAdd(ctx context.Context, t *DB, it mailItem) error {
	person, ok := strings.CutPrefix(it.From, "user:")
	if !globalMode() || !ok || person == "" {
		logf("outbox/add %s from %q: only a person's partition mails the global instance a reply — refused", it.ID, it.From)
		return nil
	}
	t.markRan(person) // handoff_people.go
	var in outboxAddItem
	if err := json.Unmarshal(it.Data, &in); err != nil || in.Handoff == "" || in.Key == "" || !outboxKinds[in.Kind] {
		logf("outbox/add %s from %s: malformed — refused", it.ID, it.From)
		return nil
	}
	var owner, kind, session, addr, peer string
	var chID, created int64
	err := t.q.QueryRow(`SELECT person, kind, session_key, address, channel_id, peer_id, created FROM handoffs WHERE id=?`, in.Handoff).
		Scan(&owner, &kind, &session, &addr, &chID, &peer, &created)
	switch {
	case err != nil || created < now()-handoffMaxAge:
		logf("outbox/add %s from %s: no handoff %s (older than %d days, or never) — refused", it.ID, it.From, in.Handoff, handoffMaxAge/86400)
		return nil
	case owner != person || kind != "dm":
		logf("outbox/add %s from %s: handoff %s isn't theirs — refused", it.ID, it.From, in.Handoff)
		return nil
	}
	var linked, peerState string
	_ = t.q.QueryRow(`SELECT xbin_user, state FROM channel_peers WHERE channel_id=? AND peer_id=?`, chID, peer).Scan(&linked, &peerState)
	if linked != person || peerState == "blocked" {
		logf("outbox/add %s from %s: the chat account of handoff %s isn't linked to them any more — dropped", it.ID, it.From, in.Handoff)
		return nil
	}
	ch, err := t.getChannel(chID)
	if err != nil || ch.State != chActive {
		logf("outbox/add %s from %s: channel %d is gone or switched off — dropped", it.ID, it.From, chID)
		return nil
	}
	origin := it.From + "/" + in.Key
	var dup int
	_ = t.q.QueryRow(`SELECT count(*) FROM outbox WHERE origin=?`, origin).Scan(&dup)
	if dup > 0 {
		return nil // a retry of a reply already posted
	}
	var files []outFile
	for i, f := range in.Files {
		of, err := stageReplyFile(ctx, t, chID, f, i)
		if err != nil {
			return err // the blob store: the item stays for the next pull
		}
		files = append(files, of)
	}
	body, _ := json.Marshal(outBody{Text: in.Text, Format: "markdown", Files: files})
	if _, err := t.q.Exec(`INSERT INTO outbox (channel_id, session_key, run_id, kind, address, body, created, origin) VALUES (?, ?, 0, ?, ?, ?, ?, ?)`,
		chID, session, in.Kind, addr, string(body), now(), origin); err != nil {
		return err
	}
	// a reply's dedupe key lives as long as its handoff may be answered (outbox.go's prune leaves these: handedKept)
	_, _ = t.q.Exec(`DELETE FROM outbox WHERE origin<>'' AND state<>'pending' AND created<?`, now()-handoffMaxAge)
	t.AfterCommit(outboxKick)
	if in.Kind != "question" && in.Kind != "approval" {
		t.AfterCommit(func() {
			outStatus(ch.Adapter, outStatusEv{ChannelID: chID, SessionKey: session, Address: json.RawMessage(addr), State: "idle"})
		})
	}
	return nil
}

// stagedPrefix marks an outbox file that is a staged channel file (a reply
// a person's partition mailed), not a session file of the row's run.
const stagedPrefix = "staged:"

// stageReplyFile keeps a reply's file i for the adapter to download (as
// channel_files, like an upload: text in the row, anything else in the blob
// store — stored before the transaction: mail_prepare.go) until the row is
// acknowledged.
func stageReplyFile(ctx context.Context, t *DB, chID int64, f hoFile, i int) (outFile, error) {
	var b [9]byte
	_, _ = rand.Read(b[:])
	id := "r" + hex.EncodeToString(b[:])
	name := sanitizeUploadName(orStr(f.Name, "file"))
	mime, text := mailFileKind(f)
	content, blob := "", ""
	if text {
		content = string(f.Data)
	} else {
		var err error
		if blob, err = preparedBlob(ctx, i); err != nil {
			return outFile{}, err
		}
		if blob == "" { // not prepared (a caller without mailbox.go's step): stored here
			blob = fmt.Sprintf("chanfiles/%d/%s", chID, id)
			if err := agent.blobs.Put(ctx, blob, f.Data, mime); err != nil {
				return outFile{}, err
			}
		}
	}
	if _, err := t.q.Exec(`INSERT INTO channel_files (id, channel_id, name, mime, size, content, blob, created) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		id, chID, name, mime, len(f.Data), content, blob, now()); err != nil {
		return outFile{}, err
	}
	return outFile{Name: name, Mime: mime, Bytes: len(f.Data), Path: stagedPrefix + id}, nil
}

// serveStagedFile answers GET /adapter/files/{oid}/{i} for a staged reply
// file (handleAdapterFile) — a file of a row a person's partition mailed,
// staged for that row's channel: true when it was one.
func serveStagedFile(w http.ResponseWriter, r *http.Request, o *OutRow, of outFile) bool {
	id, ok := strings.CutPrefix(of.Path, stagedPrefix)
	if !ok || !globalMode() || o.RunID != 0 {
		return false
	}
	var origin string
	if agent.db.q.QueryRow(`SELECT origin FROM outbox WHERE id=?`, o.ID).Scan(&origin) != nil || origin == "" {
		return false
	}
	var mime, content, blob string
	if err := agent.db.q.QueryRow(`SELECT mime, content, blob FROM channel_files WHERE id=? AND channel_id=?`, id, o.ChannelID).
		Scan(&mime, &content, &blob); err != nil {
		http.Error(w, `{"error":"the file is gone"}`, http.StatusNotFound)
		return true
	}
	data := []byte(content)
	if blob != "" {
		var err error
		if data, err = agent.readBlob(r.Context(), blob); err != nil {
			http.Error(w, `{"error":"reading the file failed"}`, http.StatusBadGateway)
			return true
		}
	}
	w.Header().Set("Content-Type", orStr(mime, "text/plain; charset=utf-8"))
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", of.Name))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(data)
	return true
}

// purgeHandedReply forgets a person's reply once its adapter acknowledged
// it (handleAdapterAck, in its transaction): the row stays — its origin
// dedupes a late retry — without its text, address or files.
func purgeHandedReply(t *DB, id int64) {
	if !globalMode() {
		return
	}
	var body string
	if t.q.QueryRow(`SELECT body FROM outbox WHERE id=? AND origin<>''`, id).Scan(&body) != nil {
		return
	}
	var b outBody
	_ = json.Unmarshal([]byte(body), &b)
	var blobs []string
	for _, f := range b.Files {
		if fid, ok := strings.CutPrefix(f.Path, stagedPrefix); ok {
			var blob string
			if t.q.QueryRow(`SELECT blob FROM channel_files WHERE id=?`, fid).Scan(&blob) == nil && blob != "" {
				blobs = append(blobs, blob)
			}
			_, _ = t.q.Exec(`DELETE FROM channel_files WHERE id=?`, fid)
		}
	}
	_, _ = t.q.Exec(`UPDATE outbox SET body='{}', address='{}' WHERE id=?`, id)
	t.AfterCommit(func() { agent.dropBlobs(blobs) })
}

// redactHanded is the channel owner's view of its outbox (GET
// /channels/{id}/outbox): at a partitioned agent's global instance a row a
// person's partition mailed is a reply in their private conversation — it
// is listed (its kind, state, times, error) without its text, files or
// address. Nothing changes anywhere else.
func redactHanded(items []*OutRow) []*OutRow {
	if !globalMode() {
		return items
	}
	for _, o := range items {
		var origin string
		if agent.db.q.QueryRow(`SELECT origin FROM outbox WHERE id=?`, o.ID).Scan(&origin) == nil && origin != "" {
			o.Body = outBody{Text: "(a reply from a person's own space — not shown here)", Format: "markdown"}
			o.Address = json.RawMessage(`{}`)
		}
	}
	return items
}

// handedKept is what the outbox's prune (outbox.go) leaves at a partitioned
// agent's global instance: the rows people's partitions mailed, whose origin
// dedupes a late retry for as long as a handoff may be answered
// (handleOutboxAdd prunes those). "" anywhere else.
func handedKept() string {
	if !globalMode() {
		return ""
	}
	return ` AND origin=''`
}
