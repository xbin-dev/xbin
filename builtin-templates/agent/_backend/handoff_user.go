// handoff_user.go — a person's partition's side of handoff.go: the DM from
// their linked chat account and the event for their private trigger that
// the global instance mails them, and the replies they mail back.
//
//   - handoff/dm: the conversation runs here, in the person's own db, under
//     the session key the channel gave it (the same key everywhere), as
//     theirs and private — with the lane, class, deny list and system text
//     the channel's rules gave it at global. Its inbox rows carry the
//     handoff as their reply address, so what the run answers (a turn's
//     end, a question, a notice) is a row of this partition's outbox naming
//     the handoff.
//   - handoff/event: the private trigger (this db's, by name) fires as it
//     would unpartitioned — its config, dedupe, cap and data rules are here.
//   - The outbox is mailed to global (outbox/add) after the commit that
//     wrote a row, oldest first, with the reply's files inline; each row
//     carries a key unique to this partition's db, so a retry after a crash
//     posts once. A failure xbind may retry is tried again with backoff.
//
// Both handlers run inside the transaction that records the item as seen
// (mailbox.go); they only write this db (and a received file's blob).
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// The channels' routes a person's partition forwards to the global instance
// are partition_routes.go's userRoutes.
func init() {
	mailHandlers[topicDM] = handleDMHandoff
	mailHandlers[topicEvent] = handleEventHandoff
}

// handleDMHandoff takes a DM global handed this person.
func handleDMHandoff(ctx context.Context, t *DB, it mailItem) error {
	if !userMode() || it.From != "global" {
		logf("handoff/dm %s from %q: only the global instance hands a person a DM — refused", it.ID, it.From)
		return nil
	}
	var h dmHandoff
	if err := json.Unmarshal(it.Data, &h); err != nil || h.Handoff == "" || !strings.HasPrefix(h.Session, "chan:") {
		logf("handoff/dm %s: malformed — dropped", it.ID)
		return nil
	}
	if err := takeFetched(ctx, &h); err != nil { // handoff_fetch.go: files too large for the mail
		return err
	}
	return agent.takeDM(ctx, t, &h)
}

// handoffAddr is the reply address of a handed-off message: its handoff.
func handoffAddr(id string) string {
	b, _ := json.Marshal(map[string]string{"handoff": id})
	return string(b)
}

// takeDM is channelDeliver (channels.go) for a handed-off DM: the session,
// its lane and class, the message — here, the person's.
func (ag *Agent) takeDM(ctx context.Context, t *DB, h *dmHandoff) error {
	var seen int
	_ = t.q.QueryRow(`SELECT count(*) FROM inbox WHERE client_id=?`, "ho:"+h.Handoff).Scan(&seen)
	if seen > 0 {
		return nil // mailed again (global retried after a crash): taken already
	}
	addr := handoffAddr(h.Handoff)
	ch := &Channel{ID: h.Channel, Platform: h.Platform, State: chActive, Policy: channelPolicy{Reset: h.Reset}}
	peer := &chanPeer{ChannelID: h.Channel, PeerID: h.PeerID, Name: h.PeerName, State: "allowed", Trusted: h.Trusted, XbinUser: runUser}
	m := &adapterMsg{ChannelID: h.Channel, EventID: h.EventID, Text: h.Text}
	m.Conversation.Type, m.Sender.ID, m.Sender.Name = "dm", h.PeerID, h.PeerName
	var after []func()
	defer func() { t.AfterCommit(func() { runAll(after) }) }()
	text := h.Text
	if h.Command != "" {
		deliver, more, err := ag.channelCommand(t, ch, m, h.Session, addr, peer, h.Command, h.Arg)
		after = append(after, more...)
		if err != nil || !deliver {
			return err
		}
		text = h.Arg // "/new <text>"
	}
	lane := orStr(h.Lane, "web")
	cls, ok := currentClasses().find(h.Class)
	if !ok {
		cls = classOf(Config{Toolset: lane})
	}
	want := cls.lane()
	if lane == "web" {
		want = "web"
	}
	stamp := runStamp{Owner: runUser, Visibility: visPrivate, TeamRole: roleViewer, Origin: "channel", OriginID: h.Channel, TitleSrc: "origin"}
	if cur, ok := t.sessionRun(h.Session); ok {
		cfg, err := t.runConfig(cur)
		if err == nil && (normalizeToolset(cfg.Toolset) != want || classOf(cfg).ID != cls.ID) {
			t.resetSession(h.Session) // the channel's rules changed: a new conversation
		}
	}
	_, _ = t.q.Exec(`UPDATE sessions SET reset_policy=? WHERE key=?`, h.Reset, h.Session)
	cfg := parseConfig(t.getSetting("config"))
	cfg.setClass(cls, false)
	cfg.Toolset, cfg.Channel = want, true
	cfg.Deny = append([]string(nil), h.Deny...)
	cfg.System += h.System
	label := orStr(h.PeerName, h.PeerID) + " (@" + runUser + ")"
	platform := (&Channel{Platform: h.Platform}).platformName()
	runID, _, _, err := ag.deliverInboundTx(t, inbound{Mode: "session", Key: h.Session, Stamp: stamp,
		Title: platform + " · " + orStr(h.PeerName, h.PeerID), Cfg: cfg, Reset: h.Reset, Source: "channel", Sender: runUser,
		Label: label, Text: text, Addr: addr, Client: "ho:" + h.Handoff,
		Adopt: func(t *DB, runID int64) ([]string, error) { return ag.adoptInline(ctx, t, runID, h.Files) }})
	if err != nil {
		return err
	}
	if t.getSetting("halt") == "1" {
		t.outboxAdd(h.Channel, h.Session, runID, "notice", addr, "I'm paused by my operator right now. Your message is saved; I'll answer when I'm back.")
		return nil
	}
	after = append(after, func() {
		if ag.eng != nil {
			ag.eng.Poke(runID)
		}
	})
	return nil
}

func runAll(fs []func()) {
	for _, f := range fs {
		f()
	}
}

// adoptInline makes files carried in a mail item session files of the run
// (text in the db, anything else in the blob store — stored before the
// transaction: mail_prepare.go).
func (ag *Agent) adoptInline(ctx context.Context, t *DB, runID int64, files []hoFile) ([]string, error) {
	var paths []string
	for i, f := range files {
		name := sanitizeUploadName(orStr(f.Name, "file"))
		mime, text := mailFileKind(f)
		p := t.freePath(runID, name)
		if text {
			rf, err := t.replPutFile(runID, p, string(f.Data), 0)
			if err != nil {
				return nil, err
			}
			_, _ = t.q.Exec(`UPDATE repl_files SET mime=? WHERE run_id=? AND path=?`, mime, runID, rf.Path)
			paths = append(paths, rf.Path)
			continue
		}
		blob, err := preparedBlob(ctx, i)
		if err != nil {
			return nil, err // the blob store: the item stays for the next pull
		}
		if blob == "" { // not prepared (a caller without mailbox.go's step): stored here
			blob = newBlobPath(runID)
			if err := ag.blobs.Put(ctx, blob, f.Data, mime); err != nil {
				return nil, err
			}
		}
		rf, err := t.replPutBinary(runID, p, mime, len(f.Data), blob)
		if err != nil {
			return nil, err
		}
		paths = append(paths, rf.Path)
	}
	return paths, nil
}

// handleEventHandoff fires this person's private trigger with an event
// global handed them.
func handleEventHandoff(_ context.Context, t *DB, it mailItem) error {
	if !userMode() || it.From != "global" {
		logf("handoff/event %s from %q: only the global instance hands a person an event — refused", it.ID, it.From)
		return nil
	}
	var e eventHandoff
	if err := json.Unmarshal(it.Data, &e); err != nil || e.Trigger == "" || e.EventID == "" {
		logf("handoff/event %s: malformed — dropped", it.ID)
		return nil
	}
	trs := t.listTriggers(`WHERE name=?`, e.Trigger)
	if len(trs) != 1 || trs[0].Source != e.Source || trs[0].SourceRef != e.SourceRef {
		logf("handoff/event %s: no trigger %q from %s here (deleted since?) — dropped", it.ID, e.Trigger, e.SourceRef)
		return nil
	}
	class := "private"
	if e.DataClass == "public" {
		class = "public"
	}
	_, err := agent.fireTriggerIn(t, trs[0], trigEvent{ID: e.Source + ":" + e.EventID, Source: e.Source, Topic: e.Topic,
		Text: e.Text, Data: e.Data, Class: class})
	return err
}

// --- the outbox, mailed ---------------------------------------------------------------

// outboxMailer mails this partition's outbox rows to global, one pass at a
// time; a pass that must be retried is, with backoff, while rows wait.
var outboxMailer = struct {
	mu      sync.Mutex
	running bool
	again   bool
	timer   *time.Timer
	backoff time.Duration
}{}

// kickOutboxMail starts a pass (after a commit that wrote a row, at start).
func kickOutboxMail() {
	if !userMode() || agent == nil {
		return
	}
	s := &outboxMailer
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
			retry := agent.mailOutbox(context.Background())
			s.mu.Lock()
			if s.again {
				s.again = false
				s.mu.Unlock()
				continue
			}
			s.running = false
			if retry {
				s.backoff = min(max(2*s.backoff, time.Second), 5*time.Minute)
				if s.timer != nil {
					s.timer.Stop()
				}
				s.timer = time.AfterFunc(s.backoff, kickOutboxMail)
			} else {
				s.backoff = 0
			}
			s.mu.Unlock()
			return
		}
	}()
}

// outboxKey is this db's own key prefix for its replies: a partition that
// was reset starts a new db, whose row ids start again — its key doesn't.
func (d *DB) outboxKey() string {
	if k := d.getSetting("outbox_key"); k != "" {
		return k
	}
	var b [8]byte
	_, _ = rand.Read(b[:])
	k := hex.EncodeToString(b[:])
	_ = d.putSetting("outbox_key", k)
	return k
}

// mailOutbox mails the pending rows; true: one waits for a retry.
func (ag *Agent) mailOutbox(ctx context.Context) bool {
	prefix := ag.db.outboxKey()
	for {
		rows := ag.db.outRows(`WHERE state='pending' ORDER BY id LIMIT 20`)
		for _, o := range rows {
			var a struct{ Handoff string }
			_ = json.Unmarshal(o.Address, &a)
			if a.Handoff == "" {
				ag.settleOut(o.ID, "failed", "no chat to answer: the conversation's messages didn't come through a channel handoff")
				continue
			}
			item := outboxAddItem{Handoff: a.Handoff, Key: prefix + "-" + strconv.FormatInt(o.ID, 10), Kind: o.Kind, Text: o.Body.Text}
			cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
			var err error
			if item.Files, item.Staged, err = ag.replyFiles(cctx, o, a.Handoff, item.Key); err == nil {
				_, err = sendMail(cctx, "global", topicOutbox, item, "")
			}
			cancel()
			switch {
			case err == nil:
				ag.settleOut(o.ID, "delivered", "")
			case errors.Is(err, errMailRefused):
				logf("outbox row %d: %v — dropped", o.ID, err)
				ag.settleOut(o.ID, "failed", err.Error())
			default:
				logf("outbox row %d: %v — tried again later", o.ID, err)
				return true
			}
		}
		if len(rows) < 20 {
			return false
		}
	}
}

// repliesWait: replies wait to be mailed (the partition stopped before
// xbind took them, or it refused for now) — work that moves without the
// person, so a stopping partition asks to be started again (userWake).
func (d *DB) repliesWait() bool {
	var n int
	_ = d.q.QueryRow(`SELECT count(*) FROM outbox WHERE state='pending'`).Scan(&n)
	return n > 0
}

func (ag *Agent) settleOut(id int64, state, why string) {
	_, _ = ag.db.q.Exec(`UPDATE outbox SET state=?, error=?, acked_at=? WHERE id=? AND state='pending'`, state, clip(why, 500), now(), id)
}

// replyFiles reads a reply's files (session files of its run) to carry them
// in the mail; one that doesn't fit is staged at the global instance first
// (handoff_fetch.go) and named in `staged`. A staging failure is the
// reply's: tried again later.
func (ag *Agent) replyFiles(ctx context.Context, o *OutRow, handoff, key string) ([]hoFile, []string, error) {
	budget := mailFileBudget - len(o.Body.Text)
	var out []hoFile
	var staged []string
	for i, of := range o.Body.Files {
		f, err := ag.db.replFile(o.RunID, of.Path)
		if err != nil {
			continue
		}
		data := []byte(f.Content)
		if f.Binary {
			if data, err = ag.readBlob(ctx, f.Blob); err != nil {
				logf("outbox row %d: reading %s: %v — sent without it", o.ID, of.Path, err)
				continue
			}
		}
		if len(data) > budget {
			id, err := stageReply(ctx, handoff, replyFileKey(key, i), hoFile{Name: of.Name, Mime: of.Mime, Data: data})
			if errors.Is(err, errStageRefused) {
				logf("outbox row %d: %s (%s): %v — sent without it", o.ID, of.Path, humanBytes(len(data)), err)
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			staged = append(staged, id)
			continue
		}
		budget -= len(data)
		out = append(out, hoFile{Name: of.Name, Mime: of.Mime, Data: data})
	}
	return out, staged, nil
}

// globalChannelItems are the channels as the global instance lists them to
// this partition's person (channelItems in a person's partition): channels
// are global's, and so are their routes (forwarded: partition_routes.go).
func globalChannelItems(_ who) []AutomationItem {
	var out []AutomationItem
	for _, it := range globalAutomations() {
		if it.Kind == "channel" {
			out = append(out, it)
		}
	}
	return out
}

// globalListing is the global instance's GET /automations as this
// partition's person reads it: the source of the channels' items
// (globalChannelItems) and of a manager's oversight rows
// (globalTriggerOversight). One call serves both, and a burst of listings
// and badge counts, for a few seconds; a write this partition forwards to
// global makes it stale at once (noteGlobalWrite); a call that fails or
// takes too long answers the last listing it had.
var globalListing struct {
	mu    sync.Mutex
	ag    *Agent // the agent it was read for
	gen   int64  // globalWrites when it was read
	at    time.Time
	items []AutomationItem
}

// globalWrites counts the writes this partition made at global.
var globalWrites atomic.Int64

const globalListingTTL = 5 * time.Second

// noteGlobalWrite: a call that may change what global lists returned
// (callGlobal, for anything but a read).
func noteGlobalWrite() { globalWrites.Add(1) }

func globalAutomations() []AutomationItem {
	l := &globalListing
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.ag != agent {
		l.ag, l.at, l.items = agent, time.Time{}, nil
	}
	gen := globalWrites.Load()
	if l.gen == gen && time.Since(l.at) < globalListingTTL {
		return l.items
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	res, err := callGlobal(ctx, http.MethodGet, "/automations", nil, "")
	if err != nil || res.Status != http.StatusOK {
		logf("the automations at the global instance: %v (HTTP %d) — the last listing stands", err, res.Status)
		l.gen, l.at = gen, time.Now().Add(-globalListingTTL+2*time.Second) // asked again in two seconds
		return l.items
	}
	var all struct{ Items []AutomationItem }
	_ = json.Unmarshal(res.Body, &all)
	l.gen, l.at, l.items = gen, time.Now(), all.Items
	return l.items
}

// startPartitionMail is what a partitioned instance does at start beside
// pulling its mail (main.go): global mails what it queued before it
// stopped; a person's partition mails its outbox and its usage.
func (ag *Agent) startPartitionMail() {
	switch {
	case globalMode():
		kickHandoffs()
	case userMode():
		kickOutboxMail()
		kickMoves() // homes_move_user.go: a move a stop cut short
		ag.usageAtStart()
		ag.sayHello(context.Background()) // handoff_people.go
	}
}
